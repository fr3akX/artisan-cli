//go:build !windows

package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadRegularSnapshotRejectsExplicitParentTraversal(t *testing.T) {
	root := canonicalTestTempDir(t)
	path := filepath.Join(root, "review")
	if err := os.WriteFile(path, []byte("private body"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostile := root + string(filepath.Separator) + "child" + string(filepath.Separator) + ".." + string(filepath.Separator) + "review"
	if _, err := ReadRegularSnapshot(hostile, 64); !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want explicit parent traversal rejection", err)
	} else if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private body") {
		t.Fatalf("error leaks path or content: %v", err)
	}
}

func TestReadRegularSnapshotRejectsFIFONonblocking(t *testing.T) {
	path := filepath.Join(canonicalTestTempDir(t), "review-fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("FIFO prerequisite unavailable: %v", err)
	}

	started := time.Now()
	if _, err := ReadRegularSnapshot(path, 64); !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("FIFO error = %v, want ErrInvalidRegularSnapshot", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("FIFO rejection blocked for %v", elapsed)
	}
}
