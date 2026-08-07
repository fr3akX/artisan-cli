package skill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSkillContentMatchesCanonicalSource(t *testing.T) {
	source := filepath.Join("..", "..", "skills", "artisan-inventory", "SKILL.md")
	want, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(Content, want) {
		t.Fatal("embedded skill differs from skills/artisan-inventory/SKILL.md; run go generate ./internal/skill")
	}
}

func TestSkillContentContract(t *testing.T) {
	text := string(Content)
	if !strings.HasPrefix(text, "---\nname: artisan-inventory\ndescription: Use when ") {
		t.Fatal("skill must have trigger-only artisan-inventory frontmatter")
	}
	frontmatterEnd := strings.Index(text[4:], "\n---\n")
	if frontmatterEnd < 0 {
		t.Fatal("skill frontmatter is not closed")
	}
	frontmatter := text[:frontmatterEnd+8]
	for _, forbidden := range []string{"workflow", "instruct", "require", "JSON", "token"} {
		if strings.Contains(frontmatter, forbidden) {
			t.Fatalf("frontmatter description summarizes behavior with %q", forbidden)
		}
	}

	required := []string{
		"artisan --json auth status",
		"never request, read, print, persist, or pass a token",
		"never run `artisan auth login`",
		"expected user, organization, server, and role",
		"integer grams",
		"explicit human approval",
		"--yes",
		"one idempotency key",
		"same key",
		"artisan --json inventory lot list",
		"--limit",
		"--cursor",
		"--all",
		"artisan --json inventory lot show",
		"artisan --json inventory lot ledger",
		"artisan --json inventory image",
		"artisan --json inventory reservation create",
		"artisan --json inventory reservation finalize",
		"artisan --json inventory conflict show",
		"artisan --json inventory conflict resolve",
		"409",
		"Do not adjust",
		"authoritative reread",
		"nonzero",
		"malformed",
		"server upgrade",
		"## Quick Reference",
		"## Common Mistakes",
	}
	for _, phrase := range required {
		if !strings.Contains(text, phrase) {
			t.Errorf("skill is missing %q", phrase)
		}
	}

	forbidden := []string{
		"ARTISAN_BEARER_TOKEN",
		"ARTISAN_SERVER_TOKEN",
		"auth login --token-stdin",
		"curl ",
		"http.Get",
	}
	for _, phrase := range forbidden {
		if strings.Contains(text, phrase) {
			t.Errorf("skill contains forbidden credential/network instruction %q", phrase)
		}
	}
}

func TestInstallCreatesPreservesRefusesAndForces(t *testing.T) {
	root := t.TempDir()
	result, err := Install(root, false)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "artisan-inventory", "SKILL.md")
	if result.Path != wantPath || !result.Installed || result.Unchanged {
		t.Fatalf("first Install() = %#v", result)
	}
	got, err := os.ReadFile(wantPath)
	if err != nil || !bytes.Equal(got, Content) {
		t.Fatalf("installed content mismatch: %v", err)
	}

	before, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err = Install(root, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Unchanged || result.Installed || !os.SameFile(before, after) {
		t.Fatalf("identical Install() rewrote the file: %#v", result)
	}

	if err := os.WriteFile(wantPath, []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(root, false); err == nil {
		t.Fatal("Install() accepted differing content without force")
	}
	got, _ = os.ReadFile(wantPath)
	if string(got) != "different\n" {
		t.Fatal("refused install modified existing content")
	}
	result, err = Install(root, true)
	if err != nil || !result.Installed || result.Unchanged {
		t.Fatalf("forced Install() = %#v, %v", result, err)
	}
	got, _ = os.ReadFile(wantPath)
	if !bytes.Equal(got, Content) {
		t.Fatal("forced install did not replace content")
	}
}

func TestInstallRejectsUnsafeTargets(t *testing.T) {
	if _, err := Install("", false); err == nil {
		t.Fatal("Install() accepted an empty root")
	}

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	t.Run("skill directory symlink", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "artisan-inventory")); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(root, false); err == nil {
			t.Fatal("Install() followed a skill-directory symlink")
		}
		if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); !os.IsNotExist(err) {
			t.Fatalf("outside target changed: %v", err)
		}
	})
	t.Run("skill file symlink", func(t *testing.T) {
		root, outside := t.TempDir(), filepath.Join(t.TempDir(), "outside")
		if err := os.Mkdir(filepath.Join(root, "artisan-inventory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "artisan-inventory", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(root, true); err == nil {
			t.Fatal("Install() replaced a skill-file symlink")
		}
		got, _ := os.ReadFile(outside)
		if string(got) != "outside" {
			t.Fatal("outside file changed")
		}
	})
}

func TestInstallConcurrentCallsLeaveWholeContent(t *testing.T) {
	root := t.TempDir()
	errs := make(chan error, 16)
	for i := 0; i < cap(errs); i++ {
		go func() {
			_, err := Install(root, false)
			errs <- err
		}()
	}
	for i := 0; i < cap(errs); i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Install() failed: %v", err)
		}
	}
	targetDir := filepath.Join(root, "artisan-inventory")
	got, err := os.ReadFile(filepath.Join(targetDir, "SKILL.md"))
	if err != nil || !bytes.Equal(got, Content) {
		t.Fatalf("concurrent install left partial content: %v", err)
	}
	if temporary, err := filepath.Glob(filepath.Join(targetDir, ".SKILL.md.tmp-*")); err != nil || len(temporary) != 0 {
		t.Fatalf("temporary files remain: %v, %v", temporary, err)
	}
}

func TestForcedInstallAtomicallyReplacesVisibleContent(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "artisan-inventory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	old := bytes.Repeat([]byte("old-content\n"), 10000)
	target := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(target, old, 0o644); err != nil {
		t.Fatal(err)
	}

	ready, stop, readerResult := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		first := true
		for {
			got, err := os.ReadFile(target)
			if err != nil {
				readerResult <- err
				return
			}
			if !bytes.Equal(got, old) && !bytes.Equal(got, Content) {
				readerResult <- errors.New("reader observed partial content")
				return
			}
			if first {
				close(ready)
				first = false
			}
			select {
			case <-stop:
				readerResult <- nil
				return
			default:
			}
		}
	}()
	<-ready
	_, installErr := Install(root, true)
	close(stop)
	if readErr := <-readerResult; installErr != nil || readErr != nil {
		t.Fatalf("Install() error = %v; reader error = %v", installErr, readErr)
	}
}
