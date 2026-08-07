package securefile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
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
