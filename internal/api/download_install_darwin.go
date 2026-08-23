//go:build darwin

package api

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func createAnonymousUnixDownloadSource(parentFD int, _ *downloadTarget, prefix string) (int, string, error) {
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

func cloneHeldUnixDownloadSource(sourceFD int, _ os.FileInfo, parentFD int, name string, operations downloadOperations, register func(os.FileInfo) error) error {
	registerExisting := func() (bool, error) {
		fd, openErr := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			return false, nil
		}
		if openErr != nil {
			return false, openErr
		}
		file := os.NewFile(uintptr(fd), name)
		info, statErr := file.Stat()
		closeErr := file.Close()
		return true, errors.Join(statErr, register(info), closeErr)
	}

	if !operations.forceCandidateCopy {
		cloneErr := unix.Fclonefileat(sourceFD, parentFD, name, 0)
		if cloneErr == nil {
			// The clone name now exists but its distinct inode identity has not yet
			// been captured. Track the name conservatively before opening it.
			if err := register(nil); err != nil {
				return err
			}
		}
		created, registerErr := registerExisting()
		if cloneErr == nil {
			return registerErr
		}
		if registerErr != nil {
			// The failed clone may still have created a name that could not be
			// reopened. Track it conservatively even though cleanup cannot infer an
			// identity from the name alone.
			return errors.Join(cloneErr, registerErr, register(nil))
		}
		if created {
			// Even a failed clone can leave a partial candidate. registerExisting
			// captured its identity for the pre-native cleanup path.
			return cloneErr
		}
		if !errors.Is(cloneErr, unix.ENOTSUP) && !errors.Is(cloneErr, unix.EXDEV) && !errors.Is(cloneErr, unix.EINVAL) {
			return cloneErr
		}
	}

	duplicate, duplicateErr := unix.Dup(sourceFD)
	if duplicateErr != nil {
		return duplicateErr
	}
	source := os.NewFile(uintptr(duplicate), "held-source-copy")
	fd, err := unix.Openat(parentFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return errors.Join(err, source.Close())
	}
	candidate := os.NewFile(uintptr(fd), name)
	// O_EXCL created the name. Register it before stat/copy/sync/hash can fail;
	// copyHeldDownloadCandidate replaces the nil identity after a successful stat.
	registerErr := register(nil)
	if registerErr != nil {
		return errors.Join(registerErr, candidate.Close(), source.Close())
	}
	_, _, copyErr := copyHeldDownloadCandidate(source, candidate, register, operations)
	return errors.Join(copyErr, source.Close())
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
		if nativeErr != nil && !target.nativeOperationInvoked && hookErr == nil {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
		}
		if (errors.Is(target.nativeOperationErr, unix.EEXIST) || errors.Is(target.nativeOperationErr, os.ErrExist)) && hookErr == nil && publication.relativeMatches(publication.candidateName, publication.candidateInfo) {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, os.ErrExist
		}
		// A missing requested leaf after rename may mean publication occurred and
		// was removed before reconciliation; it is never proof of no publication.
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
		if nativeErr != nil && !target.nativeOperationInvoked && hookErr == nil {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
		}
		candidateStillExact := publication.relativeMatches(publication.candidateName, publication.candidateInfo)
		oldStillExact := false
		if oldErr == nil {
			oldStillExact = publication.nodeMatches(leaf, oldIdentity)
		} else {
			_, err := publication.relativeNodeIdentity(leaf)
			oldStillExact = errors.Is(err, unix.ENOENT)
		}
		if nativeErr != nil && hookErr == nil && candidateStillExact && oldStillExact {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
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
