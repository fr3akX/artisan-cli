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
)

const pinnedServerRef = "4c0136fe98f6728f4bb94e416c5abe570e7f4831"

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

type cliRunner struct {
	binary         string
	baseURL        string
	env            []string
	forbiddenToken string
	records        []commandRecord
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
	ImageID  string `json:"image_id"`
	IsCover  bool   `json:"is_cover"`
	Position int64  `json:"position"`
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
	valid := []string{"http://localhost", "http://127.0.0.1:18080", "https://[::1]:8443"}
	for _, raw := range valid {
		if _, err := validateLoopbackBaseURL(raw); err != nil {
			t.Errorf("validateLoopbackBaseURL(%q): %v", raw, err)
		}
	}
	invalid := []string{
		"", "ftp://127.0.0.1", "http://example.com", "http://localhost.evil.invalid",
		"http://user@localhost", "http://localhost/", "http://localhost/path", "http://localhost?x=1",
		"http://localhost#fragment", "http://localhost:bad",
	}
	for _, raw := range invalid {
		if _, err := validateLoopbackBaseURL(raw); err == nil {
			t.Errorf("validateLoopbackBaseURL(%q) unexpectedly succeeded", raw)
		}
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

func TestIntegrationWorkflowContract(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "integration.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"permissions:\n  contents: read", pinnedServerRef, "repository: fr3akX/artisan-server",
		"integration/artisan-server.ref", "CGO_ENABLED: \"0\"", "go-version: 1.23.x",
		"scripts/e2e_compose.py", "compose.yaml", "compose.e2e.yaml", "down -v --remove-orphans",
		"if: always()", "ARTISAN_INTEGRATION_BASE_URL: http://127.0.0.1:18080",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("workflow missing %q", required)
		}
	}
	if strings.Contains(text, "workflow_dispatch") || strings.Contains(text, "docker compose") {
		t.Error("workflow must not expose dispatch targets or bypass the guarded Compose wrapper")
	}
	usesLine := regexp.MustCompile(`(?m)^\s*uses:\s*[^@\s]+@([^\s#]+)`)
	matches := usesLine.FindAllStringSubmatch(text, -1)
	if len(matches) < 3 {
		t.Fatalf("workflow has %d action uses, want checkout twice and setup-go", len(matches))
	}
	for _, match := range matches {
		if !fullSHA.MatchString(match[1]) {
			t.Errorf("action is not pinned by a full commit SHA: %s", match[0])
		}
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
	binary, err := filepath.Abs(config.binary)
	if err != nil {
		t.Fatal("ARTISAN_CLI_BINARY must resolve to an absolute path")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatal("ARTISAN_CLI_BINARY must name an existing executable regular file")
	}

	root := t.TempDir()
	for _, directory := range []string{"home", "config", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := cliRunner{
		binary:  binary,
		baseURL: config.baseURL,
		env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + filepath.Join(root, "home"),
			"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
			"XDG_STATE_HOME=" + filepath.Join(root, "state"),
			"TMPDIR=" + filepath.Join(root, "tmp"),
			"NO_PROXY=localhost,127.0.0.1,::1",
		},
	}

	httpClient, csrf, token, credentialID := issueCredential(t, config)
	runner.forbiddenToken = token
	defer revokeCredential(t, httpClient, config.baseURL, csrf, credentialID)
	defer func() {
		if err := assertTokenAbsent(token, runner.records, nil); err != nil {
			t.Error(err)
		}
	}()

	var identity map[string]any
	runner.runJSON(t, token+"\n", &identity, "auth", "login", "--token-stdin")
	if identity["role"] != "admin" {
		t.Fatalf("login identity role = %v, want admin", identity["role"])
	}
	var status map[string]any
	runner.runJSON(t, "", &status, "auth", "status")
	if status["role"] != "admin" {
		t.Fatalf("auth status role = %v, want admin", status["role"])
	}

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

	imagePath := filepath.Join(root, "fixture.png")
	writePNG(t, imagePath)
	var withImage lot
	runner.runJSON(t, "", &withImage,
		"inventory", "image", "add", "--caption", "0=Disposable integration image", "--alt-text", "0=Coffee sample",
		"--cover", "0", "--idempotency-key", "cli-"+runID+"-image", created.LotID, imagePath,
	)
	if len(withImage.Images) != 1 || !withImage.Images[0].IsCover {
		t.Fatalf("image add result = %+v, want one cover image", withImage.Images)
	}
	downloadPath := filepath.Join(root, "download.webp")
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
	if downloaded.Bytes != int64(len(downloadBytes)) || downloaded.Variant != "display" || len(downloadBytes) < 12 || string(downloadBytes[:4]) != "RIFF" || string(downloadBytes[8:12]) != "WEBP" {
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
	if err := assertTokenAbsent(token, runner.records, nil); err != nil {
		t.Fatal(err)
	}
	assertTreeTokenAbsent(t, root, token)
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
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("integration base URL must be a canonical loopback HTTP(S) origin")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path != "" {
		return "", errors.New("integration base URL must be a canonical loopback HTTP(S) origin")
	}
	hostname := parsed.Hostname()
	loopback := hostname == "localhost"
	if ip := net.ParseIP(hostname); ip != nil {
		loopback = ip.IsLoopback()
	}
	if !loopback {
		return "", errors.New("integration base URL host must be an exact loopback address")
	}
	if port := parsed.Port(); port != "" {
		value, portErr := strconv.Atoi(port)
		if portErr != nil || value < 1 || value > 65535 {
			return "", errors.New("integration base URL port is invalid")
		}
	}
	return parsed.String(), nil
}

func newCommandRecord(args []string, exitCode int, stdout, stderr []byte) commandRecord {
	return commandRecord{Args: append([]string(nil), args...), ExitCode: exitCode, Stdout: string(stdout), Stderr: string(stderr)}
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

func (runner *cliRunner) runJSON(t *testing.T, stdin string, target any, commandArgs ...string) {
	t.Helper()
	args := append([]string{"--json", "--server", runner.baseURL}, commandArgs...)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, runner.binary, args...)
	command.Env = append([]string(nil), runner.env...)
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	runner.records = append(runner.records, newCommandRecord(args, exitCode, stdout.Bytes(), stderr.Bytes()))
	if runner.forbiddenToken != "" {
		if scanErr := assertTokenAbsent(runner.forbiddenToken, runner.records, err); scanErr != nil {
			t.Fatal(scanErr)
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("CLI command %d exceeded its bounded timeout", len(runner.records)-1)
	}
	if err != nil {
		t.Fatalf("CLI command %d exited with status %d; safe stdout=%q stderr=%q", len(runner.records)-1, exitCode, bounded(stdout.String()), bounded(stderr.String()))
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(stdout.Bytes()), 2<<20))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&envelope); decodeErr != nil || !envelope.OK || len(envelope.Data) == 0 {
		t.Fatalf("CLI command %d returned an invalid success envelope", len(runner.records)-1)
	}
	if decodeErr := json.Unmarshal(envelope.Data, target); decodeErr != nil {
		t.Fatalf("CLI command %d returned unexpected structured data", len(runner.records)-1)
	}
}

func issueCredential(t *testing.T, config liveConfig) (*http.Client, string, string, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar:           jar,
		Timeout:       20 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var csrfResponse struct {
		CSRFToken string `json:"csrf_token"`
	}
	doJSON(t, client, http.MethodGet, config.baseURL+"/api/v1/session/csrf", "", nil, &csrfResponse, http.StatusOK)
	if csrfResponse.CSRFToken == "" {
		t.Fatal("browser CSRF endpoint returned an empty token")
	}
	login := map[string]string{"email": config.adminEmail, "password": config.adminPassword, "organization": config.organizationSlug}
	doJSON(t, client, http.MethodPost, config.baseURL+"/api/v1/session/login", csrfResponse.CSRFToken, login, &map[string]any{}, http.StatusOK)
	csrf := cookieValue(t, jar, config.baseURL, "artisan_server_csrf")
	issued := struct {
		Token      string `json:"token"`
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}{}
	doJSON(t, client, http.MethodPost, config.baseURL+"/api/v1/credentials", csrf, map[string]string{"name": "CLI integration " + randomHex(t, 8)}, &issued, http.StatusCreated)
	if strings.TrimSpace(issued.Token) == "" || strings.ContainsAny(issued.Token, "\r\n") || issued.Credential.ID == "" {
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

func revokeCredential(t *testing.T, client *http.Client, baseURL, csrf, credentialID string) {
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
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode != http.StatusNoContent {
		t.Errorf("credential cleanup returned HTTP %d", response.StatusCode)
	}
}

func doJSON(t *testing.T, client *http.Client, method, target, csrf string, payload, responseTarget any, expectedStatus int) {
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
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		t.Fatal("browser API redirect was rejected")
	}
	if response.StatusCode != expectedStatus {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		t.Fatalf("browser API returned HTTP %d, want %d", response.StatusCode, expectedStatus)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(responseTarget); err != nil {
		t.Fatal("browser API returned invalid JSON")
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

func assertTreeTokenAbsent(t *testing.T, root, token string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(contents, []byte(token)) {
			return errors.New("issued token remained in isolated CLI files after logout")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func bounded(value string) string {
	const limit = 2048
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
}
