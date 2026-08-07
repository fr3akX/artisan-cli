//go:build !windows

package securefile

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenPrivate opens path without following a final symlink and verifies the
// exact opened regular file is inaccessible to group and other users.
func OpenPrivate(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open private file", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(descriptor), path)
	if err := verifyPrivate(file, false); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func openPrivateDirectory(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open private directory", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func protectPrivate(file *os.File, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return verifyPrivate(file, directory)
}

func verifyPrivate(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened private file: %w", err)
	}
	if directory {
		if !info.IsDir() {
			return errors.New("unsafe_private_file: expected a directory")
		}
	} else if !info.Mode().IsRegular() {
		return errors.New("unsafe_private_file: expected a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("unsafe_private_file: mode %#o grants group or other access", info.Mode().Perm())
	}
	return nil
}
