//go:build !linux && !darwin && !windows

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestFallbackDownloadPartialCandidateFailuresAreTrackedAndCleaned(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*downloadOperations)
	}{
		{"copy", func(ops *downloadOperations) {
			ops.copyCandidate = func(destination io.Writer, source io.Reader) (int64, error) {
				buffer := make([]byte, 2)
				count, _ := source.Read(buffer)
				written, _ := destination.Write(buffer[:count])
				return int64(written), errors.New("copy")
			}
		}},
		{"sync", func(ops *downloadOperations) { ops.syncCandidate = func(*os.File) error { return errors.New("sync") } }},
		{"hash", func(ops *downloadOperations) {
			ops.digestCandidate = func(*os.File) (int64, [32]byte, error) { return 0, [32]byte{}, errors.New("hash") }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			destination := filepath.Join(directory, "profile")
			ops := defaultDownloadOperations()
			test.inject(&ops)
			target, err := newDownloadTarget(destination, false, ops)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(target.Writer(), "new")
			result, installErr := target.Install(false)
			if installErr == nil || result.Publication != publicationNone {
				t.Fatalf("install=%#v,%v", result, installErr)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("partial residue=%v", entries)
			}
		})
	}
}

func TestFallbackDownloadCandidateSwapIsRetainedWithoutTouchingOldDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	var racer, held string
	ops.afterCandidateVerifiedBeforeNative = func(target *downloadTarget) error {
		p := target.platform.(*heldOtherDownloadPublication)
		racer = filepath.Join(target.directory, p.candidate)
		held = racer + ".held"
		if err := os.Rename(racer, held); err != nil {
			return err
		}
		return os.WriteFile(racer, []byte("racer"), 0o600)
	}
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(true)
	if installErr == nil || result.Publication != publicationNone {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("destination=%q", contents)
	}
	if contents, _ := os.ReadFile(racer); string(contents) != "racer" {
		t.Fatalf("racer=%q", contents)
	}
	if contents, _ := os.ReadFile(held); string(contents) != "new" {
		t.Fatalf("held=%q", contents)
	}
}

func TestFallbackDownloadExactPublicationPreservesSourceCleanupError(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	ops.afterCleanupCheck = func(_ *downloadTarget, name string) error {
		if strings.Contains(name, ".tmp-") {
			return errors.New("source-cleanup")
		}
		return nil
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(false)
	if installErr == nil || result.Publication != publicationExact || !result.Visible() || !strings.Contains(installErr.Error(), "source-cleanup") {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "new" {
		t.Fatalf("destination=%q", contents)
	}
}

func TestFallbackDownloadCleanupErrorsAreJoinedRatherThanOverwritten(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	ops.nativeOperation = func(func() error) error { return errors.New("native") }
	ops.afterCleanupCheck = func(_ *downloadTarget, name string) error {
		if strings.Contains(name, ".tmp-") {
			return errors.New("source-cleanup")
		}
		return nil
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(false)
	if installErr == nil || result.Publication != publicationNone || !strings.Contains(installErr.Error(), "native") || !strings.Contains(installErr.Error(), "source-cleanup") {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
}

func TestFallbackDownloadPerformedAndNoOperationReconciliation(t *testing.T) {
	for _, performed := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-op", true: "performed"}[performed], func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile")
			ops := defaultDownloadOperations()
			ops.nativeOperation = func(operation func() error) error {
				if performed {
					if err := operation(); err != nil {
						return err
					}
				}
				return errors.New("native")
			}
			target, err := newDownloadTarget(destination, false, ops)
			if err != nil {
				t.Fatal(err)
			}
			platform := target.platform.(*heldOtherDownloadPublication)
			writer := platform.writer
			parent := platform.parent
			_, _ = io.WriteString(target.Writer(), "new")
			result, installErr := target.Install(false)
			want := publicationNone
			if performed {
				want = publicationExact
			}
			if installErr == nil || result.Publication != want {
				t.Fatalf("install=%#v,%v", result, installErr)
			}
			if _, err := writer.Stat(); err == nil {
				t.Fatal("writer remains open")
			}
			if _, err := parent.Stat(); err == nil {
				t.Fatal("parent remains open")
			}
			if performed {
				if contents, _ := os.ReadFile(destination); string(contents) != "new" {
					t.Fatalf("destination=%q", contents)
				}
			}
		})
	}
}

func TestFallbackDownloadPerformedThenErrorAndPostNativeDisappearanceIsAmbiguous(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	published := destination + ".published"
	ops := defaultDownloadOperations()
	ops.nativeOperation = func(operation func() error) error {
		if err := operation(); err != nil {
			return err
		}
		return errors.New("native")
	}
	ops.afterNativeBeforeReconcile = func(*downloadTarget) error { return os.Rename(destination, published) }
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(false)
	if installErr == nil || result.Publication != publicationAmbiguous || result.Visibility != visibilityAmbiguous {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
}

func TestFallbackDownloadVerifiedNoPublicationCleansBackupCandidateAndSource(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	ops.afterBackupCreatedBeforeReplace = func(*downloadTarget) error { return errors.New("replace") }
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(true)
	if installErr == nil || result.Publication != publicationNone {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("destination=%q", contents)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("residue=%v", entries)
	}
}

func TestFallbackDownloadFirstSyncFailureStillCleansOwnedSourceCandidateAndBackup(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	var syncCalls int
	ops.syncParent = func(string) error {
		syncCalls++
		if syncCalls == 1 {
			return errors.New("first parent sync")
		}
		return nil
	}
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(true)
	if installErr == nil || result.Publication != publicationExact || !result.Visible() || result.Durability != durabilityUncertain || syncCalls != 2 {
		t.Fatalf("install=%#v syncCalls=%d err=%v", result, syncCalls, installErr)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("owned residue leaked: %v", entries)
	}
}

func TestFallbackDownloadForceReplacesSymlinkAndCleansBackupWithoutFollowing(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "downloads")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "profile")
	if err := os.Symlink(outside, destination); err != nil {
		t.Fatal(err)
	}
	target, err := newDownloadTarget(destination, true, defaultDownloadOperations())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(true)
	if installErr != nil || result.Publication != publicationExact || !result.Durable() {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "new" {
		t.Fatalf("destination=%q,%v", contents, err)
	}
	if contents, err := os.ReadFile(outside); err != nil || string(contents) != "outside" {
		t.Fatalf("symlink target=%q,%v", contents, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("symlink backup leaked: %v", entries)
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
