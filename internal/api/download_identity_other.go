//go:build !linux && !darwin && !windows

package api

import (
	"errors"
	"os"
)

// Fallback platforms retain an open descriptor plus the platform FileInfo
// identity. Native publication remains conservative and path based.
type downloadFileIdentity struct {
	file *os.File
	info os.FileInfo
}

func captureDownloadFileIdentity(file *os.File) (*downloadFileIdentity, error) {
	held, err := os.Open(file.Name())
	if err != nil {
		return nil, err
	}
	info, err := held.Stat()
	if err != nil {
		_ = held.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = held.Close()
		return nil, errors.New("download temporary is not a regular file")
	}
	return &downloadFileIdentity{file: held, info: info}, nil
}

func (identity *downloadFileIdentity) matches(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular() && os.SameFile(identity.info, info), nil
}

func (identity *downloadFileIdentity) close() error {
	if identity == nil || identity.file == nil {
		return nil
	}
	err := identity.file.Close()
	identity.file = nil
	return err
}
