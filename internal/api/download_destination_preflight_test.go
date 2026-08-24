package api

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestPreflightDownloadDestinationRejectsInvalidPathsWithoutMutation(t *testing.T) {
	root := t.TempDir()
	missingParent := filepath.Join(root, "missing", "profile.alog")
	nondirectoryParent := filepath.Join(root, "parent-file")
	if err := os.WriteFile(nondirectoryParent, []byte("parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	directoryDestination := filepath.Join(root, "directory")
	if err := os.Mkdir(directoryDestination, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "existing.alog")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := directoryNames(t, root)
	for _, test := range []struct {
		name        string
		destination string
		force       bool
		invalid     bool
		exists      bool
	}{
		{name: "empty", destination: "", invalid: true},
		{name: "dot", destination: ".", invalid: true},
		{name: "dot dot", destination: "..", invalid: true},
		{name: "root", destination: string(filepath.Separator), invalid: true},
		{name: "nul", destination: filepath.Join(root, "bad\x00name"), invalid: true},
		{name: "missing parent", destination: missingParent},
		{name: "nondirectory parent", destination: filepath.Join(nondirectoryParent, "profile.alog")},
		{name: "directory destination", destination: directoryDestination, force: true, invalid: true},
		{name: "existing no force", destination: existing, exists: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := PreflightDownloadDestination(test.destination, test.force)
			if err == nil {
				t.Fatal("preflight unexpectedly succeeded")
			}
			if test.invalid && !errors.Is(err, ErrInvalidDownloadDestination) {
				t.Fatalf("error = %v, want invalid destination", err)
			}
			if test.exists && !errors.Is(err, os.ErrExist) {
				t.Fatalf("error = %v, want destination exists", err)
			}
		})
	}
	if after := directoryNames(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("preflight mutated directory: before=%q after=%q", before, after)
	}
	contents, err := os.ReadFile(existing)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("existing destination changed: %q, %v", contents, err)
	}
}

func TestPreflightDownloadDestinationAcceptsValidAndForcedExistingPaths(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "new.alog")
	if err := PreflightDownloadDestination(valid, false); err != nil {
		t.Fatalf("valid destination: %v", err)
	}
	if _, err := os.Lstat(valid); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight created destination: %v", err)
	}
	existing := filepath.Join(root, "existing.alog")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PreflightDownloadDestination(existing, true); err != nil {
		t.Fatalf("forced existing destination: %v", err)
	}
	contents, err := os.ReadFile(existing)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("forced preflight changed destination: %q, %v", contents, err)
	}
}

func directoryNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}
