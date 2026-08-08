package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestAtomicWriteOrdersFileSyncReplaceAndParentSync(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "content_generated.go")
	var events []string
	err := atomicWriteWithOperations(destination, []byte("generated"),
		func(*os.File) error { events = append(events, "file-sync"); return nil },
		func(from, to string) error { events = append(events, "replace"); return os.Rename(from, to) },
		func(string) error { events = append(events, "parent-sync"); return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"file-sync", "replace", "parent-sync"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestAtomicWriteFileSyncFailurePreservesDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "content_generated.go")
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected file sync failure")
	err := atomicWriteWithOperations(destination, []byte("new"),
		func(*os.File) error { return injected }, atomicReplace, func(string) error { return nil })
	if !errors.Is(err, injected) || securefile.ReplacementVisible(err) {
		t.Fatalf("error = %v, want non-visible injected failure", err)
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "old" {
		t.Fatalf("destination = %q, want old", got)
	}
}

func TestAtomicWriteParentSyncFailureReportsVisibleReplacement(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "content_generated.go")
	injected := errors.New("injected parent sync failure")
	err := atomicWriteWithOperations(destination, []byte("new"),
		func(file *os.File) error { return file.Sync() }, atomicReplace, func(string) error { return injected })
	if !errors.Is(err, injected) || !securefile.ReplacementVisible(err) {
		t.Fatalf("error = %v, want visible injected failure", err)
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "new" {
		t.Fatalf("destination = %q, want new", got)
	}
}
