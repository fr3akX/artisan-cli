package securefile

import (
	"errors"
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
	contents, readErr := os.ReadFile(filepath.Join(dir, "journal.json"))
	if readErr != nil || string(contents) != "pending" {
		t.Fatal("rename did not occur before injected parent sync failure")
	}
}
