//go:build linux

package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func createAnonymousUnixDownloadSource(parentFD int, prefix string) (int, string, error) {
	fd, err := unix.Openat(parentFD, ".", unix.O_RDWR|unix.O_CLOEXEC|unix.O_TMPFILE, 0o600)
	if err == nil {
		return fd, "", nil
	}
	// Some filesystems disallow O_TMPFILE. Create relative to the held parent
	// and unlink immediately; the descriptor remains the sole source identity.
	for attempt := 0; attempt < 100; attempt++ {
		name, randomErr := randomDownloadName(prefix)
		if randomErr != nil {
			return -1, "", randomErr
		}
		fd, openErr := unix.Openat(parentFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(openErr, unix.EEXIST) {
			continue
		}
		if openErr != nil {
			return -1, "", openErr
		}
		if unlinkErr := unix.Unlinkat(parentFD, name, 0); unlinkErr != nil {
			_ = unix.Close(fd)
			return -1, "", unlinkErr
		}
		return fd, "", nil
	}
	return -1, "", errors.New("could not allocate held download source")
}

func linkHeldLinuxDescriptor(sourceFD, parentFD int, name string) error {
	err := unix.Linkat(sourceFD, "", parentFD, name, unix.AT_EMPTY_PATH)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}
	return unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", sourceFD), parentFD, name, unix.AT_SYMLINK_FOLLOW)
}

func cloneHeldUnixDownloadSource(sourceFD, parentFD int, name string) error {
	return linkHeldLinuxDescriptor(sourceFD, parentFD, name)
}

func (publication *heldUnixDownloadPublication) publish(target *downloadTarget, force bool) (downloadInstallResult, error) {
	if err := target.verifyHeldSource(); err != nil {
		return downloadInstallResult{}, err
	}
	if !force {
		return publication.publishLinuxNoForce(target)
	}
	return publication.publishLinuxForce(target)
}

func (publication *heldUnixDownloadPublication) publishLinuxNoForce(target *downloadTarget) (downloadInstallResult, error) {
	leaf := filepath.Base(target.destination)
	nativeErr := publication.invokeNative(target, func() error {
		return linkHeldLinuxDescriptor(int(publication.source.Fd()), int(publication.parent.file.Fd()), leaf)
	})
	hookErr := publication.afterNative(target)
	_, exact, probeErr := publication.destinationExact(target)
	if exact {
		return resultAfterUnixExact(publication, target, errors.Join(nativeErr, hookErr, probeErr))
	}
	if nativeErr != nil && isLinuxExist(nativeErr) && hookErr == nil {
		if _, err := publication.relativeNodeIdentity(leaf); err == nil {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, os.ErrExist
		}
	}
	if nativeErr != nil && hookErr == nil && errors.Is(probeErr, unix.ENOENT) {
		return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
	}
	return downloadInstallResult{Publication: publicationAmbiguous, Visibility: visibilityAmbiguous, Durability: durabilityUncertain}, errors.Join(errDownloadIdentityAmbiguous, nativeErr, hookErr, probeErr)
}

func (publication *heldUnixDownloadPublication) publishLinuxForce(target *downloadTarget) (downloadInstallResult, error) {
	if err := publication.createCandidateFromSource(target); err != nil {
		return downloadInstallResult{}, err
	}
	if err := publication.checkCandidateAfterHook(target); err != nil {
		return downloadInstallResult{}, retainedResidueError(publication.candidateName, err)
	}
	leaf := filepath.Base(target.destination)
	oldIdentity, oldErr := publication.relativeNodeIdentity(leaf)
	if oldErr != nil && !errors.Is(oldErr, unix.ENOENT) {
		return downloadInstallResult{}, oldErr
	}
	if oldErr == nil && oldIdentity.mode&unix.S_IFMT == unix.S_IFDIR {
		return downloadInstallResult{}, errInvalidDownloadDestination
	}

	var exchanged bool
	nativeErr := publication.invokeNative(target, func() error {
		if oldErr == nil {
			err := unix.Renameat2(int(publication.parent.file.Fd()), publication.candidateName, int(publication.parent.file.Fd()), leaf, unix.RENAME_EXCHANGE)
			if err == nil {
				exchanged = true
				return nil
			}
			if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
				return err
			}
			return publication.linuxBackupReplace(target, leaf, oldIdentity)
		}
		return unix.Renameat2(int(publication.parent.file.Fd()), publication.candidateName, int(publication.parent.file.Fd()), leaf, unix.RENAME_NOREPLACE)
	})
	hookErr := publication.afterNative(target)
	_, exact, probeErr := publication.destinationExact(target)
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
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, errors.Join(nativeErr, cleanupErr)
		}
		return downloadInstallResult{Publication: publicationAmbiguous, Visibility: visibilityAmbiguous, Durability: durabilityUncertain}, errors.Join(errDownloadIdentityAmbiguous, nativeErr, hookErr, probeErr)
	}

	cleanupErr := error(nil)
	if exchanged {
		if !publication.nodeMatches(publication.candidateName, oldIdentity) {
			cleanupErr = retainedResidueError(publication.candidateName, errDownloadIdentityAmbiguous)
		} else {
			cleanupErr = publication.removeOwnedNode(target, publication.candidateName, oldIdentity)
		}
	} else if publication.backupName != "" {
		// The verified hard-link backup is preserved through the first parent
		// sync, then removed only after double-checking its identity.
		if err := publication.syncParent(target); err != nil {
			return downloadInstallResult{Publication: publicationExact, Visibility: visibilityForUnixParent(publication.parent, true), Durability: durabilityUncertain}, errors.Join(nativeErr, hookErr, err)
		}
		cleanupErr = publication.removeOwnedName(target, publication.backupName, publication.backupInfo)
	}
	return resultAfterUnixExact(publication, target, errors.Join(nativeErr, hookErr, probeErr, cleanupErr))
}

func (publication *heldUnixDownloadPublication) linuxBackupReplace(target *downloadTarget, leaf string, oldIdentity unixDownloadNodeIdentity) error {
	backup, err := randomDownloadName("." + leaf + ".backup-")
	if err != nil {
		return err
	}
	if err := unix.Linkat(int(publication.parent.file.Fd()), leaf, int(publication.parent.file.Fd()), backup, 0); err != nil {
		return err
	}
	backupIdentity, infoErr := publication.relativeNodeIdentity(backup)
	if infoErr != nil || backupIdentity != oldIdentity {
		return errors.Join(errors.New("force backup identity could not be verified"), infoErr)
	}
	backupInfo, infoErr := publication.relativeInfo(backup)
	if infoErr != nil {
		// A backed-up symlink is intentionally retained: opening it without
		// following is impossible through the regular-file cleanup helper.
		return errors.Join(errors.New("force backup is not a regular file; retained"), infoErr)
	}
	publication.backupName, publication.backupInfo = backup, backupInfo
	return unix.Renameat(int(publication.parent.file.Fd()), publication.candidateName, int(publication.parent.file.Fd()), leaf)
}

func isLinuxExist(err error) bool {
	return errors.Is(err, unix.EEXIST) || errors.Is(err, os.ErrExist)
}
