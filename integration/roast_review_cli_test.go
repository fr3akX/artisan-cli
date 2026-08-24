package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
)

const reviewProfileFixtureSHA256 = "c7f4771917845c69dee2b1ae4788a37c02e43cdf5614f2afc93faddb57681aa7"

func TestCLIRunnerOptionalPaceIsBoundedAndNotResponseDriven(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	root := canonicalTempDir(t)
	script := filepath.Join(root, "paced-command")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '{\"ok\":true}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := cliRunner{
		binary: script, baseURL: "http://127.0.0.1", cwd: root,
		env: []string{"PATH=" + os.Getenv("PATH")}, commandPace: 50 * time.Millisecond,
	}
	started := time.Now()
	execution := runner.execute("")
	if execution.err != nil || time.Since(started) < 50*time.Millisecond || time.Since(started) > time.Second {
		t.Fatalf("paced command duration=%s error=%v", time.Since(started), execution.err)
	}
}

func TestCLIErrorEnvelopeStrictWireContract(t *testing.T) {
	validCases := []struct {
		name        string
		contents    string
		wantCode    string
		wantMessage string
		wantStatus  *int
	}{
		{"omitted HTTP status", `{"ok":false,"error":{"code":"local_storage_error","message":"Destination already exists"}}`, "local_storage_error", "Destination already exists", nil},
		{"lower HTTP status boundary", `{"ok":false,"error":{"code":"server_error","message":"Request failed","http_status":100}}`, "server_error", "Request failed", intPointer(100)},
		{"upper HTTP status boundary", `{"ok":false,"error":{"code":"server_error","message":"Request failed","http_status":599}}`, "server_error", "Request failed", intPointer(599)},
	}
	for _, test := range validCases {
		t.Run(test.name, func(t *testing.T) {
			envelope, err := decodeCLIErrorEnvelope([]byte(test.contents))
			if err != nil || envelope.OK || envelope.Error.Code != test.wantCode || envelope.Error.Message != test.wantMessage || test.wantStatus == nil && envelope.Error.HTTPStatus != nil || test.wantStatus != nil && (envelope.Error.HTTPStatus == nil || *envelope.Error.HTTPStatus != *test.wantStatus) {
				t.Fatalf("valid CLI error envelope = (%+v, %v)", envelope, err)
			}
		})
	}

	mutations := []struct {
		name     string
		contents string
	}{
		{"missing ok", `{"error":{"code":"local_storage_error","message":"Destination already exists"}}`},
		{"missing error", `{"ok":false}`},
		{"missing code", `{"ok":false,"error":{"message":"Destination already exists"}}`},
		{"missing message", `{"ok":false,"error":{"code":"local_storage_error"}}`},
		{"extra envelope field", `{"ok":false,"extra":true,"error":{"code":"local_storage_error","message":"Destination already exists"}}`},
		{"extra error field", `{"ok":false,"error":{"code":"local_storage_error","message":"Destination already exists","detail":"unsafe"}}`},
		{"wrong ok value", `{"ok":true,"error":{"code":"local_storage_error","message":"Destination already exists"}}`},
		{"wrong code type", `{"ok":false,"error":{"code":7,"message":"Destination already exists"}}`},
		{"wrong message type", `{"ok":false,"error":{"code":"local_storage_error","message":7}}`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if _, err := decodeCLIErrorEnvelope([]byte(mutation.contents)); err == nil {
				t.Fatal("invalid CLI error envelope was accepted")
			}
		})
	}

	invalidStatuses := []struct {
		name   string
		status string
	}{
		{"explicit null HTTP status", "null"},
		{"below HTTP status boundary", "99"},
		{"above HTTP status boundary", "600"},
		{"negative HTTP status", "-1"},
		{"fractional HTTP status", "100.5"},
		{"string HTTP status", `"404"`},
		{"boolean HTTP status", "true"},
		{"object HTTP status", `{}`},
		{"array HTTP status", `[]`},
		{"overflowing HTTP status", "9223372036854775808"},
	}
	for _, test := range invalidStatuses {
		t.Run(test.name, func(t *testing.T) {
			contents := fmt.Sprintf(`{"ok":false,"error":{"code":"server_error","message":"Request failed","http_status":%s}}`, test.status)
			if _, err := decodeCLIErrorEnvelope([]byte(contents)); err == nil {
				t.Fatal("invalid CLI HTTP status was accepted")
			}
		})
	}
}

func intPointer(value int) *int {
	return &value
}

func TestDisposableProvisioningIncludesForeignTenantAdministratorCredential(t *testing.T) {
	contents, err := os.ReadFile("provision_member.py")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"ARTISAN_E2E_FOREIGN_EMAIL", "ARTISAN_E2E_FOREIGN_NICKNAME",
		"ARTISAN_E2E_FOREIGN_ORGANIZATION_SLUG", "Foreign review integration",
		"CLI member review integration",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("disposable provisioning is missing %q", required)
		}
	}
	foreignStart := strings.Index(text, "foreign_organization = Organization(")
	foreignEnd := strings.Index(text, "foreign_credential, foreign_token = await issue_api_credential(")
	if foreignStart < 0 || foreignEnd <= foreignStart || !strings.Contains(text[foreignStart:foreignEnd], `role="admin"`) {
		t.Fatal("foreign disposable identity is not provisioned as its tenant administrator")
	}
	if strings.Count(text, "foreign_credential, foreign_token = await issue_api_credential(") != 1 {
		t.Fatal("foreign provisioning must issue exactly the one credential exercised and revoked by the integration")
	}
}

func TestBrowserSessionLoginRoutesExactOrganizationWithoutIssuingCredential(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v1/session/csrf":
			_, _ = io.WriteString(w, `{"csrf_token":"csrf-response"}`)
		case "POST /api/v1/session/login":
			if request.Header.Get("X-CSRF-Token") != "csrf-response" {
				t.Errorf("login CSRF header = %q", request.Header.Get("X-CSRF-Token"))
			}
			var login map[string]string
			if err := json.NewDecoder(request.Body).Decode(&login); err != nil {
				t.Errorf("decode login request: %v", err)
				return
			}
			if login["email"] != "foreign@example.test" || login["password"] != "disposable-password" || login["organization"] != "foreign-review-e2e" || len(login) != 3 {
				t.Errorf("login route = %#v", login)
			}
			http.SetCookie(w, &http.Cookie{Name: "artisan_server_csrf", Value: "csrf-cookie", Path: "/"})
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected browser-session request %s %s", request.Method, request.URL.Path)
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	client, csrf := loginBrowserSession(t, server.URL, "foreign@example.test", "disposable-password", "foreign-review-e2e")
	if client == nil || csrf != "csrf-cookie" {
		t.Fatalf("browser session = (%v, %q)", client, csrf)
	}
	if got := strings.Join(paths, ","); got != "GET /api/v1/session/csrf,POST /api/v1/session/login" {
		t.Fatalf("browser session requests = %s", got)
	}
}

func TestRoastReviewInspectorContract(t *testing.T) {
	contents, err := os.ReadFile("inspect_roast_reviews.py")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`ARTISAN_SERVER_E2E_DISPOSABLE`, `artisan-server-e2e-compose-v1`,
		`/proc/1/environ`, `ARTISAN_E2E_EXPECTED_PROJECT`, `COMPOSE_PROJECT_NAME`,
		`my-roastery`, `RoastReviewComment`, `comment.created`,
		`json.dumps`, `sort_keys=True`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("review inspector is missing %q", required)
		}
	}
	for _, forbidden := range []string{"print(token", "password", "raw_object_key", "body_sha256", "request_fingerprint", "review_key"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("review inspector contains forbidden output material %q", forbidden)
		}
	}
}

func TestIndependentChartRepresentationIsBoundedAndAuthenticated(t *testing.T) {
	compressed := gzipBytes(t, []byte(`{"schema_version":1}`))
	token := "independent-chart-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/roasts/"+strings.Repeat("a", 32)+"/chart" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("request headers = %v", request.Header)
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(compressed)
	}))
	defer server.Close()

	got, err := readBoundedChartRepresentation(server.URL, token, strings.Repeat("a", 32))
	if err != nil || !bytes.Equal(got, compressed) {
		t.Fatalf("independent chart representation: bytes=%d err=%v", len(got), err)
	}

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(maxIndependentChartRepresentationBytes+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer oversized.Close()
	if _, err := readBoundedChartRepresentation(oversized.URL, token, strings.Repeat("a", 32)); err == nil {
		t.Fatal("oversized independent chart representation was accepted")
	}
}

func TestRevokedCredentialCheckIsBoundedAndAuthenticated(t *testing.T) {
	token := "revoked-check-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/auth/me" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_credentials"}}`)
	}))
	defer server.Close()

	if err := revokedCredentialRejected(newBrowserClient(nil), server.URL, token); err != nil {
		t.Fatal(err)
	}

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxBrowserJSONBytes+1))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer oversized.Close()
	if err := revokedCredentialRejected(newBrowserClient(nil), oversized.URL, token); err == nil {
		t.Fatal("oversized revoked-credential response was accepted")
	}
}

func gzipBytes(t *testing.T, contents []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func TestReviewProfileFixtureContract(t *testing.T) {
	contents, err := os.ReadFile("testdata/review-profile.alog")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	if got := hex.EncodeToString(digest[:]); got != reviewProfileFixtureSHA256 {
		t.Fatalf("review profile fixture SHA-256 = %s, want fixed reviewed digest", got)
	}
	if !bytes.Contains(contents, []byte("'version': '4.2.1'")) ||
		!bytes.Contains(contents, []byte("'timex':")) ||
		!bytes.Contains(contents, []byte("'special_events':")) ||
		!bytes.Contains(contents, []byte("'extra_curves':")) {
		t.Fatal("review profile fixture lacks required Artisan 4.x chart/event/control coverage")
	}

	text := string(contents)
	for name, pattern := range map[string]string{
		"URL":                    `(?i)[a-z][a-z0-9+.-]*://`,
		"external Unix path":     `(?:^|[[:space:]'\"])/(?:[^/[:space:]'\"]+/)+`,
		"external Windows path":  `(?i)[a-z]:\\`,
		"credential material":    `(?i)token|password|secret|api[ _-]?key|credential`,
		"executable instruction": `(?im)(?:^|[[:space:]])(?:run|execute|invoke|curl|wget|powershell|bash|sh)[[:space:]]`,
	} {
		if regexp.MustCompile(pattern).MatchString(text) {
			t.Errorf("review profile fixture contains forbidden %s", name)
		}
	}
	if strings.ContainsRune(text, '\x00') {
		t.Fatal("review profile fixture contains NUL")
	}
}

type integrationRoastSummary struct {
	RoastUUID     string  `json:"roast_uuid"`
	State         string  `json:"state"`
	Title         *string `json:"title"`
	RevisionCount int64   `json:"revision_count"`
}

type integrationRoastPage struct {
	Items []integrationRoastSummary `json:"items"`
}

type integrationRevision struct {
	RevisionNumber int64           `json:"revision_number"`
	SHA256         string          `json:"sha256"`
	ByteSize       int64           `json:"byte_size"`
	ParserVersion  string          `json:"parser_version"`
	ParseState     string          `json:"parse_state"`
	Metadata       json.RawMessage `json:"metadata"`
}

type integrationRoastDetail struct {
	integrationRoastSummary
	CurrentMetadata json.RawMessage      `json:"current_metadata"`
	CurrentRevision *integrationRevision `json:"current_revision"`
}

type integrationRevisionPage struct {
	Items []integrationRevision `json:"items"`
}

type integrationComment struct {
	CommentUUID    string  `json:"comment_uuid"`
	RoastUUID      string  `json:"roast_uuid"`
	AuthorNickname string  `json:"author_nickname"`
	Body           *string `json:"body"`
	IsDeleted      bool    `json:"is_deleted"`
}

type integrationCommentPage struct {
	Items []integrationComment `json:"items"`
}

type integrationChartDownload struct {
	Path               string `json:"path"`
	RoastUUID          string `json:"roast_uuid"`
	RevisionNumber     int64  `json:"revision_number"`
	RevisionSHA256     string `json:"revision_sha256"`
	ParserVersion      string `json:"parser_version"`
	ChartSchemaVersion int64  `json:"chart_schema_version"`
	CompressedBytes    int64  `json:"compressed_bytes"`
	CompressedSHA256   string `json:"compressed_sha256"`
	FileBytes          int64  `json:"file_bytes"`
	FileSHA256         string `json:"file_sha256"`
}

type integrationProfileDownload struct {
	Path           string `json:"path"`
	RoastUUID      string `json:"roast_uuid"`
	RevisionNumber int64  `json:"revision_number"`
	Bytes          int64  `json:"bytes"`
	SHA256         string `json:"sha256"`
}

type integrationReviewResult struct {
	Comment          integrationComment `json:"comment"`
	RevisionSHA256   string             `json:"revision_sha256"`
	TemplateVersion  string             `json:"template_version"`
	IdempotentReplay bool               `json:"idempotent_replay"`
}

type roastReviewInspection struct {
	AuditCount     int      `json:"audit_count"`
	CommentCount   int      `json:"comment_count"`
	CommentIDs     []string `json:"comment_ids"`
	SlotCommentIDs []string `json:"slot_comment_ids"`
	SlotCount      int      `json:"slot_count"`
}

func TestRoastReviewCLIAgainstArtisanServer(t *testing.T) {
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

	fixture, err := os.ReadFile("testdata/review-profile.alog")
	if err != nil {
		t.Fatal(err)
	}
	roastUUID := randomCanonicalUUID(t)
	runID := randomHex(t, 8)
	title := "CLI review " + runID
	revisionOne := profileFixtureForRoast(t, fixture, roastUUID, title)
	revisionOneSHA := digestHex(revisionOne)
	revisionTwo := bytes.Replace(revisionOne, []byte("'ambient_temp': 23.5"), []byte("'ambient_temp': 24.5"), 1)
	if bytes.Equal(revisionOne, revisionTwo) {
		t.Fatal("revision-two fixture mutation did not apply")
	}
	revisionTwoSHA := digestHex(revisionTwo)

	root := canonicalTempDir(t)
	adminRunner, adminPaths := newReviewRunner(t, root, "admin", binary, config.baseURL)
	memberRunner, memberPaths := newReviewRunner(t, root, "member", binary, config.baseURL)
	adminHTTP, csrf, adminToken, adminCredentialID := issueCredential(t, config, config.adminEmail, config.adminPassword)
	foreignHTTP, foreignCSRF := loginBrowserSession(t, config.baseURL, config.foreignEmail, config.memberPassword, config.foreignOrganizationSlug)
	adminRunner.forbiddenToken = adminToken
	memberRunner.forbiddenToken = config.reviewMemberToken
	defer revokeCredentialAndAssertRejected(t, adminHTTP, config.baseURL, csrf, adminCredentialID, adminToken)
	defer revokeCredentialAndAssertRejected(t, adminHTTP, config.baseURL, csrf, config.reviewMemberCredential, config.reviewMemberToken)
	defer revokeCredentialAndAssertRejected(t, foreignHTTP, config.baseURL, foreignCSRF, config.foreignCredential, config.foreignToken)
	defer func() {
		for _, check := range []struct {
			token  string
			runner *cliRunner
			paths  []string
		}{
			{adminToken, adminRunner, adminPaths},
			{config.reviewMemberToken, memberRunner, memberPaths},
		} {
			if err := assertTokenAbsent(check.token, check.runner.records, nil); err != nil {
				t.Error(err)
			}
			if err := assertTokenAbsentFromTrees(check.token, check.paths...); err != nil {
				t.Error(err)
			}
		}
	}()
	adminLoggedIn, memberLoggedIn := true, true
	defer func() {
		if adminLoggedIn {
			if err := adminRunner.cleanupLogout(); err != nil {
				t.Error(err)
			}
		}
		if memberLoggedIn {
			if err := memberRunner.cleanupLogout(); err != nil {
				t.Error(err)
			}
		}
	}()

	var adminIdentity, memberIdentity authIdentity
	adminRunner.runJSON(t, adminToken+"\n", &adminIdentity, "auth", "login", "--token-stdin")
	memberRunner.runJSON(t, config.reviewMemberToken+"\n", &memberIdentity, "auth", "login", "--token-stdin")
	assertExpectedIdentity(t, adminIdentity, config)
	assertExpectedMemberIdentity(t, memberIdentity, config)

	uploadRoastRevision(t, config, config.reviewMemberToken, roastUUID, revisionOne, "review-"+runID+"-revision-1")
	for _, role := range []struct {
		name   string
		token  string
		runner *cliRunner
		paths  []string
	}{
		{"admin", adminToken, adminRunner, adminPaths},
		{"member", config.reviewMemberToken, memberRunner, memberPaths},
	} {
		t.Run(role.name+" read and download", func(t *testing.T) {
			assertRoastReadsAndDownloads(t, role.runner, role.paths[len(role.paths)-1], role.name, config.baseURL, role.token, roastUUID, title, revisionOne, revisionOneSHA)
		})
	}

	memberBody := reviewBody(1, revisionOneSHA, "Member first-writer analysis")
	adminBody := reviewBody(1, revisionOneSHA, "Administrator alternate analysis")
	memberBodyPath := writeReviewBody(t, memberPaths[len(memberPaths)-1], "member-review.txt", memberBody)
	adminBodyPath := writeReviewBody(t, adminPaths[len(adminPaths)-1], "admin-review.txt", adminBody)
	var memberCreated, adminReplayed integrationReviewResult
	memberRunner.runJSON(t, "", &memberCreated, "roast", "review", "post", roastUUID,
		"--revision-sha256", revisionOneSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", memberBodyPath)
	adminRunner.runJSON(t, "", &adminReplayed, "roast", "review", "post", roastUUID,
		"--revision-sha256", revisionOneSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", adminBodyPath)
	assertReviewPair(t, memberCreated, adminReplayed, roastUUID, revisionOneSHA, memberBody)
	assertReviewComments(t, memberRunner, roastUUID, []integrationReviewResult{memberCreated})
	assertInspection(t, inspectRoastReviews(t, config, roastUUID), []string{memberCreated.Comment.CommentUUID})

	uploadRoastRevision(t, config, adminToken, roastUUID, revisionTwo, "review-"+runID+"-revision-2")
	var oldReplay integrationReviewResult
	memberRunner.runJSON(t, "", &oldReplay, "roast", "review", "post", roastUUID,
		"--revision-sha256", revisionOneSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", memberBodyPath)
	if !oldReplay.IdempotentReplay || oldReplay.Comment.CommentUUID != memberCreated.Comment.CommentUUID {
		t.Fatalf("old revision replay = %+v", oldReplay)
	}

	staleSHA := strings.Repeat("0", 64)
	staleBodyPath := writeReviewBody(t, memberPaths[len(memberPaths)-1], "stale-review.txt", reviewBody(1, staleSHA, "Never-posted stale analysis"))
	runCLIError(t, memberRunner, 7, "roast_revision_changed", 0, "roast", "review", "post", roastUUID,
		"--revision-sha256", staleSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", staleBodyPath)
	assertInspection(t, inspectRoastReviews(t, config, roastUUID), []string{memberCreated.Comment.CommentUUID})

	adminRevisionTwoBody := reviewBody(2, revisionTwoSHA, "Administrator revision-two analysis")
	memberRevisionTwoBody := reviewBody(2, revisionTwoSHA, "Member alternate revision-two analysis")
	adminRevisionTwoPath := writeReviewBody(t, adminPaths[len(adminPaths)-1], "admin-review-2.txt", adminRevisionTwoBody)
	memberRevisionTwoPath := writeReviewBody(t, memberPaths[len(memberPaths)-1], "member-review-2.txt", memberRevisionTwoBody)
	var adminRevisionTwo, memberRevisionTwoReplay integrationReviewResult
	adminRunner.runJSON(t, "", &adminRevisionTwo, "roast", "review", "post", roastUUID,
		"--revision-sha256", revisionTwoSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", adminRevisionTwoPath)
	memberRunner.runJSON(t, "", &memberRevisionTwoReplay, "roast", "review", "post", roastUUID,
		"--revision-sha256", revisionTwoSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", memberRevisionTwoPath)
	assertReviewPair(t, adminRevisionTwo, memberRevisionTwoReplay, roastUUID, revisionTwoSHA, adminRevisionTwoBody)
	assertReviewComments(t, adminRunner, roastUUID, []integrationReviewResult{adminRevisionTwo, memberCreated})
	assertInspection(t, inspectRoastReviews(t, config, roastUUID), []string{memberCreated.Comment.CommentUUID, adminRevisionTwo.Comment.CommentUUID})

	assertCookieOnlyReviewRejected(t, adminHTTP, config, roastUUID, revisionTwoSHA, adminRevisionTwoBody)
	assertForeignTenantHidden(t, config, roastUUID, revisionTwoSHA, adminRevisionTwoBody)
	trashRoast(t, adminHTTP, config, csrf, roastUUID)
	assertTrashedRoastHidden(t, memberRunner, memberPaths[len(memberPaths)-1], roastUUID, title, revisionTwoSHA, memberRevisionTwoPath)

	var logout struct {
		LoggedOut bool `json:"logged_out"`
	}
	memberRunner.runJSON(t, "", &logout, "auth", "logout")
	if !logout.LoggedOut {
		t.Fatal("member logout did not report success")
	}
	memberLoggedIn = false
	adminRunner.runJSON(t, "", &logout, "auth", "logout")
	if !logout.LoggedOut {
		t.Fatal("admin logout did not report success")
	}
	adminLoggedIn = false
}

func randomCanonicalUUID(t *testing.T) string {
	t.Helper()
	raw, err := hex.DecodeString(randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return hex.EncodeToString(raw)
}

func profileFixtureForRoast(t *testing.T, fixture []byte, roastUUID, title string) []byte {
	t.Helper()
	const fixtureUUID = "11111111111141118111111111111111"
	if strings.Count(string(fixture), fixtureUUID) != 1 || strings.Count(string(fixture), "Archive Fixture") != 1 {
		t.Fatal("review fixture identity/title placeholders are ambiguous")
	}
	profile := bytes.Replace(fixture, []byte(fixtureUUID), []byte(roastUUID), 1)
	profile = bytes.Replace(profile, []byte("Archive Fixture"), []byte(title), 1)
	return profile
}

func digestHex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func newReviewRunner(t *testing.T, root, name, binary, baseURL string) (*cliRunner, []string) {
	t.Helper()
	var paths []string
	for _, leaf := range []string{"home", "config", "state", "tmp", "run"} {
		path := filepath.Join(root, name, leaf)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return &cliRunner{
		binary: binary, baseURL: baseURL, cwd: paths[4], commandPace: 250 * time.Millisecond,
		env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + paths[0], "XDG_CONFIG_HOME=" + paths[1], "XDG_STATE_HOME=" + paths[2], "TMPDIR=" + paths[3]},
	}, paths
}

func uploadRoastRevision(t *testing.T, config liveConfig, token, roastUUID string, payload []byte, idempotencyKey string) integrationRevision {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{"sha256": digestHex(payload), "idempotency_key": idempotencyKey} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("profile", "review-profile.alog")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, config.baseURL+"/api/v1/roasts/"+roastUUID+"/revisions", &body)
	if err != nil {
		t.Fatal("construct revision upload")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := doBoundedBearerRequest(t, request, token, http.StatusCreated)
	var result struct {
		RoastUUID string              `json:"roast_uuid"`
		State     string              `json:"state"`
		Revision  integrationRevision `json:"revision"`
	}
	if err := decodeExactlyOneJSON(response, &result, false); err != nil {
		t.Fatal("revision upload returned invalid JSON")
	}
	if result.RoastUUID != roastUUID || result.State != "parsed" || result.Revision.SHA256 != digestHex(payload) || result.Revision.ParseState != "parsed" || result.Revision.ByteSize != int64(len(payload)) {
		t.Fatalf("revision upload result = %+v", result)
	}
	return result.Revision
}

const maxIndependentChartRepresentationBytes = int64(64 << 20)

func readBoundedChartRepresentation(baseURL, token, roastUUID string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/roasts/"+roastUUID+"/chart", nil)
	if err != nil {
		return nil, errors.New("construct independent chart request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	// An explicit Accept-Encoding preserves the compressed transfer bytes.
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := newBrowserClient(nil).Do(request)
	if err != nil {
		return nil, errors.New("independent chart request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("independent chart request returned HTTP %d", response.StatusCode)
	}
	if encodings := response.Header.Values("Content-Encoding"); len(encodings) != 1 || encodings[0] != "gzip" {
		return nil, errors.New("independent chart response was not one gzip representation")
	}
	if response.ContentLength > maxIndependentChartRepresentationBytes {
		return nil, errors.New("independent chart response exceeded its bound")
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxIndependentChartRepresentationBytes+1))
	if err != nil {
		return nil, errors.New("independent chart response could not be read")
	}
	if int64(len(contents)) > maxIndependentChartRepresentationBytes {
		return nil, errors.New("independent chart response exceeded its bound")
	}
	if token != "" && bytes.Contains(contents, []byte(token)) {
		return nil, errBrowserTokenExposure
	}
	return contents, nil
}

func revokedCredentialRejected(client *http.Client, baseURL, token string) error {
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/auth/me", nil)
	if err != nil {
		return errors.New("construct revoked credential check")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return errors.New("revoked credential check failed")
	}
	defer response.Body.Close()
	if response.ContentLength > maxBrowserJSONBytes {
		return errors.New("revoked credential response exceeded its bound")
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxBrowserJSONBytes+1))
	if err != nil {
		return errors.New("revoked credential response could not be read")
	}
	if len(contents) > maxBrowserJSONBytes {
		return errors.New("revoked credential response exceeded its bound")
	}
	if token != "" && bytes.Contains(contents, []byte(token)) {
		return errBrowserTokenExposure
	}
	if response.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("revoked credential was not rejected: HTTP %d", response.StatusCode)
	}
	return nil
}

func revokeCredentialAndAssertRejected(t *testing.T, client *http.Client, baseURL, csrf, credentialID, token string) {
	t.Helper()
	revokeCredential(t, client, baseURL, csrf, credentialID, token)
	if err := revokedCredentialRejected(newBrowserClient(nil), baseURL, token); err != nil {
		t.Error(err)
	}
}

func doBoundedBearerRequest(t *testing.T, request *http.Request, forbiddenToken string, expectedStatus int) []byte {
	t.Helper()
	client := newBrowserClient(nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("disposable bearer request failed")
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxBrowserJSONBytes+1))
	if err != nil || len(contents) > maxBrowserJSONBytes {
		t.Fatal("disposable bearer response exceeded its bound")
	}
	if forbiddenToken != "" && bytes.Contains(contents, []byte(forbiddenToken)) {
		t.Fatal(errBrowserTokenExposure)
	}
	if response.StatusCode != expectedStatus {
		t.Fatalf("disposable bearer request returned HTTP %d, want %d", response.StatusCode, expectedStatus)
	}
	return contents
}

func assertRoastReadsAndDownloads(t *testing.T, runner *cliRunner, runDirectory, role, baseURL, token, roastUUID, title string, expectedProfile []byte, expectedSHA string) {
	t.Helper()
	var page integrationRoastPage
	runner.runJSON(t, "", &page, "roast", "list", "--search", title)
	if len(page.Items) != 1 || page.Items[0].RoastUUID != roastUUID || page.Items[0].Title == nil || *page.Items[0].Title != title || page.Items[0].State != "parsed" || page.Items[0].RevisionCount != 1 {
		t.Fatalf("%s roast list = %+v", role, page.Items)
	}
	var detail integrationRoastDetail
	runner.runJSON(t, "", &detail, "roast", "show", roastUUID)
	if detail.RoastUUID != roastUUID || detail.CurrentRevision == nil || detail.CurrentRevision.RevisionNumber != 1 || detail.CurrentRevision.SHA256 != expectedSHA || detail.CurrentRevision.ParseState != "parsed" {
		t.Fatalf("%s roast detail = %+v", role, detail)
	}
	var revisions integrationRevisionPage
	runner.runJSON(t, "", &revisions, "roast", "revisions", roastUUID, "--all")
	if len(revisions.Items) != 1 || revisions.Items[0].SHA256 != expectedSHA || revisions.Items[0].ParserVersion != detail.CurrentRevision.ParserVersion {
		t.Fatalf("%s revisions = %+v", role, revisions.Items)
	}
	var comments integrationCommentPage
	runner.runJSON(t, "", &comments, "roast", "comment", "list", roastUUID, "--all")
	if len(comments.Items) != 0 {
		t.Fatalf("%s initial comments = %+v", role, comments.Items)
	}

	chartPath := filepath.Join(runDirectory, role+"-chart.json")
	var chart integrationChartDownload
	runner.runJSON(t, "", &chart, "roast", "chart", "download", roastUUID, chartPath)
	chartBytes, err := os.ReadFile(chartPath)
	if err != nil {
		t.Fatal(err)
	}
	compressedChart, err := readBoundedChartRepresentation(baseURL, token, roastUUID)
	if err != nil {
		t.Fatal(err)
	}
	if chart.Path != chartPath || chart.RoastUUID != roastUUID || chart.RevisionNumber != 1 || chart.RevisionSHA256 != expectedSHA || chart.ChartSchemaVersion != 1 || chart.FileBytes != int64(len(chartBytes)) || chart.FileSHA256 != digestHex(chartBytes) || chart.CompressedBytes != int64(len(compressedChart)) || chart.CompressedSHA256 != digestHex(compressedChart) {
		t.Fatalf("%s chart result = %+v", role, chart)
	}
	assertChartCoreCoverage(t, chartBytes, chart.ParserVersion)
	assertPrivateRegularFile(t, chartPath)
	chartSnapshot, err := snapshotPrivateFile(chartPath)
	if err != nil {
		t.Fatal(err)
	}
	runCLIError(t, runner, 3, "local_storage_error", 0, "roast", "chart", "download", roastUUID, chartPath)
	assertPrivateFileMatchesSnapshot(t, chartPath, chartSnapshot)

	profilePath := filepath.Join(runDirectory, role+"-profile.alog")
	var profile integrationProfileDownload
	runner.runJSON(t, "", &profile, "roast", "profile", "download", roastUUID, "1", profilePath)
	profileBytes, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(profileBytes, expectedProfile) || profile.Path != profilePath || profile.RoastUUID != roastUUID || profile.RevisionNumber != 1 || profile.Bytes != int64(len(expectedProfile)) || profile.SHA256 != expectedSHA || digestHex(profileBytes) != expectedSHA {
		t.Fatalf("%s profile download identity mismatch", role)
	}
	assertPrivateRegularFile(t, profilePath)
	profileSnapshot, err := snapshotPrivateFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	runCLIError(t, runner, 3, "local_storage_error", 0, "roast", "profile", "download", roastUUID, "1", profilePath)
	assertPrivateFileMatchesSnapshot(t, profilePath, profileSnapshot)
}

func assertChartCoreCoverage(t *testing.T, contents []byte, parserVersion string) {
	t.Helper()
	var chart struct {
		SchemaVersion int64  `json:"schema_version"`
		ParserVersion string `json:"parser_version"`
		Core          struct {
			Time []float64 `json:"time_seconds"`
			BT   []float64 `json:"bt"`
			ET   []float64 `json:"et"`
		} `json:"core"`
		Events struct {
			Milestones []json.RawMessage `json:"milestones"`
			Special    []json.RawMessage `json:"special"`
		} `json:"events"`
		Control struct {
			Markers []json.RawMessage `json:"markers"`
			Steps   []json.RawMessage `json:"steps"`
		} `json:"control"`
		Extra struct {
			Series []json.RawMessage `json:"series"`
		} `json:"extra"`
	}
	if err := decodeExactlyOneJSON(contents, &chart, false); err != nil || chart.SchemaVersion != 1 || chart.ParserVersion != parserVersion || len(chart.Core.Time) < 2 || len(chart.Core.Time) != len(chart.Core.BT) || len(chart.Core.BT) != len(chart.Core.ET) || len(chart.Events.Milestones) == 0 || len(chart.Events.Special) == 0 || chart.Control.Markers == nil || chart.Control.Steps == nil || len(chart.Extra.Series) == 0 {
		t.Fatalf("downloaded chart lacks validated core/event/control coverage")
	}
}

func assertPrivateFileMatchesSnapshot(t *testing.T, path string, snapshot privateFileSnapshot) {
	t.Helper()
	if err := privateFileMatchesSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
}

func assertPrivateRegularFile(t *testing.T, path string) {
	t.Helper()
	if _, err := snapshotPrivateFile(path); err != nil {
		t.Fatal(err)
	}
}

type cliErrorEnvelope struct {
	OK    bool
	Error struct {
		Code       string
		Message    string
		HTTPStatus *int
	}
}

func decodeCLIErrorEnvelope(contents []byte) (cliErrorEnvelope, error) {
	var wire struct {
		OK    *bool `json:"ok"`
		Error *struct {
			Code       *string         `json:"code"`
			Message    *string         `json:"message"`
			HTTPStatus json.RawMessage `json:"http_status"`
		} `json:"error"`
	}
	if err := decodeExactlyOneJSON(contents, &wire, true); err != nil || wire.OK == nil || *wire.OK || wire.Error == nil || wire.Error.Code == nil || wire.Error.Message == nil {
		return cliErrorEnvelope{}, errors.New("CLI error response did not match its strict wire contract")
	}
	var httpStatus *int
	if wire.Error.HTTPStatus != nil {
		var status int
		if bytes.Equal(bytes.TrimSpace(wire.Error.HTTPStatus), []byte("null")) || json.Unmarshal(wire.Error.HTTPStatus, &status) != nil || status < 100 || status > 599 {
			return cliErrorEnvelope{}, errors.New("CLI error response did not match its strict wire contract")
		}
		httpStatus = &status
	}
	var envelope cliErrorEnvelope
	envelope.OK = *wire.OK
	envelope.Error.Code = *wire.Error.Code
	envelope.Error.Message = *wire.Error.Message
	envelope.Error.HTTPStatus = httpStatus
	return envelope, nil
}

func runCLIError(t *testing.T, runner *cliRunner, wantExit int, wantCode string, wantHTTP int, args ...string) {
	t.Helper()
	execution := runner.execute("", args...)
	if execution.overflow || execution.timedOut || execution.record.ExitCode != wantExit || execution.record.Stderr != "" {
		t.Fatalf("CLI error command returned exit=%d overflow=%v timeout=%v stderr=%q", execution.record.ExitCode, execution.overflow, execution.timedOut, execution.record.Stderr)
	}
	if runner.forbiddenToken != "" {
		if err := assertTokenAbsent(runner.forbiddenToken, runner.records, execution.err); err != nil {
			t.Fatal(err)
		}
	}
	envelope, err := decodeCLIErrorEnvelope([]byte(execution.record.Stdout))
	if err != nil || envelope.Error.Code != wantCode || wantHTTP == 0 && envelope.Error.HTTPStatus != nil || wantHTTP != 0 && (envelope.Error.HTTPStatus == nil || *envelope.Error.HTTPStatus != wantHTTP) {
		t.Fatalf("CLI error envelope = %+v", envelope)
	}
}

func reviewBody(revision int, sha, assessment string) string {
	return fmt.Sprintf("AI roast analysis\nTemplate: %s\nProfile revision: %d (%s)\n\nOverall assessment\n%s\n\nPhase timing and ratios\nFixture-backed timing.\n\nTemperature and RoR behavior\nFixture-backed channels.\n\nEvents and control observations\nFixture-backed events.\n\nAnomalies and data limitations\nNo sensory evidence.\n\nPrioritized recommendations\nReview the measured profile.\n\nConfidence\nModerate.", api.ReviewTemplateVersion, revision, sha, assessment)
}

func writeReviewBody(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertReviewPair(t *testing.T, created, replay integrationReviewResult, roastUUID, sha, createdBody string) {
	t.Helper()
	if created.IdempotentReplay || !replay.IdempotentReplay || created.RevisionSHA256 != sha || replay.RevisionSHA256 != sha || created.TemplateVersion != api.ReviewTemplateVersion || replay.TemplateVersion != api.ReviewTemplateVersion || created.Comment.CommentUUID == "" || created.Comment.CommentUUID != replay.Comment.CommentUUID || created.Comment.RoastUUID != roastUUID || replay.Comment.RoastUUID != roastUUID || created.Comment.Body == nil || replay.Comment.Body == nil || *created.Comment.Body != createdBody || *replay.Comment.Body != createdBody {
		t.Fatalf("created/replayed review mismatch: created=%+v replay=%+v", created, replay)
	}
}

func assertReviewComments(t *testing.T, runner *cliRunner, roastUUID string, reviews []integrationReviewResult) {
	t.Helper()
	var page integrationCommentPage
	runner.runJSON(t, "", &page, "roast", "comment", "list", roastUUID, "--all")
	if len(page.Items) != len(reviews) {
		t.Fatalf("comment count = %d, want %d", len(page.Items), len(reviews))
	}
	want := make(map[string]string, len(reviews))
	for _, review := range reviews {
		want[review.Comment.CommentUUID] = *review.Comment.Body
	}
	for _, comment := range page.Items {
		if comment.IsDeleted || comment.Body == nil || want[comment.CommentUUID] != *comment.Body || comment.RoastUUID != roastUUID {
			t.Fatalf("unexpected review comment %+v", comment)
		}
		delete(want, comment.CommentUUID)
	}
	if len(want) != 0 {
		t.Fatalf("missing review comments %v", want)
	}
}

func inspectRoastReviews(t *testing.T, config liveConfig, roastUUID string) roastReviewInspection {
	t.Helper()
	serverRoot, err := filepath.Abs(config.serverRoot)
	if err != nil {
		t.Fatal("invalid disposable server root")
	}
	serverRoot = filepath.Clean(serverRoot)
	serverInfo, err := os.Lstat(serverRoot)
	if err != nil || !serverInfo.IsDir() || serverInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("disposable server root is not a real directory")
	}
	resolvedServer, err := filepath.EvalSymlinks(serverRoot)
	if err != nil || resolvedServer != serverRoot {
		t.Fatal("disposable server root is not canonical")
	}
	head := exec.Command("git", "-C", serverRoot, "rev-parse", "HEAD")
	headOutput, err := head.Output()
	if err != nil || strings.TrimSpace(string(headOutput)) != pinnedServerRef {
		t.Fatal("disposable server root is not the pinned server checkout")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration source")
	}
	script := filepath.Join(filepath.Dir(sourceFile), "inspect_roast_reviews.py")
	scriptInfo, err := os.Lstat(script)
	if err != nil || !scriptInfo.Mode().IsRegular() || scriptInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("review inspector is not a regular source file")
	}
	resolvedScript, err := filepath.EvalSymlinks(script)
	if err != nil || resolvedScript != script {
		t.Fatal("review inspector path is not canonical")
	}
	wrapper := filepath.Join(serverRoot, "scripts", "e2e_compose.py")
	args := []string{
		"--project", config.projectName,
		"-f", filepath.Join(serverRoot, "compose.yaml"),
		"-f", filepath.Join(serverRoot, "compose.e2e.yaml"),
		"run", "--rm",
		"-e", "ARTISAN_E2E_EXPECTED_PROJECT=" + config.projectName,
		"-e", "COMPOSE_PROJECT_NAME=" + config.projectName,
		"-e", "ARTISAN_E2E_ORGANIZATION_SLUG=" + config.organizationSlug,
		"-v", script + ":/tmp/inspect_roast_reviews.py:ro",
		"api", "python", "/tmp/inspect_roast_reviews.py", roastUUID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, wrapper, args...)
	command.Dir = serverRoot
	for _, name := range []string{
		"PATH", "HOME", "DOCKER_CONFIG", "XDG_RUNTIME_DIR",
		"ARTISAN_SERVER_HTTP_PORT", "ARTISAN_SERVER_E2E_PUBLIC_ORIGIN",
		"ARTISAN_SERVER_E2E_POSTGRES_PORT", "ARTISAN_SERVER_E2E_MINIO_PORT",
		"ARTISAN_SERVER_E2E_MAILPIT_HTTP_PORT",
	} {
		if value := os.Getenv(name); value != "" {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	stdout := boundedCapture{limit: 16 << 10}
	stderr := boundedCapture{limit: 16 << 10}
	command.Stdout, command.Stderr = &stdout, &stderr
	command.WaitDelay = cliCommandWaitDelay
	tree, err := prepareProcessTree(command)
	if err != nil {
		t.Fatal("review inspection containment setup failed")
	}
	if err := command.Start(); err != nil {
		_ = tree.close(cliCommandWaitDelay)
		t.Fatal("review inspection failed to start")
	}
	if err := tree.afterStart(command.Process); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = tree.close(cliCommandWaitDelay)
		t.Fatal("review inspection containment failed")
	}
	waitErr := command.Wait()
	closeErr := tree.close(cliCommandWaitDelay)
	if waitErr != nil || closeErr != nil || ctx.Err() != nil || stdout.overflow || stderr.overflow || stderr.String() != "" {
		t.Fatalf("bounded review inspection failed")
	}
	var result roastReviewInspection
	if err := decodeExactlyOneJSON(stdout.Bytes(), &result, true); err != nil || result.CommentIDs == nil || result.SlotCommentIDs == nil {
		t.Fatal("review inspection returned invalid bounded JSON")
	}
	return result
}

func assertInspection(t *testing.T, inspection roastReviewInspection, commentIDs []string) {
	t.Helper()
	want := append([]string(nil), commentIDs...)
	slicesSort(want)
	if inspection.CommentCount != len(want) || inspection.SlotCount != len(want) || inspection.AuditCount != len(want) {
		t.Fatalf("review inspection counts = %+v, want %d each", inspection, len(want))
	}
	gotComments := append([]string(nil), inspection.CommentIDs...)
	gotSlots := append([]string(nil), inspection.SlotCommentIDs...)
	slicesSort(gotComments)
	slicesSort(gotSlots)
	if strings.Join(gotComments, "\n") != strings.Join(want, "\n") || strings.Join(gotSlots, "\n") != strings.Join(want, "\n") {
		t.Fatalf("review inspection IDs = comments %v slots %v, want %v", gotComments, gotSlots, want)
	}
}

func slicesSort(values []string) {
	for index := 1; index < len(values); index++ {
		for position := index; position > 0 && values[position] < values[position-1]; position-- {
			values[position], values[position-1] = values[position-1], values[position]
		}
	}
}

func reviewRequest(t *testing.T, target, token, roastUUID, sha, body string) *http.Request {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"body": body, "revision_sha256": sha, "template_version": api.ReviewTemplateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		t.Fatal("construct review request")
	}
	request.Header.Set("Content-Type", "application/json")
	key, failure := api.CanonicalRoastReviewKey(roastUUID, sha, api.ReviewTemplateVersion)
	if failure != nil {
		t.Fatal("construct canonical review identity")
	}
	request.Header.Set("Idempotency-Key", key)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func assertCookieOnlyReviewRejected(t *testing.T, client *http.Client, config liveConfig, roastUUID, sha, body string) {
	t.Helper()
	request := reviewRequest(t, config.baseURL+"/api/v1/roasts/"+roastUUID+"/comments/ai-review", "", roastUUID, sha, body)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("cookie-only review request failed")
	}
	defer response.Body.Close()
	var result json.RawMessage
	if err := readBoundedJSON(response.Body, maxBrowserJSONBytes, "", &result); err != nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie-only review returned HTTP %d", response.StatusCode)
	}
}

func assertForeignTenantHidden(t *testing.T, config liveConfig, roastUUID, sha, body string) {
	t.Helper()
	show, err := http.NewRequest(http.MethodGet, config.baseURL+"/api/v1/roasts/"+roastUUID, nil)
	if err != nil {
		t.Fatal(err)
	}
	show.Header.Set("Authorization", "Bearer "+config.foreignToken)
	doBoundedBearerRequest(t, show, config.foreignToken, http.StatusNotFound)
	request := reviewRequest(t, config.baseURL+"/api/v1/roasts/"+roastUUID+"/comments/ai-review", config.foreignToken, roastUUID, sha, body)
	doBoundedBearerRequest(t, request, config.foreignToken, http.StatusNotFound)
}

func trashRoast(t *testing.T, client *http.Client, config liveConfig, csrf, roastUUID string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodDelete, config.baseURL+"/api/v1/browser/roasts/"+roastUUID, nil)
	if err != nil {
		t.Fatal("construct trash request")
	}
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("trash request failed")
	}
	defer response.Body.Close()
	var result struct {
		RoastUUID string `json:"roast_uuid"`
		Trashed   bool   `json:"trashed"`
	}
	if err := readBoundedJSON(response.Body, maxBrowserJSONBytes, "", &result); err != nil || response.StatusCode != http.StatusOK || result.RoastUUID != roastUUID || !result.Trashed {
		t.Fatalf("trash response = HTTP %d %+v", response.StatusCode, result)
	}
}

func assertTrashedRoastHidden(t *testing.T, runner *cliRunner, runDirectory, roastUUID, title, sha, bodyPath string) {
	t.Helper()
	var page integrationRoastPage
	runner.runJSON(t, "", &page, "roast", "list", "--search", title)
	if len(page.Items) != 0 {
		t.Fatalf("trashed roast remained in list: %+v", page.Items)
	}
	for _, args := range [][]string{
		{"roast", "show", roastUUID},
		{"roast", "revisions", roastUUID, "--all"},
		{"roast", "comment", "list", roastUUID, "--all"},
		{"roast", "chart", "download", roastUUID, filepath.Join(runDirectory, "trash-chart.json")},
		{"roast", "profile", "download", roastUUID, "1", filepath.Join(runDirectory, "trash-profile.alog")},
		{"roast", "review", "post", roastUUID, "--revision-sha256", sha, "--template-version", api.ReviewTemplateVersion, "--body-file", bodyPath},
	} {
		runCLIError(t, runner, 6, "not_found", http.StatusNotFound, args...)
	}
	for _, path := range []string{filepath.Join(runDirectory, "trash-chart.json"), filepath.Join(runDirectory, "trash-profile.alog")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("trashed download created %s", path)
		}
	}
}
