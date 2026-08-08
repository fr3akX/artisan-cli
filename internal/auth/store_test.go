package auth_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/auth"
)

func TestFileStoreRoundTripAndRemove(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "nested", "artisan")
	store := auth.NewFileStore(dir)
	if err := store.Save("secret-token"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "secret-token" {
		t.Fatalf("Load() = %q, want secret-token", got)
	}
	if err := store.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := store.Load(); !os.IsNotExist(err) {
		t.Fatalf("Load after Remove error = %v, want not-exist", err)
	}
	if err := store.Remove(); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestFileStoreRejectsInvalidTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty"},
		{name: "spaces", token: "   \t"},
		{name: "line feed", token: "token\nsecond-line"},
		{name: "carriage return", token: "token\rsecond-line"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := auth.NewFileStore(t.TempDir())
			if err := store.Save(tt.token); err == nil {
				t.Fatalf("Save(%q) succeeded, want error", tt.token)
			}
		})
	}
}

func TestFileStoreRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
	}{
		{name: "unknown field", contents: `{"token":"secret","extra":true}`},
		{name: "trailing object", contents: `{"token":"secret"}{"token":"second"}`},
		{name: "blank token", contents: `{"token":""}`},
		{name: "newline token", contents: `{"token":"first\nsecond"}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "credentials.json")
			if err := os.WriteFile(path, []byte(tt.contents), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := auth.NewFileStore(dir).Load(); err == nil {
				t.Fatalf("Load succeeded for %s", tt.name)
			}
		})
	}
}

func TestFileStoreUsesAtomicSameDirectoryWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := auth.NewFileStore(dir)
	if err := store.Save("first-token"); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	if err := store.Save("replacement-token"); err != nil {
		t.Fatalf("Save replacement: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "credentials.json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("config directory entries = %v, want only credentials.json", names)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(contents), "first-token") {
		t.Fatalf("credentials retain replaced token: %q", contents)
	}
}
