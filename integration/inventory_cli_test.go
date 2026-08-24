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
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	pinnedServerRef     = "bc62ac3c0f5a54e34119ee2546e0f9dca5f85fea"
	maxCLIOutputBytes   = 2 << 20
	maxBrowserJSONBytes = 1 << 20
	cliCommandTimeout   = 45 * time.Second
	cliCommandWaitDelay = 2 * time.Second
)

var (
	fullSHA               = regexp.MustCompile(`^[0-9a-f]{40}$`)
	disposableProjectName = regexp.MustCompile(`^artisan-server-e2e-[a-z0-9]{12}$`)
	dockerContainerID     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type liveConfig struct {
	binary                  string
	baseURL                 string
	adminEmail              string
	adminPassword           string
	adminNickname           string
	memberEmail             string
	memberPassword          string
	memberNickname          string
	memberToken             string
	memberCredential        string
	reviewMemberToken       string
	reviewMemberCredential  string
	foreignToken            string
	foreignCredential       string
	foreignEmail            string
	foreignNickname         string
	foreignOrganizationSlug string
	serverRoot              string
	organization            string
	organizationSlug        string
	projectName             string
}

type dockerMetadataCommand func(args ...string) ([]byte, error)

type dockerInspectDocument struct {
	ID    string `json:"Id"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Environment []string          `json:"Env"`
		Labels      map[string]string `json:"Labels"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
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
	commandPace      time.Duration
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
	LotID              string            `json:"lot_id"`
	Name               string            `json:"name"`
	PricePerKgEURCents *int64            `json:"price_per_kg_eur_cents"`
	Description        *string           `json:"description"`
	OnHandGrams        int64             `json:"on_hand_grams"`
	ReservedGrams      int64             `json:"reserved_grams"`
	AvailableGrams     int64             `json:"available_grams"`
	Images             []imageProjection `json:"images"`
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

type inventoryTotals struct {
	LotCount            int64  `json:"lot_count"`
	OnHandGrams         int64  `json:"on_hand_grams"`
	ReservedGrams       int64  `json:"reserved_grams"`
	AvailableGrams      int64  `json:"available_grams"`
	OnHandValueEURCents *int64 `json:"on_hand_value_eur_cents"`
	PricedLotCount      int64  `json:"priced_lot_count"`
	UnpricedLotCount    int64  `json:"unpriced_lot_count"`
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
		ActualGrams           *int64 `json:"actual_grams"`
		RoastCostEURCents     *int64 `json:"roast_cost_eur_cents"`
		LotID                 string `json:"lot_id"`
	} `json:"items"`
}

type conflictPage struct {
	Items []struct {
		ConflictID string `json:"conflict_id"`
		LotID      string `json:"lot_id"`
		State      string `json:"state"`
	} `json:"items"`
}

type conflictDetail struct {
	ConflictID string `json:"conflict_id"`
	LotID      string `json:"lot_id"`
	State      string `json:"state"`
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

func TestDisposableTargetGuardRequiresExactLocalComposeMetadata(t *testing.T) {
	const (
		project = "artisan-server-e2e-a1b2c3d4e5f6"
		apiID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		webID   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	validInspect := `[
		{"Id":"` + apiID + `","State":{"Running":true},"Config":{"Env":["PATH=/usr/local/bin:/usr/bin","ARTISAN_SERVER_E2E_DISPOSABLE=artisan-server-e2e-compose-v1"],"Labels":{"com.docker.compose.project":"` + project + `","com.docker.compose.service":"api","com.docker.compose.oneoff":"False","com.docker.compose.container-number":"1","io.artisan-server.e2e.disposable":"artisan-server-e2e-compose-v1"}},"NetworkSettings":{"Ports":{}}},
		{"Id":"` + webID + `","State":{"Running":true},"Config":{"Env":["PATH=/usr/local/bin:/usr/bin"],"Labels":{"com.docker.compose.project":"` + project + `","com.docker.compose.service":"web","com.docker.compose.oneoff":"False","com.docker.compose.container-number":"1"}},"NetworkSettings":{"Ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}]}}}
	]`
	config := liveConfig{baseURL: "http://127.0.0.1:18080", projectName: project}

	runner := func(overrides map[string]string, failures map[string]error) dockerMetadataCommand {
		return func(args ...string) ([]byte, error) {
			key := strings.Join(args, " ")
			if err := failures[key]; err != nil {
				return nil, err
			}
			if output, ok := overrides[key]; ok {
				return []byte(output), nil
			}
			switch key {
			case "context show":
				return []byte("default\n"), nil
			case `context inspect default --format {{ (index .Endpoints "docker").Host }}`:
				return []byte("unix:///var/run/docker.sock\n"), nil
			case "container ls --no-trunc --filter label=com.docker.compose.project=" + project + " --filter label=com.docker.compose.service=api --filter status=running --format {{.ID}}":
				return []byte(apiID + "\n"), nil
			case "container ls --no-trunc --filter label=com.docker.compose.project=" + project + " --filter label=com.docker.compose.service=web --filter status=running --format {{.ID}}":
				return []byte(webID + "\n"), nil
			case "inspect " + apiID + " " + webID:
				return []byte(validInspect), nil
			default:
				return nil, fmt.Errorf("unexpected Docker metadata command %q", key)
			}
		}
	}
	noEnvironment := func(string) string { return "" }
	if err := validateDisposableTarget(config, noEnvironment, runner(nil, nil)); err != nil {
		t.Fatalf("valid disposable target rejected: %v", err)
	}

	apiListCommand := "container ls --no-trunc --filter label=com.docker.compose.project=" + project + " --filter label=com.docker.compose.service=api --filter status=running --format {{.ID}}"
	inspectCommand := "inspect " + apiID + " " + webID
	tests := []struct {
		name        string
		config      liveConfig
		environment map[string]string
		overrides   map[string]string
		failures    map[string]error
	}{
		{name: "invalid project name", config: liveConfig{baseURL: config.baseURL, projectName: "archive-api"}},
		{name: "remote Docker host environment", config: config, environment: map[string]string{"DOCKER_HOST": "tcp://production.invalid:2376"}},
		{name: "nondefault context", config: config, overrides: map[string]string{"context show": "production\n"}},
		{name: "remote default endpoint", config: config, overrides: map[string]string{`context inspect default --format {{ (index .Endpoints "docker").Host }}`: "tcp://127.0.0.1:2375\n"}},
		{name: "Docker unavailable", config: config, failures: map[string]error{"context show": errors.New("executable file not found")}},
		{name: "ambiguous API", config: config, overrides: map[string]string{apiListCommand: apiID + "\n" + strings.Repeat("c", 64) + "\n"}},
		{name: "API marker label missing", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `"io.artisan-server.e2e.disposable":"artisan-server-e2e-compose-v1"`, `"other":"value"`, 1)}},
		{name: "API marker environment missing", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `,"ARTISAN_SERVER_E2E_DISPOSABLE=artisan-server-e2e-compose-v1"`, "", 1)}},
		{name: "wrong API project", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `"com.docker.compose.project":"`+project+`"`, `"com.docker.compose.project":"archive-api"`, 1)}},
		{name: "wrong API service", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `"com.docker.compose.service":"api"`, `"com.docker.compose.service":"web"`, 1)}},
		{name: "API oneoff", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `"com.docker.compose.oneoff":"False"`, `"com.docker.compose.oneoff":"True"`, 1)}},
		{name: "wrong web container number", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `"com.docker.compose.service":"web","com.docker.compose.oneoff":"False","com.docker.compose.container-number":"1"`, `"com.docker.compose.service":"web","com.docker.compose.oneoff":"False","com.docker.compose.container-number":"2"`, 1)}},
		{name: "web not running", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `"Id":"`+webID+`","State":{"Running":true}`, `"Id":"`+webID+`","State":{"Running":false}`, 1)}},
		{name: "wrong published host port", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `"HostPort":"18080"`, `"HostPort":"18081"`, 1)}},
		{name: "wrong published host IP", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `"HostIp":"127.0.0.1"`, `"HostIp":"0.0.0.0"`, 1)}},
		{name: "ambiguous web binding", config: config, overrides: map[string]string{inspectCommand: strings.Replace(validInspect, `{"HostIp":"127.0.0.1","HostPort":"18080"}`, `{"HostIp":"127.0.0.1","HostPort":"18080"},{"HostIp":"::1","HostPort":"18080"}`, 1)}},
		{name: "malformed inspect JSON", config: config, overrides: map[string]string{inspectCommand: `{"Id":`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			getenv := func(name string) string { return test.environment[name] }
			if err := validateDisposableTarget(test.config, getenv, runner(test.overrides, test.failures)); err == nil {
				t.Fatal("unsafe disposable target metadata accepted")
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

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestTokenTreeScannerFindsChunkBoundaryAndRejectsSymlinks(t *testing.T) {
	root := canonicalTempDir(t)
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
	root := canonicalTempDir(t)
	name := "artisan-real"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(root, name)
	if err := os.WriteFile(target, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveTrustedExecutable(target)
	if err != nil || resolved != target {
		t.Fatalf("regular executable = (%q, %v), want exact resolved path", resolved, err)
	}
	if runtime.GOOS == "windows" {
		nonExecutable := filepath.Join(root, "artisan-no-extension")
		if err := os.WriteFile(nonExecutable, []byte("binary"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveTrustedExecutable(nonExecutable); err == nil {
			t.Fatal("Windows regular file without .exe extension unexpectedly accepted")
		}
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
	if runtime.GOOS == "windows" {
		t.Skip("shell-script executable fixtures are Unix-specific; native Windows executable behavior is covered by the CI smoke build")
	}
	root := canonicalTempDir(t)
	runDirectory := filepath.Join(root, "run")
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	cwdScript := filepath.Join(root, "cwd-command")
	if err := os.WriteFile(cwdScript, []byte("#!/bin/sh\nprintf '{\"ok\":true,\"data\":{\"cwd\":\"%s\"}}\\n' \"$PWD\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantRunDirectory := runDirectory
	if runtime.GOOS == "darwin" {
		canonical, err := filepath.EvalSymlinks(runDirectory)
		if err != nil {
			t.Fatal(err)
		}
		wantRunDirectory = canonical
	}
	runner := cliRunner{binary: cwdScript, baseURL: "http://127.0.0.1", cwd: runDirectory, env: []string{"PATH=" + os.Getenv("PATH")}}
	execution := runner.execute("")
	if execution.err != nil || execution.overflow || execution.timedOut || !strings.Contains(execution.record.Stdout, `"cwd":"`+wantRunDirectory+`"`) {
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
	anchorMutations := []struct {
		name     string
		snippet  string
		wantUses int
	}{
		{
			name:     "alias key resolving to uses",
			snippet:  "      - name: alias key seed\n        env:\n          KEY: &uses_key uses\n      - *uses_key: actions/cache@main\n",
			wantUses: 3,
		},
		{
			name:     "anchored action step referenced once",
			snippet:  "      - &action_step\n        uses: " + pinnedTarget + "\n      - *action_step\n",
			wantUses: 4,
		},
		{
			name:     "anchored action step referenced multiple times",
			snippet:  "      - &action_step\n        uses: " + pinnedTarget + "\n      - *action_step\n      - *action_step\n",
			wantUses: 4,
		},
		{
			name:     "merge alias injecting uses",
			snippet:  "      - &action_fields\n        uses: " + pinnedTarget + "\n      - name: merged action\n        <<: *action_fields\n",
			wantUses: 4,
		},
		{
			name:     "anchored uses scalar",
			snippet:  "      - uses: &action_value " + pinnedTarget + "\n",
			wantUses: 4,
		},
		{
			name:     "benign non-action anchor and alias",
			snippet:  "      - name: benign reuse\n        env:\n          FIRST: &shared harmless\n          SECOND: *shared\n",
			wantUses: 3,
		},
	}
	for _, mutation := range anchorMutations {
		t.Run(mutation.name, func(t *testing.T) {
			mutatedWorkflow := insert(mutation.snippet)
			decoder := yaml.NewDecoder(strings.NewReader(mutatedWorkflow))
			var document yaml.Node
			if err := decoder.Decode(&document); err != nil {
				t.Fatalf("anchor mutation was not valid YAML: %v", err)
			}
			var trailing yaml.Node
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				t.Fatalf("anchor mutation did not contain exactly one YAML document: %v", err)
			}
			err := validateWorkflowActionPins(mutatedWorkflow, mutation.wantUses)
			if err == nil {
				t.Fatal("YAML anchor or alias bypassed the workflow reuse guard")
			}
			if !strings.Contains(err.Error(), "anchors, aliases, or merge keys") {
				t.Fatalf("YAML reuse failure was not clear: %v", err)
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
	if err := validateIntegrationWorkflow(contents); err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"permissions:\n  contents: read", pinnedServerRef, "repository: fr3akX/artisan-server",
		"token: ${{ secrets.ARTISAN_SERVER_REPOSITORY_TOKEN }}",
		"integration/artisan-server.ref", "CGO_ENABLED: \"0\"", "go-version: 1.23.x",
		"ARTISAN_INTEGRATION_BASE_URL: http://127.0.0.1:18080", `ARTISAN_SERVER_HTTP_PORT: "127.0.0.1:18080"`,
		"ARTISAN_INTEGRATION_MEMBER_EMAIL: member@example.com", "integration/provision_member.py",
		"ARTISAN_INTEGRATION_FOREIGN_EMAIL: foreign@example.com", "ARTISAN_INTEGRATION_FOREIGN_ORGANIZATION_SLUG: foreign-review-e2e",
		"ARTISAN_INTEGRATION_SERVER_ROOT: ${{ github.workspace }}/artisan-server", "ARTISAN_INTEGRATION_REVIEW_MEMBER_TOKEN=$review_member_token", "ARTISAN_INTEGRATION_FOREIGN_TOKEN=$foreign_token",
		"go test ./integration -run '^TestDisposableComposeTargetProof$' -count=1 -v",
		`if [[ -n "${ARTISAN_SERVER_E2E_PROJECT_NAME:-}" && -d artisan-server ]]; then`,
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
	if wrapperCount != 7 {
		t.Errorf("workflow Compose wrapper count = %d, want config, start down/up, bootstrap, member provision, logs, teardown", wrapperCount)
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
	for _, required := range []string{
		`if [[ -n "${ARTISAN_SERVER_E2E_PROJECT_NAME:-}" && -d artisan-server ]]; then`,
		"cd artisan-server", "timeout --signal=TERM --kill-after=", "logs --no-color --tail 200", "head -c 65536",
	} {
		if !strings.Contains(logs, required) {
			t.Errorf("failure log step missing %q", required)
		}
	}
	if strings.Contains(logs, "working-directory:") {
		t.Error("failure log step working directory must not depend on a successful server checkout")
	}
}

func TestRepositoryTextAttributes(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	for _, pattern := range []string{"*.go", "*.md", "*.yml", "*.yaml", "*.sh", "*.py", "*.ref", "*.mod", "*.sum", "*.ps1"} {
		want := pattern + " text eol=lf"
		if !slices.Contains(lines, want) {
			t.Errorf(".gitattributes missing %q", want)
		}
	}
}

func TestIntegrationWorkflowContractRejectsMutations(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "integration.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string][2]string{
		"extra event":                {"  pull_request:\n  push:", "  pull_request:\n  push:\n  workflow_dispatch:"},
		"filtered event":             {"  pull_request:\n  push:", "  pull_request:\n  push:\n    branches: [main]"},
		"alternate event form":       {"on:\n  pull_request:\n  push:", "on: [pull_request, push]"},
		"global scope":               {"permissions:\n  contents: read", "permissions:\n  contents: read\n  actions: read"},
		"job write-all":              {"live-integration:\n    runs-on:", "live-integration:\n    permissions: write-all\n    runs-on:"},
		"disabled required step":     {"name: Run pinned live integration\n        shell:", "name: Run pinned live integration\n        if: false\n        shell:"},
		"workspace canonicalization": {"workspace=\"$(realpath -e -- \"$workspace_input\")\"", "workspace=\"$workspace_input\""},
		"script symlink guard":       {"[[ -f \"$script_input\" && ! -L \"$script_input\" ]]", "[[ -f \"$script_input\" ]]"},
		"script containment":         {"[[ \"$script\" == \"$script_input\" && \"$script\" == \"$workspace/\"* ]]", "[[ \"$script\" == \"$script_input\" ]]"},
		"canonical mount":            {`-v "$script:/tmp/provision_member.py:ro"`, `-v "$script_input:/tmp/provision_member.py:ro"`},
		"web publish all interfaces": {`ARTISAN_SERVER_HTTP_PORT: "127.0.0.1:18080"`, `ARTISAN_SERVER_HTTP_PORT: "18080"`},
	} {
		t.Run(name, func(t *testing.T) {
			changed := bytes.Replace(contents, []byte(mutation[0]), []byte(mutation[1]), 1)
			if bytes.Equal(changed, contents) {
				t.Fatal("test mutation did not apply")
			}
			if err := validateIntegrationWorkflow(changed); err == nil {
				t.Fatal("integration workflow contract bypass was accepted")
			}
		})
	}

	t.Run("target proof after credential issuance", func(t *testing.T) {
		proof := []byte("      - name: Prove disposable Compose target\n        shell: bash\n        run: timeout --signal=TERM --kill-after=10s 60s go test ./integration -run '^TestDisposableComposeTargetProof$' -count=1 -v\n\n")
		changed := bytes.Replace(contents, proof, nil, 1)
		changed = bytes.Replace(changed, []byte("      - name: Build compiled CLI\n"), append(proof, []byte("      - name: Build compiled CLI\n")...), 1)
		if bytes.Equal(changed, contents) || bytes.Count(changed, []byte("name: Prove disposable Compose target")) != 1 {
			t.Fatal("test mutation did not apply")
		}
		if err := validateIntegrationWorkflow(changed); err == nil {
			t.Fatal("post-credential target proof was accepted")
		}
	})
}

const provisionMemberRunContract = `set -euo pipefail
workspace_input="$GITHUB_WORKSPACE"
[[ -d "$workspace_input" && ! -L "$workspace_input" ]]
workspace="$(realpath -e -- "$workspace_input")"
[[ "$workspace" == "$workspace_input" ]]
script_input="$workspace/integration/provision_member.py"
[[ -f "$script_input" && ! -L "$script_input" ]]
script="$(realpath -e -- "$script_input")"
[[ "$script" == "$script_input" && "$script" == "$workspace/"* ]]
issued_file="$(mktemp)"
trap 'rm -f "$issued_file"' EXIT
timeout --signal=TERM --kill-after=10s 2m ./scripts/e2e_compose.py --project "$ARTISAN_SERVER_E2E_PROJECT_NAME" \
  -f "$PWD/compose.yaml" -f "$PWD/compose.e2e.yaml" \
  run --rm \
  -e "ARTISAN_E2E_MEMBER_EMAIL=$ARTISAN_INTEGRATION_MEMBER_EMAIL" \
  -e "ARTISAN_E2E_MEMBER_NICKNAME=$ARTISAN_INTEGRATION_MEMBER_NICKNAME" \
  -e "ARTISAN_E2E_MEMBER_PASSWORD=$ARTISAN_INTEGRATION_MEMBER_PASSWORD" \
  -e "ARTISAN_E2E_ORGANIZATION_SLUG=$ARTISAN_INTEGRATION_ADMIN_ORGANIZATION_SLUG" \
  -e "ARTISAN_E2E_FOREIGN_EMAIL=$ARTISAN_INTEGRATION_FOREIGN_EMAIL" \
  -e "ARTISAN_E2E_FOREIGN_NICKNAME=$ARTISAN_INTEGRATION_FOREIGN_NICKNAME" \
  -e "ARTISAN_E2E_FOREIGN_ORGANIZATION_SLUG=$ARTISAN_INTEGRATION_FOREIGN_ORGANIZATION_SLUG" \
  -v "$script:/tmp/provision_member.py:ro" \
  api python /tmp/provision_member.py > "$issued_file"
IFS=$'\t' read -r member_token member_credential_id review_member_token review_member_credential_id foreign_token foreign_credential_id < <(tail -n 1 "$issued_file")
[[ "$member_token" =~ ^[^[:space:]]+$ ]]
[[ "$member_credential_id" =~ ^[0-9a-f-]{36}$ ]]
[[ "$review_member_token" =~ ^[^[:space:]]+$ ]]
[[ "$review_member_credential_id" =~ ^[0-9a-f-]{36}$ ]]
[[ "$foreign_token" =~ ^[^[:space:]]+$ ]]
[[ "$foreign_credential_id" =~ ^[0-9a-f-]{36}$ ]]
echo "::add-mask::$member_token"
echo "::add-mask::$review_member_token"
echo "::add-mask::$foreign_token"
{
  echo "ARTISAN_INTEGRATION_MEMBER_TOKEN=$member_token"
  echo "ARTISAN_INTEGRATION_MEMBER_CREDENTIAL_ID=$member_credential_id"
  echo "ARTISAN_INTEGRATION_REVIEW_MEMBER_TOKEN=$review_member_token"
  echo "ARTISAN_INTEGRATION_REVIEW_MEMBER_CREDENTIAL_ID=$review_member_credential_id"
  echo "ARTISAN_INTEGRATION_FOREIGN_TOKEN=$foreign_token"
  echo "ARTISAN_INTEGRATION_FOREIGN_CREDENTIAL_ID=$foreign_credential_id"
} >> "$GITHUB_ENV"
rm -f "$issued_file"
trap - EXIT`

func validateIntegrationWorkflow(contents []byte) error {
	if err := validateWorkflowActionPins(string(contents), 3); err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("integration workflow root must be one mapping")
	}
	root := document.Content[0]
	events, err := integrationMappingLookup(root, "on")
	if err != nil || !integrationMappingHasExactKeys(events, "pull_request", "push") {
		return errors.New("integration events must be exactly pull_request and push mappings")
	}
	for _, event := range []string{"pull_request", "push"} {
		configuration, _ := integrationMappingLookup(events, event)
		if configuration.Kind != yaml.ScalarNode || configuration.Tag != "!!null" {
			return fmt.Errorf("integration event %s must not be filtered", event)
		}
	}
	permissions, err := integrationMappingLookup(root, "permissions")
	if err != nil || !integrationMappingEquals(permissions, map[string]string{"contents": "read"}) {
		return errors.New("integration top-level permissions must be exactly contents: read")
	}
	jobs, err := integrationMappingLookup(root, "jobs")
	if err != nil {
		return err
	}
	for index := 0; index+1 < len(jobs.Content); index += 2 {
		if _, err := integrationMappingLookup(jobs.Content[index+1], "permissions"); err == nil {
			return errors.New("integration job-level permissions are forbidden")
		}
	}
	job, err := integrationMappingLookup(jobs, "live-integration")
	if err != nil {
		return err
	}
	jobEnvironment, err := integrationMappingLookup(job, "env")
	if err != nil {
		return errors.New("integration job environment is missing")
	}
	webBinding, err := integrationMappingLookup(jobEnvironment, "ARTISAN_SERVER_HTTP_PORT")
	if err != nil || webBinding.Kind != yaml.ScalarNode || webBinding.Value != "127.0.0.1:18080" {
		return errors.New("integration web publish target must be exact loopback")
	}
	for _, forbidden := range []string{"if", "continue-on-error"} {
		if _, err := integrationMappingLookup(job, forbidden); err == nil {
			return fmt.Errorf("integration job %s is forbidden", forbidden)
		}
	}
	steps, err := integrationMappingLookup(job, "steps")
	if err != nil || steps.Kind != yaml.SequenceNode {
		return errors.New("integration steps are missing")
	}
	requiredSteps := map[string]int{
		"Check out Artisan CLI": 0, "Validate pinned server ref": 0, "Check out pinned Artisan Server": 0,
		"Verify server checkout HEAD": 0, "Set up Go": 0, "Prepare disposable stack inputs": 0,
		"Validate disposable Compose configuration": 0, "Start disposable Artisan Server": 0,
		"Wait for bounded readiness": 0, "Prove disposable Compose target": 0,
		"Bootstrap disposable administrator": 0, "Provision disposable member": 0,
		"Build compiled CLI": 0, "Run pinned live integration": 0, "Print bounded server logs on failure": 0,
		"Tear down disposable stack": 0,
	}
	foundProvision := false
	foundTargetProof := false
	targetProofIndex, bootstrapIndex, provisionIndex := -1, -1, -1
	for stepIndex, step := range steps.Content {
		name, nameErr := integrationMappingLookup(step, "name")
		if nameErr != nil {
			return errors.New("integration steps must be named")
		}
		if _, required := requiredSteps[name.Value]; required {
			requiredSteps[name.Value]++
		}
		if _, err := integrationMappingLookup(step, "continue-on-error"); err == nil {
			return fmt.Errorf("step %q may not continue on error", name.Value)
		}
		condition, conditionErr := integrationMappingLookup(step, "if")
		allowedCondition := map[string]string{"Print bounded server logs on failure": "failure()", "Tear down disposable stack": "always()"}
		if want, allowed := allowedCondition[name.Value]; allowed {
			if conditionErr != nil || condition.Kind != yaml.ScalarNode || condition.Value != want {
				return fmt.Errorf("step %q condition drifted", name.Value)
			}
		} else if conditionErr == nil {
			return fmt.Errorf("required step %q may not be conditional", name.Value)
		}
		if name.Value == "Bootstrap disposable administrator" {
			bootstrapIndex = stepIndex
		}
		if name.Value == "Prove disposable Compose target" {
			run, runErr := integrationMappingLookup(step, "run")
			if runErr != nil || strings.TrimSpace(run.Value) != "timeout --signal=TERM --kill-after=10s 60s go test ./integration -run '^TestDisposableComposeTargetProof$' -count=1 -v" {
				return errors.New("disposable Compose target proof command drifted")
			}
			foundTargetProof = true
			targetProofIndex = stepIndex
		}
		if name.Value == "Provision disposable member" {
			provisionIndex = stepIndex
			run, runErr := integrationMappingLookup(step, "run")
			if runErr != nil || strings.TrimSpace(strings.ReplaceAll(run.Value, "\r\n", "\n")) != provisionMemberRunContract {
				return errors.New("member provision canonical bind contract drifted")
			}
			foundProvision = true
		}
	}
	if !foundTargetProof {
		return errors.New("disposable Compose target proof step is missing")
	}
	if !foundProvision {
		return errors.New("member provision step is missing")
	}
	if targetProofIndex < 0 || bootstrapIndex < 0 || provisionIndex < 0 || targetProofIndex >= bootstrapIndex || targetProofIndex >= provisionIndex {
		return errors.New("disposable Compose target proof must precede bootstrap and credential issuance")
	}
	for name, count := range requiredSteps {
		if count != 1 {
			return fmt.Errorf("required integration step %q appears %d times", name, count)
		}
	}
	return nil
}

func integrationMappingLookup(node *yaml.Node, key string) (*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, errors.New("mapping required")
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1], nil
		}
	}
	return nil, fmt.Errorf("mapping key %q missing", key)
}

func integrationMappingEquals(node *yaml.Node, want map[string]string) bool {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content) != len(want)*2 {
		return false
	}
	for key, value := range want {
		child, err := integrationMappingLookup(node, key)
		if err != nil || child.Kind != yaml.ScalarNode || child.Value != value {
			return false
		}
	}
	return true
}

func integrationMappingHasExactKeys(node *yaml.Node, keys ...string) bool {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content) != len(keys)*2 {
		return false
	}
	seen := make(map[string]bool, len(keys))
	for index := 0; index+1 < len(node.Content); index += 2 {
		seen[node.Content[index].Value] = true
	}
	if len(seen) != len(keys) {
		return false
	}
	for _, key := range keys {
		if !seen[key] {
			return false
		}
	}
	return true
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
	if err := rejectWorkflowYAMLReuse(&document); err != nil {
		return err
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

func rejectWorkflowYAMLReuse(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode || node.Alias != nil || node.Anchor != "" || node.Tag == "!!merge" {
		return errors.New("integration workflow must not contain YAML anchors, aliases, or merge keys")
	}
	for _, child := range node.Content {
		if err := rejectWorkflowYAMLReuse(child); err != nil {
			return err
		}
	}
	return nil
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
	if always {
		for _, required := range []string{
			`if [[ -n "${ARTISAN_SERVER_E2E_PROJECT_NAME:-}" && -d artisan-server ]]; then`,
			"cd artisan-server",
		} {
			if !strings.Contains(step, required) {
				t.Errorf("failure-only Compose step missing %q", required)
			}
		}
		if strings.Contains(step, "working-directory:") {
			t.Error("failure-only Compose step working directory must not depend on a successful server checkout")
		}
	} else if !strings.Contains(step, "working-directory: artisan-server") {
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

func TestDisposableComposeTargetProof(t *testing.T) {
	project := os.Getenv("ARTISAN_SERVER_E2E_PROJECT_NAME")
	rawBaseURL := os.Getenv("ARTISAN_INTEGRATION_BASE_URL")
	if project == "" && rawBaseURL == "" {
		t.Skip("disposable target proof environment is not configured")
	}
	baseURL, err := validateLoopbackBaseURL(rawBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDisposableTarget(liveConfig{baseURL: baseURL, projectName: project}, os.Getenv, runDockerMetadataCommand); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryCLIAgainstArtisanServer(t *testing.T) {
	config, configured, err := loadLiveConfig(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !configured {
		t.Skip("live integration environment is not configured")
	}
	if err := validateDisposableTarget(config, os.Getenv, runDockerMetadataCommand); err != nil {
		t.Fatal(err)
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

	httpClient, csrf, token, credentialID := issueCredential(t, config, config.adminEmail, config.adminPassword)
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
	initialDescription := "Initial public description for " + runID
	updatedDescription := "Updated public description for " + runID
	var created lot
	runner.runJSON(t, "", &created,
		"inventory", "lot", "create",
		"--name", lotName, "--origin", "Ethiopia", "--varietal", "Heirloom",
		"--processing-method", "washed", "--description", initialDescription, "--opening-grams", "5000",
		"--opening-reason", "Disposable CLI integration opening balance",
		"--opening-reference", "opening-"+runID, "--idempotency-key", "cli-"+runID+"-create",
	)
	assertLotBalance(t, created, 5000, 0, 5000)
	assertLotPrice(t, created, nil)
	assertLotDescription(t, created, stringPointer(initialDescription))
	if !fullSHA.MatchString(pinnedServerRef) || len(created.LotID) != 32 {
		t.Fatalf("created invalid lot ID %q", created.LotID)
	}

	var shown lot
	runner.runJSON(t, "", &shown, "inventory", "lot", "show", created.LotID)
	assertLotBalance(t, shown, 5000, 0, 5000)
	assertLotPrice(t, shown, nil)
	assertLotDescription(t, shown, stringPointer(initialDescription))
	var listed lotPage
	runner.runJSON(t, "", &listed, "inventory", "lot", "list", "--q", lotName, "--all")
	if len(listed.Items) != 1 || listed.Items[0].LotID != created.LotID {
		t.Fatalf("admin lot list did not resolve the unique created lot: %+v", listed.Items)
	}
	assertLotPrice(t, listed.Items[0], nil)
	var rawListed struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	runner.runJSON(t, "", &rawListed, "inventory", "lot", "list", "--q", lotName, "--all")
	assertLotProjectionOmitsFields(t, rawListed.Items, created.LotID, "description")

	memberRoot := filepath.Join(root, "member")
	memberPaths := make(map[string]string)
	for _, directory := range []string{"home", "config", "state", "tmp", "run"} {
		memberPaths[directory] = filepath.Join(memberRoot, directory)
		if err := os.MkdirAll(memberPaths[directory], 0o700); err != nil {
			t.Fatal(err)
		}
	}
	memberRunner := cliRunner{
		binary: binary, baseURL: config.baseURL, cwd: memberPaths["run"],
		env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + memberPaths["home"], "XDG_CONFIG_HOME=" + memberPaths["config"], "XDG_STATE_HOME=" + memberPaths["state"], "TMPDIR=" + memberPaths["tmp"]},
	}
	memberToken, memberCredentialID := config.memberToken, config.memberCredential
	memberRunner.forbiddenToken = memberToken
	defer revokeCredential(t, httpClient, config.baseURL, csrf, memberCredentialID, memberToken)
	memberNeedsLogout := true
	defer func() {
		if memberNeedsLogout {
			if err := memberRunner.cleanupLogout(); err != nil {
				t.Error(err)
			}
		}
		if err := assertTokenAbsent(memberToken, memberRunner.records, nil); err != nil {
			t.Error(err)
		}
		if err := assertTokenAbsentFromTrees(memberToken, memberPaths["config"], memberPaths["state"], memberPaths["home"], memberPaths["tmp"], memberPaths["run"]); err != nil {
			t.Error(err)
		}
	}()
	var memberIdentity authIdentity
	memberRunner.runJSON(t, memberToken+"\n", &memberIdentity, "auth", "login", "--token-stdin")
	assertExpectedMemberIdentity(t, memberIdentity, config)
	var memberListed lotPage
	memberRunner.runJSON(t, "", &memberListed, "inventory", "lot", "list", "--limit", "1", "--all")
	foundMemberLot := false
	for _, item := range memberListed.Items {
		if item.LotID == created.LotID && item.Name == lotName {
			foundMemberLot = true
			assertLotPrice(t, item, nil)
		}
	}
	if !foundMemberLot {
		t.Fatalf("member full lot list did not include created active lot: %+v", memberListed.Items)
	}
	var memberShown lot
	memberRunner.runJSON(t, "", &memberShown, "inventory", "lot", "show", created.LotID)
	assertLotPrice(t, memberShown, nil)
	assertLotDescription(t, memberShown, stringPointer(initialDescription))

	var described lot
	runner.runJSON(t, "", &described,
		"inventory", "lot", "update", created.LotID, "--description", updatedDescription,
		"--idempotency-key", "cli-"+runID+"-description-update",
	)
	assertLotPrice(t, described, nil)
	assertLotDescription(t, described, stringPointer(updatedDescription))
	var adminDescribedRead, memberDescribedRead lot
	runner.runJSON(t, "", &adminDescribedRead, "inventory", "lot", "show", created.LotID)
	memberRunner.runJSON(t, "", &memberDescribedRead, "inventory", "lot", "show", created.LotID)
	for _, value := range []lot{adminDescribedRead, memberDescribedRead} {
		assertLotPrice(t, value, nil)
		assertLotDescription(t, value, stringPointer(updatedDescription))
	}

	var priced lot
	runner.runJSON(t, "", &priced,
		"inventory", "lot", "update", created.LotID, "--price-per-kg-eur", "12.34",
		"--idempotency-key", "cli-"+runID+"-price-set",
	)
	assertLotPrice(t, priced, int64Pointer(1234))
	assertLotDescription(t, priced, stringPointer(updatedDescription))
	var adminPricedRead, memberPricedRead lot
	runner.runJSON(t, "", &adminPricedRead, "inventory", "lot", "show", created.LotID)
	memberRunner.runJSON(t, "", &memberPricedRead, "inventory", "lot", "show", created.LotID)
	for _, value := range []lot{adminPricedRead, memberPricedRead} {
		assertLotPrice(t, value, int64Pointer(1234))
		assertLotDescription(t, value, stringPointer(updatedDescription))
	}

	totalsArgs := []string{"inventory", "totals", "--q", lotName, "--state", "active", "--availability", "positive"}
	var adminPricedTotals, memberPricedTotals inventoryTotals
	runner.runJSON(t, "", &adminPricedTotals, totalsArgs...)
	memberRunner.runJSON(t, "", &memberPricedTotals, totalsArgs...)
	assertTotals(t, adminPricedTotals, inventoryTotals{
		LotCount: 1, OnHandGrams: 5000, AvailableGrams: 5000,
		OnHandValueEURCents: int64Pointer(6170), PricedLotCount: 1,
	})
	if !reflect.DeepEqual(adminPricedTotals, memberPricedTotals) {
		t.Fatalf("admin/member filtered priced totals differ: admin=%+v member=%+v", adminPricedTotals, memberPricedTotals)
	}

	memberRunner.runJSONError(t, 5, "administrator_required",
		"inventory", "lot", "update", created.LotID, "--price-per-kg-eur", "99.99",
		"--idempotency-key", "cli-"+runID+"-deny-price-update",
	)
	runner.runJSON(t, "", &adminPricedRead, "inventory", "lot", "show", created.LotID)
	memberRunner.runJSON(t, "", &memberPricedRead, "inventory", "lot", "show", created.LotID)
	assertLotPrice(t, adminPricedRead, int64Pointer(1234))
	assertLotPrice(t, memberPricedRead, int64Pointer(1234))

	desktopItems := readDesktopBeanLots(t, httpClient, config.baseURL, memberToken)
	assertLotProjectionOmitsFields(t, desktopItems, created.LotID, "description", "price_per_kg_eur_cents", "roast_cost_eur_cents", "on_hand_value_eur_cents")

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
	downloadPath := filepath.Join(memberPaths["run"], "download.webp")
	var downloaded struct {
		Path    string `json:"path"`
		Variant string `json:"variant"`
		Bytes   int64  `json:"bytes"`
	}
	memberRunner.runJSON(t, "", &downloaded,
		"inventory", "image", "download", "--variant", "display", created.LotID, withImage.Images[0].ImageID, downloadPath,
	)
	downloadBytes, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if downloaded.Path != downloadPath || downloaded.Bytes != int64(len(downloadBytes)) || downloaded.Variant != "display" || len(downloadBytes) < 12 || string(downloadBytes[:4]) != "RIFF" || string(downloadBytes[8:12]) != "WEBP" {
		t.Fatalf("downloaded image is not the exact reported WebP result")
	}

	for _, denied := range [][]string{
		{"inventory", "lot", "create", "--name", "Denied member create", "--idempotency-key", "cli-" + runID + "-deny-create"},
		{"inventory", "lot", "update", created.LotID, "--name", "Denied member update", "--idempotency-key", "cli-" + runID + "-deny-update"},
		{"inventory", "lot", "archive", created.LotID, "--yes", "--idempotency-key", "cli-" + runID + "-deny-archive"},
		{"inventory", "lot", "restore", created.LotID, "--idempotency-key", "cli-" + runID + "-deny-restore"},
		{"inventory", "adjust", created.LotID, "--grams", "1", "--reason", "Denied member adjustment", "--yes", "--idempotency-key", "cli-" + runID + "-deny-adjust"},
		{"inventory", "image", "add", "--idempotency-key", "cli-" + runID + "-deny-image", created.LotID, imagePath},
	} {
		memberRunner.runJSONError(t, 5, "administrator_required", denied...)
	}

	reservationUUID := randomHex(t, 16)
	clientUUID := randomHex(t, 16)
	roastUUID := randomHex(t, 16)
	reservedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	var reservation reservationMutation
	memberRunner.runJSON(t, "", &reservation,
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
	var reservedCosts reservationPage
	memberRunner.runJSON(t, "", &reservedCosts, "inventory", "lot", "reservations", created.LotID, "--all")
	assertReservationCost(t, reservedCosts, reservationUUID, "reserved", 1000, nil, int64Pointer(1234))

	var finalized reservationMutation
	memberRunner.runJSON(t, "", &finalized,
		"inventory", "reservation", "finalize", reservationUUID, "--actual-grams", "900",
		"--occurred-at", time.Now().UTC().Format("2006-01-02T15:04:05.000000Z"), "--idempotency-key", "cli-"+runID+"-finalize",
	)
	if finalized.Reservation.State != "finalized" || finalized.Balance.OnHandGrams != 4850 || finalized.Balance.ReservedGrams != 0 || finalized.Balance.AvailableGrams != 4850 {
		t.Fatalf("member finalize result = %+v", finalized)
	}
	var finalizedCosts reservationPage
	runner.runJSON(t, "", &finalizedCosts, "inventory", "lot", "reservations", created.LotID, "--all")
	assertReservationCost(t, finalizedCosts, reservationUUID, "finalized", 1000, int64Pointer(900), int64Pointer(1111))

	secondReservationUUID := randomHex(t, 16)
	secondRoastUUID := randomHex(t, 16)
	var secondReservation reservationMutation
	memberRunner.runJSON(t, "", &secondReservation,
		"inventory", "reservation", "create", "--client-reservation-uuid", secondReservationUUID,
		"--client-instance-uuid", clientUUID, "--roast-uuid", secondRoastUUID, "--lot-id", created.LotID,
		"--planned-grams", "250", "--occurred-at", time.Now().UTC().Format("2006-01-02T15:04:05.000000Z"), "--idempotency-key", "cli-"+runID+"-reserve-release",
	)
	var released reservationMutation
	memberRunner.runJSON(t, "", &released,
		"inventory", "reservation", "release", secondReservationUUID,
		"--occurred-at", time.Now().UTC().Format("2006-01-02T15:04:05.000000Z"), "--idempotency-key", "cli-"+runID+"-release",
	)
	if released.Reservation.State != "released" || released.Balance.OnHandGrams != 4850 || released.Balance.ReservedGrams != 0 || released.Balance.AvailableGrams != 4850 {
		t.Fatalf("member release result = %+v", released)
	}
	var releasedCosts reservationPage
	memberRunner.runJSON(t, "", &releasedCosts, "inventory", "lot", "reservations", created.LotID, "--all")
	assertReservationCost(t, releasedCosts, reservationUUID, "finalized", 1000, int64Pointer(900), int64Pointer(1111))
	assertReservationCost(t, releasedCosts, secondReservationUUID, "released", 250, nil, nil)

	var authoritative lot
	runner.runJSON(t, "", &authoritative, "inventory", "lot", "show", created.LotID)
	assertLotBalance(t, authoritative, 4850, 0, 4850)
	assertLotPrice(t, authoritative, int64Pointer(1234))
	if authoritative.Name != lotName || len(authoritative.Images) != 1 {
		t.Fatalf("authoritative lot content = name %q images %+v", authoritative.Name, authoritative.Images)
	}
	authoritativeImage := authoritative.Images[0]
	if authoritativeImage.ImageID != withImage.Images[0].ImageID || !authoritativeImage.IsCover || authoritativeImage.Position != 0 || authoritativeImage.Caption == nil || *authoritativeImage.Caption != "Disposable integration image" || authoritativeImage.AltText == nil || *authoritativeImage.AltText != "Coffee sample" {
		t.Fatalf("authoritative image metadata = %+v", authoritativeImage)
	}
	var ledger, memberLedger ledgerPage
	runner.runJSON(t, "", &ledger, "inventory", "lot", "ledger", created.LotID, "--all")
	memberRunner.runJSON(t, "", &memberLedger, "inventory", "lot", "ledger", created.LotID, "--all")
	assertLedger(t, ledger)
	assertLedger(t, memberLedger)
	if !reflect.DeepEqual(ledger, memberLedger) {
		t.Fatalf("admin/member ledger reads differ: admin=%+v member=%+v", ledger, memberLedger)
	}
	var reservations reservationPage
	runner.runJSON(t, "", &reservations, "inventory", "lot", "reservations", created.LotID, "--all")
	if len(reservations.Items) != 2 {
		t.Fatalf("authoritative reservations = %+v", reservations.Items)
	}
	states := map[string]string{}
	for _, item := range reservations.Items {
		states[item.ClientReservationUUID] = item.State
	}
	if states[reservationUUID] != "finalized" || states[secondReservationUUID] != "released" {
		t.Fatalf("authoritative reservation states = %+v", states)
	}
	assertReservationCost(t, reservations, reservationUUID, "finalized", 1000, int64Pointer(900), int64Pointer(1111))
	assertReservationCost(t, reservations, secondReservationUUID, "released", 250, nil, nil)

	var descriptionCleared lot
	runner.runJSON(t, "", &descriptionCleared,
		"inventory", "lot", "update", created.LotID, "--clear", "description",
		"--idempotency-key", "cli-"+runID+"-description-clear",
	)
	assertLotPrice(t, descriptionCleared, int64Pointer(1234))
	assertLotDescription(t, descriptionCleared, nil)
	var adminDescriptionClearedRead, memberDescriptionClearedRead lot
	runner.runJSON(t, "", &adminDescriptionClearedRead, "inventory", "lot", "show", created.LotID)
	memberRunner.runJSON(t, "", &memberDescriptionClearedRead, "inventory", "lot", "show", created.LotID)
	for _, value := range []lot{adminDescriptionClearedRead, memberDescriptionClearedRead} {
		assertLotPrice(t, value, int64Pointer(1234))
		assertLotDescription(t, value, nil)
	}

	var cleared lot
	runner.runJSON(t, "", &cleared,
		"inventory", "lot", "update", created.LotID, "--clear", "price-per-kg-eur",
		"--idempotency-key", "cli-"+runID+"-price-clear",
	)
	assertLotPrice(t, cleared, nil)
	var adminClearedRead, memberClearedRead lot
	runner.runJSON(t, "", &adminClearedRead, "inventory", "lot", "show", created.LotID)
	memberRunner.runJSON(t, "", &memberClearedRead, "inventory", "lot", "show", created.LotID)
	assertLotPrice(t, adminClearedRead, nil)
	assertLotPrice(t, memberClearedRead, nil)

	var adminClearedTotals, memberClearedTotals inventoryTotals
	runner.runJSON(t, "", &adminClearedTotals, totalsArgs...)
	memberRunner.runJSON(t, "", &memberClearedTotals, totalsArgs...)
	assertTotals(t, adminClearedTotals, inventoryTotals{
		LotCount: 1, OnHandGrams: 4850, AvailableGrams: 4850, UnpricedLotCount: 1,
	})
	if !reflect.DeepEqual(adminClearedTotals, memberClearedTotals) {
		t.Fatalf("admin/member filtered unpriced totals differ: admin=%+v member=%+v", adminClearedTotals, memberClearedTotals)
	}

	var adminUnpricedReservations, memberUnpricedReservations reservationPage
	runner.runJSON(t, "", &adminUnpricedReservations, "inventory", "lot", "reservations", created.LotID, "--all")
	memberRunner.runJSON(t, "", &memberUnpricedReservations, "inventory", "lot", "reservations", created.LotID, "--all")
	for _, page := range []reservationPage{adminUnpricedReservations, memberUnpricedReservations} {
		assertReservationCost(t, page, reservationUUID, "finalized", 1000, int64Pointer(900), nil)
		assertReservationCost(t, page, secondReservationUUID, "released", 250, nil, nil)
	}

	var conflicted lot
	runner.runJSON(t, "", &conflicted,
		"inventory", "adjust", created.LotID, "--grams", "-5000",
		"--reason", "Disposable CLI integration conflict", "--reference", "conflict-"+runID,
		"--idempotency-key", "cli-"+runID+"-conflict", "--yes",
	)
	assertLotBalance(t, conflicted, -150, 0, -150)
	var memberConflicts conflictPage
	memberRunner.runJSON(t, "", &memberConflicts, "inventory", "conflict", "list", "--lot", created.LotID, "--all")
	if len(memberConflicts.Items) != 1 || memberConflicts.Items[0].LotID != created.LotID || memberConflicts.Items[0].State != "open" {
		t.Fatalf("member conflict read = %+v, want one open disposable conflict", memberConflicts.Items)
	}
	var memberConflict conflictDetail
	memberRunner.runJSON(t, "", &memberConflict, "inventory", "conflict", "show", memberConflicts.Items[0].ConflictID)
	if memberConflict.ConflictID != memberConflicts.Items[0].ConflictID || memberConflict.LotID != created.LotID || memberConflict.State != "open" {
		t.Fatalf("member conflict detail = %+v", memberConflict)
	}

	var memberLogout struct {
		LoggedOut bool `json:"logged_out"`
	}
	memberRunner.runJSON(t, "", &memberLogout, "auth", "logout")
	if !memberLogout.LoggedOut {
		t.Fatal("member auth logout did not report success")
	}
	memberNeedsLogout = false

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

func runDockerMetadataCommand(args ...string) ([]byte, error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("Docker is required for disposable target proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, docker, args...)
	output, err := command.Output()
	if err != nil {
		return nil, errors.New("Docker metadata command failed")
	}
	return output, nil
}

func validateDisposableTarget(config liveConfig, getenv func(string) string, run dockerMetadataCommand) error {
	const (
		markerName  = "ARTISAN_SERVER_E2E_DISPOSABLE"
		markerValue = "artisan-server-e2e-compose-v1"
		markerLabel = "io.artisan-server.e2e.disposable"
	)
	if disposableProjectName.MatchString(config.projectName) == false {
		return errors.New("ARTISAN_SERVER_E2E_PROJECT_NAME must match the strict disposable project format")
	}
	baseURL, err := validateLoopbackBaseURL(config.baseURL)
	if err != nil || baseURL != config.baseURL {
		return errors.New("disposable target proof requires a canonical loopback base URL")
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT"} {
		if getenv(name) != "" {
			return fmt.Errorf("%s must be unset for disposable target proof", name)
		}
	}
	if run == nil {
		return errors.New("Docker is required for disposable target proof")
	}
	shown, err := run("context", "show")
	if err != nil || (string(shown) != "default\n" && string(shown) != "default\r\n") {
		return errors.New("Docker context must be exactly the local default context")
	}
	endpointOutput, err := run("context", "inspect", "default", "--format", `{{ (index .Endpoints "docker").Host }}`)
	if err != nil {
		return errors.New("Docker default context endpoint could not be inspected")
	}
	endpoint := strings.TrimSuffix(strings.TrimSuffix(string(endpointOutput), "\n"), "\r")
	parsedEndpoint, parseErr := url.Parse(endpoint)
	if parseErr != nil || !strings.HasPrefix(endpoint, "unix:///") || parsedEndpoint.Scheme != "unix" || parsedEndpoint.Host != "" || !strings.HasPrefix(parsedEndpoint.Path, "/") || parsedEndpoint.RawQuery != "" || parsedEndpoint.Fragment != "" || strings.ContainsAny(endpoint, "\r\n\x00") {
		return errors.New("Docker default context must use an absolute local Unix socket")
	}

	containerID := func(service string) (string, error) {
		output, commandErr := run(
			"container", "ls", "--no-trunc",
			"--filter", "label=com.docker.compose.project="+config.projectName,
			"--filter", "label=com.docker.compose.service="+service,
			"--filter", "status=running", "--format", "{{.ID}}",
		)
		identifiers := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
		if commandErr != nil || len(identifiers) != 1 || !dockerContainerID.MatchString(identifiers[0]) {
			return "", fmt.Errorf("exactly one running %s Compose container is required", service)
		}
		return identifiers[0], nil
	}
	apiID, err := containerID("api")
	if err != nil {
		return err
	}
	webID, err := containerID("web")
	if err != nil {
		return err
	}
	if apiID == webID {
		return errors.New("API and web Compose containers must be distinct")
	}
	inspectOutput, err := run("inspect", apiID, webID)
	if err != nil {
		return errors.New("Compose container metadata could not be inspected")
	}
	var documents []dockerInspectDocument
	if err := json.Unmarshal(inspectOutput, &documents); err != nil || len(documents) != 2 {
		return errors.New("Compose container metadata could not be inspected")
	}
	byID := make(map[string]dockerInspectDocument, len(documents))
	for _, document := range documents {
		if !dockerContainerID.MatchString(document.ID) {
			return errors.New("inspected Compose container ID is invalid")
		}
		if _, duplicate := byID[document.ID]; duplicate {
			return errors.New("inspected Compose container metadata is ambiguous")
		}
		byID[document.ID] = document
	}
	api, apiOK := byID[apiID]
	web, webOK := byID[webID]
	if !apiOK || !webOK {
		return errors.New("inspected Compose container IDs do not match selection")
	}
	validateComposeContainer := func(document dockerInspectDocument, service string) error {
		labels := document.Config.Labels
		if !document.State.Running || labels == nil ||
			labels["com.docker.compose.project"] != config.projectName ||
			labels["com.docker.compose.service"] != service ||
			labels["com.docker.compose.oneoff"] != "False" ||
			labels["com.docker.compose.container-number"] != "1" {
			return fmt.Errorf("%s container Compose metadata does not match the disposable target", service)
		}
		return nil
	}
	if err := validateComposeContainer(api, "api"); err != nil {
		return err
	}
	if err := validateComposeContainer(web, "web"); err != nil {
		return err
	}
	if api.Config.Labels[markerLabel] != markerValue {
		return errors.New("API container disposable label is missing or invalid")
	}
	requiredEnvironment := markerName + "=" + markerValue
	markerCount := 0
	for _, value := range api.Config.Environment {
		if strings.HasPrefix(value, markerName+"=") {
			markerCount++
			if value != requiredEnvironment {
				return errors.New("API container disposable environment marker is invalid")
			}
		}
	}
	if markerCount != 1 {
		return errors.New("API container disposable environment marker is missing or ambiguous")
	}

	parsedBase, _ := url.Parse(baseURL)
	basePort := parsedBase.Port()
	if basePort == "" {
		if parsedBase.Scheme == "https" {
			basePort = "443"
		} else {
			basePort = "80"
		}
	}
	bindings, exists := web.NetworkSettings.Ports["8080/tcp"]
	if !exists || len(bindings) != 1 || bindings[0].HostIP != parsedBase.Hostname() || bindings[0].HostPort != basePort {
		return errors.New("web container 8080/tcp binding does not exactly match ARTISAN_INTEGRATION_BASE_URL")
	}
	return nil
}

func loadLiveConfig(getenv func(string) string) (liveConfig, bool, error) {
	names := []string{
		"ARTISAN_CLI_BINARY", "ARTISAN_INTEGRATION_BASE_URL", "ARTISAN_INTEGRATION_ADMIN_EMAIL",
		"ARTISAN_INTEGRATION_ADMIN_PASSWORD", "ARTISAN_INTEGRATION_ADMIN_NICKNAME",
		"ARTISAN_INTEGRATION_MEMBER_EMAIL", "ARTISAN_INTEGRATION_MEMBER_PASSWORD", "ARTISAN_INTEGRATION_MEMBER_NICKNAME",
		"ARTISAN_INTEGRATION_MEMBER_TOKEN", "ARTISAN_INTEGRATION_MEMBER_CREDENTIAL_ID",
		"ARTISAN_INTEGRATION_REVIEW_MEMBER_TOKEN", "ARTISAN_INTEGRATION_REVIEW_MEMBER_CREDENTIAL_ID",
		"ARTISAN_INTEGRATION_FOREIGN_TOKEN", "ARTISAN_INTEGRATION_FOREIGN_CREDENTIAL_ID",
		"ARTISAN_INTEGRATION_FOREIGN_EMAIL", "ARTISAN_INTEGRATION_FOREIGN_NICKNAME",
		"ARTISAN_INTEGRATION_FOREIGN_ORGANIZATION_SLUG", "ARTISAN_INTEGRATION_SERVER_ROOT",
		"ARTISAN_INTEGRATION_ADMIN_ORGANIZATION", "ARTISAN_INTEGRATION_ADMIN_ORGANIZATION_SLUG",
		"ARTISAN_SERVER_E2E_PROJECT_NAME",
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
		return liveConfig{}, true, errors.New("live integration requires ARTISAN_SERVER_E2E_PROJECT_NAME and every explicit ARTISAN_CLI_BINARY and ARTISAN_INTEGRATION_* value")
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
		adminNickname: values[names[4]], memberEmail: values[names[5]], memberPassword: values[names[6]],
		memberNickname: values[names[7]], memberToken: values[names[8]], memberCredential: values[names[9]],
		reviewMemberToken: values[names[10]], reviewMemberCredential: values[names[11]],
		foreignToken: values[names[12]], foreignCredential: values[names[13]], foreignEmail: values[names[14]],
		foreignNickname: values[names[15]], foreignOrganizationSlug: values[names[16]], serverRoot: values[names[17]],
		organization: values[names[18]], organizationSlug: values[names[19]], projectName: values[names[20]],
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
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("ARTISAN_CLI_BINARY must name an existing executable regular file")
	}
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(filepath.Ext(resolved), ".exe") {
			return "", errors.New("ARTISAN_CLI_BINARY must name an existing .exe regular file")
		}
	} else if info.Mode()&0o111 == 0 {
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
	if runner.commandPace > 0 {
		timer := time.NewTimer(runner.commandPace)
		<-timer.C
	}
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

func (runner *cliRunner) runJSONError(t *testing.T, wantExit int, wantCode string, commandArgs ...string) {
	t.Helper()
	execution := runner.execute("", commandArgs...)
	commandIndex := len(runner.records) - 1
	if runner.forbiddenToken != "" {
		if scanErr := assertTokenAbsent(runner.forbiddenToken, runner.records, execution.err); scanErr != nil {
			t.Fatal(scanErr)
		}
	}
	if execution.overflow || execution.timedOut || execution.record.ExitCode != wantExit || execution.record.Stderr != "" {
		t.Fatalf("CLI command %d did not return bounded exit %d", commandIndex, wantExit)
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			HTTPStatus int    `json:"http_status"`
		} `json:"error"`
	}
	if err := decodeExactlyOneJSON([]byte(execution.record.Stdout), &envelope, true); err != nil || envelope.OK || envelope.Error.Code != wantCode || envelope.Error.HTTPStatus != http.StatusForbidden {
		t.Fatalf("CLI command %d returned an unstable permission error", commandIndex)
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

func issueCredential(t *testing.T, config liveConfig, email, password string) (*http.Client, string, string, string) {
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
	login := map[string]string{"email": email, "password": password, "organization": config.organizationSlug}
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

func assertExpectedMemberIdentity(t *testing.T, identity authIdentity, config liveConfig) {
	t.Helper()
	if identity.User.Email != config.memberEmail || identity.User.Nickname != config.memberNickname || identity.Organization.Name != config.organization || identity.Organization.Slug != config.organizationSlug || identity.Role != "member" {
		t.Fatalf("authenticated identity = user %q <%s>, organization %q (%s), role %q; want configured member", identity.User.Nickname, identity.User.Email, identity.Organization.Name, identity.Organization.Slug, identity.Role)
	}
}

func assertLotBalance(t *testing.T, value lot, onHand, reserved, available int64) {
	t.Helper()
	if value.OnHandGrams != onHand || value.ReservedGrams != reserved || value.AvailableGrams != available {
		t.Fatalf("lot balance = (%d, %d, %d), want (%d, %d, %d)", value.OnHandGrams, value.ReservedGrams, value.AvailableGrams, onHand, reserved, available)
	}
}

func int64Pointer(value int64) *int64    { return &value }
func stringPointer(value string) *string { return &value }

func assertLotPrice(t *testing.T, value lot, want *int64) {
	t.Helper()
	if !reflect.DeepEqual(value.PricePerKgEURCents, want) {
		t.Fatalf("lot %s price = %v, want %v", value.LotID, value.PricePerKgEURCents, want)
	}
}

func assertLotDescription(t *testing.T, value lot, want *string) {
	t.Helper()
	if !reflect.DeepEqual(value.Description, want) {
		t.Fatalf("lot %s description = %v, want %v", value.LotID, value.Description, want)
	}
}

func assertTotals(t *testing.T, got, want inventoryTotals) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory totals = %+v, want %+v", got, want)
	}
}

func assertReservationCost(t *testing.T, page reservationPage, clientUUID, state string, planned int64, actual, cost *int64) {
	t.Helper()
	for _, item := range page.Items {
		if item.ClientReservationUUID != clientUUID {
			continue
		}
		if item.State != state || item.PlannedGrams != planned || !reflect.DeepEqual(item.ActualGrams, actual) || !reflect.DeepEqual(item.RoastCostEURCents, cost) {
			t.Fatalf("reservation %s projection = %+v, want state=%s planned=%d actual=%v cost=%v", clientUUID, item, state, planned, actual, cost)
		}
		return
	}
	t.Fatalf("reservation %s was absent from projection: %+v", clientUUID, page.Items)
}

func readDesktopBeanLots(t *testing.T, client *http.Client, baseURL, token string) []map[string]json.RawMessage {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/inventory/bean-lots?limit=100", nil)
	if err != nil {
		t.Fatal("could not construct reduced desktop read request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("reduced desktop read request failed")
	}
	defer response.Body.Close()
	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := readBoundedJSON(response.Body, maxBrowserJSONBytes, token, &page); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("reduced desktop read returned HTTP %d with cache control %q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	return page.Items
}

func assertLotProjectionOmitsFields(t *testing.T, items []map[string]json.RawMessage, lotID string, fields ...string) {
	t.Helper()
	for _, item := range items {
		var candidate string
		if err := json.Unmarshal(item["lot_id"], &candidate); err != nil || candidate != lotID {
			continue
		}
		for _, field := range fields {
			if _, exists := item[field]; exists {
				t.Fatalf("lot projection unexpectedly exposed %s: %+v", field, item)
			}
		}
		return
	}
	t.Fatalf("lot projection did not contain disposable lot %s", lotID)
}

func assertLedger(t *testing.T, page ledgerPage) {
	t.Helper()
	if len(page.Items) != 6 {
		t.Fatalf("authoritative ledger contains %d entries, want 6", len(page.Items))
	}
	counts := map[string]int{}
	finalBalanceSeen := false
	for _, item := range page.Items {
		counts[item.Operation]++
		if item.ResultingOnHandGrams == 4850 && item.ResultingReservedGrams == 0 && item.ResultingAvailableGrams == 4850 {
			finalBalanceSeen = true
		}
	}
	want := map[string]int{"opening_balance": 1, "manual_adjustment": 1, "reservation": 2, "consumption": 1, "reservation_release": 1}
	if !reflect.DeepEqual(counts, want) || !finalBalanceSeen {
		t.Fatalf("authoritative ledger operations=%+v finalBalanceSeen=%v", counts, finalBalanceSeen)
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
