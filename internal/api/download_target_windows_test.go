//go:build windows

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsDownloadHeldParentPreventsParentRename(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "downloads")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "profile")
	ops := defaultDownloadOperations()
	ops.afterParentHeld = func(*downloadTarget) error { return os.Rename(directory, filepath.Join(root, "moved")) }
	target, err := newDownloadTarget(destination, false, ops)
	if target != nil || err == nil {
		t.Fatalf("parent rename unexpectedly succeeded: target=%#v err=%v", target, err)
	}
}

func TestWindowsDownloadPublishesExactCreatedHandleAfterSourceNameSwap(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	ops := defaultDownloadOperations()
	var replacement string
	ops.afterCreatedHandleBeforeProtection = func(target *downloadTarget) error {
		replacement = target.temporaryPath
		moved := replacement + ".moved"
		if err := os.Rename(replacement, moved); err != nil {
			return err
		}
		return os.WriteFile(replacement, []byte("racer-source"), 0o600)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(target.Writer(), "verified-owned"); err != nil {
		t.Fatal(err)
	}
	result, installErr := target.Install(false)
	if installErr != nil || !result.Visible() || !result.Durable() {
		t.Fatalf("install = %#v, %v", result, installErr)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "verified-owned" {
		t.Fatalf("held-handle destination = %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(replacement); err != nil || string(contents) != "racer-source" {
		t.Fatalf("racer source = %q, %v", contents, err)
	}
}

func TestWindowsDownloadFlushFailureIsVisibleButNotDurable(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	ops := defaultDownloadOperations()
	ops.flushFile = func(*os.File) error { return errors.New("flush") }
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "verified")
	result, err := target.Install(false)
	if err == nil || result.Publication != publicationExact || !result.Visible() || result.Durable() || result.Durability != durabilityUncertain {
		t.Fatalf("install = %#v, %v", result, err)
	}
}
