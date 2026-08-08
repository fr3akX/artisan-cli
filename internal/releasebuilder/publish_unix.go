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
	file                 *os.File
	info                 os.FileInfo
	payload              *os.File
	payloadInfo          os.FileInfo
	name, path           string
	ambiguous            bool
	injectCleanupFailure func() error
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
func (d *heldDist) close() error { return d.file.Close() }
func (d *heldDist) pathMatches() bool {
	info, err := os.Lstat(d.path)
	if err != nil || isLinkOrReparse(info) || !info.IsDir() || !os.SameFile(d.info, info) {
		return false
	}
	other, err := openHeldDist(d.path)
	if err != nil {
		return false
	}
	defer other.close()
	return os.SameFile(d.info, other.info)
}
func (d *heldDist) finalExists(name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(d.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return false, err
}
func openRelativeDirectory(parent int, name string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}
func (d *heldDist) createStaging() (*heldStage, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name, err := randomStagingName()
		if err != nil {
			return nil, err
		}
		if err := unix.Mkdirat(int(d.file.Fd()), name, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return nil, err
		}
		file, info, err := openRelativeDirectory(int(d.file.Fd()), name)
		if err != nil {
			_ = unix.Unlinkat(int(d.file.Fd()), name, unix.AT_REMOVEDIR)
			return nil, err
		}
		if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, 0); err != nil {
			file.Close()
			_ = unix.Unlinkat(int(d.file.Fd()), name, unix.AT_REMOVEDIR)
			return nil, err
		}
		path, err := directoryHandlePath(int(file.Fd()))
		if err != nil {
			file.Close()
			_ = unix.Unlinkat(int(d.file.Fd()), name, unix.AT_REMOVEDIR)
			return nil, err
		}
		return &heldStage{file: file, info: info, name: name, path: path}, nil
	}
	return nil, errors.New("could not allocate staging directory")
}
func (s *heldStage) preparePayload() error {
	file, info, err := openRelativeDirectory(int(s.file.Fd()), "payload")
	if err != nil {
		return err
	}
	s.payload = file
	s.payloadInfo = info
	return nil
}
func (s *heldStage) closePayload() error {
	if s.payload == nil {
		return nil
	}
	err := s.payload.Close()
	s.payload = nil
	return err
}
func (s *heldStage) closeStage() error {
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
func (s *heldStage) close() error        { return errors.Join(s.closePayload(), s.closeStage()) }
func (s *heldStage) handlesClosed() bool { return s.file == nil && s.payload == nil }
func (s *heldStage) payloadPath() (string, error) {
	if s.payload == nil {
		return "", errors.New("payload handle is closed")
	}
	return directoryHandlePath(int(s.payload.Fd()))
}
func relativeMatches(parent int, name string, want os.FileInfo) bool {
	file, info, err := openRelativeDirectory(parent, name)
	if err != nil {
		return false
	}
	defer file.Close()
	return os.SameFile(want, info)
}
func (s *heldStage) payloadMatches() bool {
	return s.payload != nil && relativeMatches(int(s.file.Fd()), "payload", s.payloadInfo)
}
func (d *heldDist) publishedMatches(s *heldStage, leaf string) bool {
	return s.payload != nil && relativeMatches(int(d.file.Fd()), leaf, s.payloadInfo)
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
	if err := renameNoReplace(int(s.file.Fd()), "payload", int(d.file.Fd()), leaf); err != nil {
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
func (d *heldDist) stageMatches(s *heldStage) bool {
	return relativeMatches(int(d.file.Fd()), s.name, s.info)
}
func (d *heldDist) cleanup(s *heldStage, keepPayload bool) (returnErr error) {
	if s.ambiguous {
		return errors.Join(s.close(), errors.New("cleanup skipped because publication identity is ambiguous; safe staging residue retained"))
	}
	if !keepPayload {
		returnErr = errors.Join(returnErr, s.closePayload())
	}
	defer func() { returnErr = errors.Join(returnErr, s.closeStage()) }()
	if !d.stageMatches(s) {
		s.ambiguous = true
		return errors.New("cleanup skipped because staging name no longer matches held staging identity")
	}
	if s.injectCleanupFailure != nil {
		if err := s.injectCleanupFailure(); err != nil {
			return err
		}
	}
	if err := removeContentsAt(int(s.file.Fd())); err != nil {
		return err
	}
	if !d.stageMatches(s) {
		s.ambiguous = true
		return errors.New("cleanup skipped after staging identity changed; held residue retained")
	}
	if err := unix.Unlinkat(int(d.file.Fd()), s.name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove held staging directory: %w", err)
	}
	return returnErr
}
func removeContentsAt(parent int) error {
	duplicate, err := unix.Openat(parent, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(duplicate), "held-directory")
	names, err := file.Readdirnames(-1)
	_ = file.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "." || name == ".." {
			continue
		}
		if err := removeTreeAt(parent, name); err != nil {
			return err
		}
	}
	return nil
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
	if err := removeContentsAt(fd); err != nil {
		unix.Close(fd)
		return err
	}
	if err := unix.Close(fd); err != nil {
		return err
	}
	if err := unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}
func isAlreadyExists(err error) bool {
	return errors.Is(err, unix.EEXIST) || errors.Is(err, fs.ErrExist)
}
