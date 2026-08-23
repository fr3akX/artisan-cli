//go:build darwin

package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

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
