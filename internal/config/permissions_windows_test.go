//go:build windows

package config_test

import (
	"path/filepath"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/windows"
)

func TestSaveServerCreatesPrivateWindowsDirectoryAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artisan")
	if err := config.SaveServer(dir, "https://artisan.example"); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	file, err := securefile.OpenPrivate(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("OpenPrivate config.json: %v", err)
	}
	file.Close()

	descriptor, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo directory: %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("directory DACL control: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("configuration directory DACL is not protected")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("directory DACL: %v", err)
	}
	if dacl == nil || dacl.AceCount != 6 {
		t.Fatalf("configuration directory ACE count = %v, want normalized count 6", dacl)
	}
}
