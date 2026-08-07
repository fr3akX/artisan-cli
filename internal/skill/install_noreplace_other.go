//go:build !linux && !darwin && !windows

package skill

import "golang.org/x/sys/unix"

func renameNoReplaceAt(directoryFD int, from, to string) error {
	return unix.Linkat(directoryFD, from, directoryFD, to, 0)
}
