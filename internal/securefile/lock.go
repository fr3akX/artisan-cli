package securefile

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// AcquirePrivateLock acquires an exclusive interprocess lock represented by a
// credential-free file in dir. Acquisition stops on caller cancellation or
// after maxWait, whichever occurs first.
func AcquirePrivateLock(ctx context.Context, dir, name string, maxWait time.Duration) (func() error, error) {
	if ctx == nil {
		return nil, errors.New("private lock context is required")
	}
	if name == "" || filepath.Base(name) != name || maxWait <= 0 {
		return nil, errors.New("invalid private lock parameters")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := EnsurePrivateDir(dir); err != nil {
		return nil, err
	}
	return acquirePrivateLock(ctx, dir, name, maxWait)
}
