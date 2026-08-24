//go:build windows

package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/windows"
)

type heldWindowsDownloadPublication struct {
	parent     windows.Handle
	parentInfo windows.ByHandleFileInformation
	parentPath string
	writer     *os.File
	source     *os.File
	sourceInfo windows.ByHandleFileInformation
	sourceName string
	closed     bool
}

func protectDownloadFile(file *os.File) error { return securefile.ProtectPrivateFile(file) }

func newHeldDownloadPublication(target *downloadTarget) (heldDownloadPublication, error) {
	parent, info, err := openWindowsDownloadParent(target.directory)
	if err != nil {
		return nil, err
	}
	if target.operations.afterParentHeld != nil {
		if err := target.operations.afterParentHeld(target); err != nil {
			return nil, errors.Join(err, windows.CloseHandle(parent))
		}
	}
	writer, name, err := createWindowsDownloadSource(parent, target, "."+filepath.Base(target.destination)+".tmp-")
	if err != nil {
		return nil, errors.Join(err, windows.CloseHandle(parent))
	}
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(process, windows.Handle(writer.Fd()), process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		cleanupErr := disposeWindowsDownloadHandle(windows.Handle(writer.Fd()))
		return nil, errors.Join(err, cleanupErr, writer.Close(), windows.CloseHandle(parent))
	}
	source := os.NewFile(uintptr(duplicate), writer.Name())
	sourceInfo, err := windowsDownloadInfo(duplicate)
	if err != nil {
		cleanupErr := disposeWindowsDownloadHandle(duplicate)
		return nil, errors.Join(err, cleanupErr, source.Close(), writer.Close(), windows.CloseHandle(parent))
	}
	return &heldWindowsDownloadPublication{parent: parent, parentInfo: info, parentPath: target.directory, writer: writer, source: source, sourceInfo: sourceInfo, sourceName: name}, nil
}

func openWindowsDownloadParent(path string) (windows.Handle, windows.ByHandleFileInformation, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, err
	}
	info, err := windowsDownloadInfo(handle)
	if err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		closeErr := windows.CloseHandle(handle)
		if err != nil {
			return windows.InvalidHandle, info, errors.Join(err, closeErr)
		}
		return windows.InvalidHandle, info, errors.Join(errors.New("download parent is not a directory"), closeErr)
	}
	return handle, info, nil
}

func createWindowsDownloadSource(parent windows.Handle, target *downloadTarget, prefix string) (*os.File, string, error) {
	if target.operations.createTemp != nil {
		file, err := target.operations.createTemp(target.directory, prefix+"*")
		return file, filepath.Base(fileName(file)), err
	}
	for attempt := 0; attempt < 100; attempt++ {
		var value [16]byte
		if _, err := rand.Read(value[:]); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(value[:])
		objectName, err := windows.NewNTUnicodeString(name)
		if err != nil {
			return nil, "", err
		}
		attributes := &windows.OBJECT_ATTRIBUTES{
			Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
			RootDirectory: parent,
			ObjectName:    objectName,
			Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		}
		var status windows.IO_STATUS_BLOCK
		var handle windows.Handle
		err = windows.NtCreateFile(&handle,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL|windows.WRITE_DAC|windows.SYNCHRONIZE,
			attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			windows.FILE_CREATE,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_WRITE_THROUGH|windows.FILE_SYNCHRONOUS_IO_NONALERT,
			0, 0)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return os.NewFile(uintptr(handle), filepath.Join(target.directory, name)), name, nil
	}
	return nil, "", errors.New("could not allocate held download source")
}

func fileName(file *os.File) string {
	if file == nil {
		return ""
	}
	return file.Name()
}
func windowsDownloadInfo(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return info, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return info, errors.New("download object is a reparse point")
	}
	return info, nil
}
func sameWindowsDownloadInfo(a, b windows.ByHandleFileInformation) bool {
	return a.VolumeSerialNumber == b.VolumeSerialNumber && a.FileIndexHigh == b.FileIndexHigh && a.FileIndexLow == b.FileIndexLow
}
func (p *heldWindowsDownloadPublication) writerFile() *os.File     { return p.writer }
func (p *heldWindowsDownloadPublication) heldSourceFile() *os.File { return p.source }
func (p *heldWindowsDownloadPublication) temporaryPath() string {
	return filepath.Join(p.parentPath, p.sourceName)
}
func (p *heldWindowsDownloadPublication) closeWriterBeforePublish() bool { return true }
func (p *heldWindowsDownloadPublication) close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var sourceErr error
	if p.source != nil {
		sourceErr = p.source.Close()
		p.source = nil
	}
	return errors.Join(sourceErr, windows.CloseHandle(p.parent))
}
func (p *heldWindowsDownloadPublication) pathMatches() bool {
	handle, info, err := openWindowsDownloadParent(p.parentPath)
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(handle)
	return sameWindowsDownloadInfo(p.parentInfo, info)
}
func openWindowsDownloadRelative(parent windows.Handle, name string, access, share uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: parent,
		ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, attributes, &status, nil,
		windows.FILE_ATTRIBUTE_NORMAL, share, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0, 0)
	return handle, err
}

func (p *heldWindowsDownloadPublication) flushDirectory(target *downloadTarget) error {
	if target.operations.flushDirectory == nil {
		return windows.FlushFileBuffers(p.parent)
	}
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(process, p.parent, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return err
	}
	directory := os.NewFile(uintptr(duplicate), p.parentPath)
	return errors.Join(target.operations.flushDirectory(directory), directory.Close())
}

func (p *heldWindowsDownloadPublication) sourceNameMatches() bool {
	handle, err := openWindowsDownloadRelative(p.parent, p.sourceName, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	info, err := windowsDownloadInfo(handle)
	return err == nil && sameWindowsDownloadInfo(info, p.sourceInfo)
}
func (p *heldWindowsDownloadPublication) abort(target *downloadTarget) error {
	return disposeWindowsDownloadHandle(windows.Handle(p.source.Fd()))
}

func disposeWindowsDownloadHandle(handle windows.Handle) error {
	// Disposition is applied to the exact retained handle, never a name.
	flags := uint32(0x1 | 0x2 | 0x8) // DELETE | POSIX_SEMANTICS | ON_CLOSE
	err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfoEx, (*byte)(unsafePointer(&flags)), uint32Size())
	if err == nil {
		return nil
	}
	if !unsupportedWindowsDispositionEx(err) {
		return err
	}
	value := byte(1)
	return windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &value, 1)
}

func unsupportedWindowsDispositionEx(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_CALL_NOT_IMPLEMENTED)
}

// Helpers avoid exporting unsafe details outside the Windows implementation.
func unsafePointer(value *uint32) *byte { return (*byte)(unsafe.Pointer(value)) }
func uint32Size() uint32                { return uint32(unsafe.Sizeof(uint32(0))) }
