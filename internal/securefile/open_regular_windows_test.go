//go:build windows

package securefile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWindowsSnapshotPathForms(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantRoot   string
		wantParts  []string
		wantAccept bool
	}{
		{name: "drive absolute", path: `C:\root\review.txt`, wantRoot: `C:\`, wantParts: []string{"root", "review.txt"}, wantAccept: true},
		{name: "drive relative", path: `C:review.txt`},
		{name: "drive relative nested", path: `C:root\review.txt`},
		{name: "root relative backslash", path: `\review.txt`},
		{name: "root relative slash", path: `/review.txt`},
		{name: "UNC absolute", path: `\\server\share\root\review.txt`, wantRoot: `\\server\share\`, wantParts: []string{"root", "review.txt"}, wantAccept: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, parts, ok := windowsSnapshotPath(test.path)
			if ok != test.wantAccept || root != test.wantRoot || !reflect.DeepEqual(parts, test.wantParts) {
				t.Fatalf("windowsSnapshotPath(%q) = %q, %#v, %t; want %q, %#v, %t", test.path, root, parts, ok, test.wantRoot, test.wantParts, test.wantAccept)
			}
		})
	}
}

func TestReadRegularSnapshotRejectsWindowsJunctionComponent(t *testing.T) {
	root := canonicalTestTempDir(t)
	target := filepath.Join(root, "target")
	junction := filepath.Join(root, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "review"), []byte("private body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("junction creation prerequisite unavailable: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if _, err := ReadRegularSnapshot(filepath.Join(junction, "review"), 64); !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("junction-component error = %v, want ErrInvalidRegularSnapshot", err)
	}
}

func TestReadRegularSnapshotWindowsHandleSharingAllowsDetectableReplacement(t *testing.T) {
	root := canonicalTestTempDir(t)
	path := filepath.Join(root, "review")
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(path, []byte("first body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("other body"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mutationErr error
	_, snapshotErr := readRegularSnapshot(path, 64, snapshotTestHooks{event: func(event string) error {
		if event == "after-read" && mutationErr == nil {
			mutationErr = replaceSnapshotPathForTest(replacement, path)
		}
		return nil
	}})
	if mutationErr != nil {
		t.Fatalf("handle-sharing replacement prerequisite failed: %v", mutationErr)
	}
	if !errors.Is(snapshotErr, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want replacement rejection", snapshotErr)
	}
}
