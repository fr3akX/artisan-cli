//go:build linux

package skill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/unix"
)

func TestNativeNoReplaceDoesNotRequireHardLinks(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "temporary"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	linkCalled := false
	err = renameNoReplaceAtWithOperations(fd, "temporary", "target", unix.Renameat2,
		func(int, string, int, string, int) error { linkCalled = true; return unix.EPERM })
	if err != nil || linkCalled {
		t.Fatalf("rename error = %v, linkCalled = %t", err, linkCalled)
	}
	assertFileEquals(t, filepath.Join(directory, "target"), []byte("new"))
}

func TestNoReplaceFallbackNeverClobbers(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "temporary"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "target"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	err = renameNoReplaceAtWithOperations(fd, "temporary", "target",
		func(int, string, int, string, uint) error { return unix.ENOSYS }, unix.Linkat)
	if !errors.Is(err, unix.EEXIST) {
		t.Fatalf("fallback error = %v, want EEXIST", err)
	}
	assertFileEquals(t, filepath.Join(directory, "target"), []byte("old"))
}

func TestInstallWalksRootComponentsHandleRelativelyWithoutFollowingSwap(t *testing.T) {
	parent := t.TempDir()
	container := filepath.Join(parent, "container")
	root := filepath.Join(container, "root")
	moved := filepath.Join(parent, "opened-container")
	outside := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outside, "root"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := installWithHooks(root, false, installHooks{afterRootComponentOpen: func(component string) error {
		if component != "container" {
			return nil
		}
		if err := os.Rename(container, moved); err != nil {
			return err
		}
		return os.Symlink(outside, container)
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, filepath.Join(moved, "root", Name, FileName), Content)
	assertPathAbsent(t, filepath.Join(outside, "root", Name))
}

func TestInstallAnchorsOpenedRootAcrossPathSwap(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	moved := filepath.Join(parent, "opened-root")
	outside := t.TempDir()
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := installWithHooks(root, false, installHooks{afterRootOpen: func() error {
		if err := os.Rename(root, moved); err != nil {
			return err
		}
		return os.Symlink(outside, root)
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, filepath.Join(moved, Name, FileName), Content)
	assertPathAbsent(t, filepath.Join(outside, Name))
}

func TestInstallAnchorsOpenedSkillDirectoryAcrossPathSwap(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "opened-skill")
	visible := filepath.Join(root, Name)
	outside := t.TempDir()
	if err := os.Mkdir(visible, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := installWithHooks(root, false, installHooks{afterSkillDirOpen: func() error {
		if err := os.Rename(visible, original); err != nil {
			return err
		}
		return os.Symlink(outside, visible)
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, filepath.Join(original, FileName), Content)
	assertPathAbsent(t, filepath.Join(outside, FileName))
}

func TestInstallTargetSymlinkRaceCannotRedirectWrite(t *testing.T) {
	root := t.TempDir()
	skillDirectory := filepath.Join(root, Name)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := installWithHooks(root, true, installHooks{beforeCommit: func() error {
		return os.Symlink(outside, filepath.Join(skillDirectory, FileName))
	}})
	if !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("Install() error = %v, want ErrUnsafeTarget", err)
	}
	assertFileEquals(t, outside, []byte("outside"))
}

func TestInstallSyncOrderingAndVisibleFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, Name), 0o755); err != nil {
		t.Fatal(err)
	}
	var events []string
	injected := errors.New("injected directory sync failure")
	_, err := installWithHooks(root, false, installHooks{
		onEvent: func(event string) { events = append(events, event) },
		syncDirectory: func(*os.File) error {
			return injected
		},
	})
	if !errors.Is(err, injected) || !securefile.ReplacementVisible(err) {
		t.Fatalf("Install() error = %v, want visible injected failure", err)
	}
	if want := []string{"file-sync", "commit", "directory-sync"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	assertFileEquals(t, filepath.Join(root, Name, FileName), Content)
}

func TestInstallFileSyncFailureLeavesNoTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, Name), 0o755); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected file sync failure")
	_, err := installWithHooks(root, false, installHooks{syncFile: func(*os.File) error { return injected }})
	if !errors.Is(err, injected) || securefile.ReplacementVisible(err) {
		t.Fatalf("Install() error = %v, want non-visible injected failure", err)
	}
	assertPathAbsent(t, filepath.Join(root, Name, FileName))
	matches, globErr := filepath.Glob(filepath.Join(root, Name, ".SKILL.md.tmp-*"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, error = %v", matches, globErr)
	}
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s content differs", path)
	}
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists or cannot be checked: %v", path, err)
	}
}
