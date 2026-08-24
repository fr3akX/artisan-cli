package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestGenerateRegistryIsDeterministicAndValidatesSources(t *testing.T) {
	directory := t.TempDir()
	write := func(name, frontmatterName string) string {
		t.Helper()
		path := filepath.Join(directory, name+".md")
		contents := "---\nname: " + frontmatterName + "\ndescription: Use when testing.\n---\n\n# " + name + "\n"
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	inventory := write("inventory", "artisan-inventory")
	roast := write("roast", "artisan-roast-review")
	first, err := generateRegistry([]sourceSpec{
		{Name: "artisan-roast-review", Path: roast},
		{Name: "artisan-inventory", Path: inventory},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := generateRegistry([]sourceSpec{
		{Name: "artisan-inventory", Path: inventory},
		{Name: "artisan-roast-review", Path: roast},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("generated registry depends on source declaration order")
	}
	if strings.Index(string(first), "generatedContentArtisanInventory") >= strings.Index(string(first), "generatedContentArtisanRoastReview") {
		t.Fatal("generated registry is not lexical")
	}

	missingName := filepath.Join(directory, "missing.md")
	if err := os.WriteFile(missingName, []byte("---\ndescription: Use when testing.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	duplicateFrontmatterName := filepath.Join(directory, "duplicate-frontmatter.md")
	if err := os.WriteFile(duplicateFrontmatterName, []byte("---\nname:\nname: artisan-inventory\ndescription: Use when testing.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		sources []sourceSpec
	}{
		{name: "duplicate", sources: []sourceSpec{{Name: "artisan-inventory", Path: inventory}, {Name: "artisan-inventory", Path: inventory}}},
		{name: "invalid name", sources: []sourceSpec{{Name: "../inventory", Path: inventory}}},
		{name: "missing frontmatter name", sources: []sourceSpec{{Name: "artisan-inventory", Path: missingName}}},
		{name: "duplicate frontmatter name", sources: []sourceSpec{{Name: "artisan-inventory", Path: duplicateFrontmatterName}}},
		{name: "source name mismatch", sources: []sourceSpec{{Name: "artisan-roast-review", Path: inventory}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := generateRegistry(test.sources); err == nil {
				t.Fatal("generateRegistry accepted invalid sources")
			}
		})
	}
}

func TestRunGeneratorRejectsUnsafeDestination(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "artisan-inventory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "artisan-roast-review"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"artisan-inventory", "artisan-roast-review"} {
		contents := "---\nname: " + name + "\ndescription: Use when testing.\n---\n"
		if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := runGenerator([]string{root, filepath.Join(t.TempDir(), "outside.go")}); err == nil {
		t.Fatal("generator accepted an absolute destination")
	}
	if err := runGenerator([]string{root, filepath.Join("..", "content_generated.go")}); err == nil {
		t.Fatal("generator accepted a parent-traversing destination")
	}
}

func TestAtomicWriteReplaceFailurePreservesDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "content_generated.go")
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected replace failure")
	err := atomicWriteWithOperations(destination, []byte("new"),
		func(file *os.File) error { return file.Sync() },
		func(string, string) error { return injected },
		func(string) error { return nil },
	)
	if !errors.Is(err, injected) || securefile.ReplacementVisible(err) {
		t.Fatalf("error = %v, want non-visible replace failure", err)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "old" {
		t.Fatalf("destination = %q, %v; want old", got, readErr)
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
