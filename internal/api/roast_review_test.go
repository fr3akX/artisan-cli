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
	"strconv"
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

func TestPostRoastReviewCurrentFailedRevisionAcceptsReplayOrRaceCreation(t *testing.T) {
	failedDetail := strings.Replace(strings.Replace(validRoastDetailJSON(), `"state":"parsed"`, `"state":"parse_failed"`, 1), `"parse_state":"parsed"`, `"parse_state":"failed"`, 1)
	tests := []struct {
		name       string
		post       func(http.ResponseWriter)
		wantReplay bool
		wantBody   string
		wantCode   string
	}{
		{name: "completed slot replays", wantReplay: true, wantBody: "Earlier completed review", post: func(w http.ResponseWriter) {
			writeReviewResponse(w, true, "Earlier completed review")
		}},
		{name: "failed-to-parsed race creates slot", wantReplay: false, wantBody: validReviewBody, post: func(w http.ResponseWriter) {
			writeReviewResponse(w, false, validReviewBody)
		}},
		{name: "unclaimed slot maps revision conflict", wantCode: "roast_revision_changed", post: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"roast_revision_changed","message":"stale"}}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					writeRoastJSON(w, failedDetail)
				case http.MethodPost:
					posts.Add(1)
					test.post(w)
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
			}))
			defer server.Close()

			client, _ := NewClient(server.URL, "failed-current-secret", time.Second)
			result, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
				Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
			})
			if posts.Load() != 1 {
				t.Fatalf("posts = %d, want 1", posts.Load())
			}
			if test.wantCode != "" {
				if failure == nil || failure.Code != test.wantCode {
					t.Fatalf("result = %#v, failure = %+v; want %s", result, failure, test.wantCode)
				}
				return
			}
			if failure != nil || result.IdempotentReplay != test.wantReplay || result.Comment.Body == nil || *result.Comment.Body != test.wantBody {
				t.Fatalf("result = %#v, failure = %+v", result, failure)
			}
		})
	}
}

func TestPostRoastReviewReplaysCompletedOldRevisionAfterCurrentAdvancesAndParseStateChanges(t *testing.T) {
	newSHA := strings.Repeat("e", 64)
	detail := roastDetailWithCurrentRevision(t, 2, newSHA)
	var revisionReads, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/roasts/" + roastUUID:
			writeRoastJSON(w, detail)
		case "GET /api/v1/roasts/" + roastUUID + "/revisions":
			revisionReads.Add(1)
			failedRevision := strings.Replace(validRoastRevisionJSON(), `"parse_state":"parsed"`, `"parse_state":"failed"`, 1)
			writeRoastJSON(w, `{"items":[`+failedRevision+`],"next_cursor":null}`)
		case "POST /api/v1/roasts/" + roastUUID + "/comments/ai-review":
			posts.Add(1)
			writeReviewResponse(w, true, "Earlier completed review")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "old-replay-secret", time.Second)
	result, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
		Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
	})
	if failure != nil || !result.IdempotentReplay || result.Comment.Body == nil || *result.Comment.Body != "Earlier completed review" {
		t.Fatalf("result = %#v, failure = %+v", result, failure)
	}
	if revisionReads.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("revision reads = %d, posts = %d", revisionReads.Load(), posts.Load())
	}
}

func TestPostRoastReviewPostsVerifiedOldUnclaimedSlotAndMapsRevisionConflict(t *testing.T) {
	newSHA := strings.Repeat("e", 64)
	detail := roastDetailWithCurrentRevision(t, 2, newSHA)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/roasts/" + roastUUID:
			writeRoastJSON(w, detail)
		case "GET /api/v1/roasts/" + roastUUID + "/revisions":
			writeRoastJSON(w, `{"items":[`+validRoastRevisionJSON()+`],"next_cursor":null}`)
		case "POST /api/v1/roasts/" + roastUUID + "/comments/ai-review":
			posts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"roast_revision_changed","message":"stale"}}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "old-unclaimed-secret", time.Second)
	_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
		Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
	})
	if failure == nil || failure.Code != "roast_revision_changed" || failure.HTTPStatus == nil || *failure.HTTPStatus != http.StatusConflict || posts.Load() != 1 {
		t.Fatalf("failure = %+v, posts = %d", failure, posts.Load())
	}
}

func TestPostRoastReviewRejectsCreationForVerifiedOldRevisionSlot(t *testing.T) {
	newSHA := strings.Repeat("e", 64)
	detail := roastDetailWithCurrentRevision(t, 2, newSHA)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/roasts/" + roastUUID:
			writeRoastJSON(w, detail)
		case "GET /api/v1/roasts/" + roastUUID + "/revisions":
			writeRoastJSON(w, `{"items":[`+validRoastRevisionJSON()+`],"next_cursor":null}`)
		case "POST /api/v1/roasts/" + roastUUID + "/comments/ai-review":
			posts.Add(1)
			writeReviewResponse(w, false, validReviewBody)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "old-create-secret", time.Second)
	_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
		Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
	})
	if failure == nil || failure.Code != "invalid_server_response" || posts.Load() != 1 {
		t.Fatalf("failure = %+v, posts = %d; want one rejected old-slot creation", failure, posts.Load())
	}
}

func TestPostRoastReviewRejectsUnknownOrMismatchedOldRevisionBeforePost(t *testing.T) {
	newSHA := strings.Repeat("e", 64)
	detail := roastDetailWithCurrentRevision(t, 2, newSHA)
	mismatchedRevision := strings.Replace(validRoastRevisionJSON(), roastSHA256, strings.Repeat("c", 64), 1)
	thirdRevision := strings.Replace(validRoastRevisionJSON(), `"revision_number":1`, `"revision_number":3`, 1)
	tests := []struct {
		name      string
		page      string
		wantCode  string
		wantReads int32
	}{
		{name: "unknown revision number", page: `{"items":[],"next_cursor":null}`, wantCode: "roast_revision_changed", wantReads: 1},
		{name: "revision SHA mismatch", page: `{"items":[` + mismatchedRevision + `],"next_cursor":null}`, wantCode: "roast_revision_changed", wantReads: 1},
		{name: "repeated cursor is bounded", page: `{"items":[` + thirdRevision + `],"next_cursor":"same"}`, wantCode: "invalid_server_response", wantReads: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var revisionReads, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/roasts/" + roastUUID:
					writeRoastJSON(w, detail)
				case "GET /api/v1/roasts/" + roastUUID + "/revisions":
					revisionReads.Add(1)
					writeRoastJSON(w, test.page)
				case "POST /api/v1/roasts/" + roastUUID + "/comments/ai-review":
					posts.Add(1)
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "old-invalid-secret", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
				Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
			})
			if failure == nil || failure.Code != test.wantCode || revisionReads.Load() != test.wantReads || posts.Load() != 0 {
				t.Fatalf("failure = %+v, revision reads = %d, posts = %d", failure, revisionReads.Load(), posts.Load())
			}
		})
	}
}

func TestPostRoastReviewRejectsSameNumberDifferentSHAAndFutureRevisionBeforeLookupOrPost(t *testing.T) {
	currentSHA := strings.Repeat("e", 64)
	detail := roastDetailWithCurrentRevision(t, 2, currentSHA)
	tests := []struct {
		name           string
		revisionNumber int64
		sha            string
	}{
		{name: "same revision number with different SHA", revisionNumber: 2, sha: strings.Repeat("c", 64)},
		{name: "future revision number", revisionNumber: 3, sha: strings.Repeat("d", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var revisionReads, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/roasts/" + roastUUID:
					writeRoastJSON(w, detail)
				case "GET /api/v1/roasts/" + roastUUID + "/revisions":
					revisionReads.Add(1)
				case "POST /api/v1/roasts/" + roastUUID + "/comments/ai-review":
					posts.Add(1)
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
			}))
			defer server.Close()

			body := "AI roast analysis\nTemplate: " + ReviewTemplateVersion + "\nProfile revision: " + strconv.FormatInt(test.revisionNumber, 10) + " (" + test.sha + ")\n\nMeasured evidence."
			client, _ := NewClient(server.URL, "ordered-revision-secret", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
				Body: body, RevisionSHA256: test.sha, TemplateVersion: ReviewTemplateVersion,
			})
			if failure == nil || failure.Code != "roast_revision_changed" || revisionReads.Load() != 0 || posts.Load() != 0 {
				t.Fatalf("failure = %+v, revision reads = %d, posts = %d", failure, revisionReads.Load(), posts.Load())
			}
		})
	}
}

func TestPostRoastReviewUsesBoundedRevisionPaginationForOldSlots(t *testing.T) {
	newSHA := strings.Repeat("e", 64)
	detail := roastDetailWithCurrentRevision(t, 2, newSHA)
	thirdRevision := strings.Replace(strings.Replace(validRoastRevisionJSON(), `"revision_number":1`, `"revision_number":3`, 1), roastSHA256, strings.Repeat("d", 64), 1)
	var revisionReads, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/roasts/" + roastUUID:
			writeRoastJSON(w, detail)
		case "GET /api/v1/roasts/" + roastUUID + "/revisions":
			read := revisionReads.Add(1)
			if r.URL.Query().Get("limit") != "100" {
				t.Fatalf("revision limit = %q", r.URL.Query().Get("limit"))
			}
			if read == 1 {
				if r.URL.Query().Get("cursor") != "" {
					t.Fatalf("first cursor = %q", r.URL.Query().Get("cursor"))
				}
				writeRoastJSON(w, `{"items":[`+thirdRevision+`],"next_cursor":"next"}`)
				return
			}
			if read != 2 || r.URL.Query().Get("cursor") != "next" {
				t.Fatalf("revision request %d cursor = %q", read, r.URL.Query().Get("cursor"))
			}
			writeRoastJSON(w, `{"items":[`+validRoastRevisionJSON()+`],"next_cursor":null}`)
		case "POST /api/v1/roasts/" + roastUUID + "/comments/ai-review":
			posts.Add(1)
			writeReviewResponse(w, true, validReviewBody)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "bounded-old-secret", time.Second)
	_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
		Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
	})
	if failure != nil || revisionReads.Load() != 2 || posts.Load() != 1 {
		t.Fatalf("failure = %+v, revision reads = %d, posts = %d", failure, revisionReads.Load(), posts.Load())
	}
}

func TestPostRoastReviewRejectsInvalidInputBeforePost(t *testing.T) {
	tests := []struct {
		name    string
		detail  string
		request RoastReviewRequest
		code    string
	}{
		{name: "bad body marker", detail: validRoastDetailJSON(), request: RoastReviewRequest{Body: "wrong", RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion}, code: "invalid_review"},
		{name: "bad template", detail: validRoastDetailJSON(), request: RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: "other"}, code: "invalid_review"},
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
		name, code, body, contentType string
		status                        int
		want                          string
	}{
		{name: "stale", code: "roast_revision_changed", status: http.StatusConflict, want: "roast_revision_changed"},
		{name: "conflict", code: "review_idempotency_conflict", status: http.StatusConflict, want: "review_idempotency_conflict"},
		{name: "chart unavailable", code: "chart_unavailable", status: http.StatusConflict, want: "chart_unavailable"},
		{name: "structured entity not found", code: "not_found", status: http.StatusNotFound, want: "not_found"},
		{name: "auth", code: "authentication_required", status: http.StatusUnauthorized, want: "authentication_required"},
		{name: "permission", code: "permission_denied", status: http.StatusForbidden, want: "permission_denied"},
		{name: "invalid review", code: "invalid_review", status: http.StatusUnprocessableEntity, want: "invalid_review"},
		{name: "missing endpoint empty", status: http.StatusNotFound, body: "", want: "server_upgrade_required"},
		{name: "empty body with declared JSON MIME", status: http.StatusNotFound, body: "", contentType: "application/json", want: "invalid_server_response"},
		{name: "missing endpoint Go default", status: http.StatusNotFound, body: "404 page not found\n", contentType: "text/plain; charset=utf-8", want: "server_upgrade_required"},
		{name: "missing endpoint supported plain text", status: http.StatusNotFound, body: "Not Found", contentType: "text/plain", want: "server_upgrade_required"},
		{name: "missing endpoint supported proxy HTML", status: http.StatusNotFound, body: "<html><body>Not Found</body></html>", contentType: "text/html; charset=UTF-8", want: "server_upgrade_required"},
		{name: "framework detail not found", status: http.StatusNotFound, body: `{"detail":"Not Found"}`, contentType: "application/json; charset=utf-8", want: "server_upgrade_required"},
		{name: "Go default with HTML MIME", status: http.StatusNotFound, body: "404 page not found\n", contentType: "text/html", want: "invalid_server_response"},
		{name: "Go default with leading whitespace", status: http.StatusNotFound, body: " 404 page not found\n", contentType: "text/plain", want: "invalid_server_response"},
		{name: "supported plain text with newline", status: http.StatusNotFound, body: "Not Found\n", contentType: "text/plain", want: "invalid_server_response"},
		{name: "supported proxy HTML with newline", status: http.StatusNotFound, body: "<html><body>Not Found</body></html>\n", contentType: "text/html", want: "invalid_server_response"},
		{name: "duplicate plain MIME", status: http.StatusNotFound, body: "Not Found", contentType: "text/plain, text/plain", want: "invalid_server_response"},
		{name: "supported plain text with JSON MIME", status: http.StatusNotFound, body: "Not Found", contentType: "application/json", want: "invalid_server_response"},
		{name: "supported HTML with plain MIME", status: http.StatusNotFound, body: "<html><body>Not Found</body></html>", contentType: "text/plain", want: "invalid_server_response"},
		{name: "arbitrary entity plain text", status: http.StatusNotFound, body: "Roast not found", contentType: "text/plain", want: "invalid_server_response"},
		{name: "authentication plain text", status: http.StatusNotFound, body: "Authentication required", contentType: "text/plain", want: "invalid_server_response"},
		{name: "maintenance plain text", status: http.StatusNotFound, body: "Service under maintenance", contentType: "text/plain", want: "invalid_server_response"},
		{name: "arbitrary HTML", status: http.StatusNotFound, body: "<html><body>Roast not found</body></html>", contentType: "text/html", want: "invalid_server_response"},
		{name: "maintenance HTML", status: http.StatusNotFound, body: "<html><body>Maintenance</body></html>", contentType: "text/html", want: "invalid_server_response"},
		{name: "whitespace only", status: http.StatusNotFound, body: " \n", contentType: "text/plain", want: "invalid_server_response"},
		{name: "malformed UTF-8 plain text", status: http.StatusNotFound, body: string([]byte{0xff}), contentType: "text/plain", want: "invalid_server_response"},
		{name: "JSON-looking scalar", status: http.StatusNotFound, body: `"Not Found"`, contentType: "application/json", want: "invalid_server_response"},
		{name: "framework detail with extra field", status: http.StatusNotFound, body: `{"detail":"Not Found","extra":true}`, contentType: "application/json", want: "invalid_server_response"},
		{name: "duplicate framework detail", status: http.StatusNotFound, body: `{"detail":"Not Found","detail":"Not Found"}`, contentType: "application/json", want: "invalid_server_response"},
		{name: "malformed JSON looking", status: http.StatusNotFound, body: `{`, contentType: "application/json", want: "invalid_server_response"},
		{name: "arbitrary JSON object", status: http.StatusNotFound, body: `{"message":"maintenance"}`, contentType: "application/json", want: "invalid_server_response"},
		{name: "empty JSON object", status: http.StatusNotFound, body: `{}`, contentType: "application/json", want: "invalid_server_response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				body := test.body
				contentType := test.contentType
				if test.code != "" {
					body = `{"error":{"code":"` + test.code + `","message":"safe"}}`
					contentType = "application/json"
				}
				if contentType != "" {
					w.Header().Set("Content-Type", contentType)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, body)
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

func TestPostRoastReviewMapsChartUnavailableToFixedPrivateFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeRoastJSON(w, validRoastDetailJSON())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"chart_unavailable","message":"Measured evidence."}}`)
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "chart-unavailable-secret", time.Second)
	_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
		Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
	})
	if failure == nil || failure.Code != "chart_unavailable" || failure.ExitCode != 7 || failure.HTTPStatus == nil || *failure.HTTPStatus != http.StatusConflict || failure.Message != "Roast chart is unavailable because the current revision is not parsed" {
		t.Fatalf("failure = %+v", failure)
	}
	if strings.Contains(failure.Message, "Measured evidence") {
		t.Fatalf("failure reflects server message: %+v", failure)
	}
}

func TestPostRoastReviewRejectsUnknownMalformedIncoherentAndReflectedErrorCodes(t *testing.T) {
	key, failure := CanonicalRoastReviewKey(roastUUID, roastSHA256, ReviewTemplateVersion)
	if failure != nil {
		t.Fatal(failure)
	}
	tests := []struct {
		name, code, message, details string
		status                       int
	}{
		{name: "auth at forbidden", code: "authentication_required", message: "safe", status: http.StatusForbidden},
		{name: "permission at unauthorized", code: "permission_denied", message: "safe", status: http.StatusUnauthorized},
		{name: "not found at conflict", code: "not_found", message: "safe", status: http.StatusConflict},
		{name: "revision changed at invalid review", code: "roast_revision_changed", message: "safe", status: http.StatusUnprocessableEntity},
		{name: "idempotency conflict at unauthorized", code: "review_idempotency_conflict", message: "safe", status: http.StatusUnauthorized},
		{name: "chart unavailable at unauthorized", code: "chart_unavailable", message: "safe", status: http.StatusUnauthorized},
		{name: "chart unavailable at forbidden", code: "chart_unavailable", message: "safe", status: http.StatusForbidden},
		{name: "chart unavailable at unprocessable entity", code: "chart_unavailable", message: "safe", status: http.StatusUnprocessableEntity},
		{name: "chart unavailable at not found", code: "chart_unavailable", message: "safe", status: http.StatusNotFound},
		{name: "invalid review at not found", code: "invalid_review", message: "safe", status: http.StatusNotFound},
		{name: "unknown", code: "custom_failure", message: "safe", status: http.StatusUnprocessableEntity},
		{name: "malformed", code: "not found", message: "safe", status: http.StatusNotFound},
		{name: "key in code", code: key, message: "safe", status: http.StatusUnprocessableEntity},
		{name: "key in message", code: "invalid_review", message: "prefix " + key + " suffix", status: http.StatusUnprocessableEntity},
		{name: "key in details", code: "invalid_review", message: "safe", details: `{"reflected":"` + key + `"}`, status: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				details := test.details
				if details == "" {
					details = `null`
				}
				encodedCode, _ := json.Marshal(test.code)
				encodedMessage, _ := json.Marshal(test.message)
				_, _ = io.WriteString(w, `{"error":{"code":`+string(encodedCode)+`,"message":`+string(encodedMessage)+`,"details":`+details+`}}`)
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "strict-error-secret", time.Second)
			_, gotFailure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
			if gotFailure == nil || gotFailure.Code != "invalid_server_response" || gotFailure.ExitCode != 9 || gotFailure.HTTPStatus == nil || *gotFailure.HTTPStatus != test.status {
				t.Fatalf("failure = %+v, want invalid_server_response at %d", gotFailure, test.status)
			}
			if strings.Contains(gotFailure.Code, key) || strings.Contains(gotFailure.Message, key) {
				t.Fatalf("failure leaks canonical key: %+v", gotFailure)
			}
		})
	}
}

func TestPostRoastReviewDoesNotReturnReviewBodyExcerptsFromAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeRoastJSON(w, validRoastDetailJSON())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_review","message":"Measured evidence."}}`)
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "secret", time.Second)
	_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
	if failure == nil || failure.Code != "invalid_review" {
		t.Fatalf("failure = %+v", failure)
	}
	if strings.Contains(failure.Message, "Measured evidence.") {
		t.Fatalf("failure reflects review-body excerpt: %+v", failure)
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

func TestPostRoastReviewRejectsSensitiveSuccessHeadersAndCommentFields(t *testing.T) {
	key, failure := CanonicalRoastReviewKey(roastUUID, roastSHA256, ReviewTemplateVersion)
	if failure != nil {
		t.Fatal(failure)
	}
	tests := []struct {
		name        string
		requestBody string
		token       string
		mutate      func(http.ResponseWriter, *http.Request, string) string
	}{
		{name: "unknown header without reflection", requestBody: validReviewBody, token: "unknown-header-secret", mutate: func(w http.ResponseWriter, _ *http.Request, body string) string {
			w.Header().Set("X-Custom-Metadata", "generic")
			return activeCommentJSON(body)
		}},
		{name: "segmented body across unknown headers", requestBody: validReviewBody, token: "segmented-header-secret", mutate: func(w http.ResponseWriter, _ *http.Request, body string) string {
			w.Header().Set("X-Review-Part-One", "Measured")
			w.Header().Set("X-Review-Part-Two", "evidence.")
			return activeCommentJSON(body)
		}},
		{name: "wrapped body excerpt in header value", requestBody: validReviewBody, token: "header-body-secret", mutate: func(w http.ResponseWriter, _ *http.Request, body string) string {
			w.Header().Set("X-Hostile", "prefix-Measured evidence.-suffix")
			return activeCommentJSON(body)
		}},
		{name: "body excerpt in header name", requestBody: validReviewBody + "\nX-Reflected-Body", token: "header-name-secret", mutate: func(w http.ResponseWriter, _ *http.Request, body string) string {
			w.Header().Set("X-Reflected-Body", "generic")
			return activeCommentJSON(body)
		}},
		{name: "key in header value", requestBody: validReviewBody, token: "header-key-secret", mutate: func(w http.ResponseWriter, _ *http.Request, body string) string {
			w.Header().Set("X-Hostile", "prefix-"+key+"-suffix")
			return activeCommentJSON(body)
		}},
		{name: "key in header name", requestBody: validReviewBody, token: "key-name-secret", mutate: func(w http.ResponseWriter, _ *http.Request, body string) string {
			w.Header().Set("X-"+key, "generic")
			return activeCommentJSON(body)
		}},
		{name: "token in header name", requestBody: validReviewBody, token: "header-name-token", mutate: func(w http.ResponseWriter, _ *http.Request, body string) string {
			w.Header().Set("X-Header-Name-Token", "generic")
			return activeCommentJSON(body)
		}},
		{name: "key in comment", requestBody: validReviewBody, token: "comment-key-secret", mutate: func(_ http.ResponseWriter, _ *http.Request, body string) string {
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"`+key+`"`, 1)
		}},
		{name: "token in comment", requestBody: validReviewBody, token: "comment-token-secret", mutate: func(_ http.ResponseWriter, _ *http.Request, body string) string {
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"comment-token-secret"`, 1)
		}},
		{name: "server URL in comment", requestBody: validReviewBody, token: "comment-url-secret", mutate: func(_ http.ResponseWriter, r *http.Request, body string) string {
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"http://`+r.Host+`"`, 1)
		}},
		{name: "body excerpt in comment", requestBody: validReviewBody, token: "comment-body-secret", mutate: func(_ http.ResponseWriter, _ *http.Request, body string) string {
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"Measured evidence."`, 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				setReviewHeaders(w, false)
				w.Header().Set("Content-Type", "application/json")
				responseBody := test.mutate(w, r, test.requestBody)
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, responseBody)
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, test.token, time.Second)
			_, gotFailure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: test.requestBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
			if gotFailure == nil || gotFailure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v, want invalid_server_response", gotFailure)
			}
			for _, forbidden := range []string{key, test.token, server.URL, "Measured evidence."} {
				if strings.Contains(gotFailure.Code, forbidden) || strings.Contains(gotFailure.Message, forbidden) {
					t.Fatalf("failure leaks reflected value %q: %+v", forbidden, gotFailure)
				}
			}
		})
	}
}

func TestPostRoastReviewRejectsPartialAndSegmentedBodyReflection(t *testing.T) {
	tests := []struct {
		name        string
		requestBody string
		mutate      func(http.ResponseWriter, string) string
	}{
		{name: "partial header value", requestBody: validReviewBody, mutate: func(w http.ResponseWriter, body string) string {
			w.Header().Set("Server", "Measured")
			return activeCommentJSON(body)
		}},
		{name: "wrapped partial header value", requestBody: validReviewBody, mutate: func(w http.ResponseWriter, body string) string {
			w.Header().Set("Server", "prefix-Measured-suffix")
			return activeCommentJSON(body)
		}},
		{name: "duplicate header value segmentation", requestBody: validReviewBody, mutate: func(w http.ResponseWriter, body string) string {
			w.Header().Add("Server", "Meas")
			w.Header().Add("Server", "ured")
			return activeCommentJSON(body)
		}},
		{name: "cross header segmentation", requestBody: validReviewBody + "\nMeasur e", mutate: func(w http.ResponseWriter, body string) string {
			w.Header().Set("Server", "Measu")
			w.Header().Set("Via", "r e")
			return activeCommentJSON(body)
		}},
		{name: "partial comment field", requestBody: validReviewBody, mutate: func(_ http.ResponseWriter, body string) string {
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"Measured"`, 1)
		}},
		{name: "wrapped partial comment field", requestBody: validReviewBody, mutate: func(_ http.ResponseWriter, body string) string {
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"prefix-Measured-suffix"`, 1)
		}},
		{name: "cross comment field segmentation", requestBody: validReviewBody + "\n" + commentUUID[len(commentUUID)-4:] + roastUUID[:4], mutate: func(_ http.ResponseWriter, body string) string {
			return activeCommentJSON(body)
		}},
		{name: "cross header and comment field segmentation", requestBody: validReviewBody, mutate: func(w http.ResponseWriter, body string) string {
			w.Header().Set("Server", "Meas")
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"ured"`, 1)
		}},
		{name: "three-way nonadjacent header and comment segmentation", requestBody: validReviewBody, mutate: func(w http.ResponseWriter, body string) string {
			w.Header().Set("Server", "Mea")
			w.Header().Set("X-Request-ID", "sur")
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"ed"`, 1)
		}},
		{name: "reverse-order three-way header and comment segmentation", requestBody: validReviewBody, mutate: func(w http.ResponseWriter, body string) string {
			w.Header().Set("Server", "ed")
			w.Header().Set("X-Request-ID", "sur")
			return strings.Replace(activeCommentJSON(body), `"author_nickname":"Member"`, `"author_nickname":"Mea"`, 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				setReviewHeaders(w, false)
				w.Header().Set("Content-Type", "application/json")
				responseBody := test.mutate(w, test.requestBody)
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, responseBody)
			}))
			defer server.Close()

			client, _ := NewClient(server.URL, "segmented-reflection-secret", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
				Body: test.requestBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
			})
			if failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v, want invalid_server_response", failure)
			}
		})
	}
}

func TestPostRoastReviewExcludesCommentBodyFromRequestExcerptReconstruction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeRoastJSON(w, validRoastDetailJSON())
			return
		}
		setReviewHeaders(w, true)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Server", "ured")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, activeCommentJSON("Meas"))
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "body-excerpt-control-token", time.Second)
	result, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
		Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
	})
	if failure != nil || !result.IdempotentReplay || result.Comment.Body == nil || *result.Comment.Body != "Meas" {
		t.Fatalf("result = %#v, failure = %+v", result, failure)
	}
}

func TestRoastReviewReflectionReconstructionIsOrderIndependentAndBounded(t *testing.T) {
	key, failure := CanonicalRoastReviewKey(roastUUID, roastSHA256, ReviewTemplateVersion)
	if failure != nil {
		t.Fatal(failure)
	}
	token := "segmented-token-value"
	serverURL := "https://trusted.example.test:8443"
	maximumBenignFields := make([]string, maxRoastReviewReflectionFields)
	for index := range maximumBenignFields {
		maximumBenignFields[index] = strings.Repeat(string(rune('A'+index)), maxRoastReviewReflectionFieldBytes)
	}
	tests := []struct {
		name      string
		fields    []string
		body      string
		forbidden []string
		want      bool
	}{
		{name: "three-way body window", fields: []string{"abc", "def", "gh"}, body: "prefix-abcdefgh-suffix", want: true},
		{name: "reverse-order body window", fields: []string{"gh", "def", "abc"}, body: "prefix-abcdefgh-suffix", want: true},
		{name: "eight-way body window", fields: []string{"a", "b", "c", "d", "e", "f", "g", "h"}, body: "prefix-abcdefgh-suffix", want: true},
		{name: "canonical key segmented", fields: splitReflectionValue(key, 4), forbidden: []string{key}, want: true},
		{name: "token segmented", fields: splitReflectionValue(token, 3), forbidden: []string{token}, want: true},
		{name: "server URL segmented in reverse", fields: reverseStrings(splitReflectionValue(serverURL, 4)), forbidden: []string{serverURL}, want: true},
		{name: "benign fragments", fields: []string{"abc", "xyz", "proxy"}, body: "Measured evidence.", forbidden: []string{key, token, serverURL}, want: false},
		{name: "bounded maximum benign input", fields: maximumBenignFields, body: strings.Repeat("😀", maxRoastReviewRunes), forbidden: []string{key, token, serverURL}, want: false},
		{name: "field count fails closed", fields: make([]string, maxRoastReviewReflectionFields+1), body: "unrelated", want: true},
		{name: "field length fails closed", fields: []string{strings.Repeat("x", maxRoastReviewReflectionFieldBytes+1)}, body: "unrelated", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := roastReviewFieldsReflectSensitiveData(test.fields, test.body, test.forbidden...); got != test.want {
				t.Fatalf("roastReviewFieldsReflectSensitiveData() = %v, want %v", got, test.want)
			}
		})
	}

	t.Run("matching prefixes hit the fixed state guard and fail closed", func(t *testing.T) {
		fields := make([]string, maxRoastReviewReflectionFields)
		for index := range fields {
			fields[index] = strings.Repeat("a", maxRoastReviewReflectionFieldBytes)
		}
		impossibleTarget := strings.Repeat("a", len(fields)*maxRoastReviewReflectionFieldBytes+1)
		reconstructed, states, exhausted := roastReviewTargetReconstructionWithinBudget(
			impossibleTarget, fields, len(fields), maxRoastReviewReconstructionStates,
		)
		if !reconstructed || !exhausted || states != maxRoastReviewReconstructionStates+1 {
			t.Fatalf("reconstructed = %v, states = %d, exhausted = %v", reconstructed, states, exhausted)
		}
	})
}

func TestPostRoastReviewRejectsSegmentedForbiddenValuesAcrossHeadersAndComment(t *testing.T) {
	key, failure := CanonicalRoastReviewKey(roastUUID, roastSHA256, ReviewTemplateVersion)
	if failure != nil {
		t.Fatal(failure)
	}
	for _, test := range []struct {
		name   string
		token  string
		target func(*httptest.Server) string
	}{
		{name: "canonical key", token: "segmented-key-control", target: func(*httptest.Server) string { return key }},
		{name: "bearer token", token: "segmented-token-value", target: func(*httptest.Server) string { return "segmented-token-value" }},
		{name: "server URL", token: "segmented-url-control", target: func(server *httptest.Server) string { return server.URL }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				parts := splitReflectionValue(test.target(server), 3)
				setReviewHeaders(w, false)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Server", parts[0])
				w.Header().Set("X-Request-ID", parts[1])
				response := strings.Replace(activeCommentJSON(validReviewBody), `"author_nickname":"Member"`, `"author_nickname":"`+parts[2]+`"`, 1)
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()

			client, _ := NewClient(server.URL, test.token, time.Second)
			_, gotFailure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
				Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
			})
			if gotFailure == nil || gotFailure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v, want invalid_server_response", gotFailure)
			}
		})
	}
}

func TestPostRoastReviewRejectsForbiddenValuesReconstructedWithCommentBody(t *testing.T) {
	key, failure := CanonicalRoastReviewKey(roastUUID, roastSHA256, ReviewTemplateVersion)
	if failure != nil {
		t.Fatal(failure)
	}
	type pattern struct {
		name   string
		count  int
		assign func(http.ResponseWriter, []string) (nickname, body string)
	}
	patterns := []pattern{
		{name: "body and header", count: 2, assign: func(w http.ResponseWriter, parts []string) (string, string) {
			w.Header().Set("Server", parts[1])
			return "Member", parts[0]
		}},
		{name: "body and nickname", count: 2, assign: func(_ http.ResponseWriter, parts []string) (string, string) {
			return parts[1], parts[0]
		}},
		{name: "three fields", count: 3, assign: func(w http.ResponseWriter, parts []string) (string, string) {
			w.Header().Set("Server", parts[1])
			return parts[2], parts[0]
		}},
		{name: "reverse order segments", count: 3, assign: func(w http.ResponseWriter, parts []string) (string, string) {
			w.Header().Set("Server", parts[2])
			return parts[1], parts[0]
		}},
	}
	for _, targetCase := range []struct {
		name  string
		token string
		value func(*httptest.Server) string
	}{
		{name: "canonical key", token: "body-key-control", value: func(*httptest.Server) string { return key }},
		{name: "bearer token", token: "body-segmented-bearer-token", value: func(*httptest.Server) string { return "body-segmented-bearer-token" }},
		{name: "server URL", token: "body-url-control", value: func(server *httptest.Server) string { return server.URL }},
	} {
		for _, patternCase := range patterns {
			t.Run(targetCase.name+"/"+patternCase.name, func(t *testing.T) {
				var posts atomic.Int32
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						writeRoastJSON(w, validRoastDetailJSON())
						return
					}
					posts.Add(1)
					parts := splitReflectionValue(targetCase.value(server), patternCase.count)
					setReviewHeaders(w, true)
					w.Header().Set("Content-Type", "application/json")
					nickname, commentBody := patternCase.assign(w, parts)
					response := strings.Replace(activeCommentJSON(commentBody), `"author_nickname":"Member"`, `"author_nickname":"`+nickname+`"`, 1)
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, response)
				}))
				defer server.Close()

				client, _ := NewClient(server.URL, targetCase.token, time.Second)
				_, gotFailure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{
					Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion,
				})
				if gotFailure == nil || gotFailure.Code != "invalid_server_response" || posts.Load() != 1 {
					t.Fatalf("failure = %+v, posts = %d; want one rejected response", gotFailure, posts.Load())
				}
			})
		}
	}
}

func TestPostRoastReviewRejectsMalformedOrOversizedOptionalResponseMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(http.ResponseWriter)
	}{
		{name: "duplicate Server", mutate: func(w http.ResponseWriter) { w.Header().Add("Server", "one"); w.Header().Add("Server", "two") }},
		{name: "oversized Server", mutate: func(w http.ResponseWriter) { w.Header().Set("Server", strings.Repeat("a", 257)) }},
		{name: "malformed Via", mutate: func(w http.ResponseWriter) { w.Header().Set("Via", "gateway-only") }},
		{name: "malformed request ID", mutate: func(w http.ResponseWriter) { w.Header().Set("X-Request-ID", "has spaces") }},
		{name: "malformed traceparent", mutate: func(w http.ResponseWriter) { w.Header().Set("traceparent", "00-short") }},
		{name: "malformed tracestate key", mutate: func(w http.ResponseWriter) { w.Header().Set("tracestate", "tenant@=opaque") }},
		{name: "duplicate tracestate key", mutate: func(w http.ResponseWriter) { w.Header().Set("tracestate", "vendor=one,vendor=two") }},
		{name: "oversized tracestate", mutate: func(w http.ResponseWriter) { w.Header().Set("tracestate", "vendor="+strings.Repeat("a", 506)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				setReviewHeaders(w, false)
				w.Header().Set("Content-Type", "application/json")
				test.mutate(w)
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, activeCommentJSON(validReviewBody))
			}))
			defer server.Close()

			client, _ := NewClient(server.URL, "metadata-format-secret", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
			if failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v, want invalid_server_response", failure)
			}
		})
	}
}

func TestValidTraceparentW3CVectorsAndFutureExtensions(t *testing.T) {
	base := "-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	largestBounded := "01" + base + "-" + strings.Repeat("x", maxRoastReviewTraceparentBytes-56)
	for _, value := range []string{
		"00" + base,
		"01" + base,
		"01" + base + "-opaque",
		"7a" + base + "-vendor=v1;next/field?yes",
		"fe" + base + "-00-opaque-field-two",
		largestBounded,
	} {
		if !validTraceparent(value) {
			t.Errorf("validTraceparent(%q) = false", value)
		}
	}
	for _, value := range []string{
		"ff" + base,
		"00" + base + "-opaque",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"01" + base + "opaque-without-leading-dash",
		"01" + base + "-has space",
		"01" + base + "-has\tcontrol",
		"01" + base + "-has\x7fdelete",
		"01" + base + "-non-ascii-界",
		largestBounded + "x",
	} {
		if validTraceparent(value) {
			t.Errorf("validTraceparent(%q) = true", value)
		}
	}
}

func TestValidTracestateW3COWSAndBounds(t *testing.T) {
	thirtyTwo := make([]string, 32)
	for index := range thirtyTwo {
		thirtyTwo[index] = "v" + strconv.Itoa(index) + "=value"
	}
	exactly512 := "a=" + strings.Repeat("x", 256) + ",b=" + strings.Repeat("y", 251)
	for _, value := range []string{
		"vendor=value",
		"vendor = value",
		"vendor\t=\tvalue",
		"vendor=one, tenant=two",
		"vendor=one two",
		"vendor=one\t,\ttenant = two",
		strings.Join(thirtyTwo, ","),
		exactly512,
	} {
		if !validTracestate(value) {
			t.Errorf("validTracestate(%q) = false", value)
		}
	}
	for _, value := range []string{
		" vendor=value",
		"vendor=value ",
		"vendor\v=value",
		"vendor=\vvalue",
		"vendor=one\ttwo",
		"vendor=value,\ftenant=two",
		"vendor=value,\u00a0tenant=two",
		"vendor = ",
		"vendor=one, vendor=two",
		strings.Join(append(thirtyTwo, "overflow=value"), ","),
		exactly512 + "z",
	} {
		if validTracestate(value) {
			t.Errorf("validTracestate(%q) = true", value)
		}
	}
}

func TestPostRoastReviewAcceptsOnlyStandardTransportAndProxyResponseHeaders(t *testing.T) {
	requestBody := validReviewBody + "\n" + ReviewTemplateVersion + "\n" + roastSHA256
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeRoastJSON(w, validRoastDetailJSON())
			return
		}
		setReviewHeaders(w, false)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Server", "trusted-proxy")
		w.Header().Set("Via", "1.1 gateway")
		w.Header().Set("X-Request-ID", "8a4f88de7c374172")
		w.Header().Set("traceparent", "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-vendor=v1;field/two")
		w.Header().Set("tracestate", "0tenant@system = opaque\t,\tvendor= value")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, activeCommentJSON(requestBody))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "standard-header-secret", time.Second)
	result, gotFailure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: requestBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
	if gotFailure != nil || result.Comment.CommentUUID != commentUUID {
		t.Fatalf("result = %#v, failure = %+v", result, gotFailure)
	}
}

func TestPostRoastReviewRejectsReflectionsInAllowedStandardHeaders(t *testing.T) {
	key, _ := CanonicalRoastReviewKey(roastUUID, roastSHA256, ReviewTemplateVersion)
	for _, test := range []struct {
		name, value string
	}{
		{name: "canonical key", value: "proxy " + key},
		{name: "token", value: "allowed-header-token"},
		{name: "server URL", value: "SERVER_URL"},
		{name: "review body line", value: "prefix Measured evidence. suffix"},
		{name: "partial review body", value: "Measured"},
		{name: "wrapped partial review body", value: "prefix-Measured-suffix"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeRoastJSON(w, validRoastDetailJSON())
					return
				}
				setReviewHeaders(w, false)
				w.Header().Set("Content-Type", "application/json")
				value := test.value
				if value == "SERVER_URL" {
					value = server.URL
				}
				w.Header().Set("Server", value)
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, activeCommentJSON(validReviewBody))
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "allowed-header-token", time.Second)
			_, failure := client.PostRoastReview(context.Background(), roastUUID, RoastReviewRequest{Body: validReviewBody, RevisionSHA256: roastSHA256, TemplateVersion: ReviewTemplateVersion})
			if failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v", failure)
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

func roastDetailWithCurrentRevision(t *testing.T, revisionNumber int64, sha string) string {
	t.Helper()
	detail := strings.Replace(validRoastDetailJSON(), `"revision_count":1`, `"revision_count":`+strconv.FormatInt(revisionNumber, 10), 1)
	current := strings.Replace(validRoastRevisionJSON(), `"revision_number":1`, `"revision_number":`+strconv.FormatInt(revisionNumber, 10), 1)
	current = strings.Replace(current, roastSHA256, sha, 1)
	detail = strings.Replace(detail, validRoastRevisionJSON(), current, 1)
	return detail
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

func splitReflectionValue(value string, count int) []string {
	parts := make([]string, 0, count)
	for index := 0; index < count; index++ {
		start := len(value) * index / count
		end := len(value) * (index + 1) / count
		parts = append(parts, value[start:end])
	}
	return parts
}

func reverseStrings(values []string) []string {
	reversed := append([]string(nil), values...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
