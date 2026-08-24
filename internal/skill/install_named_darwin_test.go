//go:build darwin

package skill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

// This mirrors the Linux named-skill security matrix against the shared Unix
// installer. It is cross-compiled on non-Darwin hosts and runs natively in
// macOS CI so RenameatxNp and directory durability are exercised there.
func TestDarwinNamedInstallsApplyLocationRaceAndDurabilityProtection(t *testing.T) {
	for _, name := range Names() {
		t.Run(name+"/root swap", func(t *testing.T) {
			parent := canonicalTempDir(t)
			root := filepath.Join(parent, "root")
			opened := filepath.Join(parent, "opened-root")
			outside := canonicalTempDir(t)
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			result, err := installWithHooks(root, name, false, installHooks{afterRootOpen: func() error {
				if err := os.Rename(root, opened); err != nil {
					return err
				}
				return os.Symlink(outside, root)
			}})
			if !errors.Is(err, ErrInstallLocationChanged) || !InstallVisible(err) || result.Path != "" {
				t.Fatalf("install = %#v, %v", result, err)
			}
			definition, _ := Lookup(name)
			got, readErr := os.ReadFile(filepath.Join(opened, name, FileName))
			if readErr != nil || !bytes.Equal(got, definition.Content) {
				t.Fatalf("opened install mismatch: %v", readErr)
			}
			if _, statErr := os.Stat(filepath.Join(outside, name, FileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("swapped root changed: %v", statErr)
			}
		})

		t.Run(name+"/location swap", func(t *testing.T) {
			root := canonicalTempDir(t)
			visible := filepath.Join(root, name)
			opened := filepath.Join(root, name+"-opened")
			outside := canonicalTempDir(t)
			if err := os.Mkdir(visible, 0o755); err != nil {
				t.Fatal(err)
			}
			result, err := installWithHooks(root, name, false, installHooks{afterSkillDirOpen: func() error {
				if err := os.Rename(visible, opened); err != nil {
					return err
				}
				return os.Symlink(outside, visible)
			}})
			if !errors.Is(err, ErrInstallLocationChanged) || !InstallVisible(err) || result.Path != "" {
				t.Fatalf("install = %#v, %v", result, err)
			}
			definition, _ := Lookup(name)
			got, readErr := os.ReadFile(filepath.Join(opened, FileName))
			if readErr != nil || !bytes.Equal(got, definition.Content) {
				t.Fatalf("opened install mismatch: %v", readErr)
			}
			if _, statErr := os.Stat(filepath.Join(outside, FileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("swapped destination changed: %v", statErr)
			}
		})

		t.Run(name+"/target race", func(t *testing.T) {
			root := canonicalTempDir(t)
			directory := filepath.Join(root, name)
			outside := filepath.Join(canonicalTempDir(t), "outside")
			if err := os.Mkdir(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := installWithHooks(root, name, true, installHooks{beforeCommit: func() error {
				return os.Symlink(outside, filepath.Join(directory, FileName))
			}})
			if !errors.Is(err, ErrUnsafeTarget) {
				t.Fatalf("race error = %v", err)
			}
			got, readErr := os.ReadFile(outside)
			if readErr != nil || string(got) != "outside" {
				t.Fatalf("outside changed: %q, %v", got, readErr)
			}
		})

		t.Run(name+"/durability", func(t *testing.T) {
			root := canonicalTempDir(t)
			if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected directory sync failure")
			result, err := installWithHooks(root, name, false, installHooks{
				syncDirectory: func(*os.File) error { return injected },
			})
			if !errors.Is(err, injected) || !securefile.ReplacementVisible(err) || !result.Installed || result.Path != "" {
				t.Fatalf("durability install = %#v, %v", result, err)
			}
			definition, _ := Lookup(name)
			got, readErr := os.ReadFile(filepath.Join(root, name, FileName))
			if readErr != nil || !bytes.Equal(got, definition.Content) {
				t.Fatalf("visible content mismatch: %v", readErr)
			}
		})
	}
}
