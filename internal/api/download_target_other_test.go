//go:build !linux && !darwin && !windows

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFallbackDownloadParentSwapFailsBeforeSourceCreation(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "downloads")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "profile")
	moved := filepath.Join(root, "held")
	ops := defaultDownloadOperations()
	ops.afterParentHeld = func(*downloadTarget) error {
		if err := os.Rename(directory, moved); err != nil {
			return err
		}
		return os.Mkdir(directory, 0o700)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if target != nil || err == nil {
		t.Fatalf("target=%#v err=%v", target, err)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("replacement parent entries=%v,%v", entries, readErr)
	}
}

func TestFallbackDownloadForceNoOperationPreservesBackupSourceAndOldDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	ops.nativeOperation = func(func() error) error { return errors.New("native") }
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, err := target.Install(true)
	if err == nil || result.Publication == publicationExact {
		t.Fatalf("install=%#v,%v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("old destination=%q", contents)
	}
}
