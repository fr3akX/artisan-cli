//go:build windows

package releasebuilder

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type heldDist struct {
	handle windows.Handle
	info   windows.ByHandleFileInformation
	path   string
}

type heldStage struct {
	handle  windows.Handle
	payload windows.Handle
	name    string
	path    string
}

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func openDirectoryHandle(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func openHeldDist(path string) (*heldDist, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Deliberately omit FILE_SHARE_DELETE so the requested dist directory cannot
	// be renamed or replaced while path-based build work is in progress.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("dist handle is reparse or not a directory")
	}
	return &heldDist{handle: handle, info: info, path: path}, nil
}

func (directory *heldDist) close() error { return windows.CloseHandle(directory.handle) }
func sameWindowsFile(a, b windows.ByHandleFileInformation) bool {
	return a.VolumeSerialNumber == b.VolumeSerialNumber && a.FileIndexHigh == b.FileIndexHigh && a.FileIndexLow == b.FileIndexLow
}
func (directory *heldDist) pathMatches() bool {
	other, err := openHeldDist(directory.path)
	if err != nil {
		return false
	}
	defer other.close()
	return sameWindowsFile(directory.info, other.info)
}
func (directory *heldDist) finalExists(name string) (bool, error) {
	_, err := os.Lstat(filepath.Join(directory.path, name))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
func (directory *heldDist) createStaging() (*heldStage, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name, err := randomStagingName()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(directory.path, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, err
		}
		handle, err := openDirectoryHandle(path, windows.GENERIC_READ)
		if err != nil {
			os.Remove(path)
			return nil, err
		}
		return &heldStage{handle: handle, payload: windows.InvalidHandle, name: name, path: path}, nil
	}
	return nil, errors.New("could not allocate staging directory")
}
func (stage *heldStage) preparePayload() error {
	handle, err := openDirectoryHandle(filepath.Join(stage.path, "payload"), windows.DELETE|windows.GENERIC_READ)
	if err != nil {
		return err
	}
	stage.payload = handle
	return nil
}
func (stage *heldStage) close() error {
	if stage.payload != windows.InvalidHandle {
		_ = windows.CloseHandle(stage.payload)
		stage.payload = windows.InvalidHandle
	}
	return windows.CloseHandle(stage.handle)
}
func renameHandleNoReplace(source, destinationRoot windows.Handle, destination string) error {
	name, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	var dummy fileRenameInformation
	size := int(unsafe.Offsetof(dummy.FileName)) + len(name)*2
	buffer := make([]byte, size)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.ReplaceIfExists = 0
	info.RootDirectory = destinationRoot
	info.FileNameLength = uint32(len(name) * 2)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&info.FileName[0]))[:len(name):len(name)], name)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(source, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}
func (directory *heldDist) publish(stage *heldStage, leaf string) error {
	return renameHandleNoReplace(stage.payload, directory.handle, leaf)
}
func (directory *heldDist) rollback(stage *heldStage, leaf string) error {
	return errors.New("rollback after dist identity change is unavailable on Windows")
}
func (directory *heldDist) cleanup(stage *heldStage) error {
	_ = stage.close()
	if directory.pathMatches() {
		return os.RemoveAll(stage.path)
	}
	return nil
}
func isAlreadyExists(err error) bool {
	return errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS)
}
