//go:build !windows

package auth_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/auth"
)

func TestFileStoreCreatesPrivateUnixPermissions(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "artisan")
	store := auth.NewFileStore(dir)
	if err := store.Save("secret-token"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %#o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("Stat credentials: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %#o, want 0600", got)
	}
}

func TestFileStoreRejectsUnsafeUnixCredentialMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode os.FileMode
	}{
		{name: "group readable", mode: 0o640},
		{name: "other readable", mode: 0o604},
		{name: "group writable", mode: 0o620},
		{name: "other executable", mode: 0o601},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "credentials.json")
			if err := os.WriteFile(path, []byte(`{"token":"secret"}`), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatalf("Chmod: %v", err)
			}
			if _, err := auth.NewFileStore(dir).Load(); err == nil || !strings.Contains(err.Error(), "unsafe_credentials") {
				t.Fatalf("Load error = %v, want unsafe_credentials", err)
			}
		})
	}
}
