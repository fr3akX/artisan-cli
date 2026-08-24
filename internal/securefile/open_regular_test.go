package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadRegularSnapshotReadsNestedNonPrivateRegularFile(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "review.txt")
	if err := os.WriteFile(path, []byte("review body"), 0o644); err != nil {
		t.Fatal(err)
	}

	contents, err := ReadRegularSnapshot(path, 64)
	if err != nil {
		t.Fatalf("ReadRegularSnapshot() error = %v", err)
	}
	if string(contents) != "review body" {
		t.Fatalf("contents = %q", contents)
	}
	contents[0] = 'R'
	onDisk, err := os.ReadFile(path)
	if err != nil || string(onDisk) != "review body" {
		t.Fatalf("returned bytes alias source: %q, %v", onDisk, err)
	}
}

func TestReadRegularSnapshotRejectsInvalidSizesAndNonregularSources(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	large := filepath.Join(root, "large")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(large, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		max  int64
	}{
		{name: "empty", path: empty, max: 8},
		{name: "oversized", path: large, max: 4},
		{name: "directory", path: root, max: 8},
		{name: "missing", path: filepath.Join(root, "secret-name"), max: 8},
		{name: "zero maximum", path: large, max: 0},
		{name: "negative maximum", path: large, max: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadRegularSnapshot(test.path, test.max)
			if !errors.Is(err, ErrInvalidRegularSnapshot) {
				t.Fatalf("error = %v, want ErrInvalidRegularSnapshot", err)
			}
			if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "secret-name") || strings.Contains(err.Error(), "12345") {
				t.Fatalf("error leaks path or content: %v", err)
			}
		})
	}
}

func TestReadRegularSnapshotRejectsFinalAndParentLinks(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(realDirectory, "review")
	if err := os.WriteFile(realFile, []byte("private body"), 0o600); err != nil {
		t.Fatal(err)
	}
	finalLink := filepath.Join(root, "final-link")
	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(realFile, finalLink); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Symlink(realDirectory, parentLink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{finalLink, filepath.Join(parentLink, "review")} {
		if _, err := ReadRegularSnapshot(path, 64); !errors.Is(err, ErrInvalidRegularSnapshot) {
			t.Fatalf("ReadRegularSnapshot(%q) error = %v", filepath.Base(path), err)
		}
	}
}

func TestReadRegularSnapshotRejectsReplacementBeforeFinalOpen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "review")
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(path, []byte("first body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("other body"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := false
	var mutationErr error
	_, err := readRegularSnapshot(path, 64, snapshotTestHooks{event: func(event string) error {
		if event == "before-open:review" && !changed {
			changed = true
			mutationErr = replaceSnapshotPathForTest(replacement, path)
		}
		return nil
	}})
	if mutationErr != nil {
		t.Fatalf("pre-open replacement failed: %v", mutationErr)
	}
	if !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want pre-open replacement rejection", err)
	}
}

func TestReadRegularSnapshotRejectsParentReplacementAfterOpen(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "review"), []byte("first body"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := false
	var mutationErr error
	_, err := readRegularSnapshot(filepath.Join(parent, "review"), 64, snapshotTestHooks{event: func(event string) error {
		if event == "after-open:parent" && !changed {
			changed = true
			if mutationErr = os.Rename(parent, filepath.Join(root, "old-parent")); mutationErr != nil {
				return nil
			}
			if mutationErr = os.Mkdir(parent, 0o700); mutationErr != nil {
				return nil
			}
			mutationErr = os.WriteFile(filepath.Join(parent, "review"), []byte("other body"), 0o600)
		}
		return nil
	}})
	if mutationErr != nil {
		t.Fatalf("parent replacement failed: %v", mutationErr)
	}
	if !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want parent replacement rejection", err)
	}
}

func TestReadRegularSnapshotRejectsPathReplacementDuringRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "review")
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(path, []byte("first body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("other body"), 0o600); err != nil {
		t.Fatal(err)
	}

	changed := false
	var mutationErr error
	_, err := readRegularSnapshot(path, 64, snapshotTestHooks{event: func(event string) error {
		if event == "during-read" && !changed {
			changed = true
			mutationErr = replaceSnapshotPathForTest(replacement, path)
		}
		return nil
	}})
	if mutationErr != nil {
		t.Fatalf("during-read replacement failed: %v", mutationErr)
	}
	if !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want during-read replacement rejection", err)
	}
}

func TestReadRegularSnapshotRejectsSameSizeChangeDuringRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review")
	if err := os.WriteFile(path, []byte("abcdefgh"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := false
	var mutationErr error
	_, err := readRegularSnapshot(path, 64, snapshotTestHooks{event: func(event string) error {
		if event == "during-read" && !changed {
			changed = true
			mutationErr = os.WriteFile(path, []byte("ABCDEFGH"), 0o600)
		}
		return nil
	}})
	if mutationErr != nil {
		t.Fatalf("same-size mutation failed: %v", mutationErr)
	}
	if !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want during-read content change rejection", err)
	}
}

func TestReadRegularSnapshotRejectsShortRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review")
	if err := os.WriteFile(path, []byte("abcdefgh"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := false
	var mutationErr error
	_, err := readRegularSnapshot(path, 64, snapshotTestHooks{event: func(event string) error {
		if event == "during-read" && !changed {
			changed = true
			mutationErr = os.Truncate(path, 4)
		}
		return nil
	}})
	if mutationErr != nil {
		t.Fatalf("truncate mutation failed: %v", mutationErr)
	}
	if !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want short-read rejection", err)
	}
}

func TestReadRegularSnapshotRejectsDevice(t *testing.T) {
	if _, err := os.Stat(os.DevNull); err != nil {
		t.Skipf("null device unavailable: %v", err)
	}
	if _, err := ReadRegularSnapshot(os.DevNull, 64); !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("null-device error = %v", err)
	}
}

func TestReadRegularSnapshotRejectsPathReplacementAfterOpen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "review")
	if err := os.WriteFile(path, []byte("first body"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("other body"), 0o600); err != nil {
		t.Fatal(err)
	}

	changed := false
	var mutationErr error
	_, err := readRegularSnapshot(path, 64, snapshotTestHooks{event: func(event string) error {
		if event == "after-read" && !changed {
			changed = true
			mutationErr = replaceSnapshotPathForTest(replacement, path)
		}
		return nil
	}})
	if mutationErr != nil {
		t.Fatalf("post-read replacement failed: %v", mutationErr)
	}
	if !errors.Is(err, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want changed snapshot rejection", err)
	}
}

func TestReadRegularSnapshotRejectsInPlaceSizeAndModtimeChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(string) error
	}{
		{name: "size", change: func(path string) error { return os.WriteFile(path, []byte("changed length"), 0o600) }},
		{name: "modtime", change: func(path string) error {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			return os.Chtimes(path, info.ModTime(), info.ModTime().AddDate(0, 0, 1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "review")
			if err := os.WriteFile(path, []byte("stable body"), 0o600); err != nil {
				t.Fatal(err)
			}
			var mutationErr error
			_, err := readRegularSnapshot(path, 64, snapshotTestHooks{event: func(event string) error {
				if event == "after-read" && mutationErr == nil {
					mutationErr = test.change(path)
				}
				return nil
			}})
			if mutationErr != nil {
				t.Fatalf("%s mutation failed: %v", test.name, mutationErr)
			}
			if !errors.Is(err, ErrInvalidRegularSnapshot) {
				t.Fatalf("error = %v, want changed snapshot rejection", err)
			}
		})
	}
}

func TestReadRegularSnapshotRejectsRelativePathWhenCurrentDirectoryChanges(t *testing.T) {
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	for _, root := range []string{firstRoot, secondRoot} {
		if err := os.WriteFile(filepath.Join(root, "review"), []byte("stable body"), 0o600); err != nil {
			t.Fatalf("write relative fixture: %v", err)
		}
	}
	if err := os.Chdir(firstRoot); err != nil {
		t.Fatalf("enter first current directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalCWD); err != nil {
			t.Errorf("restore current directory: %v", err)
		}
	})

	var mutationErr error
	_, snapshotErr := readRegularSnapshot("review", 64, snapshotTestHooks{event: func(event string) error {
		if event == "before-recheck" {
			mutationErr = os.Chdir(secondRoot)
		}
		return nil
	}})
	if mutationErr != nil {
		t.Fatalf("current-directory mutation failed: %v", mutationErr)
	}
	if !errors.Is(snapshotErr, ErrInvalidRegularSnapshot) {
		t.Fatalf("error = %v, want current-directory replacement rejection", snapshotErr)
	}
}

func replaceSnapshotPathForTest(replacement, path string) error {
	if runtime.GOOS == "windows" {
		if err := os.Rename(path, path+".replaced"); err != nil {
			return err
		}
	}
	return os.Rename(replacement, path)
}

func TestReadRegularSnapshotErrorsNeverContainPathOrContents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "do-not-reflect-review-path")
	if err := os.WriteFile(path, []byte("do-not-reflect-review-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadRegularSnapshot(path, 2)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, forbidden := range []string{root, "do-not-reflect-review-path", "do-not-reflect-review-content"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error %q contains %q", err, forbidden)
		}
	}
}
