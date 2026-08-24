//go:build !windows

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type privateFileSnapshot struct {
	contents []byte
	info     os.FileInfo
	mode     os.FileMode
}

func snapshotPrivateFile(path string) (privateFileSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return privateFileSnapshot{}, fmt.Errorf("download could not be inspected: %w", err)
	}
	if !info.Mode().IsRegular() {
		return privateFileSnapshot{}, errors.New("download is not a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return privateFileSnapshot{}, fmt.Errorf("download mode = %04o, want 0600", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return privateFileSnapshot{}, errors.New("download contents could not be snapshotted")
	}
	return privateFileSnapshot{contents: contents, info: info, mode: info.Mode()}, nil
}

func privateFileMatchesSnapshot(path string, snapshot privateFileSnapshot) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("download could not be reinspected: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("download is not the snapshotted regular file")
	}
	if !os.SameFile(snapshot.info, info) {
		return errors.New("download file identity changed")
	}
	if info.Mode() != snapshot.mode {
		return fmt.Errorf("download mode changed from %s to %s", snapshot.mode, info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("download mode = %04o, want 0600", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return errors.New("download contents could not be compared")
	}
	if !bytes.Equal(contents, snapshot.contents) {
		return errors.New("download contents changed")
	}
	return nil
}

func TestPrivateFileSnapshotDetectsNoClobberChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download")
	original := []byte("private download")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotPrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := privateFileMatchesSnapshot(path, snapshot); err != nil {
		t.Fatalf("unchanged file rejected: %v", err)
	}

	if err := os.WriteFile(path, []byte("changed download"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := privateFileMatchesSnapshot(path, snapshot); err == nil {
		t.Fatal("changed bytes were accepted")
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := privateFileMatchesSnapshot(path, snapshot); err == nil {
		t.Fatal("changed private mode was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := privateFileMatchesSnapshot(path, snapshot); err == nil {
		t.Fatal("replacement file identity was accepted")
	}
}
