//go:build windows

package api

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type downloadFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func atomicInstallDownloadNoReplace(identity *downloadFileIdentity, from, to string) (bool, error) {
	if err := renameHeldDownloadFile(identity, to, false); err != nil {
		return false, &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	if err := windows.FlushFileBuffers(identity.handle); err != nil {
		return true, &os.LinkError{Op: "sync renamed", Old: from, New: to, Err: err}
	}
	return true, nil
}

func atomicReplaceDownload(identity *downloadFileIdentity, from, to string) (bool, error) {
	if err := renameHeldDownloadFile(identity, to, true); err != nil {
		return false, &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	if err := windows.FlushFileBuffers(identity.handle); err != nil {
		return true, &os.LinkError{Op: "sync renamed", Old: from, New: to, Err: err}
	}
	return true, nil
}

func renameHeldDownloadFile(identity *downloadFileIdentity, destination string, replace bool) error {
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	directoryPointer, err := windows.UTF16PtrFromString(filepath.Dir(absolute))
	if err != nil {
		return err
	}
	directory, err := windows.CreateFile(
		directoryPointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(directory)

	name, err := windows.UTF16FromString(filepath.Base(absolute))
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	var dummy downloadFileRenameInformation
	size := int(unsafe.Offsetof(dummy.FileName)) + len(name)*2
	buffer := make([]byte, size)
	information := (*downloadFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	if replace {
		information.ReplaceIfExists = 1
	}
	information.RootDirectory = directory
	information.FileNameLength = uint32(len(name) * 2)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&information.FileName[0]))[:len(name):len(name)], name)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(identity.handle, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}
