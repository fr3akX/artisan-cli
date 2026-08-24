//go:build linux || darwin

package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/unix"
)

type heldUnixDownloadParent struct {
	file *os.File
	info os.FileInfo
	path string
}

type unixDownloadNodeIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
}

type heldUnixDownloadPublication struct {
	parent            *heldUnixDownloadParent
	writer            *os.File
	source            *os.File
	sourceInfo        os.FileInfo
	sourceName        string
	candidateName     string
	candidateInfo     os.FileInfo
	backupName        string
	backupInfo        os.FileInfo
	backupIdentity    unixDownloadNodeIdentity
	backupIdentitySet bool
	closed            bool
}

func protectDownloadFile(file *os.File) error { return securefile.ProtectPrivateFile(file) }

func newHeldDownloadPublication(target *downloadTarget) (heldDownloadPublication, error) {
	parent, err := openHeldUnixDownloadParent(target.directory)
	if err != nil {
		return nil, err
	}
	publication := &heldUnixDownloadPublication{parent: parent}
	if target.operations.afterParentHeld != nil {
		if err := target.operations.afterParentHeld(target); err != nil {
			return nil, errors.Join(err, parent.close())
		}
	}
	writer, source, sourceInfo, sourceName, err := createHeldUnixDownloadSource(parent, target, "."+filepath.Base(target.destination)+".tmp-")
	if err != nil {
		return nil, errors.Join(err, parent.close())
	}
	publication.writer, publication.source, publication.sourceInfo, publication.sourceName = writer, source, sourceInfo, sourceName
	return publication, nil
}

func openHeldUnixDownloadParent(path string) (*heldUnixDownloadParent, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		closeErr := file.Close()
		if err != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, errors.Join(errors.New("download destination parent is not a directory"), closeErr)
	}
	return &heldUnixDownloadParent{file: file, info: info, path: path}, nil
}

func (parent *heldUnixDownloadParent) close() error {
	if parent == nil || parent.file == nil {
		return nil
	}
	err := parent.file.Close()
	parent.file = nil
	return err
}

func (parent *heldUnixDownloadParent) pathMatches() bool {
	info, err := os.Lstat(parent.path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(parent.info, info) {
		return false
	}
	other, err := openHeldUnixDownloadParent(parent.path)
	if err != nil {
		return false
	}
	defer other.close()
	return os.SameFile(parent.info, other.info)
}

func createHeldUnixDownloadSource(parent *heldUnixDownloadParent, target *downloadTarget, prefix string) (*os.File, *os.File, os.FileInfo, string, error) {
	if target.operations.createTemp != nil {
		writer, err := target.operations.createTemp(parent.path, prefix+"*")
		if err != nil {
			return nil, nil, nil, "", err
		}
		info, err := writer.Stat()
		name := filepath.Base(writer.Name())
		if err != nil || !info.Mode().IsRegular() {
			cleanupErr := cleanupNewHeldUnixSource(parent, name, info)
			closeErr := writer.Close()
			if err != nil {
				return nil, nil, nil, "", errors.Join(err, cleanupErr, closeErr)
			}
			return nil, nil, nil, "", errors.Join(errors.New("download source is not regular"), cleanupErr, closeErr)
		}
		fd, err := unix.Dup(int(writer.Fd()))
		if err != nil {
			cleanupErr := cleanupNewHeldUnixSource(parent, name, info)
			return nil, nil, nil, "", errors.Join(err, cleanupErr, writer.Close())
		}
		unix.CloseOnExec(fd)
		source := os.NewFile(uintptr(fd), writer.Name())
		return writer, source, info, name, nil
	}
	fd, name, err := createAnonymousUnixDownloadSource(int(parent.file.Fd()), target, prefix)
	if err != nil {
		return nil, nil, nil, "", err
	}
	// Keep the descriptor returned by O_TMPFILE as the publication source;
	// some kernels refuse AT_EMPTY_PATH linking through a duplicate after the
	// original descriptor is closed. The duplicate is the writable os.File.
	source := os.NewFile(uintptr(fd), filepath.Join(parent.path, name))
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		cleanupErr := cleanupNewHeldUnixSource(parent, name, info)
		closeErr := source.Close()
		if err != nil {
			return nil, nil, nil, "", errors.Join(err, cleanupErr, closeErr)
		}
		return nil, nil, nil, "", errors.Join(errors.New("download source is not regular"), cleanupErr, closeErr)
	}
	duplicate, err := unix.Dup(fd)
	if err != nil {
		cleanupErr := cleanupNewHeldUnixSource(parent, name, info)
		return nil, nil, nil, "", errors.Join(err, cleanupErr, source.Close())
	}
	unix.CloseOnExec(duplicate)
	writer := os.NewFile(uintptr(duplicate), source.Name())
	return writer, source, info, name, nil
}

func cleanupNewHeldUnixSource(parent *heldUnixDownloadParent, name string, want os.FileInfo) error {
	if name == "" {
		return nil
	}
	fd, err := unix.Openat(int(parent.file.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(errDownloadIdentityAmbiguous, err)
	}
	file := os.NewFile(uintptr(fd), name)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || want == nil || !os.SameFile(info, want) {
		return errors.Join(errDownloadIdentityAmbiguous, statErr, closeErr)
	}
	return errors.Join(unix.Unlinkat(int(parent.file.Fd()), name, 0), closeErr)
}

func randomDownloadName(prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(bytes[:]), nil
}

func (publication *heldUnixDownloadPublication) writerFile() *os.File     { return publication.writer }
func (publication *heldUnixDownloadPublication) heldSourceFile() *os.File { return publication.source }
func (publication *heldUnixDownloadPublication) temporaryPath() string {
	if publication.sourceName == "" {
		return ""
	}
	return filepath.Join(publication.parent.path, publication.sourceName)
}
func (publication *heldUnixDownloadPublication) closeWriterBeforePublish() bool { return true }

func (publication *heldUnixDownloadPublication) close() error {
	if publication == nil || publication.closed {
		return nil
	}
	publication.closed = true
	var sourceErr error
	if publication.source != nil {
		sourceErr = publication.source.Close()
		publication.source = nil
	}
	return errors.Join(sourceErr, publication.parent.close())
}

func (publication *heldUnixDownloadPublication) abort(target *downloadTarget) error {
	var sourceErr, candidateErr, backupErr error
	if publication.sourceName != "" {
		sourceErr = publication.removeOwnedName(target, publication.sourceName, publication.sourceInfo)
		if sourceErr == nil {
			publication.sourceName = ""
		}
	}
	if publication.candidateName != "" {
		candidateErr = publication.removeOwnedName(target, publication.candidateName, publication.candidateInfo)
		if candidateErr == nil {
			publication.candidateName = ""
		}
	}
	if publication.backupName != "" {
		if publication.backupIdentitySet {
			backupErr = publication.removeOwnedNode(target, publication.backupName, publication.backupIdentity)
		} else if publication.backupInfo != nil {
			backupErr = publication.removeOwnedName(target, publication.backupName, publication.backupInfo)
		} else {
			backupErr = errDownloadIdentityAmbiguous
		}
		if backupErr == nil {
			publication.backupName = ""
		}
	}
	return errors.Join(sourceErr, candidateErr, backupErr)
}

func (publication *heldUnixDownloadPublication) relativeNodeIdentity(name string) (unixDownloadNodeIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(publication.parent.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unixDownloadNodeIdentity{}, err
	}
	return unixDownloadNodeIdentity{device: uint64(stat.Dev), inode: stat.Ino, mode: uint32(stat.Mode)}, nil
}

func (publication *heldUnixDownloadPublication) nodeMatches(name string, want unixDownloadNodeIdentity) bool {
	got, err := publication.relativeNodeIdentity(name)
	return err == nil && got == want
}

func (publication *heldUnixDownloadPublication) removeOwnedNode(target *downloadTarget, name string, want unixDownloadNodeIdentity) error {
	if !publication.nodeMatches(name, want) {
		return errDownloadIdentityAmbiguous
	}
	if target.operations.afterCleanupCheck != nil {
		if err := target.operations.afterCleanupCheck(target, name); err != nil {
			return err
		}
	}
	if !publication.nodeMatches(name, want) {
		return errDownloadIdentityAmbiguous
	}
	if err := unix.Unlinkat(int(publication.parent.file.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func (publication *heldUnixDownloadPublication) relativeInfo(name string) (os.FileInfo, error) {
	fd, err := unix.Openat(int(publication.parent.file.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	return file.Stat()
}

func (publication *heldUnixDownloadPublication) relativeMatches(name string, want os.FileInfo) bool {
	info, err := publication.relativeInfo(name)
	return err == nil && info.Mode().IsRegular() && os.SameFile(info, want)
}

func (publication *heldUnixDownloadPublication) removeOwnedName(target *downloadTarget, name string, want os.FileInfo) error {
	if name == "" {
		return nil
	}
	if want == nil || !publication.relativeMatches(name, want) {
		return errDownloadIdentityAmbiguous
	}
	if target.operations.afterCleanupCheck != nil {
		if err := target.operations.afterCleanupCheck(target, name); err != nil {
			return err
		}
	}
	if !publication.relativeMatches(name, want) {
		return errDownloadIdentityAmbiguous
	}
	if err := unix.Unlinkat(int(publication.parent.file.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func (publication *heldUnixDownloadPublication) openRelative(name string, write bool) (*os.File, os.FileInfo, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if write {
		flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	}
	fd, err := unix.Openat(int(publication.parent.file.Fd()), name, flags, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("download publication object is not regular")
	}
	return file, info, nil
}

func (publication *heldUnixDownloadPublication) verifyRelativeDigest(name string, count int64, digest [32]byte) (os.FileInfo, bool, error) {
	file, info, err := publication.openRelative(name, false)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	gotCount, gotDigest, err := digestDownloadDescriptor(file)
	return info, err == nil && gotCount == count && gotDigest == digest, err
}

func (publication *heldUnixDownloadPublication) createCandidateFromSource(target *downloadTarget) error {
	name, err := randomDownloadName("." + filepath.Base(target.destination) + ".candidate-")
	if err != nil {
		return err
	}
	if err := cloneHeldUnixDownloadSource(int(publication.source.Fd()), publication.sourceInfo, int(publication.parent.file.Fd()), name, target.operations, func(info os.FileInfo) error {
		publication.candidateName, publication.candidateInfo = name, info
		return nil
	}); err != nil {
		return err
	}
	info, exact, err := publication.verifyRelativeDigest(name, target.sealedCount, target.sealedDigest)
	if err != nil || !exact {
		return errors.Join(errDownloadDigestMismatch, err)
	}
	publication.candidateInfo = info
	return nil
}

func (publication *heldUnixDownloadPublication) invokeNative(target *downloadTarget, operation func() error) error {
	return target.invokeNative(operation)
}

func (publication *heldUnixDownloadPublication) afterNative(target *downloadTarget) error {
	if target.operations.afterNativeBeforeReconcile != nil {
		return target.operations.afterNativeBeforeReconcile(target)
	}
	return nil
}

func (publication *heldUnixDownloadPublication) syncParent(target *downloadTarget) error {
	if target.operations.syncParent != nil {
		return target.operations.syncParent(publication.parent.path)
	}
	return publication.parent.file.Sync()
}

func visibilityForUnixParent(parent *heldUnixDownloadParent, exact bool) downloadVisibilityState {
	if !exact {
		return visibilityAmbiguous
	}
	if parent.pathMatches() {
		return visibilityExact
	}
	return visibilityAmbiguous
}

func resultAfterUnixExact(publication *heldUnixDownloadPublication, target *downloadTarget, priorErr error) (downloadInstallResult, error) {
	result := downloadInstallResult{Publication: publicationExact, Visibility: visibilityForUnixParent(publication.parent, true), Durability: durabilityExact}
	var sourceCleanupErr error
	if publication.sourceName != "" {
		sourceCleanupErr = publication.removeOwnedName(target, publication.sourceName, publication.sourceInfo)
	}
	syncErr := publication.syncParent(target)
	if syncErr != nil {
		result.Durability = durabilityUncertain
	}
	return result, errors.Join(priorErr, sourceCleanupErr, syncErr)
}

func (publication *heldUnixDownloadPublication) destinationExact(target *downloadTarget, want os.FileInfo) (os.FileInfo, bool, error) {
	info, digestExact, err := publication.verifyRelativeDigest(filepath.Base(target.destination), target.sealedCount, target.sealedDigest)
	identityExact := err == nil && want != nil && os.SameFile(info, want)
	return info, digestExact && identityExact, err
}

func (publication *heldUnixDownloadPublication) checkCandidateAfterHook(target *downloadTarget) error {
	if target.operations.afterCandidateVerifiedBeforeNative != nil {
		if err := target.operations.afterCandidateVerifiedBeforeNative(target); err != nil {
			return err
		}
	}
	if !publication.relativeMatches(publication.candidateName, publication.candidateInfo) {
		return errDownloadIdentityAmbiguous
	}
	_, exact, err := publication.verifyRelativeDigest(publication.candidateName, target.sealedCount, target.sealedDigest)
	if err != nil || !exact {
		return errors.Join(errDownloadDigestMismatch, err)
	}
	return target.verifyHeldSource()
}

func retainedResidueError(name string, err error) error {
	return errors.Join(fmt.Errorf("download residue %q retained because identity is uncertain", name), err)
}
