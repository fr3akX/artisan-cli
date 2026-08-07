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
