//go:build windows

package skill

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsInstallUsesHandleRelativeAtomicCreateAndReplace(t *testing.T) {
	root := t.TempDir()
	result, err := Install(root, false)
	if err != nil || !result.Installed {
		t.Fatalf("first Install() = %#v, %v", result, err)
	}
	target := filepath.Join(root, Name, FileName)
	assertWindowsContent(t, target, Content)
	if err := os.WriteFile(target, []byte("different"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = Install(root, true)
	if err != nil || !result.Installed {
		t.Fatalf("forced Install() = %#v, %v", result, err)
	}
	assertWindowsContent(t, target, Content)
}

func TestWindowsInstallDetectsRequestedRootIdentityChange(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	moved := filepath.Join(parent, "opened-root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := installWithHooks(root, false, installHooks{afterRootOpen: func() error {
		if err := os.Rename(root, moved); err != nil {
			return err
		}
		return os.Mkdir(root, 0o755)
	}})
	if !errors.Is(err, ErrInstallLocationChanged) || !InstallVisible(err) || result.Path != "" {
		t.Fatalf("Install() = %#v, %v; want visible location-changed error without path", result, err)
	}
	assertWindowsContent(t, filepath.Join(moved, Name, FileName), Content)
	if _, err := os.Stat(filepath.Join(root, Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement root was modified: %v", err)
	}
}

func TestWindowsInstallDetectsSkillDirectoryIdentityChange(t *testing.T) {
	root := t.TempDir()
	visible := filepath.Join(root, Name)
	moved := filepath.Join(root, "opened-skill")
	if err := os.Mkdir(visible, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := installWithHooks(root, false, installHooks{afterSkillDirOpen: func() error {
		if err := os.Rename(visible, moved); err != nil {
			return err
		}
		return os.Mkdir(visible, 0o755)
	}})
	if !errors.Is(err, ErrInstallLocationChanged) || !InstallVisible(err) || result.Path != "" {
		t.Fatalf("Install() = %#v, %v; want visible location-changed error without path", result, err)
	}
	assertWindowsContent(t, filepath.Join(moved, FileName), Content)
	if _, err := os.Stat(filepath.Join(visible, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement skill directory was modified: %v", err)
	}
}

func TestWindowsInstallRejectsTargetReparsePoint(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, Name)
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, FileName)); err != nil {
		t.Skipf("creating a Windows symlink requires unavailable privilege: %v", err)
	}
	if _, err := Install(root, true); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("Install() error = %v, want ErrUnsafeTarget", err)
	}
	assertWindowsContent(t, outside, []byte("outside"))
}

func assertWindowsContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s content differs", path)
	}
}
