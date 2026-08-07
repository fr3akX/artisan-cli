package securefile_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestAtomicWriteAndOpenPrivate(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "artisan")
	if err := securefile.AtomicWrite(dir, "config.json", []byte("original")); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	file, err := securefile.OpenPrivate(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("OpenPrivate: %v", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := string(contents); got != "original" {
		t.Fatalf("contents = %q, want original", got)
	}
}

func TestOpenPrivateRemainsBoundToOpenedFileAfterPathReplacement(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := securefile.AtomicWrite(dir, "config.json", []byte("original")); err != nil {
		t.Fatalf("AtomicWrite original: %v", err)
	}
	file, err := securefile.OpenPrivate(path)
	if err != nil {
		t.Fatalf("OpenPrivate: %v", err)
	}
	defer file.Close()

	if err := os.Rename(path, path+".opened"); err != nil {
		t.Fatalf("Rename opened file: %v", err)
	}
	if err := securefile.AtomicWrite(dir, "config.json", []byte("replacement")); err != nil {
		t.Fatalf("AtomicWrite replacement: %v", err)
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll opened file: %v", err)
	}
	if got := string(contents); got != "original" {
		t.Fatalf("opened handle contents = %q, want original", got)
	}
}

func TestAtomicWriteCleansTemporaryFileWhenRenameFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "occupied"), 0o700); err != nil {
		t.Fatalf("Mkdir occupied destination: %v", err)
	}
	if err := securefile.AtomicWrite(dir, "occupied", []byte("contents")); err == nil {
		t.Fatal("AtomicWrite succeeded when destination was a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "occupied" || !entries[0].IsDir() {
		t.Fatalf("entries after failed write = %#v, want only occupied directory", entries)
	}
}
