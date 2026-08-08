//go:build !windows

package securefile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestAtomicWriteCreatesPrivateUnixModes(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "artisan")
	if err := securefile.AtomicWrite(dir, "config.json", []byte("private")); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	directoryInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat directory: %v", err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %#o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %#o, want 0600", got)
	}
}

func TestOpenPrivateRejectsSymlinkAndUnsafeMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if file, err := securefile.OpenPrivate(link); err == nil {
		file.Close()
		t.Fatal("OpenPrivate followed a symlink")
	}

	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatalf("Chmod target: %v", err)
	}
	if file, err := securefile.OpenPrivate(target); err == nil {
		file.Close()
		t.Fatal("OpenPrivate accepted group-readable file")
	}
}
