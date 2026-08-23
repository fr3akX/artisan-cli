//go:build linux || darwin

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// downloadFileIdentity keeps an independent descriptor and immutable file
// identity for the verified temporary through native publication.
type downloadFileIdentity struct {
	file *os.File
	info os.FileInfo
}

func captureDownloadFileIdentity(file *os.File) (*downloadFileIdentity, error) {
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	held := os.NewFile(uintptr(fd), file.Name())
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
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
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
