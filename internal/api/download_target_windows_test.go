//go:build windows

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsDownloadPublishUsesHeldHandleAfterPostCheckSourceSwap(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	ops := defaultDownloadOperations()
	defaults := ops
	var movedVerified string
	ops.installNoReplace = func(identity *downloadFileIdentity, source, destination string) (bool, error) {
		movedVerified = source + ".moved"
		if err := os.Rename(source, movedVerified); err != nil {
			return false, err
		}
		if err := os.WriteFile(source, []byte("racer-source"), 0o600); err != nil {
			return false, err
		}
		return defaults.installNoReplace(identity, source, destination)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(target.Writer(), "verified-owned"); err != nil {
		t.Fatal(err)
	}
	result, installErr := target.Install(false)
	if installErr == nil || !result.Visible || !result.Durable || !errors.Is(installErr, errDownloadIdentityAmbiguous) {
		t.Fatalf("install = %#v, %v", result, installErr)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "verified-owned" {
		t.Fatalf("held-handle destination = %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(target.temporaryPath); err != nil || string(contents) != "racer-source" {
		t.Fatalf("racer source residue = %q, %v", contents, err)
	}
	if _, err := os.Lstat(movedVerified); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("held handle rename left old name: %v", err)
	}
}
