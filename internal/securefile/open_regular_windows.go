//go:build windows

package securefile

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsSnapshotComponent struct {
	name string
	file *os.File
	info windows.ByHandleFileInformation
	dir  bool
}

func readRegularSnapshot(path string, maxBytes int64, hooks snapshotTestHooks) ([]byte, error) {
	if path == "" || maxBytes <= 0 || strings.IndexByte(path, 0) >= 0 || windowsPathContainsParent(path) {
		return nil, invalidRegularSnapshot()
	}
	rootName, components, ok := windowsSnapshotPath(path)
	if !ok {
		return nil, invalidRegularSnapshot()
	}
	rootHandle, err := openWindowsSnapshotRoot(rootName)
	if err != nil {
		return nil, invalidRegularSnapshot()
	}
	rootFile := os.NewFile(uintptr(rootHandle), "snapshot-root")
	if rootFile == nil {
		_ = windows.CloseHandle(rootHandle)
		return nil, invalidRegularSnapshot()
	}
	rootInfo, err := windowsSnapshotInfo(rootHandle)
	if err != nil || !windowsSnapshotIsDirectory(rootInfo) || windowsSnapshotIsReparse(rootInfo) {
		_ = rootFile.Close()
		return nil, invalidRegularSnapshot()
	}
	opened := make([]windowsSnapshotComponent, 0, len(components)+1)
	opened = append(opened, windowsSnapshotComponent{file: rootFile, info: rootInfo, dir: true})
	defer func() { closeWindowsSnapshotComponents(opened) }()

	current := rootHandle
	for index, component := range components {
		final := index == len(components)-1
		observedHandle, observedErr := openWindowsSnapshotRelative(current, component, !final)
		if observedErr != nil {
			return nil, invalidRegularSnapshot()
		}
		observedInfo, observedInfoErr := windowsSnapshotInfo(observedHandle)
		if observedInfoErr != nil || windowsSnapshotIsReparse(observedInfo) || windowsSnapshotIsDirectory(observedInfo) != !final {
			_ = windows.CloseHandle(observedHandle)
			return nil, invalidRegularSnapshot()
		}
		if err := hooks.emit("before-open:" + component); err != nil {
			_ = windows.CloseHandle(observedHandle)
			return nil, invalidRegularSnapshot()
		}
		handle, err := openWindowsSnapshotRelative(current, component, !final)
		if err != nil {
			_ = windows.CloseHandle(observedHandle)
			return nil, invalidRegularSnapshot()
		}
		file := os.NewFile(uintptr(handle), "snapshot-component")
		if file == nil {
			_ = windows.CloseHandle(handle)
			return nil, invalidRegularSnapshot()
		}
		info, infoErr := windowsSnapshotInfo(handle)
		_ = windows.CloseHandle(observedHandle)
		if infoErr != nil || !sameWindowsSnapshotIdentity(info, observedInfo) || windowsSnapshotIsReparse(info) || windowsSnapshotIsDirectory(info) != !final {
			_ = file.Close()
			return nil, invalidRegularSnapshot()
		}
		opened = append(opened, windowsSnapshotComponent{name: component, file: file, info: info, dir: !final})
		current = handle
		if err := hooks.emit("after-open:" + component); err != nil {
			return nil, invalidRegularSnapshot()
		}
	}

	final := opened[len(opened)-1]
	size := windowsSnapshotSize(final.info)
	if final.dir || size <= 0 || size > maxBytes || size > int64(maxInt()) {
		return nil, invalidRegularSnapshot()
	}
	contents, err := readSnapshotBytes(final.file, int(size), hooks)
	if err != nil {
		return nil, invalidRegularSnapshot()
	}
	if err := hooks.emit("after-read"); err != nil {
		return nil, invalidRegularSnapshot()
	}
	if err := verifySnapshotBytes(final.file, contents); err != nil {
		return nil, invalidRegularSnapshot()
	}
	postInfo, err := windowsSnapshotInfo(windows.Handle(final.file.Fd()))
	if err != nil || !sameWindowsSnapshotIdentity(postInfo, final.info) || windowsSnapshotSize(postInfo) != size || postInfo.LastWriteTime != final.info.LastWriteTime || windowsSnapshotIsDirectory(postInfo) || windowsSnapshotIsReparse(postInfo) {
		return nil, invalidRegularSnapshot()
	}
	if err := hooks.emit("before-recheck"); err != nil {
		return nil, invalidRegularSnapshot()
	}
	if !recheckWindowsSnapshotPath(rootName, opened) {
		return nil, invalidRegularSnapshot()
	}
	return append([]byte(nil), contents...), nil
}

func windowsPathContainsParent(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(character rune) bool { return character == '/' || character == '\\' }) {
		if component == ".." {
			return true
		}
	}
	return false
}

func windowsSnapshotPath(path string) (string, []string, bool) {
	clean := filepath.Clean(path)
	root := "."
	remainder := clean
	if filepath.IsAbs(clean) {
		volume := filepath.VolumeName(clean)
		if volume == "" {
			return "", nil, false
		}
		root = volume + `\`
		remainder = strings.TrimLeft(strings.TrimPrefix(clean, volume), `/\`)
	}
	parts := strings.FieldsFunc(remainder, func(character rune) bool { return character == '/' || character == '\\' })
	components := make([]string, 0, len(parts))
	for _, component := range parts {
		if component == "" || component == "." {
			continue
		}
		if component == ".." || strings.ContainsRune(component, ':') {
			return "", nil, false
		}
		components = append(components, component)
	}
	return root, components, len(components) > 0
}

func openWindowsSnapshotRoot(path string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(pointer, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func openWindowsSnapshotRelative(root windows.Handle, name string, directory bool) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: root, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE | windows.FILE_SEQUENTIAL_ONLY
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	var allocation int64
	err = windows.NtCreateFile(&handle, uint32(windows.FILE_GENERIC_READ|windows.SYNCHRONIZE), attributes, &status, &allocation, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options, 0, 0)
	return handle, err
}

func recheckWindowsSnapshotPath(rootName string, opened []windowsSnapshotComponent) bool {
	root, err := openWindowsSnapshotRoot(rootName)
	if err != nil {
		return false
	}
	current := root
	defer func() { _ = windows.CloseHandle(current) }()
	rootInfo, err := windowsSnapshotInfo(root)
	if err != nil || !sameWindowsSnapshotIdentity(rootInfo, opened[0].info) || windowsSnapshotIsReparse(rootInfo) {
		return false
	}
	for index := 1; index < len(opened); index++ {
		component := opened[index]
		next, err := openWindowsSnapshotRelative(current, component.name, component.dir)
		if err != nil {
			return false
		}
		info, infoErr := windowsSnapshotInfo(next)
		if infoErr != nil || !sameWindowsSnapshotIdentity(info, component.info) || windowsSnapshotIsReparse(info) || windowsSnapshotIsDirectory(info) != component.dir || (!component.dir && (windowsSnapshotSize(info) != windowsSnapshotSize(component.info) || info.LastWriteTime != component.info.LastWriteTime)) {
			_ = windows.CloseHandle(next)
			return false
		}
		_ = windows.CloseHandle(current)
		current = next
	}
	return true
}

func windowsSnapshotInfo(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(handle, &info)
	return info, err
}

func sameWindowsSnapshotIdentity(first, second windows.ByHandleFileInformation) bool {
	return first.VolumeSerialNumber == second.VolumeSerialNumber && first.FileIndexHigh == second.FileIndexHigh && first.FileIndexLow == second.FileIndexLow
}

func windowsSnapshotIsDirectory(info windows.ByHandleFileInformation) bool {
	return info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
}

func windowsSnapshotIsReparse(info windows.ByHandleFileInformation) bool {
	return info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func windowsSnapshotSize(info windows.ByHandleFileInformation) int64 {
	return int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow))
}

func closeWindowsSnapshotComponents(components []windowsSnapshotComponent) {
	for index := len(components) - 1; index >= 0; index-- {
		_ = components[index].file.Close()
	}
}
