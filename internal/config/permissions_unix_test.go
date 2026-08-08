//go:build !windows

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/config"
)

func TestSaveServerCreatesPrivateUnixDirectoryAndFile(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "artisan")
	if err := config.SaveServer(dir, "https://artisan.example"); err != nil {
		t.Fatalf("SaveServer: %v", err)
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
		t.Fatalf("Stat config: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", got)
	}
}

func TestLoadRejectsUnsafeUnixConfigMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := config.SaveServer(dir, "https://artisan.example"); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod config: %v", err)
	}
	_, err := config.Load(dir, func(key string) string {
		if key == "ARTISAN_SERVER_TOKEN" {
			return "environment-token"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe_configuration") {
		t.Fatalf("Load error = %v, want unsafe_configuration", err)
	}
}
