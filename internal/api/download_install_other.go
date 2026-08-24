//go:build !linux && !darwin && !windows

package api

import (
	"errors"
	"os"
	"path/filepath"
)

func (p *heldOtherDownloadPublication) publish(target *downloadTarget, force bool) (downloadInstallResult, error) {
	if err := target.verifyHeldSource(); err != nil {
		return downloadInstallResult{}, err
	}
	if err := p.copyCandidate(target); err != nil {
		return downloadInstallResult{}, err
	}
	if target.operations.afterCandidateVerifiedBeforeNative != nil {
		if err := target.operations.afterCandidateVerifiedBeforeNative(target); err != nil {
			return downloadInstallResult{}, err
		}
	}
	if !p.pathMatches() || !p.namedExact(p.candidate, p.candidateInfo, target.sealedCount, target.sealedDigest) || target.verifyHeldSource() != nil {
		return downloadInstallResult{}, errDownloadIdentityAmbiguous
	}
	candidatePath := filepath.Join(p.parentPath, p.candidate)
	oldInfo, oldErr := os.Lstat(target.destination)
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return downloadInstallResult{}, oldErr
	}
	linkedNoReplace := false
	nativeErr := target.invokeNative(func() error {
		target.nativeOperationErr = func() error {
			if !force {
				return os.Link(candidatePath, target.destination)
			}
			// First use a no-replace link. If the destination is absent this avoids
			// a check/rename clobber race entirely.
			if err := os.Link(candidatePath, target.destination); err == nil {
				linkedNoReplace = true
				return nil
			} else if !errors.Is(err, os.ErrExist) {
				return err
			}
			oldInfo, err := os.Lstat(target.destination)
			if err != nil {
				return err
			}
			backup, err := otherRandomName("." + filepath.Base(target.destination) + ".backup-")
			if err != nil {
				return err
			}
			backupPath := filepath.Join(p.parentPath, backup)
			if err := os.Link(target.destination, backupPath); err != nil {
				return err
			}
			p.backup = backup
			backupInfo, err := os.Lstat(backupPath)
			if err != nil || !os.SameFile(oldInfo, backupInfo) {
				return errors.Join(errors.New("force backup identity could not be verified"), err)
			}
			p.backupInfo = backupInfo
			if target.operations.afterBackupCreatedBeforeReplace != nil {
				if err := target.operations.afterBackupCreatedBeforeReplace(target); err != nil {
					return err
				}
			}
			// Backup fallback preparation can race the remote revision. Re-run
			// the fence immediately before the decisive replacement syscall.
			if err := target.prepareNativeOperation(); err != nil {
				return err
			}
			target.state = downloadTargetNativeAttempted
			target.nativeOperationInvoked = true
			return os.Rename(candidatePath, target.destination)
		}()
		return target.nativeOperationErr
	})
	var hookErr error
	if target.operations.afterNativeBeforeReconcile != nil {
		hookErr = target.operations.afterNativeBeforeReconcile(target)
	}
	if !p.destinationExact(target) || !p.pathMatches() {
		if nativeErr != nil && !target.nativeOperationInvoked && hookErr == nil {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
		}
		if !force && nativeErr != nil && errors.Is(target.nativeOperationErr, os.ErrExist) && hookErr == nil {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, os.ErrExist
		}
		unchanged := false
		if oldErr == nil {
			current, err := os.Lstat(target.destination)
			unchanged = err == nil && os.SameFile(oldInfo, current)
		} else {
			_, err := os.Lstat(target.destination)
			unchanged = errors.Is(err, os.ErrNotExist)
		}
		if nativeErr != nil && hookErr == nil && oldErr == nil && unchanged && p.namedExact(p.candidate, p.candidateInfo, target.sealedCount, target.sealedDigest) {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
		}
		return downloadInstallResult{Publication: publicationAmbiguous, Visibility: visibilityAmbiguous, Durability: durabilityUncertain}, errors.Join(errDownloadIdentityAmbiguous, nativeErr, hookErr)
	}
	result := downloadInstallResult{Publication: publicationExact, Visibility: visibilityExact, Durability: durabilityExact}
	cleanups := []exactDownloadCleanup{
		func() (bool, error) {
			if p.sourceName == "" {
				return false, nil
			}
			err := p.removeOwned(target, p.sourceName, p.sourceInfo)
			if err == nil {
				p.sourceName = ""
				return true, nil
			}
			return false, err
		},
	}
	if !force || linkedNoReplace {
		cleanups = append(cleanups, func() (bool, error) {
			if p.candidate == "" {
				return false, nil
			}
			err := p.removeOwned(target, p.candidate, p.candidateInfo)
			if err == nil {
				p.candidate = ""
				return true, nil
			}
			return false, err
		})
	}
	if p.backup != "" {
		cleanups = append(cleanups, func() (bool, error) {
			err := p.removeOwnedBackup(target, p.backup, p.backupInfo)
			if err == nil {
				p.backup = ""
				return true, nil
			}
			return false, err
		})
	}
	durability, cleanupErr := finishExactDownloadCleanup(func() error { return p.sync(target) }, cleanups...)
	result.Durability = durability
	return result, errors.Join(nativeErr, hookErr, cleanupErr)
}
