package securefile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestPrivateLockSerializesAcquisitionAndHonorsCancellation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artisan")
	release, err := securefile.AcquirePrivateLock(context.Background(), dir, ".auth-state.lock", time.Second)
	if err != nil {
		t.Fatalf("first AcquirePrivateLock() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		secondRelease, secondErr := securefile.AcquirePrivateLock(ctx, dir, ".auth-state.lock", time.Second)
		if secondRelease != nil {
			_ = secondRelease()
		}
		result <- secondErr
	}()
	select {
	case err := <-result:
		t.Fatalf("contended acquisition returned before cancellation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("contended acquisition error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("contended acquisition ignored cancellation")
	}

	if err := release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	release, err = securefile.AcquirePrivateLock(context.Background(), dir, ".auth-state.lock", time.Second)
	if err != nil {
		t.Fatalf("acquisition after release error = %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release() error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, ".auth-state.lock"))
	if err != nil {
		t.Fatalf("ReadFile(lock) error = %v", err)
	}
	if len(contents) != 0 {
		t.Fatalf("lock contains %d bytes, want no credential material", len(contents))
	}
}

func TestPrivateLockRejectsUnsafeParameters(t *testing.T) {
	for _, test := range []struct {
		name    string
		ctx     context.Context
		lock    string
		maxWait time.Duration
	}{
		{name: "nil context", lock: "lock", maxWait: time.Second},
		{name: "empty name", ctx: context.Background(), maxWait: time.Second},
		{name: "traversal name", ctx: context.Background(), lock: "../lock", maxWait: time.Second},
		{name: "unbounded wait", ctx: context.Background(), lock: "lock"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if release, err := securefile.AcquirePrivateLock(test.ctx, t.TempDir(), test.lock, test.maxWait); err == nil {
				_ = release()
				t.Fatal("AcquirePrivateLock() accepted unsafe parameters")
			}
		})
	}
}
