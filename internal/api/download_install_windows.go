//go:build windows

package api

import (
	"os"

	"golang.org/x/sys/windows"
)

func atomicInstallDownloadNoReplace(from, to string) (bool, error) {
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return false, err
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return false, err
	}
	if err := windows.MoveFileEx(fromPointer, toPointer, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return false, &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return true, nil
}

func atomicReplaceDownload(from, to string) (bool, error) {
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return false, err
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return false, err
	}
	if err := windows.MoveFileEx(fromPointer, toPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return false, &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return true, nil
}
