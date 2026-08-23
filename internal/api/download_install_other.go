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
	nativeErr := target.operations.nativeOperation(func() error {
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
		backupInfo, err := os.Lstat(backupPath)
		if err != nil || !os.SameFile(oldInfo, backupInfo) {
			return errors.Join(errors.New("force backup identity could not be verified"), err)
		}
		p.backup, p.backupInfo = backup, backupInfo
		return os.Rename(candidatePath, target.destination)
	})
	var hookErr error
	if target.operations.afterNativeBeforeReconcile != nil {
		hookErr = target.operations.afterNativeBeforeReconcile(target)
	}
	if !p.destinationExact(target) || !p.pathMatches() {
		if !force && nativeErr != nil && errors.Is(nativeErr, os.ErrExist) && hookErr == nil {
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
		if nativeErr != nil && hookErr == nil && unchanged && p.namedExact(p.candidate, p.candidateInfo, target.sealedCount, target.sealedDigest) {
			cleanupErr := errors.Join(p.removeOwned(target, p.candidate, p.candidateInfo), p.removeOwned(target, p.sourceName, p.sourceInfo))
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, errors.Join(nativeErr, cleanupErr)
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
		cleanupErr = p.removeOwned(target, p.candidate, p.candidateInfo)
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
