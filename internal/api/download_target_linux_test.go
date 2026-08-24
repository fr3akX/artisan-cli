//go:build linux

package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxDownloadCandidateRegistersImmediatelyAfterLink(t *testing.T) {
	directory := t.TempDir()
	source, err := os.CreateTemp(directory, "source-")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	sourceInfo, err := source.Stat()
	if err != nil {
		t.Fatal(err)
	}
	parentFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentFD)

	candidate := "candidate"
	moved := filepath.Join(directory, "candidate-moved")
	registered := false
	cloneErr := cloneHeldUnixDownloadSource(int(source.Fd()), sourceInfo, parentFD, candidate, defaultDownloadOperations(), func(info os.FileInfo) error {
		registered = true
		if info == nil || !os.SameFile(info, sourceInfo) {
			t.Fatalf("registered identity=%v", info)
		}
		return os.Rename(filepath.Join(directory, candidate), moved)
	})
	if cloneErr == nil || !registered {
		t.Fatalf("registered=%v err=%v", registered, cloneErr)
	}
	if info, err := os.Stat(moved); err != nil || !os.SameFile(info, sourceInfo) {
		t.Fatalf("moved candidate identity=%v,%v", info, err)
	}
}

func TestLinuxDownloadCandidateKeepsKnownIdentityWhenPostLinkStatFails(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	ops.statLinkedCandidate = func(*os.File) (os.FileInfo, error) {
		return nil, errors.New("post-link stat")
	}
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(true)
	if installErr == nil || result.Publication != publicationNone || !strings.Contains(installErr.Error(), "post-link stat") {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("candidate residue leaked: %v", entries)
	}
}

func TestLinuxDownloadBackupReplaceFirstSyncFailureStillCleansOwnedResidue(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	ops.openAnonymousSource = func(int) (int, error) { return -1, unix.EOPNOTSUPP }
	ops.forceBackupReplace = true
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
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "new" {
		t.Fatalf("destination=%q,%v", contents, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("owned residue leaked: %v", entries)
	}
}

func TestLinuxDownloadConstructorJoinsProtectionAndCleanupErrors(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "profile")
	ops := defaultDownloadOperations()
	ops.openAnonymousSource = func(int) (int, error) { return -1, unix.EOPNOTSUPP }
	ops.protect = func(*os.File) error { return errors.New("protect-failed") }
	ops.afterCleanupCheck = func(*downloadTarget, string) error { return errors.New("cleanup-failed") }
	target, err := newDownloadTarget(destination, false, ops)
	if target != nil || err == nil || !strings.Contains(err.Error(), "protect-failed") || !strings.Contains(err.Error(), "cleanup-failed") {
		t.Fatalf("target=%#v err=%v", target, err)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("retained cleanup residue=%v,%v", entries, readErr)
	}
	if removeErr := os.Remove(filepath.Join(directory, entries[0].Name())); removeErr != nil {
		t.Fatal(removeErr)
	}
}

func TestLinuxDownloadNoForceDescriptorFallbackRunsFenceAgainBeforeProcLink(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	var emptyPathCalls, procPathCalls int
	ops.linkDescriptorEmptyPath = func(int, int, string) error {
		emptyPathCalls++
		return unix.EOPNOTSUPP
	}
	ops.linkDescriptorProcPath = func(int, int, string) error {
		procPathCalls++
		return errors.New("proc link must not run after fence rejection")
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	stale := errors.New("revision changed")
	var fenceCalls int
	result, installErr := target.InstallContextBeforeNative(context.Background(), false, func() error {
		fenceCalls++
		if fenceCalls == 2 {
			return stale
		}
		return nil
	})
	if !errors.Is(installErr, stale) || result.Publication != publicationNone || result.Visible() {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	if fenceCalls != 2 || emptyPathCalls != 1 || procPathCalls != 0 {
		t.Fatalf("fence=%d empty-path=%d proc-path=%d", fenceCalls, emptyPathCalls, procPathCalls)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination published: %v", err)
	}
}

func TestLinuxDownloadNamedFallbackCompletesNoForceAndForceSafely(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-force", true: "force"}[force], func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile")
			if force {
				if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ops := defaultDownloadOperations()
			ops.openAnonymousSource = func(int) (int, error) { return -1, unix.EOPNOTSUPP }
			target, err := newDownloadTarget(destination, force, ops)
			if err != nil {
				t.Fatal(err)
			}
			temporary := target.temporaryPath
			if temporary == "" {
				t.Fatal("named fallback source was not retained")
			}
			_, _ = io.WriteString(target.Writer(), "new")
			result, err := target.Install(force)
			if err != nil || result.Publication != publicationExact || !result.Visible() {
				t.Fatalf("install=%#v,%v", result, err)
			}
			if contents, _ := os.ReadFile(destination); string(contents) != "new" {
				t.Fatalf("destination=%q", contents)
			}
			if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("named source leaked: %v", err)
			}
		})
	}
}

func TestLinuxDownloadPreNativeFailureCleansTrackedNamedSourceAndCandidateAndJoinsErrors(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	ops.openAnonymousSource = func(int) (int, error) { return -1, unix.EOPNOTSUPP }
	ops.afterCandidateVerifiedBeforeNative = func(*downloadTarget) error { return errors.New("pre-native") }
	var names []string
	ops.afterCleanupCheck = func(_ *downloadTarget, name string) error {
		names = append(names, name)
		if strings.Contains(name, ".candidate-") {
			return errors.New("candidate-cleanup")
		}
		return nil
	}
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, err := target.Install(true)
	if err == nil || result.Publication != publicationNone || !result.CleanupUncertain || !strings.Contains(err.Error(), "pre-native") || !strings.Contains(err.Error(), "candidate-cleanup") {
		t.Fatalf("install=%#v,%v", result, err)
	}
	var sawSource, sawCandidate bool
	for _, name := range names {
		sawSource = sawSource || strings.Contains(name, ".tmp-")
		sawCandidate = sawCandidate || strings.Contains(name, ".candidate-")
	}
	if !sawSource || !sawCandidate {
		t.Fatalf("cleanup names=%v", names)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("destination=%q", contents)
	}
}

func TestLinuxDownloadVerifiedNoPublicationCleansTrackedBackup(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	ops.forceBackupReplace = true
	ops.afterBackupCreatedBeforeReplace = func(*downloadTarget) error { return errors.New("replace failed") }
	target, err := newDownloadTarget(destination, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, err := target.Install(true)
	if err == nil || result.Publication != publicationNone {
		t.Fatalf("install=%#v,%v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("destination=%q", contents)
	}
	entries, readErr := os.ReadDir(filepath.Dir(destination))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".backup-") || strings.Contains(entry.Name(), ".candidate-") || strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("tracked residue leaked: %s", entry.Name())
		}
	}
}

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
	if err == nil || result.Publication != publicationNone || result.Visibility != visibilityNotVisible || target.state != downloadTargetTerminalNone {
		t.Fatalf("install=%#v state=%v, %v", result, target.state, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("old destination=%q", contents)
	}
	entries, readErr := os.ReadDir(filepath.Dir(destination))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("owned residue leaked: %v", entries)
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

func TestLinuxDownloadPostNativeIdenticalCompetitorIsNotMistakenForPublishedIdentity(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	ops := defaultDownloadOperations()
	published := destination + ".published"
	ops.afterNativeBeforeReconcile = func(*downloadTarget) error {
		if err := os.Rename(destination, published); err != nil {
			return err
		}
		return os.WriteFile(destination, []byte("exact"), 0o600)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "exact")
	result, err := target.Install(false)
	if err == nil || result.Publication != publicationAmbiguous || result.Visible() {
		t.Fatalf("install=%#v, %v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "exact" {
		t.Fatalf("competitor=%q", contents)
	}
	if contents, _ := os.ReadFile(published); string(contents) != "exact" {
		t.Fatalf("published residue=%q", contents)
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
