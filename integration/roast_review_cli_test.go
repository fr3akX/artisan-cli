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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
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

func TestConcurrentReviewCommandsUseSeparateRunnersAndDecodeAfterCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	root := canonicalTempDir(t)
	body := reviewBody(1, strings.Repeat("a", 64), "Concurrent fixture")
	response := integrationReviewResult{
		RevisionSHA256: strings.Repeat("a", 64), TemplateVersion: api.ReviewTemplateVersion,
		Comment: integrationComment{CommentUUID: strings.Repeat("b", 32), RoastUUID: strings.Repeat("c", 32), Body: &body},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	envelope := `{"ok":true,"data":` + string(data) + `}`
	runners := [2]*cliRunner{}
	for index := range runners {
		script := filepath.Join(root, fmt.Sprintf("runner-%d", index))
		if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '"+envelope+"'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		runners[index] = &cliRunner{binary: script, baseURL: "http://127.0.0.1", cwd: root, env: []string{"PATH=" + os.Getenv("PATH")}}
	}

	executions := runConcurrentReviewCommands(runners, [2][]string{{"first"}, {"second"}})
	for index, execution := range executions {
		decoded, err := decodeReviewExecution(execution)
		if err != nil || decoded.Comment.CommentUUID != response.Comment.CommentUUID || len(runners[index].records) != 1 {
			t.Fatalf("concurrent result %d = (%+v, %v), records=%d", index, decoded, err, len(runners[index].records))
		}
	}
}

func TestRevisionFenceProxyHoldsFetchedDetailUntilReleased(t *testing.T) {
	const roastID = "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"revision":"one"}`)
	}))
	defer upstream.Close()
	proxy, err := newRevisionFenceProxy(upstream.URL, roastID)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	type responseResult struct {
		status int
		body   []byte
		err    error
	}
	resultChannel := make(chan responseResult, 1)
	go func() {
		response, err := http.Get(proxy.URL() + "/api/v1/roasts/" + roastID)
		if err != nil {
			resultChannel <- responseResult{err: err}
			return
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		resultChannel <- responseResult{status: response.StatusCode, body: body, err: readErr}
	}()

	select {
	case <-proxy.Ready():
	case <-time.After(time.Second):
		t.Fatal("proxy did not hold the fetched preflight detail")
	}
	select {
	case result := <-resultChannel:
		t.Fatalf("held response completed before release: %+v", result)
	default:
	}
	proxy.Release()
	select {
	case result := <-resultChannel:
		if result.err != nil || result.status != http.StatusOK || string(result.body) != `{"revision":"one"}` {
			t.Fatalf("released response = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("released proxy response did not complete")
	}
}

func runConcurrentReviewCommands(runners [2]*cliRunner, arguments [2][]string) [2]commandExecution {
	start := make(chan struct{})
	results := make(chan int, len(runners))
	var executions [2]commandExecution
	for index := range runners {
		go func(index int) {
			<-start
			executions[index] = runners[index].execute("", arguments[index]...)
			results <- index
		}(index)
	}
	close(start)
	for range runners {
		<-results
	}
	return executions
}

func decodeReviewExecution(execution commandExecution) (integrationReviewResult, error) {
	if execution.overflow || execution.timedOut || execution.err != nil || execution.record.ExitCode != 0 || execution.record.Stderr != "" {
		return integrationReviewResult{}, errors.New("concurrent CLI review command did not complete successfully")
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := decodeExactlyOneJSON([]byte(execution.record.Stdout), &envelope, true); err != nil || !envelope.OK || len(envelope.Data) == 0 {
		return integrationReviewResult{}, errors.New("concurrent CLI review command returned an invalid success envelope")
	}
	var result integrationReviewResult
	if err := decodeExactlyOneJSON(envelope.Data, &result, false); err != nil {
		return integrationReviewResult{}, errors.New("concurrent CLI review command returned invalid review data")
	}
	return result, nil
}

type revisionFenceProxy struct {
	server      *httptest.Server
	transport   *http.Transport
	ready       chan struct{}
	release     chan struct{}
	readyOnce   sync.Once
	releaseOnce sync.Once
}

func newRevisionFenceProxy(upstream, roastUUID string) (*revisionFenceProxy, error) {
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme == "" || target.Host == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return nil, errors.New("invalid revision-fence upstream")
	}
	proxy := &revisionFenceProxy{
		transport: &http.Transport{Proxy: nil},
		ready:     make(chan struct{}),
		release:   make(chan struct{}),
	}
	proxy.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		outbound := request.Clone(request.Context())
		outbound.URL.Scheme = target.Scheme
		outbound.URL.Host = target.Host
		outbound.URL.Path = request.URL.Path
		outbound.URL.RawPath = request.URL.RawPath
		outbound.URL.RawQuery = request.URL.RawQuery
		outbound.RequestURI = ""
		outbound.Host = target.Host
		response, roundTripErr := proxy.transport.RoundTrip(outbound)
		if roundTripErr != nil {
			http.Error(writer, "upstream request failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()

		var heldBody []byte
		held := request.Method == http.MethodGet && request.URL.Path == "/api/v1/roasts/"+roastUUID && request.URL.RawQuery == ""
		if held {
			heldBody, roundTripErr = io.ReadAll(io.LimitReader(response.Body, maxBrowserJSONBytes+1))
			if roundTripErr != nil || len(heldBody) > maxBrowserJSONBytes {
				http.Error(writer, "upstream response invalid", http.StatusBadGateway)
				return
			}
			proxy.readyOnce.Do(func() { close(proxy.ready) })
			select {
			case <-proxy.release:
			case <-request.Context().Done():
				return
			}
		}
		for name, values := range response.Header {
			for _, value := range values {
				writer.Header().Add(name, value)
			}
		}
		writer.WriteHeader(response.StatusCode)
		if held {
			_, _ = writer.Write(heldBody)
			return
		}
		_, _ = io.Copy(writer, response.Body)
	}))
	return proxy, nil
}

func (proxy *revisionFenceProxy) URL() string            { return proxy.server.URL }
func (proxy *revisionFenceProxy) Ready() <-chan struct{} { return proxy.ready }
func (proxy *revisionFenceProxy) Release()               { proxy.releaseOnce.Do(func() { close(proxy.release) }) }
func (proxy *revisionFenceProxy) Close() {
	proxy.Release()
	proxy.server.Close()
	proxy.transport.CloseIdleConnections()
}

func TestBrowserRoastUUIDMatchesCompactIdentityOnlyForCanonicalDashedForm(t *testing.T) {
	const compact = "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"
	if !browserRoastUUIDMatchesCompactIdentity("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", compact) {
		t.Fatal("matching canonical dashed browser UUID was rejected")
	}
	for _, test := range []struct {
		name  string
		value string
	}{
		{"compact browser form", compact},
		{"uppercase browser form", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"},
		{"malformed hyphens", "aaaaaaaaa-aaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{"invalid version", "aaaaaaaa-aaaa-0aaa-8aaa-aaaaaaaaaaaa"},
		{"invalid variant", "aaaaaaaa-aaaa-4aaa-7aaa-aaaaaaaaaaaa"},
		{"different UUID", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if browserRoastUUIDMatchesCompactIdentity(test.value, compact) {
				t.Fatalf("invalid or different browser UUID %q was accepted", test.value)
			}
		})
	}
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
		`organization_comment_created_audit_count`,
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

func TestDecodeRoastReviewInspectionRequiresStrictCompleteCounts(t *testing.T) {
	valid := `{"audit_count":1,"comment_count":1,"comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"organization_comment_created_audit_count":7,"slot_comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"slot_count":1}`
	inspection, err := decodeRoastReviewInspection([]byte(valid))
	if err != nil || inspection.OrganizationCommentCreatedAuditCount != 7 || inspection.AuditCount != 1 {
		t.Fatalf("valid inspection = (%+v, %v)", inspection, err)
	}

	invalid := []struct {
		name     string
		contents string
	}{
		{"missing organization audit count", `{"audit_count":1,"comment_count":1,"comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"slot_comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"slot_count":1}`},
		{"null organization audit count", `{"audit_count":1,"comment_count":1,"comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"organization_comment_created_audit_count":null,"slot_comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"slot_count":1}`},
		{"negative organization audit count", `{"audit_count":1,"comment_count":1,"comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"organization_comment_created_audit_count":-1,"slot_comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"slot_count":1}`},
		{"comment count mismatch", `{"audit_count":1,"comment_count":0,"comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"organization_comment_created_audit_count":7,"slot_comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"slot_count":1}`},
		{"slot count mismatch", `{"audit_count":1,"comment_count":1,"comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"organization_comment_created_audit_count":7,"slot_comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"slot_count":0}`},
		{"malformed comment identity", `{"audit_count":1,"comment_count":1,"comment_ids":["not-a-uuid"],"organization_comment_created_audit_count":7,"slot_comment_ids":["aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"],"slot_count":1}`},
		{"unknown field", `{"audit_count":0,"comment_count":0,"comment_ids":[],"organization_comment_created_audit_count":7,"slot_comment_ids":[],"slot_count":0,"extra":true}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeRoastReviewInspection([]byte(test.contents)); err == nil {
				t.Fatal("invalid inspection was accepted")
			}
		})
	}
}

func TestRequireOrganizationCommentCreatedAuditDelta(t *testing.T) {
	before := roastReviewInspection{OrganizationCommentCreatedAuditCount: 11}
	if err := requireOrganizationCommentCreatedAuditDelta(before, roastReviewInspection{OrganizationCommentCreatedAuditCount: 12}, 1); err != nil {
		t.Fatalf("exact audit delta was rejected: %v", err)
	}
	for _, test := range []struct {
		name  string
		after int
		want  int
	}{
		{"unexpected extra audit", 13, 1},
		{"missing audit", 11, 1},
		{"decreasing total", 10, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := requireOrganizationCommentCreatedAuditDelta(before, roastReviewInspection{OrganizationCommentCreatedAuditCount: test.after}, test.want); err == nil {
				t.Fatal("invalid organization audit delta was accepted")
			}
		})
	}
}

func TestInspectionProcessResultAcceptsBoundedComposeProgress(t *testing.T) {
	result := inspectionProcessResult{
		stderr: "Creating artisan-server-e2e-api-run ... done\n",
	}
	if err := validateInspectionProcessResult(result, []string{"admin-password", "member-password", "admin-token", "member-token", "review-member-token", "foreign-token"}); err != nil {
		t.Fatalf("ordinary bounded Compose progress was rejected: %v", err)
	}
}

func TestInspectionProcessResultRejectsEachProcessAndBoundFailure(t *testing.T) {
	failures := []struct {
		name   string
		mutate func(*inspectionProcessResult)
	}{
		{"wait failure", func(result *inspectionProcessResult) { result.waitErr = errors.New("wait failed") }},
		{"containment close failure", func(result *inspectionProcessResult) { result.closeErr = errors.New("close failed") }},
		{"context deadline", func(result *inspectionProcessResult) { result.contextErr = context.DeadlineExceeded }},
		{"stdout overflow", func(result *inspectionProcessResult) { result.stdoutOverflow = true }},
		{"stderr overflow", func(result *inspectionProcessResult) { result.stderrOverflow = true }},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			result := inspectionProcessResult{stderr: "ordinary Compose progress"}
			failure.mutate(&result)
			err := validateInspectionProcessResult(result, []string{"disposable-secret"})
			if err == nil {
				t.Fatal("failed inspection process result was accepted")
			}
			if strings.Contains(err.Error(), result.stderr) {
				t.Fatal("inspection failure included captured stderr")
			}
		})
	}
}

func TestInspectionProcessResultRejectsEveryDisposableSecretClass(t *testing.T) {
	secrets := []struct {
		name  string
		value string
	}{
		{"admin password", "admin-password-secret"},
		{"member password", "member-password-secret"},
		{"admin bearer token", "admin-bearer-secret"},
		{"member bearer token", "member-bearer-secret"},
		{"review-member bearer token", "review-member-bearer-secret"},
		{"foreign bearer token", "foreign-bearer-secret"},
	}
	allSecrets := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		allSecrets = append(allSecrets, secret.value)
	}
	for _, reflected := range secrets {
		t.Run(reflected.name, func(t *testing.T) {
			stderr := "ordinary Compose progress\n" + reflected.value + "\n"
			err := validateInspectionProcessResult(inspectionProcessResult{stderr: stderr}, allSecrets)
			if err == nil {
				t.Fatal("inspection stderr reflecting a disposable secret was accepted")
			}
			if strings.Contains(err.Error(), stderr) || strings.Contains(err.Error(), reflected.value) {
				t.Fatal("inspection failure included captured stderr or the reflected secret")
			}
		})
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
	AuditCount                           int      `json:"audit_count"`
	CommentCount                         int      `json:"comment_count"`
	CommentIDs                           []string `json:"comment_ids"`
	OrganizationCommentCreatedAuditCount int      `json:"organization_comment_created_audit_count"`
	SlotCommentIDs                       []string `json:"slot_comment_ids"`
	SlotCount                            int      `json:"slot_count"`
}

func decodeRoastReviewInspection(contents []byte) (roastReviewInspection, error) {
	var wire struct {
		AuditCount                           *int      `json:"audit_count"`
		CommentCount                         *int      `json:"comment_count"`
		CommentIDs                           *[]string `json:"comment_ids"`
		OrganizationCommentCreatedAuditCount *int      `json:"organization_comment_created_audit_count"`
		SlotCommentIDs                       *[]string `json:"slot_comment_ids"`
		SlotCount                            *int      `json:"slot_count"`
	}
	if err := decodeExactlyOneJSON(contents, &wire, true); err != nil ||
		wire.AuditCount == nil || wire.CommentCount == nil || wire.CommentIDs == nil ||
		wire.OrganizationCommentCreatedAuditCount == nil || wire.SlotCommentIDs == nil || wire.SlotCount == nil {
		return roastReviewInspection{}, errors.New("review inspection did not match its strict wire contract")
	}
	if *wire.AuditCount < 0 || *wire.CommentCount < 0 || *wire.OrganizationCommentCreatedAuditCount < 0 || *wire.SlotCount < 0 ||
		*wire.AuditCount > *wire.OrganizationCommentCreatedAuditCount || *wire.CommentCount != len(*wire.CommentIDs) || *wire.SlotCount != len(*wire.SlotCommentIDs) {
		return roastReviewInspection{}, errors.New("review inspection counts were invalid")
	}
	for _, values := range [][]string{*wire.CommentIDs, *wire.SlotCommentIDs} {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			normalized, err := api.NormalizeRoastUUID(value)
			if err != nil || normalized != value {
				return roastReviewInspection{}, errors.New("review inspection contained an invalid comment identity")
			}
			if _, exists := seen[value]; exists {
				return roastReviewInspection{}, errors.New("review inspection contained a duplicate comment identity")
			}
			seen[value] = struct{}{}
		}
	}
	return roastReviewInspection{
		AuditCount:                           *wire.AuditCount,
		CommentCount:                         *wire.CommentCount,
		CommentIDs:                           *wire.CommentIDs,
		OrganizationCommentCreatedAuditCount: *wire.OrganizationCommentCreatedAuditCount,
		SlotCommentIDs:                       *wire.SlotCommentIDs,
		SlotCount:                            *wire.SlotCount,
	}, nil
}

func requireOrganizationCommentCreatedAuditDelta(before, after roastReviewInspection, want int) error {
	if want < 0 || before.OrganizationCommentCreatedAuditCount < 0 || after.OrganizationCommentCreatedAuditCount < before.OrganizationCommentCreatedAuditCount ||
		after.OrganizationCommentCreatedAuditCount-before.OrganizationCommentCreatedAuditCount != want {
		return errors.New("organization comment.created audit delta was not exact")
	}
	return nil
}

type inspectionProcessResult struct {
	waitErr        error
	closeErr       error
	contextErr     error
	stdoutOverflow bool
	stderrOverflow bool
	stderr         string
}

func validateInspectionProcessResult(result inspectionProcessResult, disposableSecrets []string) error {
	if result.waitErr != nil {
		return errors.New("review inspection process wait failed")
	}
	if result.closeErr != nil {
		return errors.New("review inspection process containment close failed")
	}
	if result.contextErr != nil {
		return errors.New("review inspection process context failed")
	}
	if result.stdoutOverflow {
		return errors.New("review inspection stdout exceeded its bound")
	}
	if result.stderrOverflow {
		return errors.New("review inspection stderr exceeded its bound")
	}
	for _, secret := range disposableSecrets {
		if secret == "" {
			return errors.New("review inspection disposable secret was empty")
		}
		if strings.Contains(result.stderr, secret) {
			return errors.New("review inspection stderr reflected a disposable secret")
		}
	}
	return nil
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
	staleRoastUUID := randomCanonicalUUID(t)
	staleTitle := "CLI stale review " + runID
	staleRevisionOne := profileFixtureForRoast(t, fixture, staleRoastUUID, staleTitle)
	staleRevisionOneSHA := digestHex(staleRevisionOne)
	staleRevisionTwo := bytes.Replace(staleRevisionOne, []byte("'ambient_temp': 23.5"), []byte("'ambient_temp': 25.5"), 1)
	if bytes.Equal(staleRevisionOne, staleRevisionTwo) {
		t.Fatal("stale-proof revision-two fixture mutation did not apply")
	}

	root := canonicalTempDir(t)
	adminRunner, adminPaths := newReviewRunner(t, root, "admin", binary, config.baseURL)
	memberRunner, memberPaths := newReviewRunner(t, root, "member", binary, config.baseURL)
	staleProxy, err := newRevisionFenceProxy(config.baseURL, staleRoastUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer staleProxy.Close()
	staleRunner, stalePaths := newReviewRunner(t, root, "stale-member", binary, staleProxy.URL())
	adminHTTP, csrf, adminToken, adminCredentialID := issueCredential(t, config, config.adminEmail, config.adminPassword)
	foreignHTTP, foreignCSRF := loginBrowserSession(t, config.baseURL, config.foreignEmail, config.memberPassword, config.foreignOrganizationSlug)
	adminRunner.forbiddenToken = adminToken
	memberRunner.forbiddenToken = config.reviewMemberToken
	inspectionSecrets := []string{
		config.adminPassword,
		config.memberPassword,
		adminToken,
		config.memberToken,
		config.reviewMemberToken,
		config.foreignToken,
	}
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
			{config.reviewMemberToken, staleRunner, stalePaths},
		} {
			if err := assertTokenAbsent(check.token, check.runner.records, nil); err != nil {
				t.Error(err)
			}
			if err := assertTokenAbsentFromTrees(check.token, check.paths...); err != nil {
				t.Error(err)
			}
		}
	}()
	adminLoggedIn, memberLoggedIn, staleMemberLoggedIn := true, true, true
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
		if staleMemberLoggedIn {
			if err := staleRunner.cleanupLogout(); err != nil {
				t.Error(err)
			}
		}
	}()

	var adminIdentity, memberIdentity, staleMemberIdentity authIdentity
	adminRunner.runJSON(t, adminToken+"\n", &adminIdentity, "auth", "login", "--token-stdin")
	memberRunner.runJSON(t, config.reviewMemberToken+"\n", &memberIdentity, "auth", "login", "--token-stdin")
	staleRunner.runJSON(t, config.reviewMemberToken+"\n", &staleMemberIdentity, "auth", "login", "--token-stdin")
	assertExpectedIdentity(t, adminIdentity, config)
	assertExpectedMemberIdentity(t, memberIdentity, config)
	assertExpectedMemberIdentity(t, staleMemberIdentity, config)

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
	concurrentArguments := [2][]string{
		{"roast", "review", "post", roastUUID, "--revision-sha256", revisionOneSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", memberBodyPath},
		{"roast", "review", "post", roastUUID, "--revision-sha256", revisionOneSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", adminBodyPath},
	}
	concurrentAuditBefore := inspectRoastReviews(t, config, roastUUID, inspectionSecrets)
	concurrentExecutions := runConcurrentReviewCommands([2]*cliRunner{memberRunner, adminRunner}, concurrentArguments)
	concurrentAuditAfter := inspectRoastReviews(t, config, roastUUID, inspectionSecrets)
	if err := requireOrganizationCommentCreatedAuditDelta(concurrentAuditBefore, concurrentAuditAfter, 1); err != nil {
		t.Fatal(err)
	}
	for index, check := range []struct {
		token  string
		runner *cliRunner
	}{{config.reviewMemberToken, memberRunner}, {adminToken, adminRunner}} {
		if err := assertTokenAbsent(check.token, check.runner.records, concurrentExecutions[index].err); err != nil {
			t.Fatal(err)
		}
	}
	memberReview, err := decodeReviewExecution(concurrentExecutions[0])
	if err != nil {
		t.Fatal(err)
	}
	adminReview, err := decodeReviewExecution(concurrentExecutions[1])
	if err != nil {
		t.Fatal(err)
	}
	firstReview := assertConcurrentReviewPair(t, memberReview, adminReview, roastUUID, revisionOneSHA, memberBody, adminBody)
	assertReviewComments(t, memberRunner, roastUUID, []integrationReviewResult{firstReview})
	assertInspection(t, concurrentAuditAfter, []string{firstReview.Comment.CommentUUID})

	uploadRoastRevision(t, config, adminToken, roastUUID, revisionTwo, "review-"+runID+"-revision-2")
	var oldReplay integrationReviewResult
	memberRunner.runJSON(t, "", &oldReplay, "roast", "review", "post", roastUUID,
		"--revision-sha256", revisionOneSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", memberBodyPath)
	if !oldReplay.IdempotentReplay || oldReplay.Comment.CommentUUID != firstReview.Comment.CommentUUID {
		t.Fatalf("old revision replay = %+v", oldReplay)
	}

	staleUploadedOne := uploadRoastRevision(t, config, config.reviewMemberToken, staleRoastUUID, staleRevisionOne, "review-"+runID+"-stale-revision-1")
	if staleUploadedOne.RevisionNumber != 1 || staleUploadedOne.SHA256 != staleRevisionOneSHA {
		t.Fatalf("stale-proof uploaded revision one = %+v", staleUploadedOne)
	}
	staleBodyPath := writeReviewBody(t, stalePaths[len(stalePaths)-1], "never-posted-stale-review.txt", reviewBody(1, staleRevisionOneSHA, "Never-posted real stale analysis"))
	staleExecutionChannel := make(chan commandExecution, 1)
	go func() {
		staleExecutionChannel <- staleRunner.execute("", "roast", "review", "post", staleRoastUUID,
			"--revision-sha256", staleRevisionOneSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", staleBodyPath)
	}()
	select {
	case <-staleProxy.Ready():
	case <-time.After(cliCommandTimeout):
		t.Fatal("stale review CLI did not complete local current-revision preflight")
	}
	staleUploadedTwo := uploadRoastRevision(t, config, adminToken, staleRoastUUID, staleRevisionTwo, "review-"+runID+"-stale-revision-2")
	if staleUploadedTwo.RevisionNumber != 2 || staleUploadedTwo.SHA256 != digestHex(staleRevisionTwo) {
		t.Fatalf("stale-proof uploaded revision two = %+v", staleUploadedTwo)
	}
	staleAuditBefore := inspectRoastReviews(t, config, staleRoastUUID, inspectionSecrets)
	staleProxy.Release()
	var staleExecution commandExecution
	select {
	case staleExecution = <-staleExecutionChannel:
	case <-time.After(cliCommandTimeout):
		t.Fatal("stale review CLI did not receive the authoritative server response")
	}
	staleAuditAfter := inspectRoastReviews(t, config, staleRoastUUID, inspectionSecrets)
	if err := requireOrganizationCommentCreatedAuditDelta(staleAuditBefore, staleAuditAfter, 0); err != nil {
		t.Fatal(err)
	}
	if err := assertTokenAbsent(config.reviewMemberToken, staleRunner.records, staleExecution.err); err != nil {
		t.Fatal(err)
	}
	staleEnvelope, err := decodeCLIErrorEnvelope([]byte(staleExecution.record.Stdout))
	if staleExecution.overflow || staleExecution.timedOut || staleExecution.record.ExitCode != 7 || staleExecution.record.Stderr != "" || err != nil || staleEnvelope.Error.Code != "roast_revision_changed" || staleEnvelope.Error.HTTPStatus == nil || *staleEnvelope.Error.HTTPStatus != http.StatusConflict {
		t.Fatalf("authoritative stale review result = execution %+v envelope %+v", staleExecution, staleEnvelope)
	}
	assertInspection(t, staleAuditAfter, nil)
	assertInspection(t, inspectRoastReviews(t, config, roastUUID, inspectionSecrets), []string{firstReview.Comment.CommentUUID})

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
	assertReviewComments(t, adminRunner, roastUUID, []integrationReviewResult{adminRevisionTwo, firstReview})
	assertInspection(t, inspectRoastReviews(t, config, roastUUID, inspectionSecrets), []string{firstReview.Comment.CommentUUID, adminRevisionTwo.Comment.CommentUUID})

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
	staleRunner.runJSON(t, "", &logout, "auth", "logout")
	if !logout.LoggedOut {
		t.Fatal("stale member logout did not report success")
	}
	staleMemberLoggedIn = false
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

func assertConcurrentReviewPair(t *testing.T, member, administrator integrationReviewResult, roastUUID, sha, memberBody, administratorBody string) integrationReviewResult {
	t.Helper()
	if member.IdempotentReplay == administrator.IdempotentReplay {
		t.Fatalf("concurrent replay flags = member %t administrator %t, want exactly one winner", member.IdempotentReplay, administrator.IdempotentReplay)
	}
	winner := member
	winnerBody := memberBody
	if !administrator.IdempotentReplay {
		winner = administrator
		winnerBody = administratorBody
	}
	if member.RevisionSHA256 != sha || administrator.RevisionSHA256 != sha ||
		member.TemplateVersion != api.ReviewTemplateVersion || administrator.TemplateVersion != api.ReviewTemplateVersion ||
		member.Comment.CommentUUID == "" || member.Comment.CommentUUID != administrator.Comment.CommentUUID ||
		member.Comment.RoastUUID != roastUUID || administrator.Comment.RoastUUID != roastUUID ||
		member.Comment.Body == nil || administrator.Comment.Body == nil ||
		*member.Comment.Body != winnerBody || *administrator.Comment.Body != winnerBody {
		t.Fatalf("concurrent first-writer mismatch: member=%+v administrator=%+v", member, administrator)
	}
	return winner
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

func inspectRoastReviews(t *testing.T, config liveConfig, roastUUID string, disposableSecrets []string) roastReviewInspection {
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
	processResult := inspectionProcessResult{
		waitErr:        waitErr,
		closeErr:       closeErr,
		contextErr:     ctx.Err(),
		stdoutOverflow: stdout.overflow,
		stderrOverflow: stderr.overflow,
		stderr:         stderr.String(),
	}
	if err := validateInspectionProcessResult(processResult, disposableSecrets); err != nil {
		t.Fatal(err)
	}
	result, err := decodeRoastReviewInspection(stdout.Bytes())
	if err != nil {
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

func browserRoastUUIDMatchesCompactIdentity(browserUUID, expectedCompact string) bool {
	if len(browserUUID) != 36 || browserUUID != strings.ToLower(browserUUID) ||
		browserUUID[8] != '-' || browserUUID[13] != '-' || browserUUID[18] != '-' || browserUUID[23] != '-' {
		return false
	}
	normalized, failure := api.NormalizeRoastUUID(browserUUID)
	return failure == nil && normalized == expectedCompact
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
	if err := readBoundedJSON(response.Body, maxBrowserJSONBytes, "", &result); err != nil || response.StatusCode != http.StatusOK || !browserRoastUUIDMatchesCompactIdentity(result.RoastUUID, roastUUID) || !result.Trashed {
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
