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
	"sort"
	"strings"
	"testing"

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
		"native OS":        {"windows-2022", "windows-missing"},
		"race":             {"go test ./... -race", "go test ./..."},
		"build":            {"go build -trimpath", "echo no-build"},
		"smoke":            {"--json version", "version-disabled"},
		"permissions":      {"contents: read", "contents: write"},
		"generation drift": {"git diff --exit-code", "git status --short"},
	} {
		t.Run("CI missing "+name, func(t *testing.T) {
			changed := bytes.Replace(ci, []byte(mutation[0]), []byte(mutation[1]), 1)
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
		"tag filter":  {"- 'v*'", "- '*'"},
		"builder":     {"scripts/build-release.sh", "echo no-builder"},
		"attestation": {"actions/attest-build-provenance@", "actions/upload-artifact@"},
		"publish":     {"softprops/action-gh-release@", "actions/upload-artifact@"},
	} {
		t.Run("release "+name, func(t *testing.T) {
			changed := bytes.Replace(release, []byte(mutation[0]), []byte(mutation[1]), 1)
			if err := validateReleaseWorkflow(changed); err == nil {
				t.Fatal("release contract drift was accepted")
			}
		})
	}
}

func TestReleaseScriptsStaticContract(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{"scripts/build-release.sh", "scripts/build-release.ps1"} {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		text := string(contents)
		for _, required := range []string{
			"linux", "darwin", "windows", "amd64", "arm64", "CGO_ENABLED", "-trimpath",
			"github.com/fr3akX/artisan-cli/internal/release.Version", "github.com/fr3akX/artisan-cli/internal/release.Commit",
			"LICENSE", "THIRD_PARTY_NOTICES.txt", "skills/artisan-inventory/SKILL.md", "checksums.txt",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing %q", path, required)
			}
		}
	}
}

func TestReleaseArchives(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell release builder is validated on Unix; Windows runs the PowerShell builder in CI")
	}
	for _, tool := range []string{"bash", "tar", "gzip", "zip", "sha256sum", "file", "ldd"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable: %v", tool, err)
		}
	}
	root := repositoryRoot(t)
	outputRelative := filepath.ToSlash(filepath.Join("dist", "contract-test-"+strings.ReplaceAll(t.Name(), "/", "-")))
	output := filepath.Join(root, filepath.FromSlash(outputRelative))
	t.Cleanup(func() { _ = os.RemoveAll(output) })
	command := exec.Command("bash", "scripts/build-release.sh", testVersion, testCommit, outputRelative)
	command.Dir = root
	command.Env = append(os.Environ(), "GO="+goCommand(t))
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("release builder failed: %v\n%s", err, combined)
	}

	wantArchives := make([]string, 0, 6)
	for _, target := range []struct{ goos, goarch, extension string }{
		{"linux", "amd64", ".tar.gz"}, {"linux", "arm64", ".tar.gz"},
		{"darwin", "amd64", ".tar.gz"}, {"darwin", "arm64", ".tar.gz"},
		{"windows", "amd64", ".zip"}, {"windows", "arm64", ".zip"},
	} {
		name := fmt.Sprintf("artisan-%s-%s-%s%s", testVersion, target.goos, target.goarch, target.extension)
		wantArchives = append(wantArchives, name)
		entries := archiveEntries(t, filepath.Join(output, name))
		top := strings.TrimSuffix(name, target.extension)
		binary := "artisan"
		if target.goos == "windows" {
			binary += ".exe"
		}
		wantEntries := []string{
			top + "/", top + "/" + binary, top + "/LICENSE", top + "/THIRD_PARTY_NOTICES.txt",
			top + "/skills/", top + "/skills/artisan-inventory/", top + "/skills/artisan-inventory/SKILL.md",
		}
		assertSameStrings(t, entries, wantEntries)
	}
	assertChecksums(t, output, wantArchives)

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
		"docs/commands.md":            {"--json --server URL --timeout DURATION", "auth login --token-stdin", "inventory lot", "inventory image", "inventory reservation", "inventory conflict", "inventory adjust", "skill install", "version"},
		"docs/json-and-exit-codes.md": {`{"ok":true,"data":`, `{"ok":false,"error":`, "130", "409", "pagination", "integer grams", "Idempotency"},
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
	qualityRuns, qualityUses, err := jobSteps(quality)
	if err != nil {
		return err
	}
	nativeRuns, nativeUses, err := jobSteps(native)
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
	for _, required := range []string{"gofmt", "go vet ./...", "go test ./...", "go test ./... -race", "go generate ./internal/skill", "git diff --exit-code"} {
		if !containsRun(qualityRuns, required) {
			return fmt.Errorf("CI quality job missing %q", required)
		}
	}
	if !containsRun(nativeRuns, "go build -trimpath") || !containsRun(nativeRuns, "--json version") {
		return errors.New("native job must build and smoke the executable")
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
	permissions, err := mappingLookup(release, "permissions")
	if err != nil || !mappingEquals(permissions, map[string]string{"contents": "write", "id-token": "write", "attestations": "write"}) {
		return errors.New("release job permissions drifted")
	}
	runs, uses, err := jobSteps(release)
	if err != nil {
		return err
	}
	for _, required := range []string{"go test ./...", "go generate ./internal/skill", "git diff --exit-code", "scripts/build-release.sh", "checksums.txt"} {
		if !containsRun(runs, required) {
			return fmt.Errorf("release job missing %q", required)
		}
	}
	attestation, publisher := false, false
	for _, use := range uses {
		if !fullActionSHA.MatchString(use) {
			return fmt.Errorf("release action is not pinned: %s", use)
		}
		attestation = attestation || strings.HasPrefix(use, "actions/attest-build-provenance@")
		publisher = publisher || strings.HasPrefix(use, "softprops/action-gh-release@")
	}
	if !attestation || !publisher {
		return errors.New("release provenance or publication action is missing")
	}
	if !workflowHasGo123(document) {
		return errors.New("release must use Go 1.23.x")
	}
	return nil
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

func containsRun(runs []string, required string) bool {
	for _, run := range runs {
		if strings.Contains(run, required) {
			return true
		}
	}
	return false
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
	if strings.HasSuffix(path, ".zip") {
		reader, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		entries := make([]string, 0, len(reader.File))
		for _, file := range reader.File {
			if file.FileInfo().Mode()&os.ModeSymlink != 0 || strings.HasPrefix(file.Name, "/") || strings.Contains(file.Name, "../") {
				t.Fatalf("unsafe zip entry %q", file.Name)
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
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink || strings.HasPrefix(header.Name, "/") || strings.Contains(header.Name, "../") {
			t.Fatalf("unsafe tar entry %q", header.Name)
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

func assertSameStrings(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("archive entries:\n got %q\nwant %q", got, want)
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
