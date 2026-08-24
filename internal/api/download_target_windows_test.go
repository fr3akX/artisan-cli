//go:build windows

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
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

func TestWindowsDownloadOperationErrorReconciliationAndNamedSourceCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation func(func() error) error
		prepare   func(string) error
		want      downloadPublicationState
		wantBytes string
	}{
		{
			name:      "no operation",
			operation: func(func() error) error { return errors.New("native-no-op") },
			want:      publicationNone,
		},
		{
			name:      "collision",
			operation: func(operation func() error) error { return operation() },
			prepare:   func(destination string) error { return os.WriteFile(destination, []byte("competitor"), 0o600) },
			want:      publicationNone,
			wantBytes: "competitor",
		},
		{
			name: "performed then error",
			operation: func(operation func() error) error {
				if err := operation(); err != nil {
					return err
				}
				return errors.New("reported-after-operation")
			},
			want:      publicationExact,
			wantBytes: "verified",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			ops := defaultDownloadOperations()
			ops.nativeOperation = func(operation func() error) error {
				if test.prepare != nil {
					if err := test.prepare(destination); err != nil {
						return err
					}
				}
				return test.operation(operation)
			}
			target, err := newDownloadTarget(destination, false, ops)
			if err != nil {
				t.Fatal(err)
			}
			temporary := target.temporaryPath
			_, _ = io.WriteString(target.Writer(), "verified")
			result, installErr := target.Install(false)
			if installErr == nil || result.Publication != test.want {
				t.Fatalf("install=%#v,%v", result, installErr)
			}
			target.Abort() // deferred caller cleanup must be idempotent after every terminal result
			if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned source leaked: %v", err)
			}
			if test.wantBytes != "" {
				if contents, _ := os.ReadFile(destination); string(contents) != test.wantBytes {
					t.Fatalf("destination=%q", contents)
				}
			}
		})
	}
}

func TestWindowsDownloadPerformedThenErrorAndPostNativeDisappearanceIsAmbiguous(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	published := destination + ".published"
	ops := defaultDownloadOperations()
	ops.nativeOperation = func(operation func() error) error {
		if err := operation(); err != nil {
			return err
		}
		return errors.New("reported-after-operation")
	}
	ops.afterNativeBeforeReconcile = func(*downloadTarget) error { return os.Rename(destination, published) }
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "verified")
	result, installErr := target.Install(false)
	if installErr == nil || result.Publication != publicationAmbiguous || result.Visibility != visibilityAmbiguous {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	if contents, _ := os.ReadFile(published); string(contents) != "verified" {
		t.Fatalf("published=%q", contents)
	}
}

func TestWindowsDownloadAbortUsesExactHandleDispositionAndLeavesNoOwnedLeak(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "profile.alog")
	target, err := newDownloadTarget(destination, false, defaultDownloadOperations())
	if err != nil {
		t.Fatal(err)
	}
	owned := target.temporaryPath
	moved := owned + ".moved"
	if err := os.Rename(owned, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(owned, []byte("competitor"), 0o600); err != nil {
		t.Fatal(err)
	}
	target.Abort()
	if contents, err := os.ReadFile(owned); err != nil || string(contents) != "competitor" {
		t.Fatalf("competitor=%q,%v", contents, err)
	}
	if _, err := os.Lstat(moved); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact held source leaked: %v", err)
	}
}

func TestWindowsDownloadForceUsesPOSIXReplaceAndClosesHandles(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := newDownloadTarget(destination, true, defaultDownloadOperations())
	if err != nil {
		t.Fatal(err)
	}
	platform := target.platform.(*heldWindowsDownloadPublication)
	writerHandle := windows.Handle(platform.writer.Fd())
	sourceHandle := windows.Handle(platform.source.Fd())
	parentHandle := platform.parent
	temporary := target.temporaryPath
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(true)
	if installErr != nil || result.Publication != publicationExact || !result.Visible() {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "new" {
		t.Fatalf("destination=%q", contents)
	}
	if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary=%v", err)
	}
	for name, handle := range map[string]windows.Handle{"writer": writerHandle, "source": sourceHandle, "parent": parentHandle} {
		if _, err := windowsDownloadInfo(handle); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			t.Fatalf("%s handle remains open: %v", name, err)
		}
	}
}

func TestWindowsDispositionFallbackIsRestrictedToUnsupportedErrors(t *testing.T) {
	for _, err := range []error{windows.ERROR_INVALID_FUNCTION, windows.ERROR_INVALID_PARAMETER, windows.ERROR_NOT_SUPPORTED, windows.ERROR_CALL_NOT_IMPLEMENTED} {
		if !unsupportedWindowsDispositionEx(err) {
			t.Fatalf("unsupported error rejected: %v", err)
		}
	}
	for _, err := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION} {
		if unsupportedWindowsDispositionEx(err) {
			t.Fatalf("operational error would be hidden: %v", err)
		}
	}
}

func TestWindowsDownloadFileOrDirectoryFlushFailureIsVisibleButNotDurable(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*downloadOperations)
		marker string
	}{
		{name: "file", inject: func(ops *downloadOperations) {
			ops.flushFile = func(*os.File) error { return errors.New("file flush") }
		}, marker: "file flush"},
		{name: "directory", inject: func(ops *downloadOperations) {
			ops.flushDirectory = func(*os.File) error { return errors.New("directory flush") }
		}, marker: "directory flush"},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			ops := defaultDownloadOperations()
			var directoryFlushCalls int
			if test.name == "file" {
				ops.flushDirectory = func(*os.File) error {
					directoryFlushCalls++
					return nil
				}
			}
			test.inject(&ops)
			target, err := newDownloadTarget(destination, false, ops)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(target.Writer(), "verified")
			result, installErr := target.Install(false)
			if installErr == nil || !strings.Contains(installErr.Error(), test.marker) || result.Publication != publicationExact || !result.Visible() || result.Durable() || result.Durability != durabilityUncertain {
				t.Fatalf("install = %#v, %v", result, installErr)
			}
			if test.name == "file" && directoryFlushCalls != 1 {
				t.Fatalf("directory flush calls=%d", directoryFlushCalls)
			}
		})
	}
}
