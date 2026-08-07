package securefile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteRenameFailurePreservesExistingFileAndCleansTemporary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := AtomicWrite(dir, "config.json", []byte("existing")); err != nil {
		t.Fatalf("AtomicWrite existing: %v", err)
	}
	injected := errors.New("injected rename failure")
	err := atomicWriteWithRename(dir, "config.json", []byte("replacement"), func(string, string) error {
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("atomicWriteWithRename error = %v, want injected failure", err)
	}

	file, err := OpenPrivate(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("OpenPrivate existing: %v", err)
	}
	contents, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		t.Fatalf("ReadAll existing: %v", err)
	}
	if got := string(contents); got != "existing" {
		t.Fatalf("existing contents = %q, want existing", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("entries after rename failure = %#v, want only config.json", entries)
	}
}

func TestAtomicWriteOrdersRenameBeforeParentSync(t *testing.T) {
	dir := t.TempDir()
	var events []string
	err := atomicWriteWithOperations(dir, "journal.json", []byte("pending"), func(from, to string) error {
		events = append(events, "rename")
		return os.Rename(from, to)
	}, func(gotDir string) error {
		if gotDir != dir {
			t.Fatalf("sync dir = %q, want %q", gotDir, dir)
		}
		events = append(events, "sync-parent")
		return nil
	})
	if err != nil {
		t.Fatalf("atomicWriteWithOperations() error = %v", err)
	}
	if got := strings.Join(events, ","); got != "rename,sync-parent" {
		t.Fatalf("operation order = %q, want rename,sync-parent", got)
	}
}

func TestAtomicWriteReportsParentSyncFailureAfterRename(t *testing.T) {
	dir := t.TempDir()
	injected := errors.New("injected parent sync failure")
	err := atomicWriteWithOperations(dir, "journal.json", []byte("pending"), os.Rename, func(string) error {
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("atomicWriteWithOperations() error = %v, want injected failure", err)
	}
	if !ReplacementVisible(err) {
		t.Fatal("parent-sync failure did not report that replacement became visible")
	}
	contents, readErr := os.ReadFile(filepath.Join(dir, "journal.json"))
	if readErr != nil || string(contents) != "pending" {
		t.Fatal("rename did not occur before injected parent sync failure")
	}
}

func TestDurableRemoveOrdersRemovalBeforeParentSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte("credential"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var events []string
	err := durableRemoveWithOperations(dir, path, func(gotPath string) error {
		if gotPath != path {
			t.Fatalf("remove path = %q, want %q", gotPath, path)
		}
		events = append(events, "remove")
		return os.Remove(gotPath)
	}, func(gotDir string) error {
		if gotDir != dir {
			t.Fatalf("sync dir = %q, want %q", gotDir, dir)
		}
		events = append(events, "sync-parent")
		return nil
	})
	if err != nil {
		t.Fatalf("durableRemoveWithOperations() error = %v", err)
	}
	if got := strings.Join(events, ","); got != "remove,sync-parent" {
		t.Fatalf("operation order = %q, want remove,sync-parent", got)
	}
}

func TestDurableRemoveFailureDoesNotSyncParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	injected := errors.New("injected remove failure")
	synced := false
	err := durableRemoveWithOperations(dir, path, func(string) error {
		return injected
	}, func(string) error {
		synced = true
		return nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("durableRemoveWithOperations() error = %v, want injected failure", err)
	}
	if synced {
		t.Fatal("parent sync ran after failed removal")
	}
}

func TestDurableRemoveReportsParentSyncFailureAndSyncsExistingAbsence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	injected := errors.New("injected remove parent sync failure")
	for _, initiallyPresent := range []bool{true, false} {
		t.Run(fmt.Sprintf("present=%t", initiallyPresent), func(t *testing.T) {
			if initiallyPresent {
				if err := os.WriteFile(path, []byte("configuration"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}
			err := durableRemoveWithOperations(dir, path, os.Remove, func(string) error {
				return injected
			})
			if !errors.Is(err, injected) {
				t.Fatalf("durableRemoveWithOperations() error = %v, want injected failure", err)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("path remains after visible removal: %v", statErr)
			}
		})
	}
}
