//go:build darwin

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func atomicInstallDownloadNoReplace(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		if linkErr := os.Link(from, to); linkErr != nil {
			return linkErr
		}
		_ = os.Remove(from)
		return nil
	}
	return err
}

func atomicReplaceDownload(from, to string) error {
	return os.Rename(from, to)
}
