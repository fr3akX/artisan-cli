//go:build !windows

package securefile_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestPrivateLockIsPrivateAndRejectsUnixSymlinks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artisan")
	release, err := securefile.AcquirePrivateLock(context.Background(), dir, ".auth-state.lock", time.Second)
	if err != nil {
		t.Fatalf("AcquirePrivateLock() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ".auth-state.lock"))
	if err != nil {
		t.Fatalf("Stat(lock) error = %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %v, want private regular 0600", info.Mode())
	}
	if err := release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".linked-lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if release, err := securefile.AcquirePrivateLock(context.Background(), dir, ".linked-lock", time.Second); err == nil {
		_ = release()
		t.Fatal("AcquirePrivateLock() followed a lock-file symlink")
	}
}
