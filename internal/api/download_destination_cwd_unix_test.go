//go:build unix

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadDestinationsPreserveUnavailableWorkingDirectoryErrors(t *testing.T) {
	withUnavailableDownloadWorkingDirectory(t, func(getwdErr error) {
		getwdCause := errors.Unwrap(getwdErr)
		preflightErr := PreflightDownloadDestination("profile.alog", false)
		if preflightErr == nil || errors.Is(preflightErr, ErrInvalidDownloadDestination) || getwdCause == nil || !errors.Is(preflightErr, getwdCause) {
			t.Fatalf("preflight error = %v, want environmental getwd cause from %v", preflightErr, getwdErr)
		}

		target, targetErr := newDownloadTarget("profile.alog", false, defaultDownloadOperations())
		if target != nil || targetErr == nil || errors.Is(targetErr, ErrInvalidDownloadDestination) || !errors.Is(targetErr, getwdCause) {
			t.Fatalf("target = %#v, error = %v, want environmental getwd cause from %v", target, targetErr, getwdErr)
		}
	})
}

func TestInventoryDownloadMapsUnavailableWorkingDirectoryToLocalStorage(t *testing.T) {
	withUnavailableDownloadWorkingDirectory(t, func(getwdErr error) {
		client, err := NewClient("http://127.0.0.1:1", "secret", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", "image.webp", false)
		if failure == nil || failure.Code != "local_storage_error" || failure.ExitCode != 3 || failure.Message != "Unable to store the image download safely" {
			t.Fatalf("failure = %#v for getwd error %v", failure, getwdErr)
		}
	})
}

func withUnavailableDownloadWorkingDirectory(t *testing.T, run func(error)) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	working := filepath.Join(root, "removed-working-directory")
	if err := os.Mkdir(working, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()
	if err := os.Remove(working); err != nil {
		t.Fatal(err)
	}
	_, getwdErr := os.Getwd()
	if getwdErr == nil {
		t.Fatal("removed working directory remained available")
	}
	run(getwdErr)
}
