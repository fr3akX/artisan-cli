//go:build linux || darwin

package releasebuilder

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

type heldDist struct {
	file *os.File
	info os.FileInfo
	path string
}

type heldStage struct {
	file *os.File
	name string
	path string
}

func openHeldDist(path string) (*heldDist, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.IsDir() {
		file.Close()
		return nil, errors.New("dist handle is not a directory")
	}
	return &heldDist{file: file, info: info, path: path}, nil
}

func (directory *heldDist) close() error { return directory.file.Close() }

func (directory *heldDist) pathMatches() bool {
	info, err := os.Lstat(directory.path)
	if err != nil || isLinkOrReparse(info) || !info.IsDir() || !os.SameFile(directory.info, info) {
		return false
	}
	other, err := openHeldDist(directory.path)
	if err != nil {
		return false
	}
	defer other.close()
	return os.SameFile(directory.info, other.info)
}

func (directory *heldDist) finalExists(name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(directory.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOENT) {
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
		if err := unix.Mkdirat(int(directory.file.Fd()), name, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return nil, err
		}
		fd, err := unix.Openat(int(directory.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			_ = unix.Unlinkat(int(directory.file.Fd()), name, unix.AT_REMOVEDIR)
			return nil, err
		}
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(int(directory.file.Fd()), name, unix.AT_REMOVEDIR)
			return nil, err
		}
		return &heldStage{file: os.NewFile(uintptr(fd), name), name: name, path: directoryHandlePath(fd)}, nil
	}
	return nil, errors.New("could not allocate staging directory")
}

func (stage *heldStage) preparePayload() error { return nil }
func (stage *heldStage) close() error          { return stage.file.Close() }

func (directory *heldDist) publish(stage *heldStage, leaf string) error {
	return renameNoReplace(int(stage.file.Fd()), "payload", int(directory.file.Fd()), leaf)
}

func (directory *heldDist) rollback(stage *heldStage, leaf string) error {
	return renameNoReplace(int(directory.file.Fd()), leaf, int(stage.file.Fd()), "payload")
}

func (directory *heldDist) cleanup(stage *heldStage) error {
	_ = stage.close()
	return removeTreeAt(int(directory.file.Fd()), stage.name)
}

func removeTreeAt(parent int, name string) error {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
			return unix.Unlinkat(parent, name, 0)
		}
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	names, readErr := file.Readdirnames(-1)
	if readErr != nil {
		file.Close()
		return readErr
	}
	for _, child := range names {
		if child == "." || child == ".." {
			continue
		}
		if err := removeTreeAt(fd, child); err != nil {
			file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove staging %s: %w", name, err)
	}
	return nil
}

func isAlreadyExists(err error) bool {
	return errors.Is(err, unix.EEXIST) || errors.Is(err, fs.ErrExist)
}
