//go:build darwin

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinDownloadPartialCandidateFailuresAreCleanedBeforeNative(t *testing.T) {
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
			destination := filepath.Join(t.TempDir(), "profile")
			ops := defaultDownloadOperations()
			ops.forceCandidateCopy = true
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
			entries, err := os.ReadDir(filepath.Dir(destination))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("partial candidate leaked: %v", entries)
			}
		})
	}
}

func TestDarwinDownloadForceUsesPOSIXSwapAndCleansDisplacedDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := newDownloadTarget(destination, true, defaultDownloadOperations())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "new")
	result, installErr := target.Install(true)
	if installErr != nil || result.Publication != publicationExact || !result.Visible() {
		t.Fatalf("install=%#v,%v", result, installErr)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "new" {
		t.Fatalf("destination=%q", contents)
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("residue=%v", entries)
	}
}

func TestDarwinDownloadParentSwapUsesHeldDirectory(t *testing.T) {
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
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "exact")
	result, err := target.Install(false)
	if err != nil || result.Publication != publicationExact || result.Visibility != visibilityAmbiguous {
		t.Fatalf("install=%#v,%v", result, err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement parent destination=%v", err)
	}
	if contents, err := os.ReadFile(filepath.Join(moved, "profile")); err != nil || string(contents) != "exact" {
		t.Fatalf("held publication=%q,%v", contents, err)
	}
}

func TestDarwinDownloadPostNativeIdenticalCompetitorIsNotMistakenForPublishedIdentity(t *testing.T) {
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
		t.Fatalf("install=%#v,%v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "exact" {
		t.Fatalf("competitor=%q", contents)
	}
	if contents, _ := os.ReadFile(published); string(contents) != "exact" {
		t.Fatalf("published residue=%q", contents)
	}
}

func TestDarwinDownloadForceCandidateSwapPreservesOldDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultDownloadOperations()
	var racer, held string
	ops.afterCandidateVerifiedBeforeNative = func(target *downloadTarget) error {
		p := target.platform.(*heldUnixDownloadPublication)
		racer = filepath.Join(target.directory, p.candidateName)
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
	result, err := target.Install(true)
	if err == nil || result.Publication != publicationNone {
		t.Fatalf("install=%#v,%v", result, err)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "old" {
		t.Fatalf("old destination=%q", contents)
	}
	if contents, _ := os.ReadFile(racer); string(contents) != "racer" {
		t.Fatalf("racer=%q", contents)
	}
	if contents, _ := os.ReadFile(held); string(contents) != "new" {
		t.Fatalf("held=%q", contents)
	}
}
