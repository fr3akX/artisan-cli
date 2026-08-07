//go:build linux

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func atomicInstallDownloadNoReplace(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		if linkErr := os.Link(from, to); linkErr != nil {
			return linkErr
		}
		// The destination is already complete and atomically visible. Cleanup is
		// also retried by the caller's unconditional temporary-file defer.
		_ = os.Remove(from)
		return nil
	}
	return err
}

func atomicReplaceDownload(from, to string) error {
	return os.Rename(from, to)
}
