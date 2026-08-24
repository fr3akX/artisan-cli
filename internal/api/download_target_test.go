package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadTargetRejectsInvalidMissingAndExistingDestination(t *testing.T) {
	for _, destination := range []string{"", ".", "..", string(filepath.Separator)} {
		if target, err := newDownloadTarget(destination, false, defaultDownloadOperations()); target != nil || !errors.Is(err, errInvalidDownloadDestination) {
			t.Fatalf("destination %q: target=%#v err=%v", destination, target, err)
		}
	}
	if target, err := newDownloadTarget(filepath.Join(t.TempDir(), "missing", "file"), false, defaultDownloadOperations()); target != nil || err == nil {
		t.Fatalf("missing parent: target=%#v err=%v", target, err)
	}
	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if target, err := newDownloadTarget(existing, false, defaultDownloadOperations()); target != nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing: target=%#v err=%v", target, err)
	}
	directoryDestination := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(directoryDestination, 0o700); err != nil {
		t.Fatal(err)
	}
	if target, err := newDownloadTarget(directoryDestination, true, defaultDownloadOperations()); target != nil || !errors.Is(err, errInvalidDownloadDestination) {
		t.Fatalf("directory target=%#v err=%v", target, err)
	}
}

func TestDownloadTargetInstallNoForceAndForce(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "no force", true: "force"}[force], func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			if force {
				if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			target, err := newDownloadTarget(destination, force, defaultDownloadOperations())
			if err != nil {
				t.Fatal(err)
			}
			defer target.Abort()
			if _, err := io.WriteString(target.Writer(), "new"); err != nil {
				t.Fatal(err)
			}
			result, err := target.Install(force)
			if err != nil || result.Publication != publicationExact || !result.Visible() || !result.Durable() {
				t.Fatalf("install = %#v, %v", result, err)
			}
			contents, _ := os.ReadFile(destination)
			if string(contents) != "new" {
				t.Fatalf("destination = %q", contents)
			}
			target.Abort()
			contents, _ = os.ReadFile(destination)
			if string(contents) != "new" {
				t.Fatalf("Abort removed destination")
			}
		})
	}
}

func TestDownloadTargetResetAndPreNativeFailuresDoNotPublish(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*downloadOperations)
		reset  bool
	}{
		{"reset", func(ops *downloadOperations) { ops.resetFile = func(*os.File) error { return errors.New("reset") } }, true},
		{"sync", func(ops *downloadOperations) { ops.syncFile = func(*os.File) error { return errors.New("sync") } }, false},
		{"close", func(ops *downloadOperations) {
			ops.closeFile = func(file *os.File) error { _ = file.Close(); return errors.New("close") }
		}, false},
		{"native no operation", func(ops *downloadOperations) {
			ops.nativeOperation = func(func() error) error { return errors.New("native") }
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile")
			ops := defaultDownloadOperations()
			test.inject(&ops)
			target, err := newDownloadTarget(destination, false, ops)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(target.Writer(), "bytes")
			if test.reset {
				err = target.Reset()
			} else {
				_, err = target.Install(false)
			}
			if err == nil {
				t.Fatal("failure succeeded")
			}
			target.Abort()
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination visible: %v", err)
			}
		})
	}
}

func TestDownloadTargetPreNativeCallbackRunsAfterSealingAndCanRejectWithoutPublication(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "no force", true: "force"}[force], func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile")
			if force {
				if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ops := defaultDownloadOperations()
			var sealed, callbackInvoked, nativeInvoked bool
			ops.afterSealedBeforeCandidate = func(*downloadTarget) error {
				sealed = true
				return nil
			}
			ops.nativeOperation = func(operation func() error) error {
				nativeInvoked = true
				return operation()
			}
			target, err := newDownloadTarget(destination, force, ops)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(target.Writer(), "new")
			callbackErr := errors.New("publication rejected")
			result, err := target.InstallContextBeforeNative(context.Background(), force, func() error {
				callbackInvoked = true
				if !sealed || target.state != downloadTargetSealed {
					t.Fatalf("callback before seal: sealed=%v state=%v", sealed, target.state)
				}
				return callbackErr
			})
			if !errors.Is(err, callbackErr) || result.Publication != publicationNone || result.Visibility != visibilityNotVisible {
				t.Fatalf("install = %#v, %v", result, err)
			}
			if !callbackInvoked || nativeInvoked || target.nativeOperationInvoked {
				t.Fatalf("callback=%v native=%v target.native=%v", callbackInvoked, nativeInvoked, target.nativeOperationInvoked)
			}
			target.Abort()
			contents, readErr := os.ReadFile(destination)
			if force {
				if readErr != nil || string(contents) != "existing" {
					t.Fatalf("forced destination = %q, %v", contents, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("destination published: %v", readErr)
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadTargetReconcilesOperationPerformedThenError(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	ops.nativeOperation = func(operation func() error) error {
		if err := operation(); err != nil {
			return err
		}
		return errors.New("reported after operation")
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "exact")
	result, err := target.Install(false)
	if err == nil || result.Publication != publicationExact || !result.Visible() {
		t.Fatalf("install = %#v, %v", result, err)
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != "exact" {
		t.Fatalf("destination = %q", contents)
	}
}

func TestDownloadTargetPerformedThenErroredAndPostNativeDestinationDisappearsIsAmbiguous(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	ops.nativeOperation = func(operation func() error) error {
		if err := operation(); err != nil {
			return err
		}
		return errors.New("reported after operation")
	}
	published := destination + ".published"
	ops.afterNativeBeforeReconcile = func(*downloadTarget) error {
		return os.Rename(destination, published)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "exact")
	result, err := target.Install(false)
	if err == nil || result.Publication != publicationAmbiguous || result.Visibility != visibilityAmbiguous {
		t.Fatalf("install = %#v, %v", result, err)
	}
	target.Abort()
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("requested destination unexpectedly exists: %v", err)
	}
	contents, _ := os.ReadFile(published)
	if string(contents) != "exact" {
		t.Fatalf("published residue = %q", contents)
	}
}

func TestDownloadTargetPostNativeDestinationSwapIsAmbiguousAndCompetitorSurvives(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	published := destination + ".published"
	ops.afterNativeBeforeReconcile = func(*downloadTarget) error {
		if err := os.Rename(destination, published); err != nil {
			return err
		}
		return os.WriteFile(destination, []byte("competitor"), 0o600)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "exact")
	result, err := target.Install(false)
	if err == nil || result.Publication != publicationAmbiguous || result.Visible() {
		t.Fatalf("install = %#v, %v", result, err)
	}
	target.Abort()
	contents, _ := os.ReadFile(destination)
	if string(contents) != "competitor" {
		t.Fatalf("competitor = %q", contents)
	}
	contents, _ = os.ReadFile(published)
	if string(contents) != "exact" {
		t.Fatalf("published residue = %q", contents)
	}
}

func TestDownloadTargetDurabilityFailureIsExactVisibleButUncertain(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	injectDownloadDurabilityFailure(&ops)
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "exact")
	result, err := target.Install(false)
	if err == nil || result.Publication != publicationExact || !result.Visible() || result.Durability != durabilityUncertain || result.Durable() {
		t.Fatalf("install = %#v, %v", result, err)
	}
}
