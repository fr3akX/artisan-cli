package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestFileStoreLoadReadsTheVerifiedOpenedHandle(t *testing.T) {
	dir := t.TempDir()
	store := &fileStore{configDir: dir}
	if err := store.Save("original-token"); err != nil {
		t.Fatalf("Save original: %v", err)
	}

	opener := func(path string) (*os.File, error) {
		file, err := securefile.OpenPrivate(path)
		if err != nil {
			return nil, err
		}
		if err := os.Rename(path, path+".opened"); err != nil {
			file.Close()
			return nil, err
		}
		replacement := []byte(`{"token":"replacement-token"}` + "\n")
		if err := securefile.AtomicWrite(dir, credentialsFileName, replacement); err != nil {
			file.Close()
			return nil, err
		}
		return file, nil
	}

	got, err := store.load(opener)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != "original-token" {
		t.Fatalf("token = %q, want original opened file", got)
	}
}

func TestFileStoreLoadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "credentials.json")
	if err := os.WriteFile(target, []byte(`{"token":"attacker-token"}`), 0o600); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, credentialsFileName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := (&fileStore{configDir: dir}).Load(); err == nil {
		t.Fatal("Load followed a credential symlink")
	}
}
