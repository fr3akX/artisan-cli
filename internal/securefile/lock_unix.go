//go:build !windows

package securefile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func acquirePrivateLock(ctx context.Context, dir, name string, maxWait time.Duration) (func() error, error) {
	directory, err := openPrivateDirectory(dir)
	if err != nil {
		return nil, fmt.Errorf("open private lock directory: %w", err)
	}
	defer directory.Close()

	descriptor, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, &os.PathError{Op: "open private lock", Path: name, Err: err}
	}
	file := os.NewFile(uintptr(descriptor), name)
	if err := ProtectPrivateFile(file); err != nil {
		file.Close()
		return nil, err
	}

	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := unix.Flock(descriptor, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if contextErr := ctx.Err(); contextErr != nil {
				_ = unix.Flock(descriptor, unix.LOCK_UN)
				_ = file.Close()
				return nil, contextErr
			}
			if err := unix.Ftruncate(descriptor, 0); err != nil {
				unlockErr := unix.Flock(descriptor, unix.LOCK_UN)
				closeErr := file.Close()
				return nil, errors.Join(fmt.Errorf("empty private lock: %w", err), unlockErr, closeErr)
			}
			if err := unix.Fsync(descriptor); err != nil {
				unlockErr := unix.Flock(descriptor, unix.LOCK_UN)
				closeErr := file.Close()
				return nil, errors.Join(fmt.Errorf("sync empty private lock: %w", err), unlockErr, closeErr)
			}
			return func() error {
				unlockErr := unix.Flock(descriptor, unix.LOCK_UN)
				return errors.Join(unlockErr, file.Close())
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			file.Close()
			return nil, fmt.Errorf("acquire private lock: %w", err)
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			file.Close()
			return nil, errors.New("private lock acquisition timed out")
		case <-ticker.C:
		}
	}
}
