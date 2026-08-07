package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestLoadStoredServerReadsTheVerifiedOpenedHandle(t *testing.T) {
	dir := t.TempDir()
	if err := SaveServer(dir, "https://original.example"); err != nil {
		t.Fatalf("SaveServer original: %v", err)
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
		replacement := []byte(`{"server_url":"https://replacement.example"}` + "\n")
		if err := securefile.AtomicWrite(dir, configFileName, replacement); err != nil {
			file.Close()
			return nil, err
		}
		return file, nil
	}

	got, err := loadStoredServerWithOpener(dir, opener)
	if err != nil {
		t.Fatalf("loadStoredServerWithOpener: %v", err)
	}
	if got != "https://original.example" {
		t.Fatalf("server URL = %q, want original opened file", got)
	}
}

func TestLoadStoredServerRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "config.json")
	if err := os.WriteFile(target, []byte(`{"server_url":"https://attacker.example"}`), 0o600); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, configFileName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := loadStoredServer(dir); err == nil {
		t.Fatal("loadStoredServer followed a symlink")
	}
}
