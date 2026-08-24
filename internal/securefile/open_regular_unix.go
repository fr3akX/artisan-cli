//go:build !windows

package securefile

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type unixSnapshotComponent struct {
	name string
	file *os.File
	info os.FileInfo
	dir  bool
}

func readRegularSnapshot(path string, maxBytes int64, hooks snapshotTestHooks) ([]byte, error) {
	if path == "" || maxBytes <= 0 || strings.IndexByte(path, 0) >= 0 || unixPathContainsParent(path) {
		return nil, invalidRegularSnapshot()
	}
	rootPath, components, ok := unixSnapshotPath(path)
	if !ok {
		return nil, invalidRegularSnapshot()
	}
	rootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, invalidRegularSnapshot()
	}
	root := os.NewFile(uintptr(rootFD), "snapshot-root")
	if root == nil {
		_ = unix.Close(rootFD)
		return nil, invalidRegularSnapshot()
	}
	opened := make([]unixSnapshotComponent, 0, len(components)+1)
	rootInfo, err := root.Stat()
	if err != nil || !rootInfo.IsDir() {
		_ = root.Close()
		return nil, invalidRegularSnapshot()
	}
	opened = append(opened, unixSnapshotComponent{file: root, info: rootInfo, dir: true})
	defer func() { closeUnixSnapshotComponents(opened) }()

	current := root
	for index, component := range components {
		final := index == len(components)-1
		var observed unix.Stat_t
		if err := unix.Fstatat(int(current.Fd()), component, &observed, unix.AT_SYMLINK_NOFOLLOW); err != nil || unixStatIsLink(observed) {
			return nil, invalidRegularSnapshot()
		}
		if err := hooks.emit("before-open:" + component); err != nil {
			return nil, invalidRegularSnapshot()
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if !final {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(current.Fd()), component, flags, 0)
		if err != nil {
			return nil, invalidRegularSnapshot()
		}
		file := os.NewFile(uintptr(fd), "snapshot-component")
		if file == nil {
			_ = unix.Close(fd)
			return nil, invalidRegularSnapshot()
		}
		info, statErr := file.Stat()
		var actual unix.Stat_t
		fstatErr := unix.Fstat(fd, &actual)
		if statErr != nil || fstatErr != nil || !sameUnixStatIdentity(observed, actual) || (!final && !info.IsDir()) || (final && !info.Mode().IsRegular()) {
			_ = file.Close()
			return nil, invalidRegularSnapshot()
		}
		opened = append(opened, unixSnapshotComponent{name: component, file: file, info: info, dir: !final})
		current = file
		if err := hooks.emit("after-open:" + component); err != nil {
			return nil, invalidRegularSnapshot()
		}
	}

	final := opened[len(opened)-1]
	if final.dir || final.info.Size() <= 0 || final.info.Size() > maxBytes || final.info.Size() > int64(maxInt()) {
		return nil, invalidRegularSnapshot()
	}
	contents, err := readSnapshotBytes(final.file, int(final.info.Size()), hooks)
	if err != nil {
		return nil, invalidRegularSnapshot()
	}
	if err := hooks.emit("after-read"); err != nil {
		return nil, invalidRegularSnapshot()
	}
	if err := verifySnapshotBytes(final.file, contents); err != nil {
		return nil, invalidRegularSnapshot()
	}
	postInfo, err := final.file.Stat()
	if err != nil || !postInfo.Mode().IsRegular() || !os.SameFile(final.info, postInfo) || postInfo.Size() != final.info.Size() || !postInfo.ModTime().Equal(final.info.ModTime()) {
		return nil, invalidRegularSnapshot()
	}
	if err := hooks.emit("before-recheck"); err != nil {
		return nil, invalidRegularSnapshot()
	}
	if !recheckUnixSnapshotPath(rootPath, opened) {
		return nil, invalidRegularSnapshot()
	}
	return append([]byte(nil), contents...), nil
}

func unixPathContainsParent(path string) bool {
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if component == ".." {
			return true
		}
	}
	return false
}

func unixSnapshotPath(path string) (string, []string, bool) {
	clean := filepath.Clean(path)
	root := "."
	remainder := clean
	if filepath.IsAbs(clean) {
		root = string(filepath.Separator)
		remainder = strings.TrimPrefix(clean, root)
	}
	parts := strings.Split(remainder, string(filepath.Separator))
	components := make([]string, 0, len(parts))
	for _, component := range parts {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			return "", nil, false
		}
		components = append(components, component)
	}
	return root, components, len(components) > 0
}

func recheckUnixSnapshotPath(rootPath string, opened []unixSnapshotComponent) bool {
	fd, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return false
	}
	current := os.NewFile(uintptr(fd), "snapshot-recheck-root")
	if current == nil {
		_ = unix.Close(fd)
		return false
	}
	defer func() { _ = current.Close() }()
	info, err := current.Stat()
	if err != nil || !os.SameFile(info, opened[0].info) {
		return false
	}
	for index := 1; index < len(opened); index++ {
		component := opened[index]
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if component.dir {
			flags |= unix.O_DIRECTORY
		}
		nextFD, err := unix.Openat(int(current.Fd()), component.name, flags, 0)
		if err != nil {
			return false
		}
		next := os.NewFile(uintptr(nextFD), "snapshot-recheck-component")
		if next == nil {
			_ = unix.Close(nextFD)
			return false
		}
		nextInfo, statErr := next.Stat()
		if statErr != nil || !os.SameFile(nextInfo, component.info) || (component.dir && !nextInfo.IsDir()) || (!component.dir && (!nextInfo.Mode().IsRegular() || nextInfo.Size() != component.info.Size() || !nextInfo.ModTime().Equal(component.info.ModTime()))) {
			_ = next.Close()
			return false
		}
		_ = current.Close()
		current = next
	}
	return true
}

func closeUnixSnapshotComponents(components []unixSnapshotComponent) {
	for index := len(components) - 1; index >= 0; index-- {
		_ = components[index].file.Close()
	}
}

func unixStatIsLink(info unix.Stat_t) bool {
	return info.Mode&unix.S_IFMT == unix.S_IFLNK
}

func sameUnixStatIdentity(first, second unix.Stat_t) bool {
	return first.Dev == second.Dev && first.Ino == second.Ino && first.Mode&unix.S_IFMT == second.Mode&unix.S_IFMT
}
