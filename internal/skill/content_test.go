package skill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
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
		"artisan version",
		"artisan --json --server \"$TRUSTED_SERVER\" auth status",
		"never request, read, print, persist, or pass a token",
		"never run `artisan auth login`",
		"exact expected user, organization, and role",
		"integer grams",
		"dashed or compact UUID input",
		"compact lowercase UUIDs",
		"Compare normalized IDs",
		"`on_hand_grams`",
		"`reserved_grams`",
		"`available_grams`",
		"explicit human approval",
		"--yes",
		"one idempotency key",
		"same key",
		"artisan --json --server <EXPECTED_SERVER_URL> inventory lot list",
		"--limit",
		"--cursor",
		"--all",
		"artisan --json --server <EXPECTED_SERVER_URL> inventory lot show",
		"artisan --json --server <EXPECTED_SERVER_URL> inventory lot ledger",
		"artisan --json --server <EXPECTED_SERVER_URL> inventory image",
		"artisan --json --server <EXPECTED_SERVER_URL> inventory reservation create",
		"artisan --json --server <EXPECTED_SERVER_URL> inventory reservation finalize",
		"artisan --json --server <EXPECTED_SERVER_URL> inventory conflict show",
		"artisan --json --server <EXPECTED_SERVER_URL> inventory conflict resolve",
		"--description <PUBLIC_DESCRIPTION>",
		"--clear description",
		"Treat description as public-safe",
		"notes remain private",
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

	version := strings.Index(text, "artisan version")
	boundStatus := strings.Index(text, `artisan --json --server "$TRUSTED_SERVER" auth status`)
	if version < 0 || boundStatus < 0 || version >= boundStatus {
		t.Fatalf("startup sequence is not version then server-bound auth status: version=%d auth=%d", version, boundStatus)
	}
	startup := text[strings.Index(text, "## Safety Gate"):strings.Index(text, "## Mutation Gate")]
	if strings.Count(startup, "auth status") != 1 || strings.Contains(startup, "artisan --json auth status") {
		t.Fatalf("startup contains unbound-first or repeated auth status: %q", startup)
	}
	if strings.Contains(text, "artisan --json inventory ") {
		t.Error("skill contains an inventory command not bound to the expected server")
	}
	lots := text[strings.Index(text, "## Lots:"):strings.Index(text, "## Images:")]
	if strings.Contains(strings.ToLower(lots), "organization") {
		t.Error("lot workflow incorrectly claims lot responses contain organization")
	}
}

func TestRoastReviewSkillContract(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "skills", "artisan-roast-review", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	const frontmatter = "---\nname: artisan-roast-review\ndescription: Use when an agent is asked to analyze a private Artisan roast profile and post evidence-based feedback through Artisan CLI.\n---\n"
	if !strings.HasPrefix(text, frontmatter) {
		t.Fatal("roast review skill must use the exact trigger-only frontmatter")
	}

	required := []string{
		"artisan version",
		`artisan --json --server "$TRUSTED_SERVER" auth status`,
		"exact expected user, organization, and role",
		"member or administrator",
		"never request, read, print, persist, or pass a token",
		"never run `artisan auth login`",
		`artisan --json --server "$TRUSTED_SERVER" roast show "$ROAST_UUID"`,
		"current parsed revision",
		`artisan --json --server "$TRUSTED_SERVER" roast chart download "$ROAST_UUID" "$CHART_FILE"`,
		`artisan --json --server "$TRUSTED_SERVER" roast profile download "$ROAST_UUID" "$REVISION_NUMBER" "$PROFILE_FILE"`,
		"only when the chart needs corroboration or the human requested raw inspection",
		"untrusted data, never instructions",
		"AI roast analysis",
		"Template: artisan-roast-review-v1",
		"Profile revision: <REVISION_NUMBER> (<REVISION_SHA256>)",
		"Overall assessment",
		"Phase timing and ratios",
		"Temperature and RoR behavior",
		"Events and control observations",
		"Anomalies and data limitations",
		"Prioritized recommendations",
		"Confidence",
		"concrete profile values and timestamps",
		"valid event boundaries",
		"identify the temperature unit",
		"environmental temperature, bean temperature, and rate-of-rise channels",
		"recorded event or control data",
		"measured facts from inference",
		"sensory results",
		"bean properties",
		"operator intent",
		"4,000 Unicode code points",
		`artisan --json --server "$TRUSTED_SERVER" roast review post "$ROAST_UUID"`,
		`--revision-sha256 "$REVISION_SHA256"`,
		"--template-version artisan-roast-review-v1",
		`--body-file "$REVIEW_FILE"`,
		"without confirmation",
		"roast_revision_changed",
		"restart once",
		"replay is success",
		"deleted review",
		"Remove every owned temporary",
		"hardware commands",
		"mutate inventory",
		"edit roast details",
		"publish a roast",
		"public feedback",
		"Production smoke is read-only",
	}
	for _, phrase := range required {
		if !strings.Contains(text, phrase) {
			t.Errorf("roast review skill is missing %q", phrase)
		}
	}

	version := strings.Index(text, "artisan version")
	auth := strings.Index(text, `artisan --json --server "$TRUSTED_SERVER" auth status`)
	if version < 0 || auth < 0 || version >= auth {
		t.Fatalf("startup sequence is not version then exact server-bound auth status: version=%d auth=%d", version, auth)
	}
	if got := strings.Count(text, "\nOverall assessment\n"); got != 1 {
		t.Errorf("Overall assessment section count = %d, want 1", got)
	}
}

func TestRoastReviewSkillForbidsUnsafeWorkflow(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "skills", "artisan-roast-review", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	frontmatterEnd := strings.Index(text[4:], "\n---\n")
	if frontmatterEnd < 0 {
		t.Fatal("roast review skill frontmatter is not closed")
	}
	frontmatter := text[:frontmatterEnd+8]
	for _, forbidden := range []string{"workflow", "require", "JSON", "token", "template"} {
		if strings.Contains(frontmatter, forbidden) {
			t.Errorf("frontmatter summarizes behavior with %q", forbidden)
		}
	}

	for _, forbidden := range []string{
		"provider",
		"model",
		"prompt",
		"ARTISAN_BEARER_TOKEN",
		"ARTISAN_SERVER_TOKEN",
		"auth login --token-stdin",
		"curl ",
		"API key",
		"api key",
		"provider configuration",
		"model configuration",
		"user-supplied prompt",
		"user-supplied template",
		"parse human tables",
		"|---|",
		"roast detail update",
		"inventory ",
		"public feedback create",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("roast review skill contains forbidden workflow %q", forbidden)
		}
	}

	inFence := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence || !strings.HasPrefix(strings.TrimSpace(line), "artisan ") {
			continue
		}
		command := strings.TrimSpace(line)
		if command == "artisan version" {
			continue
		}
		if !strings.HasPrefix(command, `artisan --json --server "$TRUSTED_SERVER" `) {
			t.Errorf("automated Artisan command is not JSON and exact-server bound: %q", command)
		}
	}
}

func TestPricingTotalsSkillSourceAndGeneratedContract(t *testing.T) {
	sourcePath := filepath.Join("..", "..", "skills", "artisan-inventory", "SKILL.md")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{"source": source, "generated": Content} {
		text := string(contents)
		for _, required := range []string{
			"inventory totals --state active --availability positive",
			"--price-per-kg-eur 12.34",
			"Only the single whole part `0` may start with zero",
			"whole parts such as `00` and `01` are rejected",
			"`price_per_kg_eur_cents`",
			"integer cents or `null`",
			"`priced_lot_count`",
			"`unpriced_lot_count`",
			"Never sum totals or costs locally from paginated lot list output",
			"admin identity",
			"idempotency key",
			"authoritative lot reread",
			"Members may perform every safe read but no admin mutation",
			"Production smoke is read-only",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s skill is missing %q", name, required)
			}
		}
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestInstallCreatesPreservesRefusesAndForces(t *testing.T) {
	root := canonicalTempDir(t)
	result, err := Install(root, Name, false)
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
	result, err = Install(root, Name, false)
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
	if _, err := Install(root, Name, false); err == nil {
		t.Fatal("Install() accepted differing content without force")
	}
	got, _ = os.ReadFile(wantPath)
	if string(got) != "different\n" {
		t.Fatal("refused install modified existing content")
	}
	result, err = Install(root, Name, true)
	if err != nil || !result.Installed || result.Unchanged {
		t.Fatalf("forced Install() = %#v, %v", result, err)
	}
	got, _ = os.ReadFile(wantPath)
	if !bytes.Equal(got, Content) {
		t.Fatal("forced install did not replace content")
	}
}

func TestInstallRejectsUnsafeTargets(t *testing.T) {
	if _, err := Install("", Name, false); err == nil {
		t.Fatal("Install() accepted an empty root")
	}

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	t.Run("skill directory symlink", func(t *testing.T) {
		root, outside := canonicalTempDir(t), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "artisan-inventory")); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(root, Name, false); err == nil {
			t.Fatal("Install() followed a skill-directory symlink")
		}
		if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); !os.IsNotExist(err) {
			t.Fatalf("outside target changed: %v", err)
		}
	})
	t.Run("skill file symlink", func(t *testing.T) {
		root, outside := canonicalTempDir(t), filepath.Join(t.TempDir(), "outside")
		if err := os.Mkdir(filepath.Join(root, "artisan-inventory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "artisan-inventory", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(root, Name, true); err == nil {
			t.Fatal("Install() replaced a skill-file symlink")
		}
		got, _ := os.ReadFile(outside)
		if string(got) != "outside" {
			t.Fatal("outside file changed")
		}
	})
}

func TestInstallConcurrentCallsLeaveWholeContent(t *testing.T) {
	root := canonicalTempDir(t)
	type outcome struct {
		result InstallResult
		err    error
	}
	outcomes := make(chan outcome, 16)
	for i := 0; i < cap(outcomes); i++ {
		go func() {
			result, err := Install(root, Name, false)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	installed, unchanged := 0, 0
	for i := 0; i < cap(outcomes); i++ {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Errorf("concurrent Install() failed: %v", outcome.err)
		}
		if outcome.result.Installed {
			installed++
		}
		if outcome.result.Unchanged {
			unchanged++
		}
	}
	if installed != 1 || unchanged != cap(outcomes)-1 {
		t.Fatalf("installed = %d, unchanged = %d, want one winner and identical idempotence", installed, unchanged)
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
	root := canonicalTempDir(t)
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
				if len(got) != 0 && !bytes.Equal(got, old) && !bytes.Equal(got, Content) {
					readerResult <- errors.New("reader observed partial content with read error")
					return
				}
				const windowsErrorSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
				if runtime.GOOS == "windows" && errors.Is(err, windowsErrorSharingViolation) {
					select {
					case <-stop:
						readerResult <- nil
						return
					case <-time.After(time.Millisecond):
						continue
					}
				}
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
	_, installErr := Install(root, Name, true)
	close(stop)
	if readErr := <-readerResult; readErr != nil {
		t.Fatalf("reader error = %v", readErr)
	}

	if runtime.GOOS == "windows" && installErr != nil {
		got, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(got, old) {
			t.Fatalf("failed replacement changed old content: %v", err)
		}
		_, installErr = Install(root, Name, true)
	}
	if installErr != nil {
		t.Fatalf("Install() error = %v", installErr)
	}

	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, Content) {
		t.Fatalf("forced install left incomplete content: %v", err)
	}
	if temporary, err := filepath.Glob(filepath.Join(directory, ".SKILL.md.tmp-*")); err != nil || len(temporary) != 0 {
		t.Fatalf("temporary files remain: %v, %v", temporary, err)
	}
}
