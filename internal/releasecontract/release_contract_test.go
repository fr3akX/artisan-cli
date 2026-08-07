package releasecontract_test

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/releasebuilder"
	"gopkg.in/yaml.v3"
)

const (
	testVersion = "v0.0.0-contract"
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
)

var fullActionSHA = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-fA-F]{40}$`)

func TestCIWorkflowContract(t *testing.T) {
	root := repositoryRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCIWorkflow(contents); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseWorkflowContract(t *testing.T) {
	root := repositoryRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseWorkflow(contents); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowValidatorRejectsBypasses(t *testing.T) {
	for name, workflow := range map[string]string{
		"mutable action":     "name: x\non: push\njobs:\n  x:\n    steps:\n      - uses: actions/checkout@v4\n",
		"multiple documents": "name: x\n---\nname: hidden\n",
		"anchor":             "name: &shared x\non: push\n",
		"alias":              "name: &shared x\non: *shared\n",
		"merge":              "defaults: &defaults\n  run: echo x\njob:\n  <<: *defaults\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseWorkflow([]byte(workflow)); err == nil {
				t.Fatal("unsafe workflow accepted")
			}
		})
	}

	root := repositoryRoot(t)
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string][2]string{
		"native OS":             {"windows-2022", "windows-missing"},
		"race flags":            {"run: go test ./... -race", "run: go test -race"},
		"build command":         {"go build -trimpath", "echo no-build"},
		"smoke command":         {`"./artisan-ci${suffix}" --json version`, "echo smoke-disabled"},
		"permissions":           {"contents: read", "contents: write"},
		"generation drift":      {"git diff --exit-code", "git status --short"},
		"command in comment":    {"run: go vet ./...", "run: echo vet-disabled\n      # go vet ./..."},
		"wrong shell":           {"name: Check formatting\n        shell: bash", "name: Check formatting\n        shell: sh"},
		"test if false":         {"name: Test\n        run:", "name: Test\n        if: false\n        run:"},
		"race false expression": {"name: Race test\n        run:", "name: Race test\n        if: ${{ false }}\n        run:"},
		"quality continue":      {"quality:\n    runs-on:", "quality:\n    continue-on-error: ${{ matrix.allowed }}\n    runs-on:"},
	} {
		t.Run("CI missing "+name, func(t *testing.T) {
			changed := bytes.Replace(ci, []byte(mutation[0]), []byte(mutation[1]), 1)
			if bytes.Equal(changed, ci) {
				t.Fatal("test mutation did not apply")
			}
			if err := validateCIWorkflow(changed); err == nil {
				t.Fatal("CI contract drift was accepted")
			}
		})
	}

	release, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string][2]string{
		"tag filter":           {"- 'v*'", "- '*'"},
		"builder":              {`run: scripts/build-release.sh "$VERSION" "$COMMIT" release`, "run: echo no-builder"},
		"builder in text":      {`run: scripts/build-release.sh "$VERSION" "$COMMIT" release`, `run: echo 'scripts/build-release.sh "$VERSION" "$COMMIT" release'`},
		"wrong shell":          {"name: Build and verify static release archives\n        shell: bash", "name: Build and verify static release archives\n        shell: sh"},
		"attestation":          {"actions/attest-build-provenance@", "actions/upload-artifact@"},
		"attestation input":    {"subject-path: 'dist/release/*'", "subject-path: 'dist/release/*.zip'"},
		"publish":              {"softprops/action-gh-release@", "actions/upload-artifact@"},
		"builder if false":     {"name: Build and verify static release archives\n        shell:", "name: Build and verify static release archives\n        if: false\n        shell:"},
		"attestation continue": {"name: Attest archives and checksums\n        uses:", "name: Attest archives and checksums\n        continue-on-error: true\n        uses:"},
		"publish dynamic if":   {"name: Publish GitHub release\n        uses:", "name: Publish GitHub release\n        if: ${{ success() }}\n        uses:"},
	} {
		t.Run("release "+name, func(t *testing.T) {
			changed := bytes.Replace(release, []byte(mutation[0]), []byte(mutation[1]), 1)
			if bytes.Equal(changed, release) {
				t.Fatal("test mutation did not apply")
			}
			if err := validateReleaseWorkflow(changed); err == nil {
				t.Fatal("release contract drift was accepted")
			}
		})
	}
}

func TestReleaseScriptsAreExactThinWrappers(t *testing.T) {
	root := repositoryRoot(t)
	expected := map[string]string{
		"scripts/build-release.sh":  "#!/usr/bin/env bash\nset -euo pipefail\n\n[[ $# -ge 2 && $# -le 3 ]] || {\n  echo \"Usage: scripts/build-release.sh VERSION COMMIT [DESTINATION_LEAF]\" >&2\n  exit 2\n}\n\nROOT=$(CDPATH= cd -- \"$(dirname -- \"${BASH_SOURCE[0]}\")/..\" && pwd -P)\nGO_BIN=${GO:-go}\ncd \"$ROOT\"\nexec \"$GO_BIN\" run ./internal/releasebuilder/cmd \"$1\" \"$2\" \"${3:-release}\"\n",
		"scripts/build-release.ps1": "[CmdletBinding()]\nparam(\n    [Parameter(Mandatory = $true)][string]$Version,\n    [Parameter(Mandatory = $true)][string]$Commit,\n    [string]$Destination = \"release\"\n)\n\n$ErrorActionPreference = \"Stop\"\nSet-StrictMode -Version Latest\n$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot \"..\"))\n$go = if ($env:GO) { $env:GO } else { \"go\" }\nSet-Location $root\n& $go run ./internal/releasebuilder/cmd $Version $Commit $Destination\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\n",
	}
	for path, want := range expected {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.ReplaceAll(string(contents), "\r\n", "\n") != want {
			t.Errorf("%s is not the reviewed thin wrapper", path)
		}
	}
}

func TestPowerShellWrapperWhenAvailable(t *testing.T) {
	powerShell, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is unavailable")
	}
	root := repositoryRoot(t)
	leaf := "contract-powershell"
	output := filepath.Join(root, "dist", leaf)
	_ = os.RemoveAll(output)
	t.Cleanup(func() { _ = os.RemoveAll(output) })
	command := exec.Command(powerShell, "-NoProfile", "-File", "scripts/build-release.ps1", "-Version", testVersion, "-Commit", testCommit, "-Destination", leaf)
	command.Dir = root
	command.Env = append(os.Environ(), "GO="+goCommand(t))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("PowerShell wrapper failed: %v\n%s", err, output)
	}
}

func TestReleaseArchives(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("archive implementation is exercised through the shared Go builder; wrapper execution is platform-specific")
	}
	for _, tool := range []string{"bash", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable: %v", tool, err)
		}
	}
	root := repositoryRoot(t)
	leafA := "work"
	leafB := "contract-test-b"
	output := filepath.Join(root, "dist", leafA)
	outputB := filepath.Join(root, "dist", leafB)
	for _, path := range []string{output, outputB} {
		_ = os.RemoveAll(path)
	}
	t.Cleanup(func() { _ = os.RemoveAll(output); _ = os.RemoveAll(outputB) })
	runReleaseBuilder(t, root, leafA, "077")
	runReleaseBuilder(t, root, leafB, "022")

	wantArchives := make([]string, 0, 6)
	for _, target := range []struct{ goos, goarch, extension string }{
		{"linux", "amd64", ".tar.gz"}, {"linux", "arm64", ".tar.gz"},
		{"darwin", "amd64", ".tar.gz"}, {"darwin", "arm64", ".tar.gz"},
		{"windows", "amd64", ".zip"}, {"windows", "arm64", ".zip"},
	} {
		name := fmt.Sprintf("artisan-%s-%s-%s%s", testVersion, target.goos, target.goarch, target.extension)
		wantArchives = append(wantArchives, name)
		archivePath := filepath.Join(output, name)
		entries := archiveEntries(t, archivePath)
		top := strings.TrimSuffix(name, target.extension)
		binary := "artisan"
		if target.goos == "windows" {
			binary += ".exe"
		}
		wantEntries := []string{
			top + "/", top + "/LICENSE", top + "/THIRD_PARTY_NOTICES.txt", top + "/" + binary,
			top + "/skills/", top + "/skills/artisan-inventory/", top + "/skills/artisan-inventory/SKILL.md",
		}
		if strings.Join(entries, "\n") != strings.Join(wantEntries, "\n") {
			t.Errorf("archive order/content = %v, want %v", entries, wantEntries)
		}
		if err := releasebuilder.InspectArchive(archivePath, testVersion, target.goos, target.goarch); err != nil {
			t.Fatal(err)
		}
		first, err := os.ReadFile(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		second, err := os.ReadFile(filepath.Join(outputB, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("archive differs across umasks: %s", name)
		}
	}
	assertChecksums(t, output, wantArchives)
	manifestA, err := os.ReadFile(filepath.Join(output, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	manifestB, err := os.ReadFile(filepath.Join(outputB, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifestA, manifestB) {
		t.Error("checksum manifests differ across umasks")
	}

	native := filepath.Join(output, fmt.Sprintf("artisan-%s-%s-%s.tar.gz", testVersion, runtime.GOOS, runtime.GOARCH))
	if runtime.GOOS == "linux" {
		top := fmt.Sprintf("artisan-%s-%s-%s", testVersion, runtime.GOOS, runtime.GOARCH)
		extract := t.TempDir()
		command := exec.Command("tar", "-xzf", native, "-C", extract)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("extract native archive: %v: %s", err, output)
		}
		version := exec.Command(filepath.Join(extract, top, "artisan"), "--json", "version")
		output, err := version.CombinedOutput()
		if err != nil {
			t.Fatalf("native version smoke failed: %v: %s", err, output)
		}
		if !strings.Contains(string(output), `"version":"`+testVersion+`"`) || !strings.Contains(string(output), `"commit":"`+testCommit+`"`) {
			t.Fatalf("native version metadata mismatch: %s", output)
		}
	}
}

func TestDocumentationContract(t *testing.T) {
	root := repositoryRoot(t)
	required := map[string][]string{
		"docs/installation.md":        {"checksums.txt", "sha256sum", "Get-FileHash", "unsigned", "not notarized", "CGO_ENABLED=0", "skills/artisan-inventory/SKILL.md"},
		"docs/commands.md":            {"--json --server URL --timeout DURATION", "auth login --token-stdin", "inventory lot", "inventory image", "inventory reservation", "inventory conflict", "inventory adjust", "skill install", "version", "actual grams, when present, must be at least 1", "external-reference", "external_reference", "altitude-max-metres", "altitude_max_metres"},
		"docs/json-and-exit-codes.md": {`{"ok":true,"data":`, `{"ok":false,"error":`, "130", "409", "pagination", "integer grams", "Idempotency", "actual grams must be at least 1"},
		"docs/security.md":            {"bearer", "stdin", "redirect", "HTTPS", "loopback", "proxy", "symlink", "--yes", "conflict", "token"},
		"docs/agent-skill.md":         {"artisan skill show", "artisan skill install --directory ROOT", "--force", "must not log in", "must not handle tokens"},
	}
	for path, snippets := range required {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		for _, snippet := range snippets {
			if !strings.Contains(string(contents), snippet) {
				t.Errorf("%s missing %q", path, snippet)
			}
		}
	}
	for _, path := range append([]string{"README.md"}, keys(required)...) {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err == nil && !strings.Contains(string(contents), "4c0136fe98f6728f4bb94e416c5abe570e7f4831") {
			t.Errorf("%s missing minimum compatible server ref", path)
		}
	}
}

func runReleaseBuilder(t *testing.T, root, leaf, umask string) {
	t.Helper()
	command := exec.Command("bash", "-c", `umask "$1"; exec bash scripts/build-release.sh "$2" "$3" "$4"`, "builder", umask, testVersion, testCommit, leaf)
	command.Dir = root
	command.Env = append(os.Environ(), "GO="+goCommand(t))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release builder (%s) failed: %v\n%s", umask, err, output)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func parseWorkflow(contents []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(contents)))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("workflow must contain one nonempty mapping document")
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("workflow must contain exactly one YAML document")
	}
	if err := rejectYAMLReuse(&document); err != nil {
		return nil, err
	}
	if err := walkYAML(&document, func(node *yaml.Node) error {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == "uses" {
					value := node.Content[i+1]
					if value.Kind != yaml.ScalarNode || !fullActionSHA.MatchString(value.Value) {
						return fmt.Errorf("untrusted action reference at line %d", value.Line)
					}
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &document, nil
}

func validateCIWorkflow(contents []byte) error {
	document, err := parseWorkflow(contents)
	if err != nil {
		return err
	}
	root := document.Content[0]
	permissions, err := mappingLookup(root, "permissions")
	if err != nil || !mappingEquals(permissions, map[string]string{"contents": "read"}) {
		return errors.New("CI permissions must contain only contents: read")
	}
	if workflowHasWritePermission(document) {
		return errors.New("CI jobs must not grant write permissions")
	}

	events, err := mappingLookup(root, "on")
	if err != nil || !hasMappingKeys(events, "pull_request", "push") {
		return errors.New("CI must run for pull requests and pushes")
	}
	jobs, err := mappingLookup(root, "jobs")
	if err != nil {
		return err
	}
	quality, err := mappingLookup(jobs, "quality")
	if err != nil {
		return err
	}
	native, err := mappingLookup(jobs, "native-build-smoke")
	if err != nil {
		return err
	}
	if err := rejectDisableControls(quality); err != nil {
		return fmt.Errorf("quality job: %w", err)
	}
	if err := rejectDisableControls(native); err != nil {
		return fmt.Errorf("native job: %w", err)
	}
	_, qualityUses, err := jobSteps(quality)
	if err != nil {
		return err
	}
	_, nativeUses, err := jobSteps(native)
	if err != nil {
		return err
	}
	allUses := append(append([]string(nil), qualityUses...), nativeUses...)
	if len(allUses) == 0 {
		return errors.New("CI has no actions")
	}
	for _, use := range allUses {
		if !fullActionSHA.MatchString(use) {
			return fmt.Errorf("CI action is not pinned: %s", use)
		}
	}
	for _, expected := range []struct{ name, shell, run string }{
		{"Check formatting", "bash", `test -z "$(gofmt -l $(find cmd internal integration -name '*.go' -type f))"`},
		{"Vet", "", "go vet ./..."},
		{"Test", "", "go test ./..."},
		{"Race test", "", "go test ./... -race"},
		{"Check generated skill", "bash", "go generate ./internal/skill\ngit diff --exit-code"},
	} {
		if err := requireRunStep(quality, expected.name, expected.shell, expected.run); err != nil {
			return err
		}
	}
	if err := requireRunStep(native, "Build and smoke native executable", "bash", `set -euo pipefail
suffix=
if [[ "${{ runner.os }}" == "Windows" ]]; then suffix=.exe; fi
go build -trimpath -o "artisan-ci${suffix}" ./cmd/artisan
"./artisan-ci${suffix}" --json version`); err != nil {
		return err
	}
	environment, err := mappingLookup(native, "env")
	if err != nil || !mappingEquals(environment, map[string]string{"CGO_ENABLED": "0"}) {
		return errors.New("native builds must set CGO_ENABLED=0")
	}
	strategy, err := mappingLookup(native, "strategy")
	if err != nil {
		return err
	}
	matrix, err := mappingLookup(strategy, "matrix")
	if err != nil {
		return err
	}
	oses, err := mappingLookup(matrix, "os")
	if err != nil || oses.Kind != yaml.SequenceNode {
		return errors.New("native OS matrix is missing")
	}
	seen := make(map[string]bool)
	for _, node := range oses.Content {
		seen[node.Value] = true
	}
	for _, want := range []string{"ubuntu-24.04", "macos-14", "windows-2022"} {
		if !seen[want] {
			return fmt.Errorf("native OS matrix missing %s", want)
		}
	}
	if !workflowHasGo123(document) {
		return errors.New("CI must use Go 1.23.x")
	}
	return nil
}

func validateReleaseWorkflow(contents []byte) error {
	document, err := parseWorkflow(contents)
	if err != nil {
		return err
	}
	root := document.Content[0]
	events, err := mappingLookup(root, "on")
	if err != nil {
		return err
	}
	if _, err := mappingLookup(events, "pull_request"); err == nil {
		return errors.New("release must not run for pull requests")
	}
	push, err := mappingLookup(events, "push")
	if err != nil {
		return errors.New("release push trigger is missing")
	}
	tags, err := mappingLookup(push, "tags")
	if err != nil || tags.Kind != yaml.SequenceNode || len(tags.Content) != 1 || tags.Content[0].Value != "v*" {
		return errors.New("release must run only for v* tags")
	}
	globalPermissions, err := mappingLookup(root, "permissions")
	if err != nil || !mappingEquals(globalPermissions, map[string]string{"contents": "read"}) {
		return errors.New("release global permissions must be read-only")
	}
	jobs, err := mappingLookup(root, "jobs")
	if err != nil {
		return err
	}
	release, err := mappingLookup(jobs, "release")
	if err != nil {
		return err
	}
	if err := rejectDisableControls(release); err != nil {
		return fmt.Errorf("release job: %w", err)
	}
	permissions, err := mappingLookup(release, "permissions")
	if err != nil || !mappingEquals(permissions, map[string]string{"contents": "write", "id-token": "write", "attestations": "write"}) {
		return errors.New("release job permissions drifted")
	}
	_, uses, err := jobSteps(release)
	if err != nil {
		return err
	}
	for _, expected := range []struct{ name, shell, run string }{
		{"Test and check generated skill", "bash", "set -euo pipefail\ngo test ./...\ngo generate ./internal/skill\ngit diff --exit-code"},
		{"Validate release identity", "bash", "set -euo pipefail\n[[ \"$VERSION\" =~ ^v[0-9A-Za-z][0-9A-Za-z._-]{0,63}$ ]]\n[[ \"$COMMIT\" =~ ^[0-9a-fA-F]{40}$ ]]"},
		{"Build and verify static release archives", "bash", `scripts/build-release.sh "$VERSION" "$COMMIT" release`},
		{"Verify release asset set", "bash", "set -euo pipefail\ntest \"$(find dist/release -maxdepth 1 -type f | wc -l)\" -eq 7\ntest \"$(find dist/release -maxdepth 1 -type f \\( -name '*.tar.gz' -o -name '*.zip' \\) | wc -l)\" -eq 6\n(cd dist/release && sha256sum -c checksums.txt)"},
	} {
		if err := requireRunStep(release, expected.name, expected.shell, expected.run); err != nil {
			return err
		}
	}
	for _, use := range uses {
		if !fullActionSHA.MatchString(use) {
			return fmt.Errorf("release action is not pinned: %s", use)
		}
	}
	if err := requireUsesStep(release, "Attest archives and checksums", "actions/attest-build-provenance@e8998f949152b193b063cb0ec769d69d929409be", map[string]string{"subject-path": "dist/release/*"}); err != nil {
		return err
	}
	if err := requireUsesStep(release, "Publish GitHub release", "softprops/action-gh-release@72f2c25fcb47643c292f7107632f7a47c1df5cd8", map[string]string{"fail_on_unmatched_files": "true", "files": "dist/release/*.tar.gz\ndist/release/*.zip\ndist/release/checksums.txt\n"}); err != nil {
		return err
	}
	if !workflowHasGo123(document) {
		return errors.New("release must use Go 1.23.x")
	}
	return nil
}

func rejectDisableControls(node *yaml.Node) error {
	return walkYAML(node, func(current *yaml.Node) error {
		if current.Kind != yaml.MappingNode {
			return nil
		}
		for index := 0; index+1 < len(current.Content); index += 2 {
			if current.Content[index].Value == "if" || current.Content[index].Value == "continue-on-error" {
				return fmt.Errorf("%s is forbidden on required jobs and steps", current.Content[index].Value)
			}
		}
		return nil
	})
}

func mappingLookup(node *yaml.Node, key string) (*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%q parent is not a mapping", key)
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1], nil
		}
	}
	return nil, fmt.Errorf("mapping key %q not found", key)
}

func mappingEquals(node *yaml.Node, want map[string]string) bool {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content) != len(want)*2 {
		return false
	}
	for key, value := range want {
		child, err := mappingLookup(node, key)
		if err != nil || child.Kind != yaml.ScalarNode || child.Value != value {
			return false
		}
	}
	return true
}

func hasMappingKeys(node *yaml.Node, keys ...string) bool {
	for _, key := range keys {
		if _, err := mappingLookup(node, key); err != nil {
			return false
		}
	}
	return true
}

func jobSteps(job *yaml.Node) (runs, uses []string, err error) {
	steps, err := mappingLookup(job, "steps")
	if err != nil || steps.Kind != yaml.SequenceNode {
		return nil, nil, errors.New("job steps are missing")
	}
	for _, step := range steps.Content {
		if step.Kind != yaml.MappingNode {
			return nil, nil, errors.New("workflow step is not a mapping")
		}
		if run, lookupErr := mappingLookup(step, "run"); lookupErr == nil {
			runs = append(runs, run.Value)
		}
		if use, lookupErr := mappingLookup(step, "uses"); lookupErr == nil {
			uses = append(uses, use.Value)
		}
	}
	return runs, uses, nil
}

func requireUsesStep(job *yaml.Node, name, use string, with map[string]string) error {
	steps, err := mappingLookup(job, "steps")
	if err != nil || steps.Kind != yaml.SequenceNode {
		return errors.New("job steps are missing")
	}
	for _, step := range steps.Content {
		stepName, lookupErr := mappingLookup(step, "name")
		if lookupErr != nil || stepName.Value != name {
			continue
		}
		actualUse, lookupErr := mappingLookup(step, "uses")
		if lookupErr != nil || actualUse.Value != use {
			return fmt.Errorf("step %q action drifted", name)
		}
		actualWith, lookupErr := mappingLookup(step, "with")
		if lookupErr != nil || !mappingEquals(actualWith, with) {
			return fmt.Errorf("step %q inputs drifted", name)
		}
		return nil
	}
	return fmt.Errorf("action step %q is missing", name)
}

func requireRunStep(job *yaml.Node, name, shell, run string) error {
	steps, err := mappingLookup(job, "steps")
	if err != nil || steps.Kind != yaml.SequenceNode {
		return errors.New("job steps are missing")
	}
	for _, step := range steps.Content {
		stepName, lookupErr := mappingLookup(step, "name")
		if lookupErr != nil || stepName.Value != name {
			continue
		}
		actualRun, lookupErr := mappingLookup(step, "run")
		if lookupErr != nil {
			return fmt.Errorf("step %q has no run command", name)
		}
		actualShell := ""
		if shellNode, shellErr := mappingLookup(step, "shell"); shellErr == nil {
			actualShell = shellNode.Value
		}
		normalize := func(value string) string { return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n")) }
		if actualShell != shell || normalize(actualRun.Value) != normalize(run) {
			return fmt.Errorf("step %q command/shell drifted", name)
		}
		return nil
	}
	return fmt.Errorf("run step %q is missing", name)
}

func workflowHasWritePermission(document *yaml.Node) bool {
	found := false
	_ = walkYAML(document, func(node *yaml.Node) error {
		if node.Kind == yaml.ScalarNode && node.Value == "write" {
			found = true
		}
		return nil
	})
	return found
}

func workflowHasGo123(document *yaml.Node) bool {
	found := false
	_ = walkYAML(document, func(node *yaml.Node) error {
		if node.Kind != yaml.MappingNode {
			return nil
		}
		for index := 0; index+1 < len(node.Content); index += 2 {
			if node.Content[index].Value == "go-version" && node.Content[index+1].Value == "1.23.x" {
				found = true
			}
		}
		return nil
	})
	return found
}

func rejectYAMLReuse(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode || node.Alias != nil || node.Anchor != "" || node.Tag == "!!merge" {
		return errors.New("anchors, aliases, and merge keys are forbidden")
	}
	for _, child := range node.Content {
		if err := rejectYAMLReuse(child); err != nil {
			return err
		}
	}
	return nil
}

func walkYAML(node *yaml.Node, visit func(*yaml.Node) error) error {
	if err := visit(node); err != nil {
		return err
	}
	for _, child := range node.Content {
		if err := walkYAML(child, visit); err != nil {
			return err
		}
	}
	return nil
}

func archiveEntries(t *testing.T, path string) []string {
	t.Helper()
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	modeFor := func(name string) os.FileMode {
		if strings.HasSuffix(name, "/") {
			return 0o755
		}
		if filepath.Base(name) == "artisan" || filepath.Base(name) == "artisan.exe" {
			return 0o755
		}
		return 0o644
	}
	if strings.HasSuffix(path, ".zip") {
		reader, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		entries := make([]string, 0, len(reader.File))
		for _, file := range reader.File {
			mode := file.Mode()
			if mode&os.ModeSymlink != 0 || strings.HasPrefix(file.Name, "/") || strings.Contains(file.Name, "../") {
				t.Fatalf("unsafe zip entry %q", file.Name)
			}
			if mode.Perm() != modeFor(file.Name) || mode.IsDir() != strings.HasSuffix(file.Name, "/") || !file.Modified.Equal(epoch) {
				t.Fatalf("noncanonical zip metadata for %q: mode=%v time=%v", file.Name, mode, file.Modified)
			}
			entries = append(entries, file.Name)
		}
		return entries
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	if !compressed.Header.ModTime.Equal(epoch) || compressed.Header.Name != "" || compressed.Header.Comment != "" || compressed.Header.OS != 255 {
		t.Fatal("noncanonical gzip header")
	}
	reader := tar.NewReader(compressed)
	var entries []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		isDirectory := strings.HasSuffix(header.Name, "/")
		wantType := byte(tar.TypeReg)
		if isDirectory {
			wantType = tar.TypeDir
		}
		if header.Typeflag != wantType || header.Linkname != "" || strings.HasPrefix(header.Name, "/") || strings.Contains(header.Name, "../") {
			t.Fatalf("unsafe tar entry %q", header.Name)
		}
		if os.FileMode(header.Mode) != modeFor(header.Name) || !header.ModTime.Equal(epoch) || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatalf("noncanonical tar metadata for %q", header.Name)
		}
		entries = append(entries, header.Name)
	}
	return entries
}

func assertChecksums(t *testing.T, directory string, archives []string) {
	t.Helper()
	file, err := os.Open(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			t.Fatalf("invalid checksum line %q", scanner.Text())
		}
		got[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(archives) {
		t.Fatalf("checksum count = %d, want %d", len(got), len(archives))
	}
	for _, archive := range archives {
		contents, err := os.ReadFile(filepath.Join(directory, archive))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		if got[archive] != hex.EncodeToString(digest[:]) {
			t.Errorf("checksum mismatch for %s", archive)
		}
	}
}

func goCommand(t *testing.T) string {
	t.Helper()
	if candidate := os.Getenv("GO"); candidate != "" {
		return candidate
	}
	path, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func keys(values map[string][]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}
