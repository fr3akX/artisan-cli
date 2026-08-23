package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

var (
	errInvalidDownloadDestination = errors.New("invalid download destination")
	errDownloadIdentityAmbiguous  = errors.New("download temporary identity is ambiguous")
)

type downloadOperations struct {
	createTemp       func(string, string) (*os.File, error)
	protect          func(*os.File) error
	writer           func(*os.File) io.Writer
	resetFile        func(*os.File) error
	syncFile         func(*os.File) error
	closeFile        func(*os.File) error
	installNoReplace func(*downloadFileIdentity, string, string) (bool, error)
	replace          func(*downloadFileIdentity, string, string) (bool, error)
	syncParent       func(string) error

	// Test seams bracket the identity checks immediately around native
	// publication and cleanup. Production leaves them nil.
	beforeInstall func(string, string) error
	afterInstall  func(string, string) error
	beforeAbort   func(string) error
}

func defaultDownloadOperations() downloadOperations {
	return downloadOperations{
		createTemp: createDownloadTemp,
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

// downloadTarget owns one protected same-directory temporary file and a
// separately held native identity for it. Its bytes cannot become visible at
// destination until Install performs the final atomic no-replace or replace
// operation and proves the final name refers to that held identity.
type downloadTarget struct {
	destination   string
	directory     string
	temporaryPath string
	file          *os.File
	identity      *downloadFileIdentity
	operations    downloadOperations
	state         downloadTargetState
	ambiguous     bool
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
	identity, err := captureDownloadFileIdentity(file)
	if err != nil {
		_ = file.Close()
		// Without a captured identity, deleting by name could remove a racer's
		// replacement. Retain the ambiguous private residue instead.
		return nil, err
	}
	target.identity = identity
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
	if target.operations.beforeInstall != nil {
		if err := target.operations.beforeInstall(target.temporaryPath, target.destination); err != nil {
			return downloadInstallResult{}, err
		}
	}
	matches, err := target.identity.matches(target.temporaryPath)
	if err != nil || !matches {
		target.ambiguous = true
		return downloadInstallResult{}, errors.Join(errDownloadIdentityAmbiguous, err)
	}

	var visible bool
	var installErr error
	if force {
		visible, installErr = target.operations.replace(target.identity, target.temporaryPath, target.destination)
	} else {
		visible, installErr = target.operations.installNoReplace(target.identity, target.temporaryPath, target.destination)
	}
	if !visible {
		return downloadInstallResult{}, installErr
	}
	if target.operations.afterInstall != nil {
		if err := target.operations.afterInstall(target.temporaryPath, target.destination); err != nil {
			target.ambiguous = true
			return downloadInstallResult{}, err
		}
	}
	matches, identityErr := target.identity.matches(target.destination)
	if identityErr != nil || !matches {
		target.ambiguous = true
		return downloadInstallResult{}, errors.Join(errDownloadIdentityAmbiguous, identityErr, installErr)
	}

	// Ownership transfers only after the final name is proven to identify the
	// held verified file. Abort can never remove destination after this state.
	target.state = downloadTargetInstalled
	cleanupErr := target.removeTemporaryIfOwned()
	parentErr := target.operations.syncParent(target.directory)
	closeErr := target.identity.close()
	result := downloadInstallResult{Visible: true, Durable: parentErr == nil}
	return result, errors.Join(parentErr, installErr, cleanupErr, closeErr)
}

func (target *downloadTarget) removeTemporaryIfOwned() error {
	matches, err := target.identity.matches(target.temporaryPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !matches {
		target.ambiguous = true
		return errors.Join(errDownloadIdentityAmbiguous, err)
	}
	return os.Remove(target.temporaryPath)
}

func (target *downloadTarget) Abort() {
	if target == nil || target.state == downloadTargetAborted || target.state == downloadTargetInstalled {
		return
	}
	if target.state == downloadTargetActive {
		_ = target.file.Close()
		target.state = downloadTargetClosed
	}
	if !target.ambiguous {
		if target.operations.beforeAbort != nil {
			if err := target.operations.beforeAbort(target.temporaryPath); err != nil {
				target.ambiguous = true
			}
		}
		if !target.ambiguous {
			_ = target.removeTemporaryIfOwned()
		}
	}
	_ = target.identity.close()
	target.state = downloadTargetAborted
}

type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }
