//go:build !windows

package auth

import (
	"errors"
	"fmt"
	"os"
)

func preparePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func applyPrivatePermissions(path string) error {
	return os.Chmod(path, 0o600)
}

func verifyPrivatePermissions(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("inspect credentials: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("unsafe_credentials: credential path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("unsafe_credentials: credential mode %#o grants group or other access", info.Mode().Perm())
	}
	return nil
}
