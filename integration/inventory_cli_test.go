package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	pinnedServerRef     = "4c0136fe98f6728f4bb94e416c5abe570e7f4831"
	maxCLIOutputBytes   = 2 << 20
	maxBrowserJSONBytes = 1 << 20
	cliCommandTimeout   = 45 * time.Second
	cliCommandWaitDelay = 2 * time.Second
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type liveConfig struct {
	binary           string
	baseURL          string
	adminEmail       string
	adminPassword    string
	adminNickname    string
	organization     string
	organizationSlug string
}

type commandRecord struct {
	Args     []string `json:"args"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}

type commandExecution struct {
	record   commandRecord
	err      error
	timedOut bool
	overflow bool
}

type boundedCapture struct {
	contents []byte
	limit    int
	overflow bool
}

func (capture *boundedCapture) Write(value []byte) (int, error) {
	remaining := capture.limit - len(capture.contents)
	if remaining > 0 {
		kept := len(value)
		if kept > remaining {
			kept = remaining
		}
		capture.contents = append(capture.contents, value[:kept]...)
	}
	if len(value) > remaining {
		capture.overflow = true
	}
	return len(value), nil
}

func (capture *boundedCapture) Bytes() []byte  { return capture.contents }
func (capture *boundedCapture) String() string { return string(capture.contents) }

type cliRunner struct {
	binary           string
	baseURL          string
	cwd              string
	env              []string
	commandTimeout   time.Duration
	commandWaitDelay time.Duration
	forbiddenToken   string
	records          []commandRecord
}

type authIdentity struct {
	User struct {
		Email    string `json:"email"`
		Nickname string `json:"nickname"`
	} `json:"user"`
	Organization struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"organization"`
	Role string `json:"role"`
}

type lot struct {
	LotID          string            `json:"lot_id"`
	Name           string            `json:"name"`
	OnHandGrams    int64             `json:"on_hand_grams"`
	ReservedGrams  int64             `json:"reserved_grams"`
	AvailableGrams int64             `json:"available_grams"`
	Images         []imageProjection `json:"images"`
}

type imageProjection struct {
	ImageID  string  `json:"image_id"`
	Caption  *string `json:"caption"`
	AltText  *string `json:"alt_text"`
	IsCover  bool    `json:"is_cover"`
	Position int64   `json:"position"`
}

type lotPage struct {
	Items []lot `json:"items"`
}

type ledgerPage struct {
	Items []struct {
		Operation               string `json:"operation"`
		OnHandDelta             int64  `json:"on_hand_delta"`
		ReservedDelta           int64  `json:"reserved_delta"`
		ResultingOnHandGrams    int64  `json:"resulting_on_hand_grams"`
		ResultingReservedGrams  int64  `json:"resulting_reserved_grams"`
		ResultingAvailableGrams int64  `json:"resulting_available_grams"`
	} `json:"items"`
}

type reservationPage struct {
	Items []struct {
		ClientReservationUUID string `json:"client_reservation_uuid"`
		State                 string `json:"state"`
		PlannedGrams          int64  `json:"planned_grams"`
		LotID                 string `json:"lot_id"`
	} `json:"items"`
}

type reservationMutation struct {
	Reservation struct {
		ClientReservationUUID string `json:"client_reservation_uuid"`
		State                 string `json:"state"`
		PlannedGrams          int64  `json:"planned_grams"`
	} `json:"reservation"`
	Balance struct {
		OnHandGrams    int64 `json:"on_hand_grams"`
		ReservedGrams  int64 `json:"reserved_grams"`
		AvailableGrams int64 `json:"available_grams"`
	} `json:"balance"`
}

func TestPinnedServerRef(t *testing.T) {
	contents, err := os.ReadFile("artisan-server.ref")
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != pinnedServerRef+"\n" || !fullSHA.MatchString(strings.TrimSuffix(string(contents), "\n")) {
		t.Fatalf("artisan-server.ref must contain exactly the pinned 40-hex commit plus newline")
	}
}

func TestValidateLoopbackBaseURL(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		valid bool
	}{
		{name: "IPv4 loopback", raw: "http://127.0.0.1", valid: true},
		{name: "IPv4 loopback with port", raw: "http://127.0.0.2:18080", valid: true},
		{name: "entire IPv4 loopback range", raw: "https://127.255.255.255:443", valid: true},
		{name: "IPv6 loopback", raw: "http://[::1]", valid: true},
		{name: "IPv6 loopback with port", raw: "https://[::1]:8443", valid: true},
		{name: "blank", raw: ""},
		{name: "whitespace", raw: " http://127.0.0.1"},
		{name: "hostname localhost", raw: "http://localhost"},
		{name: "hostname", raw: "http://example.com"},
		{name: "hostname suffix", raw: "http://localhost.evil.invalid"},
		{name: "IPv4 mapped loopback rejected", raw: "http://[::ffff:127.0.0.1]"},
		{name: "IPv4 mapped nonloopback", raw: "http://[::ffff:192.0.2.1]"},
		{name: "IPv6 zone", raw: "http://[::1%25lo0]"},
		{name: "expanded IPv6 alternate", raw: "http://[0:0:0:0:0:0:0:1]"},
		{name: "IPv6 uppercase alternate", raw: "http://[0:0:0:0:0:0:0:0A]"},
		{name: "integer IPv4", raw: "http://2130706433"},
		{name: "hex IPv4", raw: "http://0x7f000001"},
		{name: "octal IPv4", raw: "http://0177.0.0.1"},
		{name: "leading-zero IPv4", raw: "http://127.000.000.001"},
		{name: "nonloopback IPv4", raw: "http://126.255.255.255"},
		{name: "unspecified IPv4", raw: "http://0.0.0.0"},
		{name: "nonloopback IPv6", raw: "http://[::2]"},
		{name: "unspecified IPv6", raw: "http://[::]"},
		{name: "unsupported scheme", raw: "ftp://127.0.0.1"},
		{name: "credentials", raw: "http://user:pass@127.0.0.1"},
		{name: "trailing slash", raw: "http://127.0.0.1/"},
		{name: "path", raw: "http://127.0.0.1/api"},
		{name: "escaped path", raw: "http://127.0.0.1/%2e"},
		{name: "query", raw: "http://127.0.0.1?x=1"},
		{name: "empty query", raw: "http://127.0.0.1?"},
		{name: "fragment", raw: "http://127.0.0.1#fragment"},
		{name: "empty IPv4 port", raw: "http://127.0.0.1:"},
		{name: "empty IPv6 port", raw: "https://[::1]:"},
		{name: "zero port", raw: "http://127.0.0.1:0"},
		{name: "oversized port", raw: "http://127.0.0.1:65536"},
		{name: "malformed port", raw: "http://127.0.0.1:http"},
		{name: "newline injection", raw: "http://127.0.0.1\n.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateLoopbackBaseURL(test.raw)
			if test.valid && (err != nil || got != test.raw) {
				t.Fatalf("validateLoopbackBaseURL(%q) = (%q, %v), want exact accepted origin", test.raw, got, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("validateLoopbackBaseURL(%q) unexpectedly succeeded as %q", test.raw, got)
			}
		})
	}
}

func TestLoadLiveConfigAbsentAndPartialEnvironment(t *testing.T) {
	config, configured, err := loadLiveConfig(func(string) string { return "" })
	if err != nil || configured || config != (liveConfig{}) {
		t.Fatalf("absent live environment = (%+v, %v, %v), want zero, false, nil", config, configured, err)
	}
	_, configured, err = loadLiveConfig(func(name string) string {
		if name == "ARTISAN_CLI_BINARY" {
			return "/tmp/artisan"
		}
		return ""
	})
	if err == nil || !configured {
		t.Fatalf("partial live environment = configured %v, error %v; want true and error", configured, err)
	}
}

func TestCommandRecordNeverContainsTokenInput(t *testing.T) {
	token := "artisan-secret-token-for-test"
	record := newCommandRecord([]string{"--json", "auth", "login", "--token-stdin"}, 0, []byte(`{"ok":true}`), nil)
	serialized, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serialized, []byte(token)) || bytes.Contains(serialized, []byte(`"stdin"`)) {
		t.Fatalf("command record exposed stdin token material: %s", serialized)
	}
	if err := assertTokenAbsent(token, []commandRecord{record}, errors.New("safe failure")); err != nil {
		t.Fatal(err)
	}
	record.Stderr = token
	if err := assertTokenAbsent(token, []commandRecord{record}, nil); err == nil {
		t.Fatal("token scanner accepted a captured token")
	}
}

func TestBoundedCaptureStopsRetainingOverflow(t *testing.T) {
	capture := boundedCapture{limit: 4}
	if written, err := capture.Write([]byte("abcdef")); err != nil || written != 6 {
		t.Fatalf("Write = (%d, %v), want (6, nil)", written, err)
	}
	if string(capture.Bytes()) != "abcd" || !capture.overflow {
		t.Fatalf("bounded capture = (%q, %v), want retained prefix and overflow", capture.Bytes(), capture.overflow)
	}
	if written, err := capture.Write([]byte("secret-unscanned-tail")); err != nil || written != 21 {
		t.Fatalf("overflow Write = (%d, %v)", written, err)
	}
	if string(capture.Bytes()) != "abcd" {
		t.Fatalf("overflow retained additional bytes: %q", capture.Bytes())
	}
}

func TestDecodeExactlyOneJSONObject(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{name: "one object", input: `{"value":"ok"}`, valid: true},
		{name: "one object with whitespace", input: "  {\"value\":\"ok\"}\n\t", valid: true},
		{name: "empty"},
		{name: "null", input: `null`},
		{name: "array", input: `[]`},
		{name: "scalar", input: `true`},
		{name: "truncated", input: `{"value":`},
		{name: "two objects", input: `{"value":"one"}{"value":"two"}`},
		{name: "object and scalar", input: `{"value":"one"}true`},
		{name: "trailing junk", input: `{"value":"one"}junk`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var target struct {
				Value string `json:"value"`
			}
			err := decodeExactlyOneJSON([]byte(test.input), &target, false)
			if test.valid && (err != nil || target.Value != "ok") {
				t.Fatalf("decode = (%+v, %v), want one object", target, err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid JSON stream unexpectedly succeeded")
			}
		})
	}
}

func TestReadBoundedBrowserJSON(t *testing.T) {
	var target map[string]string
	if err := readBoundedJSON(strings.NewReader(`{"ok":"yes"}`), 32, "", &target); err != nil || target["ok"] != "yes" {
		t.Fatalf("bounded browser decode = (%v, %v)", target, err)
	}
	if err := readBoundedJSON(strings.NewReader(`{} {}`), 32, "", &target); err == nil {
		t.Fatal("browser decoder accepted two JSON objects")
	}
	if err := readBoundedJSON(strings.NewReader(strings.Repeat("x", 33)), 32, "", &target); !errors.Is(err, errBrowserResponseTooLarge) {
		t.Fatalf("browser overflow error = %v, want static size error", err)
	}
	token := "browser-secret-token"
	if err := readBoundedJSON(strings.NewReader(`{"error":"`+token+`"}`), 128, token, &target); !errors.Is(err, errBrowserTokenExposure) || strings.Contains(err.Error(), token) {
		t.Fatalf("browser token scan returned unsafe error %q", err)
	}
}

func TestTokenTreeScannerFindsChunkBoundaryAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	token := "boundary-secret-token"
	contents := append(bytes.Repeat([]byte{'x'}, (32<<10)-5), []byte(token)...)
	if err := os.WriteFile(filepath.Join(root, "state"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := assertTokenAbsentFromTrees(token, root); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("tree token scan error = %q, want static exposure error", err)
	}
	if err := os.Remove(filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := assertTokenAbsentFromTrees(token, root); err == nil {
		t.Fatal("tree token scan accepted a symlink")
	}
}

func TestBrowserClientDisablesProxiesAndRedirects(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := newBrowserClient(jar)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("browser client transport did not disable proxies")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestResolveTrustedExecutableRejectsFinalSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "artisan-real")
	if err := os.WriteFile(target, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveTrustedExecutable(target)
	if err != nil || resolved != target {
		t.Fatalf("regular executable = (%q, %v), want exact resolved path", resolved, err)
	}
	link := filepath.Join(root, "artisan-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTrustedExecutable(link); err == nil {
		t.Fatal("final-component executable symlink unexpectedly accepted")
	}
}

func TestCLIRunnerUsesIsolatedCWDAndBoundsChildPipeWait(t *testing.T) {
	root := t.TempDir()
	runDirectory := filepath.Join(root, "run")
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	cwdScript := filepath.Join(root, "cwd-command")
	if err := os.WriteFile(cwdScript, []byte("#!/bin/sh\nprintf '{\"ok\":true,\"data\":{\"cwd\":\"%s\"}}\\n' \"$PWD\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := cliRunner{binary: cwdScript, baseURL: "http://127.0.0.1", cwd: runDirectory, env: []string{"PATH=" + os.Getenv("PATH")}}
	execution := runner.execute("")
	if execution.err != nil || execution.overflow || execution.timedOut || !strings.Contains(execution.record.Stdout, `"cwd":"`+runDirectory+`"`) {
		t.Fatalf("isolated cwd execution = %+v", execution)
	}

	holderScript := filepath.Join(root, "pipe-holder")
	if err := os.WriteFile(holderScript, []byte("#!/bin/sh\n(sleep 5) &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner = cliRunner{
		binary: holderScript, baseURL: "http://127.0.0.1", cwd: runDirectory,
		env: []string{"PATH=" + os.Getenv("PATH")}, commandTimeout: 50 * time.Millisecond, commandWaitDelay: 50 * time.Millisecond,
	}
	started := time.Now()
	execution = runner.execute("")
	if !execution.timedOut || time.Since(started) > time.Second {
		t.Fatalf("child pipe holder execution = timedOut %v duration %s, want bounded", execution.timedOut, time.Since(started))
	}
}

func TestWorkflowActionPinGuardMutations(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "integration.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	insert := func(snippet string) string {
		t.Helper()
		return strings.Replace(workflow, "    steps:\n", "    steps:\n"+snippet, 1)
	}
	pinnedTarget := "actions/cache@ABCDEF0123456789abcdef0123456789ABCDEF01"
	pinned := "      - uses: " + pinnedTarget + " # audited\n"
	positiveMutations := map[string]string{
		"nameless plain":         pinned,
		"nameless double quoted": "      - uses: \"" + pinnedTarget + "\" # audited\n",
		"named single quoted":    "      - name: quoted pinned action\n        uses: '" + pinnedTarget + "' # audited\n",
	}
	for name, snippet := range positiveMutations {
		t.Run(name, func(t *testing.T) {
			if err := validateWorkflowActionPins(insert(snippet), 4); err != nil {
				t.Fatalf("valid pinned action rejected: %v", err)
			}
		})
	}
	mutations := []struct {
		name     string
		snippet  string
		wantUses int
	}{
		{name: "nameless branch", snippet: "      - uses: actions/cache@main\n", wantUses: 4},
		{name: "nameless tag", snippet: "      - uses: actions/cache@v4\n", wantUses: 4},
		{name: "named unpinned", snippet: "      - name: bypass\n        uses: actions/cache@main\n", wantUses: 4},
		{name: "double quoted unpinned", snippet: "      - uses: \"actions/cache@main\"\n", wantUses: 4},
		{name: "single quoted unpinned", snippet: "      - uses: 'actions/cache@v4'\n", wantUses: 4},
		{name: "missing at", snippet: "      - uses: actions/cache\n", wantUses: 4},
		{name: "malformed at", snippet: "      - uses: actions/cache@@ABCDEF0123456789abcdef0123456789ABCDEF01\n", wantUses: 4},
		{name: "expression", snippet: "      - uses: ${{ github.event.action }}\n", wantUses: 4},
		{name: "local action", snippet: "      - uses: ./local-action\n", wantUses: 4},
		{name: "docker action", snippet: "      - uses: docker://alpine:3\n", wantUses: 4},
		{name: "mapping value", snippet: "      - uses: {action: " + pinnedTarget + "}\n", wantUses: 4},
		{name: "sequence value", snippet: "      - uses: [" + pinnedTarget + "]\n", wantUses: 4},
		{name: "alias value", snippet: "      - name: alias seed\n        env:\n          ACTION: &pinned " + pinnedTarget + "\n      - uses: *pinned\n", wantUses: 4},
		{name: "exact count", snippet: pinned, wantUses: 3},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if err := validateWorkflowActionPins(insert(mutation.snippet), mutation.wantUses); err == nil {
				t.Fatal("workflow action mutation bypassed the full-SHA guard")
			}
		})
	}
	insideRunBlock := insert("      - name: text is not an action\n        run: |\n          uses: actions/cache@main\n")
	if err := validateWorkflowActionPins(insideRunBlock, 3); err != nil {
		t.Fatalf("uses text inside a run block was treated as an action: %v", err)
	}
	if err := validateWorkflowActionPins(workflow+"\n# trailing audit comment\n   \n\n", 3); err != nil {
		t.Fatalf("trailing comments and whitespace rejected: %v", err)
	}
	secondDocuments := map[string]string{
		"unpinned uses": "steps:\n  - uses: actions/cache@main\n",
		"pinned uses":   "steps:\n  - uses: " + pinnedTarget + "\n",
		"inert mapping": "review: inert\n",
	}
	for name, secondDocument := range secondDocuments {
		t.Run("second document "+name, func(t *testing.T) {
			if err := validateWorkflowActionPins(workflow+"\n---\n"+secondDocument, 3); err == nil {
				t.Fatal("second YAML document bypassed the single-document workflow guard")
			}
		})
	}
	for name, emptyWorkflow := range map[string]string{"empty stream": "", "empty document": "---\n# comment only\n"} {
		t.Run(name, func(t *testing.T) {
			if err := validateWorkflowActionPins(emptyWorkflow, 0); err == nil {
				t.Fatal("empty YAML workflow bypassed the nonempty-document guard")
			}
		})
	}
}

func TestIntegrationWorkflowContract(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "integration.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"permissions:\n  contents: read", pinnedServerRef, "repository: fr3akX/artisan-server",
		"integration/artisan-server.ref", "CGO_ENABLED: \"0\"", "go-version: 1.23.x",
		"ARTISAN_INTEGRATION_BASE_URL: http://127.0.0.1:18080",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("workflow missing %q", required)
		}
	}
	if strings.Contains(text, "workflow_dispatch") || strings.Contains(text, "docker compose") || strings.Contains(text, "docker-compose") {
		t.Error("workflow must not expose dispatch targets or bypass the guarded Compose wrapper")
	}
	if err := validateWorkflowActionPins(text, 3); err != nil {
		t.Fatal(err)
	}
	wrapperCount := 0
	for lineNumber, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "./scripts/e2e_compose.py") {
			continue
		}
		wrapperCount++
		if !regexp.MustCompile(`^\s*timeout --signal=TERM --kill-after=[^ ]+ [^ ]+ \./scripts/e2e_compose\.py `).MatchString(line) {
			t.Errorf("Compose wrapper line %d is not directly timeout-bounded: %q", lineNumber+1, line)
		}
	}
	if wrapperCount != 6 {
		t.Errorf("workflow Compose wrapper count = %d, want config, start down/up, bootstrap, logs, teardown", wrapperCount)
	}
	for index, invocation := range composeInvocations(text) {
		if !strings.Contains(invocation, `-f "$PWD/compose.yaml" -f "$PWD/compose.e2e.yaml"`) || strings.Count(invocation, " -f ") != 2 {
			t.Errorf("workflow Compose invocation %d does not use exactly the two absolute pinned files: %q", index, invocation)
		}
	}

	start := workflowStep(t, text, "Start disposable Artisan Server")
	assertGuardedComposeStep(t, start, false, "down -v --remove-orphans", "up -d --build")
	teardown := workflowStep(t, text, "Tear down disposable stack")
	assertGuardedComposeStep(t, teardown, true, "down -v --remove-orphans")
	logs := workflowStep(t, text, "Print bounded server logs on failure")
	for _, required := range []string{"timeout --signal=TERM --kill-after=", "logs --no-color --tail 200", "head -c 65536"} {
		if !strings.Contains(logs, required) {
			t.Errorf("failure log step missing %q", required)
		}
	}
}

func validateWorkflowActionPins(workflow string, expectedCount int) error {
	decoder := yaml.NewDecoder(strings.NewReader(workflow))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return errors.New("integration workflow must contain one nonempty YAML document")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || isEmptyYAMLDocument(document.Content[0]) {
		return errors.New("integration workflow must contain one nonempty YAML document")
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return errors.New("integration workflow was not valid YAML")
		}
		return errors.New("integration workflow must contain exactly one YAML document")
	}
	pinnedUse := regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9A-Fa-f]{40}$`)
	usesCount := 0
	visited := make(map[*yaml.Node]bool)
	var inspect func(*yaml.Node) error
	inspect = func(node *yaml.Node) error {
		if node == nil || visited[node] {
			return nil
		}
		visited[node] = true
		switch node.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, child := range node.Content {
				if err := inspect(child); err != nil {
					return err
				}
			}
		case yaml.MappingNode:
			for index := 0; index+1 < len(node.Content); index += 2 {
				key, value := node.Content[index], node.Content[index+1]
				if key.Kind == yaml.ScalarNode && key.Value == "uses" {
					usesCount++
					if value.Kind != yaml.ScalarNode || !pinnedUse.MatchString(value.Value) {
						return fmt.Errorf("workflow uses line %d is not exactly owner/repo@full-SHA", value.Line)
					}
				}
				if err := inspect(value); err != nil {
					return err
				}
			}
		case yaml.AliasNode:
			return inspect(node.Alias)
		}
		return nil
	}
	if err := inspect(&document); err != nil {
		return err
	}
	if usesCount != expectedCount {
		return fmt.Errorf("workflow has %d action uses, want exactly %d", usesCount, expectedCount)
	}
	return nil
}

func isEmptyYAMLDocument(root *yaml.Node) bool {
	return root == nil || root.Kind == 0 || (root.Kind == yaml.ScalarNode && root.Tag == "!!null")
}

func workflowStep(t *testing.T, workflow, name string) string {
	t.Helper()
	marker := "      - name: " + name + "\n"
	start := strings.Index(workflow, marker)
	if start < 0 {
		t.Fatalf("workflow step %q not found", name)
	}
	rest := workflow[start+len(marker):]
	if end := strings.Index(rest, "\n      - name: "); end >= 0 {
		rest = rest[:end]
	}
	return marker + rest
}

func assertGuardedComposeStep(t *testing.T, step string, always bool, operations ...string) {
	t.Helper()
	if (strings.Contains(step, "        if: always()")) != always {
		t.Errorf("step always condition = %v, want %v", strings.Contains(step, "        if: always()"), always)
	}
	if !strings.Contains(step, "working-directory: artisan-server") {
		t.Error("guarded Compose step does not use the pinned server directory")
	}
	invocations := composeInvocations(step)
	if len(invocations) != len(operations) {
		t.Fatalf("guarded Compose invocation count = %d, want %d", len(invocations), len(operations))
	}
	for index, invocation := range invocations {
		for _, required := range []string{
			"timeout --signal=TERM --kill-after=",
			"./scripts/e2e_compose.py --project \"$ARTISAN_SERVER_E2E_PROJECT_NAME\"",
			"-f \"$PWD/compose.yaml\" -f \"$PWD/compose.e2e.yaml\"",
			operations[index],
		} {
			if !strings.Contains(invocation, required) {
				t.Errorf("guarded Compose invocation %d missing %q: %q", index, required, invocation)
			}
		}
		if strings.Count(invocation, " -f ") != 2 {
			t.Errorf("guarded Compose invocation %d does not contain exactly two files: %q", index, invocation)
		}
	}
}

func composeInvocations(step string) []string {
	lines := strings.Split(step, "\n")
	var invocations []string
	for index := 0; index < len(lines); index++ {
		if !strings.Contains(lines[index], "./scripts/e2e_compose.py") {
			continue
		}
		invocation := strings.TrimSpace(lines[index])
		for strings.HasSuffix(invocation, "\\") && index+1 < len(lines) {
			index++
			invocation = strings.TrimSuffix(invocation, "\\") + " " + strings.TrimSpace(lines[index])
		}
		invocations = append(invocations, invocation)
	}
	return invocations
}

func TestInventoryCLIAgainstArtisanServer(t *testing.T) {
	config, configured, err := loadLiveConfig(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !configured {
		t.Skip("live integration environment is not configured")
	}
	binary, err := resolveTrustedExecutable(config.binary)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	paths := make(map[string]string)
	for _, directory := range []string{"home", "config", "state", "tmp", "run"} {
		paths[directory] = filepath.Join(root, directory)
		if err := os.Mkdir(paths[directory], 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := cliRunner{
		binary:  binary,
		baseURL: config.baseURL,
		cwd:     paths["run"],
		env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + paths["home"],
			"XDG_CONFIG_HOME=" + paths["config"],
			"XDG_STATE_HOME=" + paths["state"],
			"TMPDIR=" + paths["tmp"],
		},
	}

	httpClient, csrf, token, credentialID := issueCredential(t, config)
	runner.forbiddenToken = token
	defer revokeCredential(t, httpClient, config.baseURL, csrf, credentialID, token)
	defer func() {
		if err := assertTokenAbsent(token, runner.records, nil); err != nil {
			t.Error(err)
		}
		if err := assertTokenAbsentFromTrees(token, paths["config"], paths["state"], paths["home"], paths["tmp"], paths["run"]); err != nil {
			t.Error(err)
		}
	}()
	needsLogout := true
	defer func() {
		if needsLogout {
			if err := runner.cleanupLogout(); err != nil {
				t.Error(err)
			}
		}
	}()

	var identity authIdentity
	runner.runJSON(t, token+"\n", &identity, "auth", "login", "--token-stdin")
	assertExpectedIdentity(t, identity, config)
	var status authIdentity
	runner.runJSON(t, "", &status, "auth", "status")
	assertExpectedIdentity(t, status, config)

	runID := randomHex(t, 12)
	lotName := "CLI integration " + runID
	var created lot
	runner.runJSON(t, "", &created,
		"inventory", "lot", "create",
		"--name", lotName, "--origin", "Ethiopia", "--varietal", "Heirloom",
		"--processing-method", "washed", "--opening-grams", "5000",
		"--opening-reason", "Disposable CLI integration opening balance",
		"--opening-reference", "opening-"+runID, "--idempotency-key", "cli-"+runID+"-create",
	)
	assertLotBalance(t, created, 5000, 0, 5000)
	if !fullSHA.MatchString(pinnedServerRef) || len(created.LotID) != 32 {
		t.Fatalf("created invalid lot ID %q", created.LotID)
	}

	var shown lot
	runner.runJSON(t, "", &shown, "inventory", "lot", "show", created.LotID)
	assertLotBalance(t, shown, 5000, 0, 5000)
	var listed lotPage
	runner.runJSON(t, "", &listed, "inventory", "lot", "list", "--q", lotName, "--all")
	if len(listed.Items) != 1 || listed.Items[0].LotID != created.LotID {
		t.Fatalf("lot list did not resolve the unique created lot: %+v", listed.Items)
	}

	occurredAt := time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05.000000Z")
	var adjusted lot
	runner.runJSON(t, "", &adjusted,
		"inventory", "adjust", created.LotID, "--grams", "750", "--reason", "Disposable CLI integration adjustment",
		"--reference", "adjust-"+runID, "--occurred-at", occurredAt, "--idempotency-key", "cli-"+runID+"-adjust", "--yes",
	)
	assertLotBalance(t, adjusted, 5750, 0, 5750)

	imagePath := filepath.Join(paths["run"], "fixture.png")
	writePNG(t, imagePath)
	var withImage lot
	runner.runJSON(t, "", &withImage,
		"inventory", "image", "add", "--caption", "0=Disposable integration image", "--alt-text", "0=Coffee sample",
		"--cover", "0", "--idempotency-key", "cli-"+runID+"-image", created.LotID, imagePath,
	)
	if len(withImage.Images) != 1 || !withImage.Images[0].IsCover {
		t.Fatalf("image add result = %+v, want one cover image", withImage.Images)
	}
	downloadPath := filepath.Join(paths["run"], "download.webp")
	var downloaded struct {
		Path    string `json:"path"`
		Variant string `json:"variant"`
		Bytes   int64  `json:"bytes"`
	}
	runner.runJSON(t, "", &downloaded,
		"inventory", "image", "download", "--variant", "display", created.LotID, withImage.Images[0].ImageID, downloadPath,
	)
	downloadBytes, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if downloaded.Path != downloadPath || downloaded.Bytes != int64(len(downloadBytes)) || downloaded.Variant != "display" || len(downloadBytes) < 12 || string(downloadBytes[:4]) != "RIFF" || string(downloadBytes[8:12]) != "WEBP" {
		t.Fatalf("downloaded image is not the exact reported WebP result")
	}

	reservationUUID := randomHex(t, 16)
	clientUUID := randomHex(t, 16)
	roastUUID := randomHex(t, 16)
	reservedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	var reservation reservationMutation
	runner.runJSON(t, "", &reservation,
		"inventory", "reservation", "create", "--client-reservation-uuid", reservationUUID,
		"--client-instance-uuid", clientUUID, "--roast-uuid", roastUUID, "--lot-id", created.LotID,
		"--planned-grams", "1000", "--occurred-at", reservedAt, "--idempotency-key", "cli-"+runID+"-reserve",
	)
	if reservation.Reservation.ClientReservationUUID != reservationUUID || reservation.Reservation.State != "reserved" || reservation.Reservation.PlannedGrams != 1000 {
		t.Fatalf("reservation mutation = %+v", reservation.Reservation)
	}
	if reservation.Balance.OnHandGrams != 5750 || reservation.Balance.ReservedGrams != 1000 || reservation.Balance.AvailableGrams != 4750 {
		t.Fatalf("reservation balance = %+v", reservation.Balance)
	}

	var authoritative lot
	runner.runJSON(t, "", &authoritative, "inventory", "lot", "show", created.LotID)
	assertLotBalance(t, authoritative, 5750, 1000, 4750)
	if authoritative.Name != lotName || len(authoritative.Images) != 1 {
		t.Fatalf("authoritative lot content = name %q images %+v", authoritative.Name, authoritative.Images)
	}
	authoritativeImage := authoritative.Images[0]
	if authoritativeImage.ImageID != withImage.Images[0].ImageID || !authoritativeImage.IsCover || authoritativeImage.Position != 0 || authoritativeImage.Caption == nil || *authoritativeImage.Caption != "Disposable integration image" || authoritativeImage.AltText == nil || *authoritativeImage.AltText != "Coffee sample" {
		t.Fatalf("authoritative image metadata = %+v", authoritativeImage)
	}
	var ledger ledgerPage
	runner.runJSON(t, "", &ledger, "inventory", "lot", "ledger", created.LotID, "--all")
	assertLedger(t, ledger)
	var reservations reservationPage
	runner.runJSON(t, "", &reservations, "inventory", "lot", "reservations", created.LotID, "--all")
	if len(reservations.Items) != 1 || reservations.Items[0].ClientReservationUUID != reservationUUID || reservations.Items[0].State != "reserved" || reservations.Items[0].PlannedGrams != 1000 || reservations.Items[0].LotID != created.LotID {
		t.Fatalf("authoritative reservations = %+v", reservations.Items)
	}

	var logout struct {
		LoggedOut bool `json:"logged_out"`
	}
	runner.runJSON(t, "", &logout, "auth", "logout")
	if !logout.LoggedOut {
		t.Fatal("auth logout did not report success")
	}
	needsLogout = false
	if err := assertTokenAbsent(token, runner.records, nil); err != nil {
		t.Fatal(err)
	}
}

func loadLiveConfig(getenv func(string) string) (liveConfig, bool, error) {
	names := []string{
		"ARTISAN_CLI_BINARY", "ARTISAN_INTEGRATION_BASE_URL", "ARTISAN_INTEGRATION_ADMIN_EMAIL",
		"ARTISAN_INTEGRATION_ADMIN_PASSWORD", "ARTISAN_INTEGRATION_ADMIN_NICKNAME",
		"ARTISAN_INTEGRATION_ADMIN_ORGANIZATION", "ARTISAN_INTEGRATION_ADMIN_ORGANIZATION_SLUG",
	}
	values := make(map[string]string, len(names))
	present := 0
	for _, name := range names {
		values[name] = getenv(name)
		if values[name] != "" {
			present++
		}
	}
	if present == 0 {
		return liveConfig{}, false, nil
	}
	if present != len(names) {
		return liveConfig{}, true, errors.New("live integration requires every explicit ARTISAN_CLI_BINARY and ARTISAN_INTEGRATION_* value")
	}
	baseURL, err := validateLoopbackBaseURL(values["ARTISAN_INTEGRATION_BASE_URL"])
	if err != nil {
		return liveConfig{}, true, err
	}
	for _, name := range names[2:] {
		if strings.TrimSpace(values[name]) != values[name] || strings.ContainsAny(values[name], "\r\n\x00") {
			return liveConfig{}, true, errors.New("live integration bootstrap values must be nonblank single-line values without surrounding whitespace")
		}
	}
	return liveConfig{
		binary: values[names[0]], baseURL: baseURL, adminEmail: values[names[2]], adminPassword: values[names[3]],
		adminNickname: values[names[4]], organization: values[names[5]], organizationSlug: values[names[6]],
	}, true, nil
}

func validateLoopbackBaseURL(raw string) (string, error) {
	const invalidOrigin = "integration base URL must be a canonical numeric loopback HTTP(S) origin"
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New(invalidOrigin)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path != "" {
		return "", errors.New(invalidOrigin)
	}
	hostname := parsed.Hostname()
	ip := net.ParseIP(hostname)
	if ip == nil || !ip.IsLoopback() || strings.Contains(hostname, "%") {
		return "", errors.New(invalidOrigin)
	}
	// Reject IPv4-mapped IPv6 and noncanonical alternate spellings so URL and
	// transport layers cannot disagree about the exact destination literal.
	if strings.Contains(hostname, ":") && ip.To4() != nil {
		return "", errors.New(invalidOrigin)
	}
	if ip.String() != hostname {
		return "", errors.New(invalidOrigin)
	}
	port := parsed.Port()
	if strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New(invalidOrigin)
	}
	if port != "" {
		value, portErr := strconv.Atoi(port)
		if portErr != nil || value < 1 || value > 65535 {
			return "", errors.New(invalidOrigin)
		}
	}
	expectedHost := hostname
	if strings.Contains(hostname, ":") {
		expectedHost = "[" + hostname + "]"
	}
	if port != "" {
		expectedHost += ":" + port
	}
	if parsed.Host != expectedHost || parsed.String() != raw {
		return "", errors.New(invalidOrigin)
	}
	return raw, nil
}

func resolveTrustedExecutable(raw string) (string, error) {
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", errors.New("ARTISAN_CLI_BINARY must resolve to an absolute path")
	}
	absolute = filepath.Clean(absolute)
	linkInfo, err := os.Lstat(absolute)
	if err != nil {
		return "", errors.New("ARTISAN_CLI_BINARY must name an existing executable regular file")
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("ARTISAN_CLI_BINARY final path must not be a symbolic link")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("ARTISAN_CLI_BINARY could not be resolved")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", errors.New("ARTISAN_CLI_BINARY must name an existing executable regular file")
	}
	return resolved, nil
}

func newCommandRecord(args []string, exitCode int, stdout, stderr []byte) commandRecord {
	return commandRecord{Args: append([]string(nil), args...), ExitCode: exitCode, Stdout: string(stdout), Stderr: string(stderr)}
}

func decodeExactlyOneJSON(contents []byte, target any, disallowUnknown bool) error {
	trimmed := bytes.TrimSpace(contents)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("response was not one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if disallowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return errors.New("response was not one JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("response contained data after its JSON object")
	}
	return nil
}

func assertTokenAbsent(token string, records []commandRecord, commandErr error) error {
	if token == "" {
		return errors.New("issued token was blank")
	}
	for index, record := range records {
		serialized, err := json.Marshal(record)
		if err != nil {
			return errors.New("could not inspect captured command record")
		}
		if bytes.Contains(serialized, []byte(token)) {
			return fmt.Errorf("issued token appeared in captured command record %d", index)
		}
	}
	if commandErr != nil && strings.Contains(commandErr.Error(), token) {
		return errors.New("issued token appeared in a command error representation")
	}
	return nil
}

func (runner *cliRunner) execute(stdin string, commandArgs ...string) commandExecution {
	args := append([]string{"--json", "--server", runner.baseURL}, commandArgs...)
	timeout := runner.commandTimeout
	if timeout == 0 {
		timeout = cliCommandTimeout
	}
	waitDelay := runner.commandWaitDelay
	if waitDelay == 0 {
		waitDelay = cliCommandWaitDelay
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, runner.binary, args...)
	command.Dir = runner.cwd
	command.Env = append([]string(nil), runner.env...)
	command.Stdin = strings.NewReader(stdin)
	command.WaitDelay = waitDelay
	stdout := boundedCapture{limit: maxCLIOutputBytes}
	stderr := boundedCapture{limit: maxCLIOutputBytes}
	command.Stdout = &stdout
	command.Stderr = &stderr
	tree, treeErr := prepareProcessTree(command)
	var err error
	var timedOut bool
	if treeErr != nil {
		err = errors.New("CLI process-tree containment setup failed")
	} else {
		startErr := command.Start()
		if startErr != nil {
			err = startErr
		} else {
			containErr := tree.afterStart(command.Process)
			if containErr != nil {
				_ = command.Process.Kill()
			}
			waitErr := command.Wait()
			timedOut = waitErr != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
			if containErr != nil {
				err = errors.New("CLI process-tree containment failed")
			} else {
				err = waitErr
			}
		}
		if closeErr := tree.close(waitDelay); closeErr != nil {
			err = errors.New("CLI process-tree cleanup failed")
		}
	}
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	record := newCommandRecord(args, exitCode, stdout.Bytes(), stderr.Bytes())
	runner.records = append(runner.records, record)
	return commandExecution{record: record, err: err, timedOut: timedOut, overflow: stdout.overflow || stderr.overflow}
}

func (runner *cliRunner) runJSON(t *testing.T, stdin string, target any, commandArgs ...string) {
	t.Helper()
	execution := runner.execute(stdin, commandArgs...)
	commandIndex := len(runner.records) - 1
	if runner.forbiddenToken != "" {
		if scanErr := assertTokenAbsent(runner.forbiddenToken, runner.records, execution.err); scanErr != nil {
			t.Fatal(scanErr)
		}
	}
	if execution.overflow {
		t.Fatalf("CLI command %d exceeded the bounded output limit", commandIndex)
	}
	if execution.timedOut {
		t.Fatalf("CLI command %d exceeded its bounded timeout", commandIndex)
	}
	if execution.err != nil {
		t.Fatalf("CLI command %d exited with status %d; safe stdout=%q stderr=%q", commandIndex, execution.record.ExitCode, bounded(execution.record.Stdout), bounded(execution.record.Stderr))
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if decodeErr := decodeExactlyOneJSON([]byte(execution.record.Stdout), &envelope, true); decodeErr != nil || !envelope.OK || len(envelope.Data) == 0 {
		t.Fatalf("CLI command %d returned an invalid single success envelope", commandIndex)
	}
	if decodeErr := decodeExactlyOneJSON(envelope.Data, target, false); decodeErr != nil {
		t.Fatalf("CLI command %d returned unexpected structured data", commandIndex)
	}
}

func (runner *cliRunner) cleanupLogout() error {
	execution := runner.execute("", "auth", "logout")
	if runner.forbiddenToken != "" {
		if err := assertTokenAbsent(runner.forbiddenToken, runner.records, execution.err); err != nil {
			return err
		}
	}
	if execution.overflow {
		return errors.New("cleanup logout exceeded the bounded output limit")
	}
	if execution.timedOut {
		return errors.New("cleanup logout exceeded its bounded timeout")
	}
	if execution.err != nil {
		return errors.New("cleanup logout failed")
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			LoggedOut bool `json:"logged_out"`
		} `json:"data"`
	}
	if err := decodeExactlyOneJSON([]byte(execution.record.Stdout), &envelope, true); err != nil || !envelope.OK || !envelope.Data.LoggedOut {
		return errors.New("cleanup logout returned an invalid response")
	}
	return nil
}

func newBrowserClient(jar http.CookieJar) *http.Client {
	return &http.Client{
		Jar:           jar,
		Timeout:       20 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func issueCredential(t *testing.T, config liveConfig) (*http.Client, string, string, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := newBrowserClient(jar)
	var csrfResponse struct {
		CSRFToken string `json:"csrf_token"`
	}
	doJSON(t, client, http.MethodGet, config.baseURL+"/api/v1/session/csrf", "", nil, &csrfResponse, http.StatusOK, "")
	if csrfResponse.CSRFToken == "" {
		t.Fatal("browser CSRF endpoint returned an empty token")
	}
	login := map[string]string{"email": config.adminEmail, "password": config.adminPassword, "organization": config.organizationSlug}
	doJSON(t, client, http.MethodPost, config.baseURL+"/api/v1/session/login", csrfResponse.CSRFToken, login, &map[string]any{}, http.StatusOK, "")
	csrf := cookieValue(t, jar, config.baseURL, "artisan_server_csrf")
	issued := struct {
		Token      string `json:"token"`
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}{}
	doJSON(t, client, http.MethodPost, config.baseURL+"/api/v1/credentials", csrf, map[string]string{"name": "CLI integration " + randomHex(t, 8)}, &issued, http.StatusCreated, "")
	if strings.TrimSpace(issued.Token) == "" || strings.ContainsAny(issued.Token, "\r\n") || issued.Credential.ID == "" {
		if issued.Credential.ID != "" {
			revokeCredential(t, client, config.baseURL, csrf, issued.Credential.ID, issued.Token)
		}
		t.Fatal("credential issue response was incomplete")
	}
	return client, csrf, issued.Token, issued.Credential.ID
}

func cookieValue(t *testing.T, jar http.CookieJar, baseURL, name string) string {
	t.Helper()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal("could not inspect browser cookies")
	}
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == name && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatalf("browser session did not provide %s", name)
	return ""
}

var (
	errBrowserResponseTooLarge = errors.New("browser response exceeded the bounded size limit")
	errBrowserTokenExposure    = errors.New("issued token appeared in a browser response")
)

func readBoundedJSON(body io.Reader, limit int64, forbiddenToken string, target any) error {
	contents, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return errors.New("browser response could not be read")
	}
	if int64(len(contents)) > limit {
		return errBrowserResponseTooLarge
	}
	if forbiddenToken != "" && bytes.Contains(contents, []byte(forbiddenToken)) {
		return errBrowserTokenExposure
	}
	if err := decodeExactlyOneJSON(contents, target, false); err != nil {
		return errors.New("browser response was not exactly one JSON object")
	}
	return nil
}

func revokeCredential(t *testing.T, client *http.Client, baseURL, csrf, credentialID, forbiddenToken string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/credentials/"+url.PathEscape(credentialID), nil)
	if err != nil {
		t.Error("could not construct credential cleanup request")
		return
	}
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Error("credential cleanup request failed")
		return
	}
	defer response.Body.Close()
	contents, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if readErr != nil {
		t.Error("credential cleanup response could not be read")
		return
	}
	if forbiddenToken != "" && bytes.Contains(contents, []byte(forbiddenToken)) {
		t.Error(errBrowserTokenExposure)
		return
	}
	if len(contents) > 64<<10 {
		t.Error("credential cleanup response exceeded the bounded size limit")
		return
	}
	if response.StatusCode != http.StatusNoContent {
		t.Errorf("credential cleanup returned HTTP %d", response.StatusCode)
	}
}

func doJSON(t *testing.T, client *http.Client, method, target, csrf string, payload, responseTarget any, expectedStatus int, forbiddenToken string) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal("could not encode browser request")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal("could not construct browser request")
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("browser API request failed")
	}
	defer response.Body.Close()
	var decoded json.RawMessage
	if err := readBoundedJSON(response.Body, maxBrowserJSONBytes, forbiddenToken, &decoded); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		t.Fatal("browser API redirect was rejected")
	}
	if response.StatusCode != expectedStatus {
		t.Fatalf("browser API returned HTTP %d, want %d", response.StatusCode, expectedStatus)
	}
	if err := decodeExactlyOneJSON(decoded, responseTarget, false); err != nil {
		t.Fatal("browser API returned invalid JSON object")
	}
}

func randomHex(t *testing.T, bytesCount int) string {
	t.Helper()
	value := make([]byte, bytesCount)
	if _, err := rand.Read(value); err != nil {
		t.Fatal("could not generate a unique integration identifier")
	}
	return hex.EncodeToString(value)
}

func writePNG(t *testing.T, path string) {
	t.Helper()
	fixture := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	fixture.Set(0, 0, color.NRGBA{R: 80, G: 40, B: 20, A: 255})
	fixture.Set(1, 0, color.NRGBA{R: 120, G: 70, B: 30, A: 255})
	fixture.Set(0, 1, color.NRGBA{R: 30, G: 90, B: 40, A: 255})
	fixture.Set(1, 1, color.NRGBA{R: 180, G: 140, B: 90, A: 255})
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, fixture); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertExpectedIdentity(t *testing.T, identity authIdentity, config liveConfig) {
	t.Helper()
	if identity.User.Email != config.adminEmail || identity.User.Nickname != config.adminNickname || identity.Organization.Name != config.organization || identity.Organization.Slug != config.organizationSlug || identity.Role != "admin" {
		t.Fatalf("authenticated identity = user %q <%s>, organization %q (%s), role %q; want configured administrator", identity.User.Nickname, identity.User.Email, identity.Organization.Name, identity.Organization.Slug, identity.Role)
	}
}

func assertLotBalance(t *testing.T, value lot, onHand, reserved, available int64) {
	t.Helper()
	if value.OnHandGrams != onHand || value.ReservedGrams != reserved || value.AvailableGrams != available {
		t.Fatalf("lot balance = (%d, %d, %d), want (%d, %d, %d)", value.OnHandGrams, value.ReservedGrams, value.AvailableGrams, onHand, reserved, available)
	}
}

func assertLedger(t *testing.T, page ledgerPage) {
	t.Helper()
	if len(page.Items) != 3 {
		t.Fatalf("authoritative ledger contains %d entries, want 3", len(page.Items))
	}
	type transition struct {
		operation                                               string
		onHandDelta, reservedDelta, onHand, reserved, available int64
	}
	want := map[string]transition{
		"opening_balance":   {"opening_balance", 5000, 0, 5000, 0, 5000},
		"manual_adjustment": {"manual_adjustment", 750, 0, 5750, 0, 5750},
		"reservation":       {"reservation", 0, 1000, 5750, 1000, 4750},
	}
	for _, item := range page.Items {
		expected, ok := want[item.Operation]
		if !ok || item.OnHandDelta != expected.onHandDelta || item.ReservedDelta != expected.reservedDelta || item.ResultingOnHandGrams != expected.onHand || item.ResultingReservedGrams != expected.reserved || item.ResultingAvailableGrams != expected.available {
			t.Fatalf("unexpected authoritative ledger entry: %+v", item)
		}
		delete(want, item.Operation)
	}
	if len(want) != 0 {
		t.Fatalf("authoritative ledger is missing transitions: %+v", want)
	}
}

func assertTokenAbsentFromTrees(token string, roots ...string) error {
	if token == "" {
		return errors.New("issued token was blank")
	}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return errors.New("isolated CLI tree contained a non-regular file")
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			found, err := readerContains(file, []byte(token))
			if err != nil {
				return err
			}
			if found {
				return errors.New("issued token remained in an isolated CLI tree after cleanup")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func readerContains(reader io.Reader, needle []byte) (bool, error) {
	buffer := make([]byte, 32<<10)
	carry := make([]byte, 0, len(needle)-1)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			chunk := append(carry, buffer[:count]...)
			if bytes.Contains(chunk, needle) {
				return true, nil
			}
			keep := len(needle) - 1
			if keep > len(chunk) {
				keep = len(chunk)
			}
			carry = append(carry[:0], chunk[len(chunk)-keep:]...)
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

func bounded(value string) string {
	const limit = 2048
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
}
