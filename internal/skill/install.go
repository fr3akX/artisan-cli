package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

const FileName = "SKILL.md"

var (
	ErrUnknownSkill           = errors.New("unknown skill")
	ErrInvalidDirectory       = errors.New("invalid skill directory")
	ErrDifferentContent       = errors.New("installed skill differs")
	ErrUnsafeTarget           = errors.New("unsafe skill target")
	ErrInstallLocationChanged = errors.New("skill install location changed")
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

// Install atomically and durably installs the selected embedded definition
// below root/name/SKILL.md while walking paths without following links.
func Install(root, name string, force bool) (InstallResult, error) {
	return installWithHooks(root, name, force, installHooks{})
}

func installWithHooks(root, name string, force bool, hooks installHooks) (InstallResult, error) {
	definition, ok := Lookup(name)
	if !ok {
		return InstallResult{}, ErrUnknownSkill
	}
	rootPath, err := normalizeRoot(root)
	if err != nil {
		return InstallResult{}, err
	}
	result, err := installPlatform(rootPath, definition, force, hooks)
	if err != nil {
		result.Path = ""
		return result, err
	}
	result.Path = filepath.Join(rootPath, definition.Name, FileName)
	return result, nil
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

func installLocationChanged(installed bool) error {
	if installed {
		return &securefile.ReplacementError{Err: ErrInstallLocationChanged}
	}
	return ErrInstallLocationChanged
}

func event(hooks installHooks, name string) {
	if hooks.onEvent != nil {
		hooks.onEvent(name)
	}
}
