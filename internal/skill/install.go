package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/fr3akX/artisan-cli/internal/securefile"
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

type installHooks struct {
	afterRootComponentOpen func(string) error
	afterRootOpen          func() error
	afterSkillDirOpen      func() error
	beforeCommit           func() error
	syncFile               func(*os.File) error
	syncDirectory          func(*os.File) error
	onEvent                func(string)
}

// Install atomically and durably installs Content below
// root/artisan-inventory/SKILL.md by walking root components without following
// links, then keeping all child operations relative to opened directory handles.
func Install(root string, force bool) (InstallResult, error) {
	return installWithHooks(root, force, installHooks{})
}

func installWithHooks(root string, force bool, hooks installHooks) (InstallResult, error) {
	rootPath, err := normalizeRoot(root)
	if err != nil {
		return InstallResult{}, err
	}
	result, err := installPlatform(rootPath, force, hooks)
	if result.Path == "" {
		result.Path = filepath.Join(rootPath, Name, FileName)
	}
	return result, err
}

// InstallVisible reports whether an install error occurred after the canonical
// target became visible, making durability (but not file contents) ambiguous.
func InstallVisible(err error) bool { return securefile.ReplacementVisible(err) }

func normalizeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || hasParentComponent(root) {
		return "", ErrInvalidDirectory
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
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

func runHook(hook func() error) error {
	if hook == nil {
		return nil
	}
	return hook()
}

func event(hooks installHooks, name string) {
	if hooks.onEvent != nil {
		hooks.onEvent(name)
	}
}
