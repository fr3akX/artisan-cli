//go:build darwin

package releasebuilder

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDirectoryHandlePath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const child = "child"
	if err := os.WriteFile(filepath.Join(dir, child), []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)

	got, err := directoryHandlePath(fd)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("directoryHandlePath() = %q, want canonical path %q", got, dir)
	}
	if _, err := os.Stat(filepath.Join(got, child)); err != nil {
		t.Fatalf("stat child through resolved handle path: %v", err)
	}
}
