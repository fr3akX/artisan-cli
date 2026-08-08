//go:build windows

package securefile_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestPrivateLockHasProtectedWindowsACLAndRejectsReparsePoints(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artisan")
	if err := securefile.EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := securefile.AtomicWrite(dir, ".auth-state.lock", []byte("pre-populated")); err != nil {
		t.Fatal(err)
	}
	release, err := securefile.AcquirePrivateLock(context.Background(), dir, ".auth-state.lock", time.Second)
	if err != nil {
		t.Fatalf("AcquirePrivateLock() error = %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	file, err := securefile.OpenPrivate(filepath.Join(dir, ".auth-state.lock"))
	if err != nil {
		t.Fatalf("OpenPrivate(lock) error = %v", err)
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || info.Size() != 0 {
		t.Fatalf("empty durable lock info=%v stat=%v close=%v", info, statErr, closeErr)
	}

	target := filepath.Join(dir, "target")
	if err := securefile.AtomicWrite(dir, "target", nil); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".linked-lock")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if release, err := securefile.AcquirePrivateLock(context.Background(), dir, ".linked-lock", time.Second); err == nil {
		_ = release()
		t.Fatal("AcquirePrivateLock() followed a lock-file reparse point")
	}
}
