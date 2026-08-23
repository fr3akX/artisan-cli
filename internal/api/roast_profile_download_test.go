package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadRoastProfileFindsBoundedRevisionAndInstallsExactRawBytes(t *testing.T) {
	body := []byte("exact immutable artisan profile bytes\x00\xff")
	sha := profileSHA(body)
	destination := filepath.Join(t.TempDir(), "chosen-local-name.alog")
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer profile-secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.RequestURI() {
		case "/api/v1/roasts/" + roastUUID + "/revisions?limit=100":
			writeRevisionPage(t, w, []string{profileRevisionJSON(1, profileSHA([]byte("old")), 3)}, profileStringPointer("next-page"))
		case "/api/v1/roasts/" + roastUUID + "/revisions?cursor=next-page&limit=100":
			writeRevisionPage(t, w, []string{profileRevisionJSON(2, sha, int64(len(body)))}, nil)
		case "/api/v1/roasts/" + roastUUID + "/revisions/2/download":
			setProfileHeaders(w.Header(), body, 2, sha, "informational-server-name.alog")
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected request %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "profile-secret", time.Second)
	result, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 2, destination, false)
	if failure != nil {
		t.Fatal(failure)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(contents, body) {
		t.Fatalf("contents = %x, %v", contents, err)
	}
	if result.Path != destination || result.RoastUUID != roastUUID || result.RevisionNumber != 2 || result.Bytes != int64(len(body)) || result.SHA256 != sha {
		t.Fatalf("result = %#v", result)
	}
	wantPaths := []string{
		"/api/v1/roasts/" + roastUUID + "/revisions?limit=100",
		"/api/v1/roasts/" + roastUUID + "/revisions?cursor=next-page&limit=100",
		"/api/v1/roasts/" + roastUUID + "/revisions/2/download",
	}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("paths = %v", paths)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadRoastProfileValidatesRevisionBeforeFilesystemOrDownload(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeRevisionPage(t, w, []string{profileRevisionJSON(1, profileSHA([]byte("one")), 3)}, nil)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	for _, revision := range []int64{0, -1, maxRoastRevisionNumber + 1} {
		destination := filepath.Join(t.TempDir(), "profile.alog")
		if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, revision, destination, false); failure == nil || failure.ExitCode != 2 || failure.Code != "invalid_revision_number" {
			t.Fatalf("revision %d failure = %#v", revision, failure)
		}
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("revision %d destination exists: %v", revision, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid revisions made %d requests", requests.Load())
	}

	destination := filepath.Join(t.TempDir(), "absent.alog")
	if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 2, destination, false); failure == nil || failure.ExitCode != 6 || failure.Code != "not_found" {
		t.Fatalf("absent failure = %#v", failure)
	}
	if requests.Load() != 1 {
		t.Fatalf("absent revision requests = %d", requests.Load())
	}
}

func TestDownloadRoastProfileRejectsHostileHeadersWithoutVisibility(t *testing.T) {
	body := []byte("profile")
	sha := profileSHA(body)
	for _, test := range []struct {
		name   string
		mutate func(http.Header)
	}{
		{name: "content type parameter", mutate: func(h http.Header) { h.Set("Content-Type", "application/x-artisan-profile; charset=binary") }},
		{name: "unsafe filename path", mutate: func(h http.Header) { h.Set("Content-Disposition", `attachment; filename="../stolen.alog"`) }},
		{name: "unsafe filename control", mutate: func(h http.Header) { h.Set("Content-Disposition", "attachment; filename=bad\\name.alog") }},
		{name: "inline disposition", mutate: func(h http.Header) { h.Set("Content-Disposition", `inline; filename="profile.alog"`) }},
		{name: "missing length", mutate: func(h http.Header) { h.Del("Content-Length") }},
		{name: "wrong length", mutate: func(h http.Header) { h.Set("Content-Length", "8") }},
		{name: "wrong etag", mutate: func(h http.Header) { h.Set("ETag", `"`+strings.Repeat("a", 64)+`"`) }},
		{name: "wrong content sha", mutate: func(h http.Header) { h.Set("X-Content-SHA256", strings.Repeat("a", 64)) }},
		{name: "wrong checksum sha", mutate: func(h http.Header) { h.Set("X-Checksum-SHA256", strings.Repeat("a", 64)) }},
		{name: "wrong revision", mutate: func(h http.Header) { h.Set("X-Revision-Number", "2") }},
		{name: "duplicate content type", mutate: func(h http.Header) { h.Add("Content-Type", "application/x-artisan-profile") }},
		{name: "duplicate disposition", mutate: func(h http.Header) { h.Add("Content-Disposition", `attachment; filename="other.alog"`) }},
		{name: "duplicate length", mutate: func(h http.Header) { h.Add("Content-Length", strconv.Itoa(len(body))) }},
		{name: "duplicate etag", mutate: func(h http.Header) { h.Add("ETag", `"`+sha+`"`) }},
		{name: "comma hidden etag", mutate: func(h http.Header) { h.Set("ETag", `"`+sha+`", "`+sha+`"`) }},
		{name: "duplicate content sha", mutate: func(h http.Header) { h.Add("X-Content-SHA256", sha) }},
		{name: "duplicate checksum", mutate: func(h http.Header) { h.Add("X-Checksum-SHA256", sha) }},
		{name: "duplicate revision", mutate: func(h http.Header) { h.Add("X-Revision-Number", "1") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "local.alog")
			client := profileClientWithTransport(t, body, sha, func(response *http.Response) { test.mutate(response.Header) })
			if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false); failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %#v", failure)
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination exists: %v", err)
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadRoastProfileRequiresExactBoundedCountAndSHA(t *testing.T) {
	declared := []byte("1234567")
	sha := profileSHA(declared)
	for _, test := range []struct {
		name string
		body []byte
		sha  string
	}{
		{name: "short", body: declared[:6], sha: sha},
		{name: "long", body: append(append([]byte{}, declared...), '8'), sha: sha},
		{name: "hash mismatch", body: []byte("7654321"), sha: sha},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			client := profileClientWithTransport(t, declared, test.sha, func(response *http.Response) {
				response.Body = io.NopCloser(bytes.NewReader(test.body))
				response.ContentLength = int64(len(declared))
			})
			if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false); failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %#v", failure)
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination exists: %v", err)
			}
			assertNoDownloadTemps(t, destination)
		})
	}

	oversizedRevision := profileRevisionJSON(1, sha, maxRoastProfileBytes+1)
	client, _ := NewClient("http://127.0.0.1", "secret", time.Second)
	var requests atomic.Int32
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if strings.HasSuffix(request.URL.Path, "/revisions") {
			return jsonHTTPResponse(http.StatusOK, `{"items":[`+oversizedRevision+`],"next_cursor":null}`), nil
		}
		return nil, errors.New("download must not be requested")
	})
	if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, filepath.Join(t.TempDir(), "oversized.alog"), false); failure == nil || failure.Code != "invalid_server_response" {
		t.Fatalf("oversized revision failure = %#v", failure)
	}
	if requests.Load() != 1 {
		t.Fatalf("oversized requests = %d", requests.Load())
	}
}

func TestDownloadRoastProfileRetriesOnlyBeforeInstallAndResetsTemporary(t *testing.T) {
	body := []byte("complete-profile")
	sha := profileSHA(body)
	destination := filepath.Join(t.TempDir(), "retry.alog")
	var downloads atomic.Int32
	var closes atomic.Int32
	client, _ := NewClient("http://127.0.0.1", "secret", time.Second)
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/revisions") {
			return jsonHTTPResponse(http.StatusOK, `{"items":[`+profileRevisionJSON(1, sha, int64(len(body)))+`],"next_cursor":null}`), nil
		}
		attempt := downloads.Add(1)
		header := make(http.Header)
		setProfileHeaders(header, body, 1, sha, "retry.alog")
		if attempt < 3 {
			return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: int64(len(body)), Body: &failingDownloadReadCloser{data: []byte("stale"), err: errors.New("read failure"), closes: &closes}}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: int64(len(body)), Body: &failingDownloadReadCloser{data: body, closes: &closes}}, nil
	})
	result, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false)
	if failure != nil {
		t.Fatal(failure)
	}
	contents, _ := os.ReadFile(destination)
	if downloads.Load() != 3 || closes.Load() != 3 || !bytes.Equal(contents, body) || result.SHA256 != sha {
		t.Fatalf("downloads=%d closes=%d contents=%q result=%#v", downloads.Load(), closes.Load(), contents, result)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadRoastProfileLocalWriteAndValidationFailuresPreserveForcedDestination(t *testing.T) {
	body := []byte("complete-profile")
	sha := profileSHA(body)
	for _, test := range []struct {
		name   string
		inject func(*Client)
	}{
		{name: "write failure", inject: func(client *Client) {
			client.downloadOps.writer = func(*os.File) io.Writer { return failingDownloadWriter{err: errors.New("disk failure")} }
		}},
		{name: "checksum failure", inject: func(client *Client) {
			defaults := client.httpClient.Transport
			client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, err := defaults.RoundTrip(request)
				if err == nil && strings.HasSuffix(request.URL.Path, "/download") {
					response.Header.Set("X-Checksum-SHA256", strings.Repeat("a", 64))
				}
				return response, err
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
			client := profileClientWithTransport(t, body, sha, nil)
			test.inject(client)
			if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, true); failure == nil {
				t.Fatal("failure succeeded")
			}
			contents, err := os.ReadFile(destination)
			if err != nil || string(contents) != "existing" {
				t.Fatalf("destination = %q, %v", contents, err)
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadRoastProfileCancellationClosesBodyWithoutRetryOrVisibility(t *testing.T) {
	body := []byte("profile")
	sha := profileSHA(body)
	destination := filepath.Join(t.TempDir(), "cancel.alog")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blockedBody := &cancelReadCloser{ctx: ctx}
	var downloads atomic.Int32
	client, _ := NewClient("http://127.0.0.1", "secret", time.Second)
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/revisions") {
			return jsonHTTPResponse(http.StatusOK, `{"items":[`+profileRevisionJSON(1, sha, int64(len(body)))+`],"next_cursor":null}`), nil
		}
		downloads.Add(1)
		header := make(http.Header)
		setProfileHeaders(header, body, 1, sha, "profile.alog")
		return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: int64(len(body)), Body: blockedBody}, nil
	})
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, failure := client.DownloadRoastProfile(ctx, roastUUID, 1, destination, false); failure == nil || failure.Code != "interrupted" || failure.ExitCode != 130 {
		t.Fatalf("failure = %#v", failure)
	}
	if downloads.Load() != 1 || !blockedBody.closed.Load() {
		t.Fatalf("downloads=%d closed=%v", downloads.Load(), blockedBody.closed.Load())
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination visible: %v", err)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadRoastProfileInstallRacePreservesCompetitor(t *testing.T) {
	body := []byte("profile")
	sha := profileSHA(body)
	destination := filepath.Join(t.TempDir(), "race.alog")
	client := profileClientWithTransport(t, body, sha, nil)
	defaults := client.downloadOps
	client.downloadOps.installNoReplace = func(from, to string) (bool, error) {
		if err := os.WriteFile(to, []byte("competitor"), 0o600); err != nil {
			return false, err
		}
		return defaults.installNoReplace(from, to)
	}
	if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false); failure == nil || failure.Message != "Destination already exists; use --force to replace it" {
		t.Fatalf("failure = %#v", failure)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "competitor" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadRoastProfileRefusesRedirectClassifies404AndProtectsSecrets(t *testing.T) {
	body := []byte("profile")
	sha := profileSHA(body)
	for _, test := range []struct {
		name, errorBody, wantCode string
		status                    int
	}{
		{name: "entity not found", status: http.StatusNotFound, errorBody: `{"error":{"code":"not_found","message":"Not found","details":null}}`, wantCode: "not_found"},
		{name: "route absent", status: http.StatusNotFound, errorBody: `{"detail":"Not Found"}`, wantCode: "server_upgrade_required"},
		{name: "secret reflection", status: http.StatusInternalServerError, errorBody: `{"error":{"code":"profile-secret","message":"http://127.0.0.1 secret profile-secret","details":null}}`, wantCode: "invalid_server_response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			client := profileClientWithTransport(t, body, sha, func(response *http.Response) {
				response.StatusCode = test.status
				response.Header = http.Header{"Content-Type": []string{"application/json"}}
				response.Body = io.NopCloser(strings.NewReader(test.errorBody))
				response.ContentLength = int64(len(test.errorBody))
			})
			_, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false)
			if failure == nil || failure.Code != test.wantCode || strings.Contains(failure.Code+failure.Message, "profile-secret") || strings.Contains(failure.Code+failure.Message, "http://127.0.0.1") {
				t.Fatalf("failure = %#v", failure)
			}
			assertNoDownloadTemps(t, destination)
		})
	}

	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/revisions") {
			writeRevisionPage(t, w, []string{profileRevisionJSON(1, sha, int64(len(body)))}, nil)
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, _ := NewClient(source.URL, "secret", time.Second)
	if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, filepath.Join(t.TempDir(), "redirect.alog"), false); failure == nil || failure.Code != "redirect_refused" {
		t.Fatalf("redirect failure = %#v", failure)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d", targetRequests.Load())
	}
}

func TestRoastProfileDownloadJSONContract(t *testing.T) {
	encoded, err := json.Marshal(RoastProfileDownload{Path: "profile.alog", RoastUUID: roastUUID, RevisionNumber: 2, Bytes: 12, SHA256: roastSHA256})
	want := `{"path":"profile.alog","roast_uuid":"` + roastUUID + `","revision_number":2,"bytes":12,"sha256":"` + roastSHA256 + `"}`
	if err != nil || string(encoded) != want {
		t.Fatalf("encoded = %s, %v", encoded, err)
	}
}

func profileClientWithTransport(t *testing.T, declaredBody []byte, sha string, mutate func(*http.Response)) *Client {
	t.Helper()
	client, _ := NewClient("http://127.0.0.1", "profile-secret", time.Second)
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/revisions") {
			return jsonHTTPResponse(http.StatusOK, `{"items":[`+profileRevisionJSON(1, sha, int64(len(declaredBody)))+`],"next_cursor":null}`), nil
		}
		header := make(http.Header)
		setProfileHeaders(header, declaredBody, 1, sha, "profile.alog")
		response := &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: int64(len(declaredBody)), Body: io.NopCloser(bytes.NewReader(declaredBody))}
		if mutate != nil {
			mutate(response)
		}
		return response, nil
	})
	return client
}

func setProfileHeaders(header http.Header, body []byte, revision int64, sha, filename string) {
	header.Set("Content-Type", "application/x-artisan-profile")
	header.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("ETag", `"`+sha+`"`)
	header.Set("X-Content-SHA256", sha)
	header.Set("X-Checksum-SHA256", sha)
	header.Set("X-Revision-Number", strconv.FormatInt(revision, 10))
}

func profileSHA(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func profileRevisionJSON(number int64, sha string, size int64) string {
	return fmt.Sprintf(`{"revision_number":%d,"sha256":"%s","byte_size":%d,"parser_version":"artisan-4-v1","parse_state":"parsed","parse_diagnostic_code":null,"parse_diagnostic_message":null,"uploaded_at":"%s","metadata":{},"reparse_recommended":false}`, number, sha, size, roastTimestamp)
}

func writeRevisionPage(t *testing.T, w http.ResponseWriter, items []string, next *string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Roast-UUID", roastUUID)
	w.Header().Set("X-Roast-Revisions-Version", "1")
	nextJSON := "null"
	if next != nil {
		encoded, _ := json.Marshal(*next)
		nextJSON = string(encoded)
	}
	_, _ = io.WriteString(w, `{"items":[`+strings.Join(items, ",")+`],"next_cursor":`+nextJSON+`}`)
}

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

func profileStringPointer(value string) *string { return &value }
