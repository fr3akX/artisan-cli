package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
)

var (
	errInvalidDownloadDestination = errors.New("invalid download destination")
	errDownloadIdentityAmbiguous  = errors.New("download publication identity is ambiguous")
	errDownloadDigestMismatch     = errors.New("download held file content does not match the observed stream")
)

type downloadPublicationState uint8

const (
	publicationNone downloadPublicationState = iota
	publicationExact
	publicationAmbiguous
)

type downloadVisibilityState uint8

const (
	visibilityNotVisible downloadVisibilityState = iota
	visibilityExact
	visibilityAmbiguous
)

type downloadDurabilityState uint8

const (
	durabilityNotApplicable downloadDurabilityState = iota
	durabilityExact
	durabilityUncertain
)

type downloadInstallResult struct {
	Publication      downloadPublicationState
	Visibility       downloadVisibilityState
	Durability       downloadDurabilityState
	CleanupUncertain bool
}

func (result downloadInstallResult) Visible() bool {
	return result.Visibility == visibilityExact
}

func (result downloadInstallResult) Durable() bool {
	return result.Visibility == visibilityExact && result.Durability == durabilityExact
}

// downloadOperations contains deterministic fault/race boundaries. Production
// publication itself is always performed by the platform-owned held object.
type downloadOperations struct {
	// createTemp is a test-only source creation override. Platform defaults
	// create relative to the already-held parent.
	createTemp      func(string, string) (*os.File, error)
	protect         func(*os.File) error
	writer          func(*os.File) io.Writer
	resetFile       func(*os.File) error
	syncFile        func(*os.File) error
	closeFile       func(*os.File) error
	flushFile       func(*os.File) error
	flushDirectory  func(*os.File) error
	copyCandidate   func(io.Writer, io.Reader) (int64, error)
	syncCandidate   func(*os.File) error
	digestCandidate func(*os.File) (int64, [sha256.Size]byte, error)

	// Linux-only deterministic capability seams. Other platforms leave these
	// unused while sharing the same operation bundle in common tests.
	openAnonymousSource     func(int) (int, error)
	statLinkedCandidate     func(*os.File) (os.FileInfo, error)
	linkDescriptorEmptyPath func(int, int, string) error
	linkDescriptorProcPath  func(int, int, string) error
	forceBackupReplace      bool
	forceCandidateCopy      bool

	// A non-nil syncParent is a deterministic fault seam; production syncs the
	// retained parent descriptor/handle directly.
	syncParent func(string) error

	afterParentHeld                    func(*downloadTarget) error
	afterCreatedHandleBeforeProtection func(*downloadTarget) error
	afterSealedBeforeCandidate         func(*downloadTarget) error
	afterCandidateVerifiedBeforeNative func(*downloadTarget) error
	afterBackupCreatedBeforeReplace    func(*downloadTarget) error
	afterNativeBeforeReconcile         func(*downloadTarget) error
	afterCleanupCheck                  func(*downloadTarget, string) error

	// nativeOperation can run operation and return a later error, or return an
	// error without running it. Reconciliation, never this return alone,
	// determines publication.
	nativeOperation func(operation func() error) error
}

func defaultDownloadOperations() downloadOperations {
	return downloadOperations{
		protect: func(file *os.File) error { return protectDownloadFile(file) },
		writer:  func(file *os.File) io.Writer { return file },
		resetFile: func(file *os.File) error {
			if err := file.Truncate(0); err != nil {
				return err
			}
			_, err := file.Seek(0, io.SeekStart)
			return err
		},
		syncFile:        func(file *os.File) error { return file.Sync() },
		closeFile:       func(file *os.File) error { return file.Close() },
		copyCandidate:   io.Copy,
		syncCandidate:   func(file *os.File) error { return file.Sync() },
		digestCandidate: digestDownloadDescriptor,
		nativeOperation: func(operation func() error) error { return operation() },
	}
}

type downloadTargetState uint8

const (
	downloadTargetActive downloadTargetState = iota
	downloadTargetSealed
	downloadTargetNativeAttempted
	downloadTargetInstalledExact
	downloadTargetTerminalNone
	downloadTargetTerminalAmbiguous
	downloadTargetAborted
)

type heldDownloadPublication interface {
	writerFile() *os.File
	heldSourceFile() *os.File
	temporaryPath() string
	closeWriterBeforePublish() bool
	publish(*downloadTarget, bool) (downloadInstallResult, error)
	abort(*downloadTarget) error
	close() error
}

// downloadTarget observes the exact successful write stream and delegates all
// namespace operations to a platform object retaining the source and parent.
type downloadTarget struct {
	destination            string
	directory              string
	temporaryPath          string
	operations             downloadOperations
	platform               heldDownloadPublication
	state                  downloadTargetState
	observer               *observedTargetWriter
	sealedCount            int64
	sealedDigest           [sha256.Size]byte
	writerClosed           bool
	nativeOperationInvoked bool
	nativeOperationErr     error
	preNativeCallback      func() error
}

func newDownloadTarget(destination string, force bool, operations downloadOperations) (*downloadTarget, error) {
	base := filepath.Base(destination)
	if destination == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return nil, errInvalidDownloadDestination
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Lstat(absolute); statErr == nil {
		if !force {
			return nil, os.ErrExist
		}
		if info.IsDir() {
			return nil, errInvalidDownloadDestination
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	target := &downloadTarget{
		destination: absolute,
		directory:   filepath.Dir(absolute),
		operations:  operations,
		state:       downloadTargetActive,
	}
	platform, err := newHeldDownloadPublication(target)
	if err != nil {
		return nil, err
	}
	target.platform = platform
	target.temporaryPath = platform.temporaryPath()
	if operations.afterCreatedHandleBeforeProtection != nil {
		if err := operations.afterCreatedHandleBeforeProtection(target); err != nil {
			cleanupErr := target.abortOwned()
			return nil, errors.Join(err, cleanupErr)
		}
	}
	if err := operations.protect(platform.writerFile()); err != nil {
		cleanupErr := target.abortOwned()
		return nil, errors.Join(err, cleanupErr)
	}
	target.observer = &observedTargetWriter{destination: operations.writer(platform.writerFile()), hash: sha256.New()}
	return target, nil
}

func (target *downloadTarget) heldSourceFile() *os.File {
	if target == nil || target.platform == nil {
		return nil
	}
	return target.platform.heldSourceFile()
}

func (target *downloadTarget) Writer() io.Writer {
	if target == nil || target.state != downloadTargetActive {
		return failingWriter{err: errors.New("download target is not writable")}
	}
	return target.observer
}

func (target *downloadTarget) Reset() error {
	if target == nil || target.state != downloadTargetActive {
		return errors.New("download target cannot be reset")
	}
	if err := target.operations.resetFile(target.platform.writerFile()); err != nil {
		return err
	}
	target.observer.reset()
	return nil
}

func (target *downloadTarget) Install(force bool) (downloadInstallResult, error) {
	return target.install(context.Background(), force, nil)
}

func (target *downloadTarget) InstallContext(ctx context.Context, force bool) (downloadInstallResult, error) {
	return target.InstallContextBeforeNative(ctx, force, nil)
}

// InstallContextBeforeNative installs through the ordinary protected target
// lifecycle and invokes callback only after sealing and platform candidate
// preparation, immediately before the native namespace operation. A callback
// error guarantees that no native publication operation is attempted.
func (target *downloadTarget) InstallContextBeforeNative(ctx context.Context, force bool, callback func() error) (downloadInstallResult, error) {
	if ctx == nil {
		return downloadInstallResult{}, errors.New("download install context is required")
	}
	return target.install(ctx, force, callback)
}

func (target *downloadTarget) install(ctx context.Context, force bool, callback func() error) (result downloadInstallResult, returnErr error) {
	if target == nil || target.state != downloadTargetActive {
		return result, errors.New("download target cannot be installed")
	}
	defer func() {
		if returnErr != nil && (target.state == downloadTargetActive || target.state == downloadTargetSealed) {
			returnErr = errors.Join(returnErr, target.abortOwned())
		}
	}()
	if err := target.operations.syncFile(target.platform.writerFile()); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	count, digest, err := digestDownloadDescriptor(target.platform.heldSourceFile())
	if err != nil || count != target.observer.count || digest != target.observer.digest() {
		return result, errors.Join(errDownloadDigestMismatch, err)
	}
	target.sealedCount, target.sealedDigest = count, digest
	target.state = downloadTargetSealed
	target.preNativeCallback = callback
	if target.platform.closeWriterBeforePublish() {
		if err := target.closeWriter(); err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if target.operations.afterSealedBeforeCandidate != nil {
		if err := target.operations.afterSealedBeforeCandidate(target); err != nil {
			return result, err
		}
	}
	// Re-read immediately after the seal boundary. This catches same-UID
	// in-place mutation before any namespace operation is attempted.
	if err := target.verifyHeldSource(); err != nil {
		return result, err
	}
	result, returnErr = target.platform.publish(target, force)
	var cleanupErr error
	switch result.Publication {
	case publicationExact:
		target.state = downloadTargetInstalledExact
	case publicationNone:
		// A reconciled no-publication result is terminal and non-retryable, but
		// it still authorizes identity-bound cleanup of every definitely-owned
		// source, candidate, and backup.
		target.state = downloadTargetTerminalNone
		cleanupErr = target.platform.abort(target)
	case publicationAmbiguous:
		target.state = downloadTargetTerminalAmbiguous
	default:
		if target.state == downloadTargetNativeAttempted {
			target.state = downloadTargetTerminalAmbiguous
		}
	}
	closeErr := target.closeAll()
	if result.Publication == publicationNone && (cleanupErr != nil || closeErr != nil) {
		// Keep fence/API errors distinguishable from a fence that also left
		// local cleanup or descriptor-close uncertainty.
		result.CleanupUncertain = true
	}
	return result, errors.Join(returnErr, cleanupErr, closeErr)
}

func (target *downloadTarget) prepareNativeOperation() error {
	target.nativeOperationInvoked = false
	target.nativeOperationErr = nil
	if target.preNativeCallback != nil {
		return target.preNativeCallback()
	}
	return nil
}

func (target *downloadTarget) invokeNative(operation func() error) error {
	if err := target.prepareNativeOperation(); err != nil {
		return err
	}
	target.state = downloadTargetNativeAttempted
	return target.operations.nativeOperation(func() error {
		target.nativeOperationInvoked = true
		target.nativeOperationErr = operation()
		return target.nativeOperationErr
	})
}

func (target *downloadTarget) verifyHeldSource() error {
	count, digest, err := digestDownloadDescriptor(target.platform.heldSourceFile())
	if err != nil || count != target.sealedCount || digest != target.sealedDigest {
		return errors.Join(errDownloadDigestMismatch, err)
	}
	return nil
}

func (target *downloadTarget) closeWriter() error {
	if target.writerClosed || target.platform == nil || target.platform.writerFile() == nil {
		return nil
	}
	target.writerClosed = true
	file := target.platform.writerFile()
	err := target.operations.closeFile(file)
	if err != nil {
		// Fault seams and partial close failures must not turn an error path into
		// a descriptor leak. A second Close is harmless when the first one did
		// close the os.File and otherwise closes the exact writer handle.
		_ = file.Close()
	}
	return err
}

func (target *downloadTarget) closeAll() error {
	if target == nil || target.platform == nil {
		return nil
	}
	return errors.Join(target.closeWriter(), target.platform.close())
}

func (target *downloadTarget) abortOwned() error {
	if target == nil || target.state == downloadTargetAborted || target.state == downloadTargetInstalledExact {
		return nil
	}
	var cleanupErr error
	// Ambiguity never authorizes destructive cleanup. Pre-native states do;
	// terminal-none was already cleaned synchronously before handles closed.
	if target.state != downloadTargetNativeAttempted && target.state != downloadTargetTerminalNone && target.state != downloadTargetTerminalAmbiguous && target.platform != nil {
		cleanupErr = target.platform.abort(target)
	}
	closeErr := target.closeAll()
	target.state = downloadTargetAborted
	return errors.Join(cleanupErr, closeErr)
}

func (target *downloadTarget) Abort() { _ = target.abortOwned() }

func digestDownloadDescriptor(file *os.File) (int64, [sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if file == nil {
		return 0, digest, errors.New("download held source is closed")
	}
	hasher := sha256.New()
	count, err := io.Copy(hasher, io.NewSectionReader(file, 0, 1<<63-1))
	if err != nil {
		return count, digest, err
	}
	copy(digest[:], hasher.Sum(nil))
	return count, digest, nil
}

type observedTargetWriter struct {
	destination io.Writer
	hash        hash.Hash
	count       int64
}

func (writer *observedTargetWriter) Write(buffer []byte) (int, error) {
	count, err := writer.destination.Write(buffer)
	if count > 0 {
		_, _ = writer.hash.Write(buffer[:count])
		writer.count += int64(count)
	}
	if err == nil && count != len(buffer) {
		err = io.ErrShortWrite
	}
	return count, err
}

func (writer *observedTargetWriter) digest() [sha256.Size]byte {
	var digest [sha256.Size]byte
	copy(digest[:], writer.hash.Sum(nil))
	return digest
}

func (writer *observedTargetWriter) reset() {
	writer.hash.Reset()
	writer.count = 0
}

type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }
