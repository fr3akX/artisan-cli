// Package securefile provides private, atomic local file persistence.
package securefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReplacementError reports an error after the destination replacement became
// visible. Callers must treat the destination's durability as ambiguous.
type ReplacementError struct {
	Err error
}

func (e *ReplacementError) Error() string { return "replacement became visible: " + e.Err.Error() }
func (e *ReplacementError) Unwrap() error { return e.Err }

// ReplacementVisible reports whether err occurred after destination replacement.
func ReplacementVisible(err error) bool {
	var replacementError *ReplacementError
	return errors.As(err, &replacementError)
}

// EnsurePrivateDir creates path when needed and applies and verifies the
// platform's private-directory protection on the exact opened directory.
func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	directory, err := openPrivateDirectory(path)
	if err != nil {
		return fmt.Errorf("open private directory: %w", err)
	}
	defer directory.Close()
	if err := protectPrivate(directory, true); err != nil {
		return fmt.Errorf("protect private directory: %w", err)
	}
	return nil
}

// ProtectPrivateFile applies and verifies the platform's private-file
// protection on the exact opened regular file.
func ProtectPrivateFile(file *os.File) error {
	if file == nil {
		return errors.New("private file is required")
	}
	return protectPrivate(file, false)
}

// AtomicWrite durably replaces name in dir using a protected same-directory
// temporary file. It syncs file contents and protection before replacement,
// then establishes the platform-specific durable rename boundary.
func AtomicWrite(dir, name string, contents []byte) error {
	return atomicWriteWithOperations(dir, name, contents, durableReplace, syncParentDirectory)
}

// DurableRemove makes name durably absent from dir. It succeeds when name is
// already absent and establishes the platform-specific deletion boundary before
// returning success.
func DurableRemove(dir, name string) error {
	if name == "" || filepath.Base(name) != name {
		return errors.New("invalid private file name")
	}
	if err := EnsurePrivateDir(dir); err != nil {
		return err
	}
	return durableRemove(dir, name)
}

func durableRemoveWithOperations(dir, path string, remove func(string) error, syncParent func(string) error) error {
	if err := remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove private file: %w", err)
	}
	// Sync even when the file is already visibly absent. A prior interrupted
	// deletion may have made that absence visible without making it durable.
	if err := syncParent(dir); err != nil {
		return fmt.Errorf("sync private file deletion: %w", err)
	}
	return nil
}

func atomicWriteWithRename(dir, name string, contents []byte, rename func(string, string) error) error {
	return atomicWriteWithOperations(dir, name, contents, rename, syncParentDirectory)
}

func atomicWriteWithOperations(dir, name string, contents []byte, rename func(string, string) error, syncParent func(string) error) error {
	if name == "" || filepath.Base(name) != name {
		return errors.New("invalid private file name")
	}
	if err := EnsurePrivateDir(dir); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary private file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary private file: %w", err)
	}
	if err := protectPrivate(temporary, false); err != nil {
		return fmt.Errorf("protect temporary private file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary private file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary private file: %w", err)
	}
	if err := rename(temporaryPath, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("replace private file: %w", err)
	}
	if err := syncParent(dir); err != nil {
		return &ReplacementError{Err: fmt.Errorf("sync private file directory: %w", err)}
	}
	return nil
}
