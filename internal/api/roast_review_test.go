package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

const validReviewBody = "AI roast analysis\nTemplate: artisan-roast-review-v1\nProfile revision: 1 (" + roastSHA256 + ")\n\nOverall assessment\nMeasured evidence."

func TestCanonicalRoastReviewKeyExactContract(t *testing.T) {
	sum := sha256.Sum256([]byte("artisan-roast-review\x00" + roastUUID + "\x00" + roastSHA256 + "\x00" + ReviewTemplateVersion))
	want := "review-" + hex.EncodeToString(sum[:])
	got, failure := CanonicalRoastReviewKey(roastUUID, roastSHA256, ReviewTemplateVersion)
	if failure != nil || got != want {
		t.Fatalf("CanonicalRoastReviewKey() = %q, %+v; want %q", got, failure, want)
	}
	dashed := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	got, failure = CanonicalRoastReviewKey(dashed, roastSHA256, ReviewTemplateVersion)
	if failure != nil || got != want {
		t.Fatalf("dashed key = %q, %+v; want %q", got, failure, want)
	}
	for _, test := range []struct{ roast, sha, template string }{
		{roast: "bad", sha: roastSHA256, template: ReviewTemplateVersion},
		{roast: roastUUID, sha: strings.ToUpper(roastSHA256), template: ReviewTemplateVersion},
		{roast: roastUUID, sha: roastSHA256[:63], template: ReviewTemplateVersion},
		{roast: roastUUID, sha: roastSHA256, template: "other"},
	} {
		if key, failure := CanonicalRoastReviewKey(test.roast, test.sha, test.template); failure == nil || key != "" {
			t.Fatalf("invalid key inputs returned %q, %+v", key, failure)
		}
	}
}

func TestReadRoastReviewFileNormalizesOnlySurroundingUnicodeWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.txt")
	input := "\u2003\n" + validReviewBody + "\n\u2002"
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	request, failure := ReadRoastReviewFile(path, roastSHA256, ReviewTemplateVersion)
	if failure != nil {
		t.Fatalf("failure = %+v", failure)
	}
	if request != (RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion}) {
		t.Fatalf("request = %#v", request)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"body":` + strconvQuote(validReviewBody) + `,"revision_sha256":"` + roastSHA256 + `","template_version":"artisan-roast-review-v1"}`
	if string(encoded) != wantJSON {
		t.Fatalf("JSON = %s; want %s", encoded, wantJSON)
	}
}

func TestReadRoastReviewFileRejectsHostileBodies(t *testing.T) {
	invalidUTF8 := append([]byte(validReviewBody), 0xff)
	tests := []struct {
		name     string
		contents []byte
		sha      string
		template string
	}{
		{name: "empty", contents: nil, sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "carriage return", contents: []byte(strings.ReplaceAll(validReviewBody, "\n", "\r\n")), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "invalid UTF8", contents: invalidUTF8, sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "nul", contents: []byte(validReviewBody + "\x00"), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "tab", contents: []byte(validReviewBody + "\tbad"), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "leading text", contents: []byte("ignore this\n" + validReviewBody), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "marker only", contents: []byte(strings.Join(strings.Split(validReviewBody, "\n")[:3], "\n")), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "missing blank after marker", contents: []byte(strings.Replace(validReviewBody, ")\n\nOverall", ")\nOverall", 1)), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "revision zero", contents: []byte(strings.Replace(validReviewBody, "Profile revision: 1", "Profile revision: 0", 1)), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "revision overflow", contents: []byte(strings.Replace(validReviewBody, "Profile revision: 1", "Profile revision: 2147483648", 1)), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "sha marker mismatch", contents: []byte(validReviewBody), sha: strings.Repeat("e", 64), template: ReviewTemplateVersion},
		{name: "template marker mismatch", contents: []byte(validReviewBody), sha: roastSHA256, template: "other"},
		{name: "too many runes", contents: []byte(validReviewBody + strings.Repeat("a", 4001-utf8.RuneCountInString(validReviewBody))), sha: roastSHA256, template: ReviewTemplateVersion},
		{name: "too many bytes", contents: []byte(validReviewBody + strings.Repeat("界", 6000)), sha: roastSHA256, template: ReviewTemplateVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "review.txt")
			if err := os.WriteFile(path, test.contents, 0o600); err != nil {
				t.Fatal(err)
			}
			request, failure := ReadRoastReviewFile(path, test.sha, test.template)
			if failure == nil || failure.Code != "invalid_review_file" || failure.ExitCode != 2 || request != (RoastReviewRequest{}) {
				t.Fatalf("result = %#v, %+v", request, failure)
			}
			if strings.Contains(failure.Message, path) || (len(test.contents) > 0 && strings.Contains(failure.Message, string(test.contents))) {
				t.Fatalf("failure leaks path/body: %+v", failure)
			}
		})
	}
}

func TestReadRoastReviewFileRejectsUnsafePath(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(real, []byte(validReviewBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, failure := ReadRoastReviewFile(link, roastSHA256, ReviewTemplateVersion); failure == nil || failure.Code != "invalid_review_file" {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestPostRoastReviewSendsStrictRevisionBoundRequestAndValidatesResponse(t *testing.T) {
	var postBody []byte
	var postHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/roasts/" + roastUUID:
			writeRoastJSON(w, validRoastDetailJSON())
		case "POST /api/v1/roasts/" + roastUUID + "/comments/ai-review":
			postHeader = r.Header.Clone()
			postBody, _ = io.ReadAll(r.Body)
			writeReviewResponse(w, false, validReviewBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "review-secret-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion}
	result, failure := client.PostRoastReview(context.Background(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", request)
	if failure != nil {
		t.Fatalf("failure = %+v", failure)
	}
	if result.Comment.CommentUUID != commentUUID || result.Comment.RoastUUID != roastUUID || result.RevisionSHA256 != roastSHA256 || result.TemplateVersion != ReviewTemplateVersion || result.IdempotentReplay {
		t.Fatalf("result = %#v", result)
	}
	wantBody := `{"body":` + strconvQuote(validReviewBody) + `,"revision_sha256":"` + roastSHA256 + `","template_version":"artisan-roast-review-v1"}`
	if string(postBody) != wantBody {
		t.Fatalf("post body = %s; want %s", postBody, wantBody)
	}
	wantKey, _ := CanonicalRoastReviewKey(roastUUID, roastSHA256, ReviewTemplateVersion)
	if postHeader.Get("Idempotency-Key") != wantKey || postHeader.Get("Authorization") != "Bearer review-secret-token" || postHeader.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %#v", postHeader)
	}
	for _, name := range []string{"Cookie", "X-CSRF-Token", "X-CSRFToken"} {
		if postHeader.Get(name) != "" {
			t.Fatalf("unexpected browser header %s", name)
		}
	}
}

func TestPostRoastReviewRetriesExactBodyAndKeyAndAcceptsReplay(t *testing.T) {
	var mu sync.Mutex
	var bodies, keys []string
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeRoastJSON(w, validRoastDetailJSON())
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		if posts.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"temporarily_unavailable","message":"retry"}}`)
			return
		}
		writeReviewResponse(w, true, "AI roast analysis\nTemplate: artisan-roast-review-v1\nProfile revision: 1 ("+roastSHA256+")\n\nEarlier review")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "retry-secret-token", 2*time.Second)
	result, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
	if failure != nil || !result.IdempotentReplay || posts.Load() != 2 {
		t.Fatalf("result = %#v, failure = %+v, posts = %d", result, failure, posts.Load())
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("bodies/keys changed across retry: %#v %#v", bodies, keys)
	}
}

func TestPostRoastReviewRejectsStaleOrInvalidInputBeforePost(t *testing.T) {
	changedSHA := strings.Repeat("e", 64)
	tests := []struct {
		name    string
		detail  string
		request RoastReviewRequest
		code    string
	}{
		{name: "stale SHA", detail: validRoastDetailJSON(), request: RoastReviewRequest{Body: strings.Replace(validReviewBody, roastSHA256, changedSHA, 1), RevisionSHA256: changedSHA, TemplateVersion: ReviewTemplateVersion}, code: "roast_revision_changed"},
		{name: "bad body marker", detail: validRoastDetailJSON(), request: RoastReviewRequest{Body: "wrong", RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion}, code: "invalid_review"},
		{name: "bad template", detail: validRoastDetailJSON(), request: RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: "other"}, code: "invalid_review"},
		{name: "not parsed", detail: strings.Replace(strings.Replace(validRoastDetailJSON(), `"state":"parsed"`, `"state":"parse_failed"`, 1), `"parse_state":"parsed"`, `"parse_state":"failed"`, 1), request: RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion}, code: "roast_revision_changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
				}
				writeRoastJSON(w, test.detail)
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "secret", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, test.request)
			if failure == nil || failure.Code != test.code || posts.Load() != 0 {
				t.Fatalf("failure = %+v, posts = %d", failure, posts.Load())
			}
		})
	}
}

func TestPostRoastReviewRequiresExactSecurityHeadersAndCoherentComment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(http.ResponseWriter)
		body   string
	}{
		{name: "missing cache control", mutate: func(w http.ResponseWriter) { w.Header().Del("Cache-Control") }},
		{name: "duplicate replay", mutate: func(w http.ResponseWriter) { w.Header().Add("X-Idempotent-Replay", "false") }},
		{name: "bad replay", mutate: func(w http.ResponseWriter) { w.Header().Set("X-Idempotent-Replay", "yes") }},
		{name: "bad location", mutate: func(w http.ResponseWriter) { w.Header().Set("Location", "/other") }},
		{name: "bad SHA", mutate: func(w http.ResponseWriter) { w.Header().Set("X-Roast-Revision-SHA256", strings.Repeat("e", 64)) }},
		{name: "bad template", mutate: func(w http.ResponseWriter) { w.Header().Set("X-Review-Template-Version", "other") }},
		{name: "comment roast mismatch", body: strings.Replace(activeCommentJSON(validReviewBody), `"roast_uuid":"`+roastUUID+`"`, `"roast_uuid":"`+labelUUID+`"`, 1)},
		{name: "creation body mismatch", body: activeCommentJSON("different body")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				setReviewHeaders(w, false)
				if test.mutate != nil {
					test.mutate(w)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				body := test.body
				if body == "" {
					body = activeCommentJSON(validReviewBody)
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "secret", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
			if failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v", failure)
			}
		})
	}
}

func TestPostRoastReviewMapsAPIAndMissingEndpointErrors(t *testing.T) {
	for _, test := range []struct {
		name, code string
		status     int
		want       string
		malformed  bool
	}{
		{name: "stale", code: "roast_revision_changed", status: http.StatusConflict, want: "roast_revision_changed"},
		{name: "conflict", code: "review_idempotency_conflict", status: http.StatusConflict, want: "review_idempotency_conflict"},
		{name: "not found", code: "not_found", status: http.StatusNotFound, want: "not_found"},
		{name: "auth", code: "authentication_required", status: http.StatusUnauthorized, want: "authentication_required"},
		{name: "missing endpoint", status: http.StatusNotFound, want: "server_upgrade_required", malformed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				if test.malformed {
					_, _ = io.WriteString(w, `{}`)
				} else {
					_, _ = io.WriteString(w, `{"error":{"code":"`+test.code+`","message":"safe"}}`)
				}
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "secret", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
			if failure == nil || failure.Code != test.want {
				t.Fatalf("failure = %+v; want %s", failure, test.want)
			}
		})
	}
}

func TestPostRoastReviewRejectsRedirectsMalformedResponsesAndReflections(t *testing.T) {
	for _, test := range []struct {
		name string
		post func(http.ResponseWriter, *http.Request)
	}{
		{name: "redirect", post: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
		}},
		{name: "malformed", post: func(w http.ResponseWriter, _ *http.Request) {
			setReviewHeaders(w, false)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{`)
		}},
		{name: "token header reflection", post: func(w http.ResponseWriter, _ *http.Request) {
			setReviewHeaders(w, false)
			w.Header().Set("X-Hostile", "reflection-secret-token")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, activeCommentJSON(validReviewBody))
		}},
		{name: "server header reflection", post: func(w http.ResponseWriter, r *http.Request) {
			setReviewHeaders(w, false)
			w.Header().Set("X-Hostile", "http://"+r.Host)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, activeCommentJSON(validReviewBody))
		}},
		{name: "body header reflection", post: func(w http.ResponseWriter, _ *http.Request) {
			setReviewHeaders(w, false)
			w.Header().Set("X-Hostile", "Measured evidence.")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, activeCommentJSON(validReviewBody))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				test.post(w, r)
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "reflection-secret-token", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
			if failure == nil {
				t.Fatal("expected failure")
			}
			for _, forbidden := range []string{"reflection-secret-token", server.URL, validReviewBody} {
				if strings.Contains(failure.Code, forbidden) || strings.Contains(failure.Message, forbidden) {
					t.Fatalf("failure leaks reflected data: %+v", failure)
				}
			}
		})
	}
}

func TestRoastReviewPublicTypesHaveExactJSONContract(t *testing.T) {
	requestType := reflect.TypeOf(RoastReviewRequest{})
	if requestType.NumField() != 3 {
		t.Fatalf("RoastReviewRequest fields = %d, want exactly 3", requestType.NumField())
	}
	want := []string{"body", "revision_sha256", "template_version"}
	for index, jsonName := range want {
		if got := strings.Split(requestType.Field(index).Tag.Get("json"), ",")[0]; got != jsonName {
			t.Fatalf("field %d JSON name = %q, want %q", index, got, jsonName)
		}
	}
}

func writeReviewResponse(w http.ResponseWriter, replay bool, body string) {
	setReviewHeaders(w, replay)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, activeCommentJSON(body))
}

func setReviewHeaders(w http.ResponseWriter, replay bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/api/v1/roasts/"+roastUUID+"/comments/"+commentUUID)
	w.Header().Set("X-Idempotent-Replay", map[bool]string{false: "false", true: "true"}[replay])
	w.Header().Set("X-Roast-Revision-SHA256", roastSHA256)
	w.Header().Set("X-Review-Template-Version", ReviewTemplateVersion)
}

func activeCommentJSON(body string) string {
	encoded, _ := json.Marshal(body)
	return `{"comment_uuid":"` + commentUUID + `","roast_uuid":"` + roastUUID + `","author_nickname":"Member","body":` + string(encoded) + `,"created_at":"` + roastTimestamp + `","edited_at":null,"deleted_at":null,"is_deleted":false,"can_edit":false,"can_delete":false}`
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
