//go:build linux

package skill

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplaceAt(directoryFD int, from, to string) error {
	return renameNoReplaceAtWithOperations(directoryFD, from, to, unix.Renameat2, unix.Linkat)
}

func renameNoReplaceAtWithOperations(
	directoryFD int,
	from, to string,
	renameat2 func(int, string, int, string, uint) error,
	linkat func(int, string, int, string, int) error,
) error {
	err := renameat2(directoryFD, from, directoryFD, to, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}
	return linkat(directoryFD, from, directoryFD, to, 0)
}
