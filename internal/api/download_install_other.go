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
	target.state = downloadTargetNativeAttempted
	target.nativeOperationInvoked = false
	target.nativeOperationErr = nil
	nativeErr := target.operations.nativeOperation(func() error {
		target.nativeOperationInvoked = true
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
	if err := p.sync(target); err != nil {
		result.Durability = durabilityUncertain
		return result, errors.Join(nativeErr, hookErr, err)
	}
	cleanupErr := p.removeOwned(target, p.sourceName, p.sourceInfo)
	if !force || linkedNoReplace {
		cleanupErr = errors.Join(cleanupErr, p.removeOwned(target, p.candidate, p.candidateInfo))
	}
	if p.backup != "" {
		cleanupErr = errors.Join(cleanupErr, p.removeOwned(target, p.backup, p.backupInfo))
	}
	if err := p.sync(target); err != nil {
		result.Durability = durabilityUncertain
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return result, errors.Join(nativeErr, hookErr, cleanupErr)
}
