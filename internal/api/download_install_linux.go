//go:build linux

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func atomicInstallDownloadNoReplace(from, to string) (bool, error) {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		if linkErr := os.Link(from, to); linkErr != nil {
			return false, linkErr
		}
		if removeErr := os.Remove(from); removeErr != nil {
			return true, removeErr
		}
		return true, nil
	}
	return false, err
}

func atomicReplaceDownload(from, to string) (bool, error) {
	if err := os.Rename(from, to); err != nil {
		return false, err
	}
	return true, nil
}
