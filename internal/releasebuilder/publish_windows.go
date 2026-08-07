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
	handle, payload   windows.Handle
	info, payloadInfo windows.ByHandleFileInformation
	name, path        string
	ambiguous         bool
}
type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func openDirectoryHandle(path string, access, share uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, access, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}
func handleInfo(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(handle, &info)
	return info, err
}
func validDirectoryInfo(info windows.ByHandleFileInformation) bool {
	return info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
}
func openHeldDist(path string) (*heldDist, error) {
	handle, err := openDirectoryHandle(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
	if err != nil {
		return nil, err
	}
	info, err := handleInfo(handle)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	if !validDirectoryInfo(info) {
		windows.CloseHandle(handle)
		return nil, errors.New("dist handle is reparse or not a directory")
	}
	return &heldDist{handle: handle, info: info, path: path}, nil
}
func (d *heldDist) close() error { return windows.CloseHandle(d.handle) }
func sameWindowsFile(a, b windows.ByHandleFileInformation) bool {
	return a.VolumeSerialNumber == b.VolumeSerialNumber && a.FileIndexHigh == b.FileIndexHigh && a.FileIndexLow == b.FileIndexLow
}
func (d *heldDist) pathMatches() bool {
	other, err := openHeldDist(d.path)
	if err != nil {
		return false
	}
	defer other.close()
	return sameWindowsFile(d.info, other.info)
}
func (d *heldDist) finalExists(name string) (bool, error) {
	_, err := os.Lstat(filepath.Join(d.path, name))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
func (d *heldDist) createStaging() (*heldStage, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name, err := randomStagingName()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(d.path, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, err
		}
		handle, err := openDirectoryHandle(path, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
		if err != nil {
			os.Remove(path)
			return nil, err
		}
		info, err := handleInfo(handle)
		if err != nil || !validDirectoryInfo(info) {
			windows.CloseHandle(handle)
			os.Remove(path)
			if err != nil {
				return nil, err
			}
			return nil, errors.New("staging is reparse or not directory")
		}
		return &heldStage{handle: handle, payload: windows.InvalidHandle, info: info, name: name, path: path}, nil
	}
	return nil, errors.New("could not allocate staging directory")
}
func (s *heldStage) preparePayload() error {
	handle, err := openDirectoryHandle(filepath.Join(s.path, "payload"), windows.DELETE|windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
	if err != nil {
		return err
	}
	info, err := handleInfo(handle)
	if err != nil || !validDirectoryInfo(info) {
		windows.CloseHandle(handle)
		if err != nil {
			return err
		}
		return errors.New("payload is reparse or not directory")
	}
	s.payload = handle
	s.payloadInfo = info
	return nil
}
func (s *heldStage) close() error {
	if s.payload != windows.InvalidHandle {
		_ = windows.CloseHandle(s.payload)
		s.payload = windows.InvalidHandle
	}
	if s.handle == windows.InvalidHandle {
		return nil
	}
	err := windows.CloseHandle(s.handle)
	s.handle = windows.InvalidHandle
	return err
}
func openMatchingDirectory(path string, want windows.ByHandleFileInformation) bool {
	handle, err := openDirectoryHandle(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	info, err := handleInfo(handle)
	return err == nil && validDirectoryInfo(info) && sameWindowsFile(want, info)
}
func (s *heldStage) payloadMatches() bool {
	return s.payload != windows.InvalidHandle && openMatchingDirectory(filepath.Join(s.path, "payload"), s.payloadInfo)
}
func (d *heldDist) publishedMatches(s *heldStage, leaf string) bool {
	return s.payload != windows.InvalidHandle && openMatchingDirectory(filepath.Join(d.path, leaf), s.payloadInfo)
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
	info.RootDirectory = destinationRoot
	info.FileNameLength = uint32(len(name) * 2)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&info.FileName[0]))[:len(name):len(name)], name)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(source, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}
func (d *heldDist) publish(s *heldStage, leaf string, before, after func() error) error {
	if !s.payloadMatches() {
		s.ambiguous = true
		return errors.New("held payload identity does not match staging payload name")
	}
	if before != nil {
		if err := before(); err != nil {
			return err
		}
	}
	if !s.payloadMatches() {
		s.ambiguous = true
		return errors.New("staging payload identity changed immediately before native publish")
	}
	if err := renameHandleNoReplace(s.payload, d.handle, leaf); err != nil {
		return err
	}
	if after != nil {
		if err := after(); err != nil {
			s.ambiguous = true
			return err
		}
	}
	if !d.publishedMatches(s, leaf) {
		s.ambiguous = true
		return errors.New("published final identity is ambiguous: final name does not match verified payload")
	}
	return nil
}
func (d *heldDist) stageMatches(s *heldStage) bool { return openMatchingDirectory(s.path, s.info) }
func (d *heldDist) cleanup(s *heldStage) error {
	if s.ambiguous {
		_ = s.close()
		return errors.New("cleanup skipped because publication identity is ambiguous; safe staging residue retained")
	}
	if !d.stageMatches(s) {
		s.ambiguous = true
		_ = s.close()
		return errors.New("cleanup skipped because staging name no longer matches held staging identity")
	}
	if s.payload != windows.InvalidHandle {
		_ = windows.CloseHandle(s.payload)
		s.payload = windows.InvalidHandle
	}
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(s.path, entry.Name())); err != nil {
			return err
		}
	}
	if !d.stageMatches(s) {
		s.ambiguous = true
		_ = s.close()
		return errors.New("cleanup skipped after staging identity changed")
	}
	deleteOnClose := byte(1)
	if err := windows.SetFileInformationByHandle(s.handle, windows.FileDispositionInfo, &deleteOnClose, 1); err != nil {
		return err
	}
	return s.close()
}
func isAlreadyExists(err error) bool {
	return errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS)
}
