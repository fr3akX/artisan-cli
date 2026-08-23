//go:build windows

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// downloadFileIdentity retains a DELETE-capable handle and Windows file ID so
// publication renames the verified object rather than reopening its path.
type downloadFileIdentity struct {
	handle windows.Handle
	info   windows.ByHandleFileInformation
}

func captureDownloadFileIdentity(file *os.File) (*downloadFileIdentity, error) {
	path, err := windows.UTF16PtrFromString(file.Name())
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	info, err := downloadWindowsFileInfo(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &downloadFileIdentity{handle: handle, info: info}, nil
}

func downloadWindowsFileInfo(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return info, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return info, errors.New("download temporary is reparse or not a regular file")
	}
	return info, nil
}

func sameDownloadWindowsFile(left, right windows.ByHandleFileInformation) bool {
	return left.VolumeSerialNumber == right.VolumeSerialNumber && left.FileIndexHigh == right.FileIndexHigh && left.FileIndexLow == right.FileIndexLow
}

func (identity *downloadFileIdentity) matches(path string) (bool, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle)
	info, err := downloadWindowsFileInfo(handle)
	return err == nil && sameDownloadWindowsFile(identity.info, info), err
}

func (identity *downloadFileIdentity) close() error {
	if identity == nil || identity.handle == windows.InvalidHandle {
		return nil
	}
	err := windows.CloseHandle(identity.handle)
	identity.handle = windows.InvalidHandle
	return err
}
