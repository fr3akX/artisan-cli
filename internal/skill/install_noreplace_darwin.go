//go:build darwin

package skill

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplaceAt(directoryFD int, from, to string) error {
	err := unix.RenameatxNp(directoryFD, from, directoryFD, to, unix.RENAME_EXCL)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	return unix.Linkat(directoryFD, from, directoryFD, to, 0)
}
