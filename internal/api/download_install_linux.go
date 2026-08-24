//go:build linux

package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func createAnonymousUnixDownloadSource(parentFD int, target *downloadTarget, prefix string) (int, string, error) {
	var fd int
	var err error
	if target.operations.openAnonymousSource != nil {
		fd, err = target.operations.openAnonymousSource(parentFD)
	} else {
		fd, err = unix.Openat(parentFD, ".", unix.O_RDWR|unix.O_CLOEXEC|unix.O_TMPFILE, 0o600)
	}
	if err == nil {
		return fd, "", nil
	}
	// Some filesystems disallow O_TMPFILE. Retain the protected random source
	// name: an ordinary inode cannot be unlinked and later relinked reliably via
	// AT_EMPTY_PATH without elevated capabilities.
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
		return fd, name, nil
	}
	return -1, "", errors.New("could not allocate held download source")
}

func linkHeldLinuxDescriptorEmptyPath(sourceFD, parentFD int, name string, operations downloadOperations) error {
	if operations.linkDescriptorEmptyPath != nil {
		return operations.linkDescriptorEmptyPath(sourceFD, parentFD, name)
	}
	return unix.Linkat(sourceFD, "", parentFD, name, unix.AT_EMPTY_PATH)
}

func linkHeldLinuxDescriptorProcPath(sourceFD, parentFD int, name string, operations downloadOperations) error {
	if operations.linkDescriptorProcPath != nil {
		return operations.linkDescriptorProcPath(sourceFD, parentFD, name)
	}
	return unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", sourceFD), parentFD, name, unix.AT_SYMLINK_FOLLOW)
}

func linuxDescriptorLinkFallbackEligible(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP)
}

func linkHeldLinuxDescriptor(sourceFD, parentFD int, name string, operations downloadOperations) error {
	err := linkHeldLinuxDescriptorEmptyPath(sourceFD, parentFD, name, operations)
	if err == nil || !linuxDescriptorLinkFallbackEligible(err) {
		return err
	}
	return linkHeldLinuxDescriptorProcPath(sourceFD, parentFD, name, operations)
}

func cloneHeldUnixDownloadSource(sourceFD int, sourceInfo os.FileInfo, parentFD int, name string, operations downloadOperations, register func(os.FileInfo) error) error {
	if err := linkHeldLinuxDescriptor(sourceFD, parentFD, name, operations); err != nil {
		return err
	}
	// A hard link has the exact source identity. Register it before any later
	// open/stat operation can fail so pre-native cleanup always owns the name.
	if err := register(sourceInfo); err != nil {
		return err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	var info os.FileInfo
	var statErr error
	if operations.statLinkedCandidate != nil {
		info, statErr = operations.statLinkedCandidate(file)
	} else {
		info, statErr = file.Stat()
	}
	var registerErr error
	if statErr == nil {
		// Never overwrite the known hard-link identity with nil after a later
		// confirming stat failure. The initial source identity remains enough
		// for conservative name revalidation and cleanup.
		registerErr = register(info)
	}
	closeErr := file.Close()
	return errors.Join(statErr, registerErr, closeErr)
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
	sourceFD, parentFD := int(publication.source.Fd()), int(publication.parent.file.Fd())
	nativeErr := publication.invokeNative(target, func() error {
		return linkHeldLinuxDescriptorEmptyPath(sourceFD, parentFD, leaf, target.operations)
	})
	if target.nativeOperationInvoked && linuxDescriptorLinkFallbackEligible(target.nativeOperationErr) {
		// The procfs capability fallback is a separate namespace operation. Run
		// the publication fence again immediately before its decisive linkat.
		// The expected capability error is superseded by this attempt's result.
		nativeErr = publication.invokeNative(target, func() error {
			return linkHeldLinuxDescriptorProcPath(sourceFD, parentFD, leaf, target.operations)
		})
	}
	hookErr := publication.afterNative(target)
	_, exact, probeErr := publication.destinationExact(target, publication.sourceInfo)
	if exact {
		return resultAfterUnixExact(publication, target, errors.Join(nativeErr, hookErr, probeErr))
	}
	if nativeErr != nil && !target.nativeOperationInvoked && hookErr == nil {
		return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
	}
	if nativeErr != nil && isLinuxExist(target.nativeOperationErr) && hookErr == nil {
		if _, err := publication.relativeNodeIdentity(leaf); err == nil {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, os.ErrExist
		}
	}
	// Once linkat may have executed, a missing requested leaf cannot prove that
	// publication never occurred; it may have been removed before reconciliation.
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
			err := error(unix.EOPNOTSUPP)
			if !target.operations.forceBackupReplace {
				err = unix.Renameat2(int(publication.parent.file.Fd()), publication.candidateName, int(publication.parent.file.Fd()), leaf, unix.RENAME_EXCHANGE)
			}
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

	cleanupErr := error(nil)
	if exchanged {
		if !publication.nodeMatches(publication.candidateName, oldIdentity) {
			cleanupErr = retainedResidueError(publication.candidateName, errDownloadIdentityAmbiguous)
		} else {
			cleanupErr = publication.removeOwnedNode(target, publication.candidateName, oldIdentity)
		}
	} else if publication.backupName != "" {
		// Preserve the verified backup through the first sync, but do not let a
		// sync failure skip identity-bound backup/source cleanup. Every changed
		// namespace is followed by one final parent sync.
		cleanups := []exactDownloadCleanup{
			func() (bool, error) {
				err := publication.removeOwnedName(target, publication.backupName, publication.backupInfo)
				if err == nil {
					publication.backupName = ""
					return true, nil
				}
				return false, err
			},
		}
		if publication.sourceName != "" {
			cleanups = append(cleanups, func() (bool, error) {
				err := publication.removeOwnedName(target, publication.sourceName, publication.sourceInfo)
				if err == nil {
					publication.sourceName = ""
					return true, nil
				}
				return false, err
			})
		}
		durability, finishErr := finishExactDownloadCleanup(func() error { return publication.syncParent(target) }, cleanups...)
		result := downloadInstallResult{Publication: publicationExact, Visibility: visibilityForUnixParent(publication.parent, true), Durability: durability}
		return result, errors.Join(nativeErr, hookErr, probeErr, finishErr)
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
	publication.backupName = backup
	backupIdentity, infoErr := publication.relativeNodeIdentity(backup)
	if infoErr == nil {
		publication.backupIdentity, publication.backupIdentitySet = backupIdentity, true
	}
	if infoErr != nil || backupIdentity != oldIdentity {
		return errors.Join(errors.New("force backup identity could not be verified"), infoErr)
	}
	backupInfo, infoErr := publication.relativeInfo(backup)
	if infoErr != nil {
		// A backed-up symlink is intentionally retained: opening it without
		// following is impossible through the regular-file cleanup helper.
		return errors.Join(errors.New("force backup is not a regular file; retained"), infoErr)
	}
	publication.backupInfo = backupInfo
	if target.operations.afterBackupCreatedBeforeReplace != nil {
		if err := target.operations.afterBackupCreatedBeforeReplace(target); err != nil {
			return err
		}
	}
	// Renameat2 capability fallback performs additional preparation. Re-run the
	// fence at the decisive replacement boundary after that preparation.
	if err := target.prepareNativeOperation(); err != nil {
		return err
	}
	target.state = downloadTargetNativeAttempted
	target.nativeOperationInvoked = true
	return unix.Renameat(int(publication.parent.file.Fd()), publication.candidateName, int(publication.parent.file.Fd()), leaf)
}

func isLinuxExist(err error) bool {
	return errors.Is(err, unix.EEXIST) || errors.Is(err, os.ErrExist)
}
