//go:build !windows

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

	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/unix"
)

func installPlatform(rootPath string, force bool, hooks installHooks) (InstallResult, error) {
	result := InstallResult{}
	root, err := openUnixRootNoFollow(rootPath, hooks)
	if err != nil {
		return result, err
	}
	defer root.Close()
	rootFD := int(root.Fd())
	if err := runHook(hooks.afterRootOpen); err != nil {
		return result, err
	}

	createdDirectory := false
	if err := unix.Mkdirat(rootFD, Name, 0o755); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return result, fmt.Errorf("create skill directory: %w", err)
		}
	} else {
		createdDirectory = true
	}
	if createdDirectory {
		if err := syncOpenedDirectory(root, hooks, "root-directory-sync"); err != nil {
			return result, fmt.Errorf("sync skill root: %w", err)
		}
	}

	skillFD, err := unix.Openat(rootFD, Name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return result, ErrUnsafeTarget
	}
	directory := os.NewFile(uintptr(skillFD), Name)
	defer directory.Close()
	if err := verifyDirectory(directory); err != nil {
		return result, err
	}
	if err := runHook(hooks.afterSkillDirOpen); err != nil {
		return result, err
	}

	exists, identical, err := inspectTargetAt(skillFD)
	if err != nil {
		return result, err
	}
	if exists && identical {
		if err := syncExistingAt(skillFD, directory, hooks); err != nil {
			return result, err
		}
		result.Unchanged = true
		if !unixInstallLocationMatches(rootPath, root, directory) {
			return result, installLocationChanged(false)
		}
		return result, nil
	}
	if exists && !force {
		return result, ErrDifferentContent
	}

	temporaryName, temporary, err := createTemporaryAt(skillFD)
	if err != nil {
		return result, fmt.Errorf("create temporary skill: %w", err)
	}
	defer func() {
		_ = temporary.Close()
		_ = unix.Unlinkat(skillFD, temporaryName, 0)
	}()
	if _, err := temporary.Write(Content); err != nil {
		return result, fmt.Errorf("write temporary skill: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		return result, fmt.Errorf("set temporary skill mode: %w", err)
	}
	event(hooks, "file-sync")
	if err := syncOpenedFile(temporary, hooks); err != nil {
		return result, fmt.Errorf("sync temporary skill: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return result, fmt.Errorf("close temporary skill: %w", err)
	}
	if err := runHook(hooks.beforeCommit); err != nil {
		return result, err
	}

	if exists && force {
		err = unix.Renameat(skillFD, temporaryName, skillFD, FileName)
	} else {
		err = renameNoReplaceAt(skillFD, temporaryName, FileName)
	}
	if errors.Is(err, unix.EEXIST) {
		exists, identical, inspectErr := inspectTargetAt(skillFD)
		if inspectErr != nil {
			return result, inspectErr
		}
		if exists && identical {
			if cleanupErr := unix.Unlinkat(skillFD, temporaryName, 0); cleanupErr != nil && !errors.Is(cleanupErr, unix.ENOENT) {
				return result, fmt.Errorf("clean raced temporary skill: %w", cleanupErr)
			}
			if err := syncExistingAt(skillFD, directory, hooks); err != nil {
				return result, err
			}
			result.Unchanged = true
			if !unixInstallLocationMatches(rootPath, root, directory) {
				return result, installLocationChanged(false)
			}
			return result, nil
		}
		if !force {
			return result, ErrDifferentContent
		}
		err = unix.Renameat(skillFD, temporaryName, skillFD, FileName)
	}
	if err != nil {
		return result, fmt.Errorf("commit skill atomically: %w", err)
	}
	event(hooks, "commit")
	result.Installed = true
	cleanupErr := unix.Unlinkat(skillFD, temporaryName, 0)
	if errors.Is(cleanupErr, unix.ENOENT) {
		cleanupErr = nil
	}
	event(hooks, "directory-sync")
	if err := syncOpenedDirectory(directory, hooks, ""); err != nil {
		return result, &securefile.ReplacementError{Err: fmt.Errorf("sync skill directory: %w", err)}
	}
	if cleanupErr != nil {
		return result, &securefile.ReplacementError{Err: fmt.Errorf("clean committed temporary skill: %w", cleanupErr)}
	}
	if !unixInstallLocationMatches(rootPath, root, directory) {
		return result, installLocationChanged(true)
	}
	return result, nil
}

func openUnixRootNoFollow(path string, hooks installHooks) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalidDirectory
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrInvalidDirectory
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	components := strings.Split(strings.Trim(path, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			current.Close()
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, ErrUnsafeTarget
			}
			return nil, ErrInvalidDirectory
		}
		next := os.NewFile(uintptr(nextFD), component)
		current.Close()
		current = next
		if hooks.afterRootComponentOpen != nil {
			if err := hooks.afterRootComponentOpen(component); err != nil {
				current.Close()
				return nil, err
			}
		}
	}
	if err := verifyDirectory(current); err != nil {
		current.Close()
		return nil, err
	}
	return current, nil
}

func verifyDirectory(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened directory: %w", err)
	}
	if !info.IsDir() {
		return ErrUnsafeTarget
	}
	return nil
}

func inspectTargetAt(directoryFD int) (exists, identical bool, returnedErr error) {
	exists, err := preflightUnixRegularAt(directoryFD)
	if err != nil || !exists {
		return exists, false, err
	}
	fd, err := unix.Openat(directoryFD, FileName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, false, nil
	}
	if err != nil {
		if unixNonregularOpenError(err) {
			return false, false, ErrUnsafeTarget
		}
		return false, false, fmt.Errorf("open installed skill: %w", err)
	}
	file := os.NewFile(uintptr(fd), FileName)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, false, fmt.Errorf("inspect installed skill: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, false, ErrUnsafeTarget
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(len(Content)+1)))
	if err != nil {
		return false, false, fmt.Errorf("read installed skill: %w", err)
	}
	return true, bytes.Equal(contents, Content), nil
}

func syncExistingAt(directoryFD int, directory *os.File, hooks installHooks) error {
	exists, err := preflightUnixRegularAt(directoryFD)
	if err != nil {
		return err
	}
	if !exists {
		return ErrDifferentContent
	}
	fd, err := unix.Openat(directoryFD, FileName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if unixNonregularOpenError(err) {
			return ErrUnsafeTarget
		}
		return fmt.Errorf("open installed skill for sync: %w", err)
	}
	file := os.NewFile(uintptr(fd), FileName)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrUnsafeTarget
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(len(Content)+1)))
	if err != nil {
		return fmt.Errorf("reread installed skill: %w", err)
	}
	if !bytes.Equal(contents, Content) {
		return ErrDifferentContent
	}
	if err := syncOpenedFile(file, hooks); err != nil {
		return fmt.Errorf("sync installed skill: %w", err)
	}
	if err := syncOpenedDirectory(directory, hooks, ""); err != nil {
		return fmt.Errorf("sync installed skill directory: %w", err)
	}
	return nil
}

func unixNonregularOpenError(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENXIO) || errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EISDIR)
}

func preflightUnixRegularAt(directoryFD int) (bool, error) {
	var info unix.Stat_t
	if err := unix.Fstatat(directoryFD, FileName, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, fmt.Errorf("inspect installed skill metadata: %w", err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG {
		return true, ErrUnsafeTarget
	}
	return true, nil
}

func unixInstallLocationMatches(rootPath string, root, directory *os.File) bool {
	requestedRootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	requestedRoot := os.NewFile(uintptr(requestedRootFD), rootPath)
	defer requestedRoot.Close()
	openedRootInfo, err := root.Stat()
	if err != nil {
		return false
	}
	requestedRootInfo, err := requestedRoot.Stat()
	if err != nil || !os.SameFile(openedRootInfo, requestedRootInfo) {
		return false
	}
	requestedDirectoryFD, err := unix.Openat(requestedRootFD, Name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	requestedDirectory := os.NewFile(uintptr(requestedDirectoryFD), Name)
	defer requestedDirectory.Close()
	openedDirectoryInfo, err := directory.Stat()
	if err != nil {
		return false
	}
	requestedDirectoryInfo, err := requestedDirectory.Stat()
	return err == nil && os.SameFile(openedDirectoryInfo, requestedDirectoryInfo)
}

func createTemporaryAt(directoryFD int) (string, *os.File, error) {
	for attempt := 0; attempt < 128; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := ".SKILL.md.tmp-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o644)
		if err == nil {
			return name, os.NewFile(uintptr(fd), name), nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("unable to allocate unique temporary skill name")
}

func syncOpenedFile(file *os.File, hooks installHooks) error {
	if hooks.syncFile != nil {
		return hooks.syncFile(file)
	}
	return file.Sync()
}

func syncOpenedDirectory(directory *os.File, hooks installHooks, eventName string) error {
	if eventName != "" {
		event(hooks, eventName)
	}
	if hooks.syncDirectory != nil {
		return hooks.syncDirectory(directory)
	}
	return directory.Sync()
}
