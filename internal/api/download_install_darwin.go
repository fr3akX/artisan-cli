//go:build darwin

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func atomicInstallDownloadNoReplace(_ *downloadFileIdentity, from, to string) (bool, error) {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
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

func atomicReplaceDownload(_ *downloadFileIdentity, from, to string) (bool, error) {
	if err := os.Rename(from, to); err != nil {
		return false, err
	}
	return true, nil
}
