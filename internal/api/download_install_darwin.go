//go:build darwin

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func createAnonymousUnixDownloadSource(parentFD int, prefix string) (int, string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name, err := randomDownloadName(prefix)
		if err != nil {
			return -1, "", err
		}
		fd, err := unix.Openat(parentFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return -1, "", err
		}
		if err := unix.Unlinkat(parentFD, name, 0); err != nil {
			_ = unix.Close(fd)
			return -1, "", err
		}
		return fd, "", nil
	}
	return -1, "", errors.New("could not allocate held download source")
}

func cloneHeldUnixDownloadSource(sourceFD, parentFD int, name string) error {
	if err := unix.Fclonefileat(sourceFD, parentFD, name, 0); err == nil {
		return nil
	} else if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	candidate := os.NewFile(uintptr(fd), name)
	duplicate, duplicateErr := unix.Dup(sourceFD)
	if duplicateErr != nil {
		_ = candidate.Close()
		return duplicateErr
	}
	source := os.NewFile(uintptr(duplicate), "held-source-copy")
	_, copyErr := io.Copy(candidate, io.NewSectionReader(source, 0, 1<<63-1))
	sourceCloseErr := source.Close()
	syncErr := candidate.Sync()
	closeErr := candidate.Close()
	return errors.Join(copyErr, sourceCloseErr, syncErr, closeErr)
}

func (publication *heldUnixDownloadPublication) publish(target *downloadTarget, force bool) (downloadInstallResult, error) {
	if err := target.verifyHeldSource(); err != nil {
		return downloadInstallResult{}, err
	}
	leaf := filepath.Base(target.destination)
	if !force {
		// Stage the clone/copy under a held identity before the no-replace
		// rename. A direct clone to the destination has no pre-held identity,
		// so an identical-byte competitor could otherwise be mistaken for the
		// object created by a native operation that later reported an error.
		if err := publication.createCandidateFromSource(target); err != nil {
			return downloadInstallResult{}, err
		}
		if err := publication.checkCandidateAfterHook(target); err != nil {
			return downloadInstallResult{}, retainedResidueError(publication.candidateName, err)
		}
		nativeErr := publication.invokeNative(target, func() error {
			return unix.RenameatxNp(int(publication.parent.file.Fd()), publication.candidateName, int(publication.parent.file.Fd()), leaf, unix.RENAME_EXCL)
		})
		hookErr := publication.afterNative(target)
		_, exact, probeErr := publication.destinationExact(target, publication.candidateInfo)
		if exact {
			return resultAfterUnixExact(publication, target, errors.Join(nativeErr, hookErr, probeErr))
		}
		if (errors.Is(nativeErr, unix.EEXIST) || errors.Is(nativeErr, os.ErrExist)) && hookErr == nil {
			cleanupErr := publication.removeOwnedName(target, publication.candidateName, publication.candidateInfo)
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, errors.Join(os.ErrExist, cleanupErr)
		}
		if nativeErr != nil && hookErr == nil && errors.Is(probeErr, unix.ENOENT) {
			cleanupErr := publication.removeOwnedName(target, publication.candidateName, publication.candidateInfo)
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, errors.Join(nativeErr, cleanupErr)
		}
		return downloadInstallResult{Publication: publicationAmbiguous, Visibility: visibilityAmbiguous, Durability: durabilityUncertain}, errors.Join(errDownloadIdentityAmbiguous, nativeErr, hookErr, probeErr)
	}

	if err := publication.createCandidateFromSource(target); err != nil {
		return downloadInstallResult{}, err
	}
	if err := publication.checkCandidateAfterHook(target); err != nil {
		return downloadInstallResult{}, retainedResidueError(publication.candidateName, err)
	}
	oldIdentity, oldErr := publication.relativeNodeIdentity(leaf)
	if oldErr != nil && !errors.Is(oldErr, unix.ENOENT) {
		return downloadInstallResult{}, oldErr
	}
	if oldErr == nil && oldIdentity.mode&unix.S_IFMT == unix.S_IFDIR {
		return downloadInstallResult{}, errInvalidDownloadDestination
	}
	exchanged := oldErr == nil
	nativeErr := publication.invokeNative(target, func() error {
		flag := uint32(unix.RENAME_EXCL)
		if exchanged {
			flag = unix.RENAME_SWAP
		}
		return unix.RenameatxNp(int(publication.parent.file.Fd()), publication.candidateName, int(publication.parent.file.Fd()), leaf, flag)
	})
	hookErr := publication.afterNative(target)
	_, exact, probeErr := publication.destinationExact(target, publication.candidateInfo)
	if !exact {
		candidateStillExact := publication.relativeMatches(publication.candidateName, publication.candidateInfo)
		oldStillExact := false
		if oldErr == nil {
			oldStillExact = publication.nodeMatches(leaf, oldIdentity)
		} else {
			_, err := publication.relativeNodeIdentity(leaf)
			oldStillExact = errors.Is(err, unix.ENOENT)
		}
		if nativeErr != nil && hookErr == nil && candidateStillExact && oldStillExact {
			cleanupErr := publication.removeOwnedName(target, publication.candidateName, publication.candidateInfo)
			return downloadInstallResult{}, errors.Join(nativeErr, cleanupErr)
		}
		return downloadInstallResult{Publication: publicationAmbiguous, Visibility: visibilityAmbiguous, Durability: durabilityUncertain}, errors.Join(errDownloadIdentityAmbiguous, nativeErr, hookErr, probeErr)
	}
	var cleanupErr error
	if exchanged {
		if !publication.nodeMatches(publication.candidateName, oldIdentity) {
			cleanupErr = retainedResidueError(publication.candidateName, errDownloadIdentityAmbiguous)
		} else {
			cleanupErr = publication.removeOwnedNode(target, publication.candidateName, oldIdentity)
		}
	}
	return resultAfterUnixExact(publication, target, errors.Join(nativeErr, hookErr, cleanupErr))
}
