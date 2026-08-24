//go:build unix

package command

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRoastDownloadsMapUnavailableWorkingDirectoryToLocalStorage(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	working := filepath.Join(root, "removed-working-directory")
	if err := os.Mkdir(working, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()
	if err := os.Remove(working); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); err == nil {
		if runtime.GOOS == "darwin" {
			t.Skip("Darwin retained a usable cwd after unlink; unavailable-cwd behavior cannot be exercised by this fixture")
		}
		t.Fatal("removed working directory remained available")
	}

	for _, args := range [][]string{
		{"--json", "roast", "chart", "download", commandRoastID, "chart.json"},
		{"--json", "roast", "profile", "download", commandRoastID, "1", "profile.alog"},
	} {
		runtime := Runtime{ConfigDir: "\x00", Getenv: func(string) string {
			t.Fatal("working-directory failure loaded authentication configuration")
			return ""
		}}
		result := runAuthCommand(t, runtime, args...)
		if result.code != 3 || result.stderr != "" || !strings.Contains(result.stdout, `"code":"local_storage_error"`) || strings.Contains(result.stdout, "invalid_destination") {
			t.Fatalf("Run(%q) = %#v", args, result)
		}
	}
}
