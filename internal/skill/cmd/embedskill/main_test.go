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
		{name: "overlong registry name", sources: []sourceSpec{{Name: strings.Repeat("a", 65), Path: inventory}}},
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

func TestParseFrontmatterNameRequiresStrictSimpleTwoScalarDocument(t *testing.T) {
	valid := []byte("---\ndescription: Use when testing strict frontmatter.\nname: artisan-inventory\n---\n\n# Skill\n...\n\n---\n")
	if got, err := parseFrontmatterName(valid); err != nil || got != "artisan-inventory" {
		t.Fatalf("valid frontmatter = %q, %v", got, err)
	}

	longDescription := "Use when " + strings.Repeat("x", 1016)
	longName := strings.Repeat("a", 65)
	tests := []struct {
		name     string
		contents string
	}{
		{name: "missing opener", contents: "name: artisan-inventory\ndescription: Use when testing.\n---\n"},
		{name: "leading blank before opener", contents: "\n---\nname: artisan-inventory\ndescription: Use when testing.\n---\n"},
		{name: "commented opener", contents: "--- # yaml\nname: artisan-inventory\ndescription: Use when testing.\n---\n"},
		{name: "crlf delimiters", contents: "---\r\nname: artisan-inventory\r\ndescription: Use when testing.\r\n---\r\n"},
		{name: "missing closer", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\n"},
		{name: "malformed closer", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\n--- \n"},
		{name: "missing name", contents: "---\ndescription: Use when testing.\n---\n"},
		{name: "missing description", contents: "---\nname: artisan-inventory\n---\n"},
		{name: "duplicate name", contents: "---\nname: artisan-inventory\nname: artisan-inventory\ndescription: Use when testing.\n---\n"},
		{name: "duplicate name alternate spelling", contents: "---\nname: artisan-inventory\nname : artisan-roast-review\ndescription: Use when testing.\n---\n"},
		{name: "duplicate description", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\ndescription: Use when testing again.\n---\n"},
		{name: "unknown key", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\nlicense: MIT\n---\n"},
		{name: "malformed key", contents: "---\nname : artisan-inventory\ndescription: Use when testing.\n---\n"},
		{name: "tab", contents: "---\nname:\tartisan-inventory\ndescription: Use when testing.\n---\n"},
		{name: "comment line", contents: "---\nname: artisan-inventory\n# comment\ndescription: Use when testing.\n---\n"},
		{name: "inline comment", contents: "---\nname: artisan-inventory # comment\ndescription: Use when testing.\n---\n"},
		{name: "multiline literal", contents: "---\nname: artisan-inventory\ndescription: |\n  Use when testing.\n---\n"},
		{name: "multiline folded", contents: "---\nname: artisan-inventory\ndescription: >\n  Use when testing.\n---\n"},
		{name: "quoted scalar", contents: "---\nname: \"artisan-inventory\"\ndescription: Use when testing.\n---\n"},
		{name: "blank line", contents: "---\nname: artisan-inventory\n\ndescription: Use when testing.\n---\n"},
		{name: "extra yaml document", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\n---\n---\nname: other\n"},
		{name: "extra yaml terminator", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\n---\n...\n"},
		{name: "invalid uppercase name", contents: "---\nname: Artisan-Inventory\ndescription: Use when testing.\n---\n"},
		{name: "overlong name", contents: "---\nname: " + longName + "\ndescription: Use when testing.\n---\n"},
		{name: "empty description", contents: "---\nname: artisan-inventory\ndescription: \n---\n"},
		{name: "non-trigger description", contents: "---\nname: artisan-inventory\ndescription: Testing helper.\n---\n"},
		{name: "nested mapping description", contents: "---\nname: artisan-inventory\ndescription: Use when testing: hostile syntax.\n---\n"},
		{name: "control in description", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\x00\n---\n"},
		{name: "unicode line separator extra document", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\u2028...\u2028---\u2028hostile\n---\n"},
		{name: "unicode paragraph separator extra document", contents: "---\nname: artisan-inventory\ndescription: Use when testing.\u2029...\u2029---\u2029hostile\n---\n"},
		{name: "overlong description", contents: "---\nname: artisan-inventory\ndescription: " + longDescription + "\n---\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if name, err := parseFrontmatterName([]byte(test.contents)); err == nil {
				t.Fatalf("accepted hostile frontmatter with name %q", name)
			}
		})
	}
	invalidUTF8 := append([]byte("---\nname: artisan-inventory\ndescription: Use when testing "), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(".\n---\n")...)
	if name, err := parseFrontmatterName(invalidUTF8); err == nil {
		t.Fatalf("accepted invalid UTF-8 frontmatter with name %q", name)
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
