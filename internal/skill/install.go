package skill

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	Name     = "artisan-inventory"
	FileName = "SKILL.md"
)

var (
	ErrInvalidDirectory = errors.New("invalid skill directory")
	ErrDifferentContent = errors.New("installed skill differs")
	ErrUnsafeTarget     = errors.New("unsafe skill target")
)

// InstallResult describes an installation without exposing platform-specific details.
type InstallResult struct {
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Unchanged bool   `json:"unchanged"`
}

// Install atomically installs Content below root/artisan-inventory/SKILL.md.
func Install(root string, force bool) (InstallResult, error) {
	rootPath, err := validateRoot(root)
	if err != nil {
		return InstallResult{}, err
	}
	skillDir := filepath.Join(rootPath, Name)
	if err := ensureSkillDirectory(skillDir); err != nil {
		return InstallResult{}, err
	}
	target := filepath.Join(skillDir, FileName)
	result := InstallResult{Path: target}

	exists, identical, err := inspectTarget(target)
	if err != nil {
		return InstallResult{}, err
	}
	if exists && identical {
		result.Unchanged = true
		return result, nil
	}
	if exists && !force {
		return InstallResult{}, ErrDifferentContent
	}

	temporary, err := writeTemporary(skillDir)
	if err != nil {
		return InstallResult{}, fmt.Errorf("write temporary skill: %w", err)
	}
	defer os.Remove(temporary)

	if exists && force {
		if err := atomicReplace(temporary, target); err != nil {
			return InstallResult{}, fmt.Errorf("replace skill atomically: %w", err)
		}
		result.Installed = true
		return result, nil
	}

	if err := os.Link(temporary, target); err != nil {
		if !os.IsExist(err) {
			return InstallResult{}, fmt.Errorf("install skill atomically: %w", err)
		}
		_, identical, inspectErr := inspectTarget(target)
		if inspectErr != nil {
			return InstallResult{}, inspectErr
		}
		if identical {
			result.Unchanged = true
			return result, nil
		}
		if !force {
			return InstallResult{}, ErrDifferentContent
		}
		if err := atomicReplace(temporary, target); err != nil {
			return InstallResult{}, fmt.Errorf("replace raced skill atomically: %w", err)
		}
	}
	result.Installed = true
	return result, nil
}

func validateRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || hasParentComponent(root) {
		return "", ErrInvalidDirectory
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", ErrInvalidDirectory
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", ErrInvalidDirectory
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", ErrUnsafeTarget
	}
	if !info.IsDir() {
		return "", ErrInvalidDirectory
	}
	return absolute, nil
}

func hasParentComponent(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if component == ".." {
			return true
		}
	}
	return false
}

func ensureSkillDirectory(path string) error {
	if err := os.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create skill directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect skill directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrUnsafeTarget
	}
	return nil
}

func inspectTarget(path string) (exists, identical bool, err error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("inspect installed skill: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, false, ErrUnsafeTarget
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return false, false, fmt.Errorf("read installed skill: %w", err)
	}
	return true, bytes.Equal(contents, Content), nil
}

func writeTemporary(directory string) (path string, returnedErr error) {
	file, err := os.CreateTemp(directory, ".SKILL.md.tmp-*")
	if err != nil {
		return "", err
	}
	path = file.Name()
	defer func() {
		if returnedErr != nil {
			file.Close()
			os.Remove(path)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return "", err
	}
	if _, err := file.Write(Content); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}
