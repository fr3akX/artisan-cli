//go:build windows

package skill

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

type fileDispositionInformation struct{ DeleteFile uint32 }

func installPlatform(rootPath string, definition Definition, force bool, hooks installHooks) (InstallResult, error) {
	result := InstallResult{}
	root, err := openWindowsRoot(rootPath, hooks)
	if err != nil {
		return result, err
	}
	defer windows.CloseHandle(root)
	if err := runHook(hooks.afterRootOpen); err != nil {
		return result, err
	}

	directory, err := openWindowsRelative(root, definition.Name, true, windows.FILE_OPEN_IF)
	if err != nil {
		return result, ErrUnsafeTarget
	}
	defer windows.CloseHandle(directory)
	if err := verifyWindowsHandle(directory, true); err != nil {
		return result, err
	}
	if err := runHook(hooks.afterSkillDirOpen); err != nil {
		return result, err
	}

	exists, identical, err := inspectWindowsTarget(directory, definition)
	if err != nil {
		return result, err
	}
	if exists && identical {
		if err := syncWindowsExisting(directory, definition, hooks); err != nil {
			return result, err
		}
		result.Unchanged = true
		if !windowsInstallLocationMatches(rootPath, root, directory, definition) {
			return result, installLocationChanged(false)
		}
		return result, nil
	}
	if exists && !force {
		return result, ErrDifferentContent
	}

	temporaryName, temporaryHandle, err := createWindowsTemporary(directory)
	if err != nil {
		return result, fmt.Errorf("create temporary skill: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = deleteWindowsHandle(temporaryHandle)
		}
		windows.CloseHandle(temporaryHandle)
	}()
	if err := writeWindowsHandle(temporaryHandle, definition.Content); err != nil {
		return result, fmt.Errorf("write temporary skill: %w", err)
	}
	event(hooks, "file-sync")
	if err := syncWindowsHandle(temporaryHandle, temporaryName, hooks); err != nil {
		return result, fmt.Errorf("sync temporary skill: %w", err)
	}
	if err := runHook(hooks.beforeCommit); err != nil {
		return result, err
	}

	replace := exists && force
	err = renameWindowsHandle(temporaryHandle, directory, FileName, replace)
	if isWindowsExists(err) {
		exists, identical, inspectErr := inspectWindowsTarget(directory, definition)
		if inspectErr != nil {
			return result, inspectErr
		}
		if exists && identical {
			if err := syncWindowsExisting(directory, definition, hooks); err != nil {
				return result, err
			}
			result.Unchanged = true
			if !windowsInstallLocationMatches(rootPath, root, directory, definition) {
				return result, installLocationChanged(false)
			}
			return result, nil
		}
		if !force {
			return result, ErrDifferentContent
		}
		err = renameWindowsHandle(temporaryHandle, directory, FileName, true)
	}
	if err != nil {
		return result, fmt.Errorf("commit skill atomically: %w", err)
	}
	committed = true
	event(hooks, "commit")
	result.Installed = true
	event(hooks, "directory-sync")
	if err := syncWindowsDirectory(directory, definition, hooks); err != nil {
		return result, &securefile.ReplacementError{Err: fmt.Errorf("flush skill directory: %w", err)}
	}
	if !windowsInstallLocationMatches(rootPath, root, directory, definition) {
		return result, installLocationChanged(true)
	}
	return result, nil
}

func openWindowsRoot(path string, hooks installHooks) (windows.Handle, error) {
	return openWindowsRootAccess(path, hooks, true)
}

func openWindowsRootAccess(path string, hooks installHooks, writable bool) (windows.Handle, error) {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return 0, ErrInvalidDirectory
	}
	remainder := strings.TrimLeft(strings.TrimPrefix(clean, volume), `/\\`)
	components := strings.FieldsFunc(remainder, func(r rune) bool { return r == '/' || r == '\\' })
	ntRoot := `\??\` + volume + `\`
	if strings.HasPrefix(volume, `\\`) {
		ntRoot = `\??\UNC\` + strings.TrimPrefix(volume, `\\`) + `\`
	}
	name, err := windows.NewNTUnicodeString(ntRoot)
	if err != nil {
		return 0, ErrInvalidDirectory
	}
	current, err := ntOpenWindows(0, name, true, windows.FILE_OPEN, len(components) == 0 && writable)
	if err != nil {
		return 0, ErrInvalidDirectory
	}
	for index, component := range components {
		objectName, nameErr := windows.NewNTUnicodeString(component)
		if nameErr != nil {
			windows.CloseHandle(current)
			return 0, ErrInvalidDirectory
		}
		next, openErr := ntOpenWindows(current, objectName, true, windows.FILE_OPEN, writable && index == len(components)-1)
		windows.CloseHandle(current)
		if openErr != nil {
			return 0, ErrUnsafeTarget
		}
		current = next
		if hooks.afterRootComponentOpen != nil {
			if err := hooks.afterRootComponentOpen(component); err != nil {
				windows.CloseHandle(current)
				return 0, err
			}
		}
	}
	return current, nil
}

func windowsInstallLocationMatches(rootPath string, root, directory windows.Handle, definition Definition) bool {
	requestedRoot, err := openWindowsRootAccess(rootPath, installHooks{}, false)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(requestedRoot)
	if !sameWindowsHandleIdentity(root, requestedRoot) {
		return false
	}
	requestedDirectory, err := openWindowsRelativeAccess(requestedRoot, definition.Name, true, windows.FILE_OPEN, false)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(requestedDirectory)
	return sameWindowsHandleIdentity(directory, requestedDirectory)
}

func sameWindowsHandleIdentity(first, second windows.Handle) bool {
	var firstInfo, secondInfo windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(first, &firstInfo) != nil || windows.GetFileInformationByHandle(second, &secondInfo) != nil {
		return false
	}
	return firstInfo.VolumeSerialNumber == secondInfo.VolumeSerialNumber &&
		firstInfo.FileIndexHigh == secondInfo.FileIndexHigh &&
		firstInfo.FileIndexLow == secondInfo.FileIndexLow
}

func openWindowsRelative(root windows.Handle, name string, directory bool, disposition uint32) (windows.Handle, error) {
	return openWindowsRelativeAccess(root, name, directory, disposition, true)
}

func openWindowsRelativeAccess(root windows.Handle, name string, directory bool, disposition uint32, writable bool) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	return ntOpenWindows(root, objectName, directory, disposition, writable)
}

func ntOpenWindows(root windows.Handle, name *windows.NTUnicodeString, directory bool, disposition uint32, writable bool) (windows.Handle, error) {
	attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: root, ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if writable {
		options |= windows.FILE_WRITE_THROUGH
	}
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	var allocation int64
	access := uint32(windows.FILE_GENERIC_READ | windows.SYNCHRONIZE)
	if writable {
		access |= windows.FILE_GENERIC_WRITE
		if !directory {
			access |= windows.DELETE
		}
	}
	err := windows.NtCreateFile(&handle,
		access,
		attributes, &status, &allocation, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition, options, 0, 0)
	if err != nil {
		return 0, err
	}
	if err := verifyWindowsHandle(handle, directory); err != nil {
		windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func verifyWindowsHandle(handle windows.Handle, directory bool) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrUnsafeTarget
	}
	isDirectory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory {
		return ErrUnsafeTarget
	}
	return nil
}

func inspectWindowsTarget(directory windows.Handle, definition Definition) (bool, bool, error) {
	handle, err := openWindowsRelativeAccess(directory, FileName, false, windows.FILE_OPEN, false)
	if isWindowsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, ErrUnsafeTarget
	}
	file := os.NewFile(uintptr(handle), FileName)
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, int64(len(definition.Content)+1)))
	if err != nil {
		return false, false, err
	}
	return true, bytes.Equal(contents, definition.Content), nil
}

func createWindowsTemporary(directory windows.Handle) (string, windows.Handle, error) {
	for attempt := 0; attempt < 128; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", 0, err
		}
		name := ".SKILL.md.tmp-" + hex.EncodeToString(random[:])
		handle, err := openWindowsRelative(directory, name, false, windows.FILE_CREATE)
		if err == nil {
			return name, handle, nil
		}
		if !isWindowsExists(err) {
			return "", 0, err
		}
	}
	return "", 0, errors.New("unable to allocate unique temporary skill name")
}

func renameWindowsHandle(handle, directory windows.Handle, name string, replace bool) error {
	utf16Name, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	nameBytes := (len(utf16Name) - 1) * 2
	var layout fileRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+nameBytes)
	information := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	if replace {
		information.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	}
	information.RootDirectory = directory
	information.FileNameLength = uint32(nameBytes)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&information.FileName[0]))[:nameBytes/2:nameBytes/2], utf16Name)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}

func deleteWindowsHandle(handle windows.Handle) error {
	information := fileDispositionInformation{DeleteFile: 1}
	return windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information)))
}

func writeWindowsHandle(handle windows.Handle, contents []byte) error {
	for len(contents) != 0 {
		var written uint32
		if err := windows.WriteFile(handle, contents, &written, nil); err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		contents = contents[written:]
	}
	return nil
}

func syncWindowsHandle(handle windows.Handle, name string, hooks installHooks) error {
	if hooks.syncFile == nil {
		return windows.FlushFileBuffers(handle)
	}
	duplicate, err := duplicateWindowsHandle(handle)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(duplicate), name)
	defer file.Close()
	return hooks.syncFile(file)
}

func syncWindowsExisting(directory windows.Handle, definition Definition, hooks installHooks) error {
	handle, err := openWindowsRelative(directory, FileName, false, windows.FILE_OPEN)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := syncWindowsHandle(handle, FileName, hooks); err != nil {
		return err
	}
	return syncWindowsDirectory(directory, definition, hooks)
}

func syncWindowsDirectory(directory windows.Handle, definition Definition, hooks installHooks) error {
	if hooks.syncDirectory != nil {
		duplicate, err := duplicateWindowsHandle(directory)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(duplicate), definition.Name)
		defer file.Close()
		return hooks.syncDirectory(file)
	}
	return windows.FlushFileBuffers(directory)
}

func duplicateWindowsHandle(handle windows.Handle) (windows.Handle, error) {
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(process, handle, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return 0, err
	}
	return duplicate, nil
}

func isWindowsExists(err error) bool {
	return errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) || errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}

func isWindowsNotExist(err error) bool {
	return errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}
