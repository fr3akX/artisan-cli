package api

import (
	"errors"
	"os"
)

type exactDownloadCleanup func() (changed bool, err error)

// finishExactDownloadCleanup preserves the first post-publication durability
// error while still attempting every identity-bound namespace cleanup. A
// second sync is required whenever cleanup changed the namespace.
func finishExactDownloadCleanup(syncParent func() error, cleanups ...exactDownloadCleanup) (downloadDurabilityState, error) {
	durability := durabilityExact
	firstSyncErr := syncParent()
	if firstSyncErr != nil {
		durability = durabilityUncertain
	}
	changed := false
	errs := []error{firstSyncErr}
	for _, cleanup := range cleanups {
		if cleanup == nil {
			continue
		}
		cleanupChanged, cleanupErr := cleanup()
		changed = changed || cleanupChanged
		errs = append(errs, cleanupErr)
	}
	if changed {
		finalSyncErr := syncParent()
		if finalSyncErr != nil {
			durability = durabilityUncertain
		}
		errs = append(errs, finalSyncErr)
	}
	return durability, errors.Join(errs...)
}

func ownedDownloadNodeMatches(path string, want os.FileInfo, allowSymlink bool) bool {
	if want == nil || !allowedOwnedDownloadNode(want, allowSymlink) {
		return false
	}
	got, err := os.Lstat(path)
	return err == nil && allowedOwnedDownloadNode(got, allowSymlink) && sameOwnedDownloadNodeKind(got, want) && os.SameFile(got, want)
}

func allowedOwnedDownloadNode(info os.FileInfo, allowSymlink bool) bool {
	return info != nil && (info.Mode().IsRegular() || allowSymlink && info.Mode()&os.ModeSymlink != 0)
}

func sameOwnedDownloadNodeKind(first, second os.FileInfo) bool {
	firstSymlink := first.Mode()&os.ModeSymlink != 0
	secondSymlink := second.Mode()&os.ModeSymlink != 0
	return firstSymlink == secondSymlink && (firstSymlink || first.Mode().IsRegular() && second.Mode().IsRegular())
}

// removeOwnedDownloadNode uses Lstat at both identity boundaries and os.Remove
// only after verifying a regular file or explicitly allowed symlink. It never
// opens or follows a symlink target.
func removeOwnedDownloadNode(path string, want os.FileInfo, allowSymlink bool, beforeSecondCheck func() error) error {
	if !ownedDownloadNodeMatches(path, want, allowSymlink) {
		return errDownloadIdentityAmbiguous
	}
	if beforeSecondCheck != nil {
		if err := beforeSecondCheck(); err != nil {
			return err
		}
	}
	if !ownedDownloadNodeMatches(path, want, allowSymlink) {
		return errDownloadIdentityAmbiguous
	}
	return os.Remove(path)
}
