//go:build linux

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxDownloadParentSwapPublishesOnlyHeldParentAndReportsVisibilityAmbiguous(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "downloads")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "profile")
	moved := filepath.Join(root, "held-parent")
	ops := defaultDownloadOperations()
	ops.afterParentHeld = func(*downloadTarget) error {
		if err := os.Rename(directory, moved); err != nil {
			return err
		}
		return os.Mkdir(directory, 0o700)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "exact")
	result, err := target.Install(false)
	if err != nil || result.Publication != publicationExact || result.Visibility != visibilityAmbiguous || result.Visible() {
		t.Fatalf("install = %#v, %v", result, err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escaped to replacement parent: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(moved, "profile"))
	if err != nil || string(contents) != "exact" {
		t.Fatalf("held-parent publication=%q, %v", contents, err)
	}
}

func TestLinuxDownloadForceCandidateSwapBeforeNativePreservesOldDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	var racerCandidate, heldCandidate string
	ops.afterCandidateVerifiedBeforeNative = func(target *downloadTarget) error {
		platform := target.platform.(*heldUnixDownloadPublication)
		racerCandidate = filepath.Join(target.directory, platform.candidateName)
		heldCandidate = racerCandidate + ".held"
		if err := os.Rename(racerCandidate, heldCandidate); err != nil {
			return err
		}
		return os.WriteFile(racerCandidate, []byte("racer"), 0o600)
	}
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, err := target.Install(true)
	if err == nil || result.Publication != publicationNone {
		t.Fatalf("install=%#v, %v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("old destination lost: %q", contents)
	}
	if contents, _ := os.ReadFile(racerCandidate); string(contents) != "racer" {
		t.Fatalf("racer candidate removed: %q", contents)
	}
	if contents, _ := os.ReadFile(heldCandidate); string(contents) != "new" {
		t.Fatalf("exact candidate residue=%q", contents)
	}
}

func TestLinuxDownloadForceNoOperationErrorIsReconciledAndPreservesOldDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	ops.nativeOperation = func(func() error) error { return errors.New("native no operation") }
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, err := target.Install(true)
	if err == nil || result.Publication != publicationNone || result.Visibility != visibilityNotVisible {
		t.Fatalf("install=%#v, %v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("old destination=%q", contents)
	}
}

func TestLinuxDownloadNoForceDestinationRaceIsExactExistAndPreservesCompetitor(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	ops.nativeOperation = func(operation func() error) error {
		if err := os.WriteFile(destination, []byte("competitor"), 0o600); err != nil {
			return err
		}
		return operation()
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, err := target.Install(false)
	if !errors.Is(err, os.ErrExist) || result.Publication != publicationNone || result.Visibility != visibilityNotVisible {
		t.Fatalf("install=%#v, %v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "competitor" {
		t.Fatalf("competitor=%q", contents)
	}
}

func TestLinuxDownloadCleanupSwapRetainsCompetitorAndPriorForceDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	var competitor, displaced string
	ops.afterCleanupCheck = func(target *downloadTarget, name string) error {
		competitor = filepath.Join(target.directory, name)
		displaced = competitor + ".displaced"
		if err := os.Rename(competitor, displaced); err != nil {
			return err
		}
		return os.WriteFile(competitor, []byte("competitor"), 0o600)
	}
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, err := target.Install(true)
	if err == nil || result.Publication != publicationExact || !result.Visible() {
		t.Fatalf("install=%#v, %v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "new" {
		t.Fatalf("destination=%q", contents)
	}
	if contents, _ := os.ReadFile(competitor); string(contents) != "competitor" {
		t.Fatalf("competitor removed=%q", contents)
	}
	if contents, _ := os.ReadFile(displaced); string(contents) != "old" {
		t.Fatalf("prior destination not retained=%q", contents)
	}
}

func TestLinuxDownloadExactTerminalClosesWriterSourceAndParentDescriptors(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	target, err := newDownloadTarget(destination, false, defaultDownloadOperations())
	if err != nil {
		t.Fatal(err)
	}
	platform := target.platform.(*heldUnixDownloadPublication)
	writerFD, sourceFD, parentFD := int(platform.writer.Fd()), int(platform.source.Fd()), int(platform.parent.file.Fd())
	_, _ = io.WriteString(target.Writer(), "exact")
	result, err := target.Install(false)
	if err != nil || !result.Visible() {
		t.Fatalf("install=%#v,%v", result, err)
	}
	for name, fd := range map[string]int{"writer": writerFD, "source": sourceFD, "parent": parentFD} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatalf("%s fd %d remains open: %v", name, fd, err)
		}
	}
}

func TestLinuxDownloadForcePostNativeSwapIsAmbiguousAndPriorDestinationRemainsAtCandidate(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	var candidate, exactMoved string
	ops.afterNativeBeforeReconcile = func(target *downloadTarget) error {
		candidate = filepath.Join(target.directory, target.platform.(*heldUnixDownloadPublication).candidateName)
		exactMoved = destination + ".exact"
		if err := os.Rename(destination, exactMoved); err != nil {
			return err
		}
		return os.WriteFile(destination, []byte("competitor"), 0o600)
	}
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, err := target.Install(true)
	if err == nil || result.Publication != publicationAmbiguous {
		t.Fatalf("install=%#v, %v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "competitor" {
		t.Fatalf("competitor=%q", contents)
	}
	if contents, _ := os.ReadFile(exactMoved); string(contents) != "new" {
		t.Fatalf("exact residue=%q", contents)
	}
	if contents, _ := os.ReadFile(candidate); string(contents) != "old" {
		t.Fatalf("old destination lost=%q", contents)
	}
}
