package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

var errInvalidDownloadDestination = errors.New("invalid download destination")

type downloadOperations struct {
	createTemp       func(string, string) (*os.File, error)
	protect          func(*os.File) error
	writer           func(*os.File) io.Writer
	resetFile        func(*os.File) error
	syncFile         func(*os.File) error
	closeFile        func(*os.File) error
	installNoReplace func(string, string) (bool, error)
	replace          func(string, string) (bool, error)
	syncParent       func(string) error
}

func defaultDownloadOperations() downloadOperations {
	return downloadOperations{
		createTemp: os.CreateTemp,
		protect:    securefile.ProtectPrivateFile,
		writer:     func(file *os.File) io.Writer { return file },
		resetFile: func(file *os.File) error {
			if err := file.Truncate(0); err != nil {
				return err
			}
			_, err := file.Seek(0, io.SeekStart)
			return err
		},
		syncFile:         func(file *os.File) error { return file.Sync() },
		closeFile:        func(file *os.File) error { return file.Close() },
		installNoReplace: atomicInstallDownloadNoReplace,
		replace:          atomicReplaceDownload,
		syncParent:       securefile.SyncParentDirectory,
	}
}

type downloadTargetState uint8

const (
	downloadTargetActive downloadTargetState = iota
	downloadTargetClosed
	downloadTargetInstalled
	downloadTargetAborted
)

type downloadInstallResult struct {
	Visible bool
	Durable bool
}

// downloadTarget owns one protected same-directory temporary file. Its bytes
// cannot become visible at destination until Install performs the final atomic
// no-replace or replace operation.
type downloadTarget struct {
	destination   string
	directory     string
	temporaryPath string
	file          *os.File
	operations    downloadOperations
	state         downloadTargetState
}

func newDownloadTarget(destination string, force bool, operations downloadOperations) (*downloadTarget, error) {
	if destination == "" || filepath.Base(destination) == "." || filepath.Base(destination) == string(filepath.Separator) {
		return nil, errInvalidDownloadDestination
	}
	if !force {
		if _, err := os.Lstat(destination); err == nil {
			return nil, os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	directory := filepath.Dir(destination)
	file, err := operations.createTemp(directory, "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return nil, err
	}
	target := &downloadTarget{
		destination: destination, directory: directory, temporaryPath: file.Name(),
		file: file, operations: operations, state: downloadTargetActive,
	}
	if err := operations.protect(file); err != nil {
		target.Abort()
		return nil, err
	}
	return target, nil
}

func (target *downloadTarget) Writer() io.Writer {
	if target == nil || target.state != downloadTargetActive {
		return failingWriter{err: errors.New("download target is not writable")}
	}
	return target.operations.writer(target.file)
}

func (target *downloadTarget) Reset() error {
	if target == nil || target.state != downloadTargetActive {
		return errors.New("download target cannot be reset")
	}
	return target.operations.resetFile(target.file)
}

func (target *downloadTarget) Install(force bool) (downloadInstallResult, error) {
	return target.install(context.Background(), force)
}

func (target *downloadTarget) InstallContext(ctx context.Context, force bool) (downloadInstallResult, error) {
	if ctx == nil {
		return downloadInstallResult{}, errors.New("download install context is required")
	}
	return target.install(ctx, force)
}

func (target *downloadTarget) install(ctx context.Context, force bool) (downloadInstallResult, error) {
	if target == nil || target.state != downloadTargetActive {
		return downloadInstallResult{}, errors.New("download target cannot be installed")
	}
	if err := target.operations.syncFile(target.file); err != nil {
		return downloadInstallResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return downloadInstallResult{}, err
	}
	if err := target.operations.closeFile(target.file); err != nil {
		return downloadInstallResult{}, err
	}
	target.state = downloadTargetClosed
	if err := ctx.Err(); err != nil {
		return downloadInstallResult{}, err
	}

	var visible bool
	var installErr error
	if force {
		visible, installErr = target.operations.replace(target.temporaryPath, target.destination)
	} else {
		visible, installErr = target.operations.installNoReplace(target.temporaryPath, target.destination)
	}
	if !visible {
		return downloadInstallResult{}, installErr
	}

	// Ownership transfers only after the platform operation explicitly reports
	// visibility. Abort may still clean a fallback source name, but can never
	// remove destination after this terminal transition.
	target.state = downloadTargetInstalled
	parentErr := target.operations.syncParent(target.directory)
	_ = os.Remove(target.temporaryPath)
	result := downloadInstallResult{Visible: true, Durable: parentErr == nil}
	if parentErr != nil && installErr != nil {
		return result, errors.Join(parentErr, installErr)
	}
	if parentErr != nil {
		return result, parentErr
	}
	return result, installErr
}

func (target *downloadTarget) Abort() {
	if target == nil || target.state == downloadTargetAborted {
		return
	}
	if target.state == downloadTargetActive {
		_ = target.file.Close()
	}
	_ = os.Remove(target.temporaryPath)
	if target.state != downloadTargetInstalled {
		target.state = downloadTargetAborted
	}
}

type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }
