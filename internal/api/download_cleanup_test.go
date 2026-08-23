//go:build !windows

package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinishExactDownloadCleanupContinuesAfterFirstSyncFailureAndJoinsErrors(t *testing.T) {
	var syncCalls int
	var cleanupCalls []string
	durability, err := finishExactDownloadCleanup(func() error {
		syncCalls++
		if syncCalls == 1 {
			return errors.New("first-sync")
		}
		return nil
	},
		func() (bool, error) {
			cleanupCalls = append(cleanupCalls, "source")
			return true, errors.New("source-cleanup")
		},
		func() (bool, error) {
			cleanupCalls = append(cleanupCalls, "candidate")
			return true, errors.New("candidate-cleanup")
		},
		func() (bool, error) {
			cleanupCalls = append(cleanupCalls, "backup")
			return true, errors.New("backup-cleanup")
		},
	)
	if durability != durabilityUncertain || syncCalls != 2 {
		t.Fatalf("durability=%v syncCalls=%d err=%v", durability, syncCalls, err)
	}
	if strings.Join(cleanupCalls, ",") != "source,candidate,backup" {
		t.Fatalf("cleanup calls=%v", cleanupCalls)
	}
	for _, message := range []string{"first-sync", "source-cleanup", "candidate-cleanup", "backup-cleanup"} {
		if err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("missing joined error %q in %v", message, err)
		}
	}
}

func TestRemoveOwnedDownloadNodeRemovesSymlinkWithoutFollowingAndRefusesUnsafeNodes(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "outside")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "backup")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedDownloadNode(link, linkInfo, true, nil); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != "outside" {
		t.Fatalf("symlink target=%q,%v", contents, err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink backup remains: %v", err)
	}

	subdirectory := filepath.Join(directory, "directory")
	if err := os.Mkdir(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Lstat(subdirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedDownloadNode(subdirectory, directoryInfo, true, nil); !errors.Is(err, errDownloadIdentityAmbiguous) {
		t.Fatalf("directory cleanup err=%v", err)
	}
	if _, err := os.Lstat(subdirectory); err != nil {
		t.Fatalf("directory was removed: %v", err)
	}

	owned := filepath.Join(directory, "owned")
	if err := os.WriteFile(owned, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	ownedInfo, err := os.Lstat(owned)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(owned, owned+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(owned, []byte("competitor"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedDownloadNode(owned, ownedInfo, false, nil); !errors.Is(err, errDownloadIdentityAmbiguous) {
		t.Fatalf("non-owned cleanup err=%v", err)
	}
	if contents, err := os.ReadFile(owned); err != nil || string(contents) != "competitor" {
		t.Fatalf("competitor=%q,%v", contents, err)
	}
}
