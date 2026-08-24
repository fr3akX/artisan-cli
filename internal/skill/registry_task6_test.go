package skill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestSkillRegistryNamesLookupAndImmutableCopies(t *testing.T) {
	wantNames := []string{"artisan-inventory", "artisan-roast-review"}
	firstNames := Names()
	if !reflect.DeepEqual(firstNames, wantNames) {
		t.Fatalf("Names() = %q, want %q", firstNames, wantNames)
	}
	firstNames[0] = "mutated"
	if got := Names(); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("mutating Names result changed registry: %q", got)
	}

	for _, name := range wantNames {
		definition, ok := Lookup(name)
		if !ok || definition.Name != name || len(definition.Content) == 0 {
			t.Fatalf("Lookup(%q) = %#v, %t", name, definition, ok)
		}
		source, err := os.ReadFile(filepath.Join("..", "..", "skills", name, FileName))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(definition.Content, source) {
			t.Fatalf("Lookup(%q) content differs from canonical source", name)
		}
		definition.Content[0] ^= 0xff
		again, ok := Lookup(name)
		if !ok || !bytes.Equal(again.Content, source) {
			t.Fatalf("mutating Lookup(%q) result changed registry", name)
		}
	}

	if definition, ok := Lookup("unknown"); ok || definition.Name != "" || definition.Content != nil {
		t.Fatalf("Lookup(unknown) = %#v, %t", definition, ok)
	}
}

func TestInstallUnknownSkillFailsBeforeFilesystemAccess(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "must-not-be-created")
	result, err := Install(missingRoot, "unknown", false)
	if !errors.Is(err, ErrUnknownSkill) || result != (InstallResult{}) {
		t.Fatalf("Install unknown = %#v, %v", result, err)
	}
	if _, statErr := os.Lstat(missingRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unknown install touched root: %v", statErr)
	}
}

func TestNamedInstallsAreIndependentAndConcurrent(t *testing.T) {
	root := canonicalTempDir(t)
	names := Names()
	type outcome struct {
		name   string
		result InstallResult
		err    error
	}
	outcomes := make(chan outcome, len(names)*8)
	var start sync.WaitGroup
	start.Add(1)
	for _, name := range names {
		for i := 0; i < 8; i++ {
			go func(name string) {
				start.Wait()
				result, err := Install(root, name, false)
				outcomes <- outcome{name: name, result: result, err: err}
			}(name)
		}
	}
	start.Done()

	counts := make(map[string]struct{ installed, unchanged int })
	for range names {
		for i := 0; i < 8; i++ {
			outcome := <-outcomes
			if outcome.err != nil {
				t.Errorf("Install(%q) error = %v", outcome.name, outcome.err)
				continue
			}
			count := counts[outcome.name]
			if outcome.result.Installed {
				count.installed++
			}
			if outcome.result.Unchanged {
				count.unchanged++
			}
			counts[outcome.name] = count
		}
	}
	for _, name := range names {
		count := counts[name]
		if count.installed != 1 || count.unchanged != 7 {
			t.Errorf("%s outcomes = %#v, want one install and seven unchanged", name, count)
		}
		definition, _ := Lookup(name)
		path := filepath.Join(root, name, FileName)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, definition.Content) {
			t.Errorf("%s installed content mismatch: %v", name, err)
		}
	}

	inventory, _ := Lookup("artisan-inventory")
	roastPath := filepath.Join(root, "artisan-roast-review", FileName)
	if err := os.WriteFile(roastPath, []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(root, "artisan-roast-review", false); !errors.Is(err, ErrDifferentContent) {
		t.Fatalf("differing roast install error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "artisan-inventory", FileName))
	if err != nil || !bytes.Equal(got, inventory.Content) {
		t.Fatal("roast refusal changed inventory installation")
	}
}

func TestNamedInstallSecurityHooksUseSelectedDefinition(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			root := canonicalTempDir(t)
			result, err := installWithHooks(root, name, false, installHooks{})
			if err != nil || !result.Installed {
				t.Fatalf("installWithHooks = %#v, %v", result, err)
			}
			definition, _ := Lookup(name)
			got, err := os.ReadFile(filepath.Join(root, name, FileName))
			if err != nil || !bytes.Equal(got, definition.Content) {
				t.Fatalf("selected content mismatch: %v", err)
			}
		})
	}
}
