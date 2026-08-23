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

	"github.com/fr3akX/artisan-cli/internal/securefile"
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
		{name: "missing content type", mutate: func(h http.Header) { h.Del("Content-Type") }},
		{name: "content type parameter", mutate: func(h http.Header) { h.Set("Content-Type", "application/x-artisan-profile; charset=binary") }},
		{name: "smuggled content type", mutate: func(h http.Header) {
			h.Set("Content-Type", "application/x-artisan-profile, application/x-artisan-profile")
		}},
		{name: "missing disposition", mutate: func(h http.Header) { h.Del("Content-Disposition") }},
		{name: "unsafe filename path", mutate: func(h http.Header) { h.Set("Content-Disposition", `attachment; filename="../stolen.alog"`) }},
		{name: "unsafe filename control", mutate: func(h http.Header) { h.Set("Content-Disposition", "attachment; filename=bad\\name.alog") }},
		{name: "inline disposition", mutate: func(h http.Header) { h.Set("Content-Disposition", `inline; filename="profile.alog"`) }},
		{name: "missing length", mutate: func(h http.Header) { h.Del("Content-Length") }},
		{name: "whitespace length", mutate: func(h http.Header) { h.Set("Content-Length", " 7") }},
		{name: "smuggled length", mutate: func(h http.Header) { h.Set("Content-Length", "7, 7") }},
		{name: "wrong length", mutate: func(h http.Header) { h.Set("Content-Length", "8") }},
		{name: "missing etag", mutate: func(h http.Header) { h.Del("ETag") }},
		{name: "wrong etag", mutate: func(h http.Header) { h.Set("ETag", `"`+strings.Repeat("a", 64)+`"`) }},
		{name: "missing content sha", mutate: func(h http.Header) { h.Del("X-Content-SHA256") }},
		{name: "wrong content sha", mutate: func(h http.Header) { h.Set("X-Content-SHA256", strings.Repeat("a", 64)) }},
		{name: "missing checksum sha", mutate: func(h http.Header) { h.Del("X-Checksum-SHA256") }},
		{name: "wrong checksum sha", mutate: func(h http.Header) { h.Set("X-Checksum-SHA256", strings.Repeat("a", 64)) }},
		{name: "missing revision", mutate: func(h http.Header) { h.Del("X-Revision-Number") }},
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

func TestDownloadRoastProfileRetriesTransientStatusWhenErrorBodyReadFails(t *testing.T) {
	body := []byte("complete-profile")
	sha := profileSHA(body)
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "retry-status.alog")
			var downloads atomic.Int32
			var closes atomic.Int32
			client := profileClientWithTransport(t, body, sha, func(response *http.Response) {
				if downloads.Add(1) == 1 {
					response.StatusCode = status
					response.Header = http.Header{"Content-Type": []string{"text/plain"}}
					response.Body = &failingDownloadReadCloser{data: []byte("partial untrusted error"), err: errors.New("error body read failed"), closes: &closes}
					response.ContentLength = -1
				}
			})
			result, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false)
			if failure != nil {
				t.Fatal(failure)
			}
			if downloads.Load() != 2 || closes.Load() != 1 || result.SHA256 != sha {
				t.Fatalf("downloads=%d closes=%d result=%#v", downloads.Load(), closes.Load(), result)
			}
			if contents, err := os.ReadFile(destination); err != nil || !bytes.Equal(contents, body) {
				t.Fatalf("contents = %q, %v", contents, err)
			}
		})
	}
}

func TestDownloadRoastProfileCancellationDuringRetryBackoffIsInterrupted(t *testing.T) {
	body := []byte("profile")
	sha := profileSHA(body)
	for _, test := range []struct {
		name       string
		response   func(*atomic.Int32, *atomic.Int32) (*http.Response, error)
		wantCloses int32
	}{
		{name: "transport", response: func(_ *atomic.Int32, _ *atomic.Int32) (*http.Response, error) {
			return nil, errors.New("temporary transport failure")
		}},
		{name: "response read", wantCloses: 1, response: func(_ *atomic.Int32, closes *atomic.Int32) (*http.Response, error) {
			header := make(http.Header)
			setProfileHeaders(header, body, 1, sha, "profile.alog")
			return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: int64(len(body)), Body: &failingDownloadReadCloser{err: errors.New("temporary read failure"), closes: closes}}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "cancel-backoff.alog")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var downloads atomic.Int32
			var closes atomic.Int32
			client, _ := NewClient("http://127.0.0.1", "secret", time.Second)
			client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if strings.HasSuffix(request.URL.Path, "/revisions") {
					return jsonHTTPResponse(http.StatusOK, `{"items":[`+profileRevisionJSON(1, sha, int64(len(body)))+`],"next_cursor":null}`), nil
				}
				downloads.Add(1)
				go func() {
					time.Sleep(time.Millisecond)
					cancel()
				}()
				return test.response(&downloads, &closes)
			})
			if _, failure := client.DownloadRoastProfile(ctx, roastUUID, 1, destination, false); failure == nil || failure.Code != "interrupted" || failure.ExitCode != 130 {
				t.Fatalf("failure = %#v", failure)
			}
			if downloads.Load() != 1 || closes.Load() != test.wantCloses {
				t.Fatalf("downloads=%d closes=%d", downloads.Load(), closes.Load())
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination visible: %v", err)
			}
			assertNoDownloadTemps(t, destination)
		})
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

func TestDownloadRoastProfileRejectsHeldSourceMutationBeforePublication(t *testing.T) {
	body := []byte("verified-profile")
	sha := profileSHA(body)
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "no force", true: "force existing"}[force], func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "source-mutation.alog")
			if force {
				if err := os.WriteFile(destination, []byte("existing-destination"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			client := profileClientWithTransport(t, body, sha, nil)
			client.downloadOps.afterSealedBeforeCandidate = func(target *downloadTarget) error {
				_, err := target.heldSourceFile().WriteAt([]byte("MUTATION"), 0)
				return err
			}
			result, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, force)
			if failure == nil || result != (RoastProfileDownload{}) || failure.Code != "local_storage_error" {
				t.Fatalf("result=%#v failure=%#v", result, failure)
			}
			if force {
				if contents, err := os.ReadFile(destination); err != nil || string(contents) != "existing-destination" {
					t.Fatalf("existing destination = %q, %v", contents, err)
				}
			} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination visible: %v", err)
			}
		})
	}
}

func TestDownloadRoastProfileReportsAmbiguousPublicationSideEffects(t *testing.T) {
	body := []byte("complete-profile")
	sha := profileSHA(body)
	destination := filepath.Join(t.TempDir(), "ambiguous.alog")
	published := destination + ".published"
	client := profileClientWithTransport(t, body, sha, nil)
	client.downloadOps.nativeOperation = func(operation func() error) error {
		if err := operation(); err != nil {
			return err
		}
		return errors.New("reported after operation")
	}
	client.downloadOps.afterNativeBeforeReconcile = func(*downloadTarget) error {
		if err := os.Rename(destination, published); err != nil {
			return err
		}
		return os.WriteFile(destination, []byte("competitor"), 0o600)
	}
	result, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false)
	if failure == nil || failure.Message != "The roast profile may have been published, but its requested path identity is uncertain" || result.Path != destination || result.SHA256 != sha {
		t.Fatalf("result=%#v failure=%#v", result, failure)
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "competitor" {
		t.Fatalf("competitor=%q", contents)
	}
	if contents, _ := os.ReadFile(published); !bytes.Equal(contents, body) {
		t.Fatalf("published=%q", contents)
	}
}

func TestDownloadRoastProfileInstallAndDurabilityFailuresPropagate(t *testing.T) {
	body := []byte("complete-profile")
	sha := profileSHA(body)
	for _, test := range []struct {
		name        string
		force       bool
		inject      func(*downloadOperations)
		wantVisible bool
		wantMessage string
	}{
		{name: "sync", inject: func(ops *downloadOperations) { ops.syncFile = func(*os.File) error { return errors.New("sync") } }, wantMessage: "Unable to store the roast profile safely"},
		{name: "close", inject: func(ops *downloadOperations) {
			ops.closeFile = func(file *os.File) error { _ = file.Close(); return errors.New("close") }
		}, wantMessage: "Unable to store the roast profile safely"},
		{name: "no replace install", inject: func(ops *downloadOperations) {
			ops.nativeOperation = func(func() error) error { return errors.New("install") }
		}, wantMessage: "Unable to store the roast profile safely"},
		{name: "force install", force: true, inject: func(ops *downloadOperations) {
			ops.nativeOperation = func(func() error) error { return errors.New("replace") }
		}, wantMessage: "Unable to store the roast profile safely"},
		{name: "parent sync visible", inject: func(ops *downloadOperations) {
			ops.syncParent = func(string) error { return errors.New("parent sync") }
		}, wantVisible: true, wantMessage: "The roast profile is installed, but storage durability is uncertain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "failure.alog")
			if test.force {
				if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			client := profileClientWithTransport(t, body, sha, nil)
			test.inject(&client.downloadOps)
			result, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, test.force)
			if failure == nil || failure.Code != "local_storage_error" || failure.Message != test.wantMessage {
				t.Fatalf("failure = %#v", failure)
			}
			if test.wantVisible {
				if result.Path != destination || result.SHA256 != sha {
					t.Fatalf("visible result = %#v", result)
				}
				if contents, err := os.ReadFile(destination); err != nil || !bytes.Equal(contents, body) {
					t.Fatalf("visible contents = %q, %v", contents, err)
				}
			} else {
				if result != (RoastProfileDownload{}) {
					t.Fatalf("pre-visibility result = %#v", result)
				}
				if test.force {
					if contents, err := os.ReadFile(destination); err != nil || string(contents) != "existing" {
						t.Fatalf("existing destination = %q, %v", contents, err)
					}
				} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("destination visible: %v", err)
				}
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadRoastProfileUsesPrivateTemporaryAndInstalledFile(t *testing.T) {
	body := []byte("private-profile")
	sha := profileSHA(body)
	destination := filepath.Join(t.TempDir(), "private.alog")
	client := profileClientWithTransport(t, body, sha, nil)
	if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false); failure != nil {
		t.Fatal(failure)
	}
	file, err := securefile.OpenPrivate(destination)
	if err != nil {
		t.Fatalf("installed private contract: %v", err)
	}
	_ = file.Close()
}

func TestDownloadRoastProfileClosesHeldIdentityAfterSuccess(t *testing.T) {
	body := []byte("closed-profile")
	sha := profileSHA(body)
	destination := filepath.Join(t.TempDir(), "closed.alog")
	client := profileClientWithTransport(t, body, sha, nil)
	if _, failure := client.DownloadRoastProfile(context.Background(), roastUUID, 1, destination, false); failure != nil {
		t.Fatal(failure)
	}
	moved := destination + ".moved"
	if err := os.Rename(destination, moved); err != nil {
		t.Fatalf("rename after successful download (held descriptor leak): %v", err)
	}
	if contents, err := os.ReadFile(moved); err != nil || !bytes.Equal(contents, body) {
		t.Fatalf("moved contents = %q, %v", contents, err)
	}
}

func TestDownloadRoastProfileInstallRacePreservesCompetitor(t *testing.T) {
	body := []byte("profile")
	sha := profileSHA(body)
	destination := filepath.Join(t.TempDir(), "race.alog")
	client := profileClientWithTransport(t, body, sha, nil)
	client.downloadOps.nativeOperation = func(operation func() error) error {
		if err := os.WriteFile(destination, []byte("competitor"), 0o600); err != nil {
			return err
		}
		return operation()
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

func TestFindRoastRevisionRejectsMissingProgressAndBoundsRequests(t *testing.T) {
	fullPage := func(request int32, terminal bool) string {
		items := make([]string, maxRoastPageItems)
		for index := range items {
			number := int64(request-1)*maxRoastPageItems + int64(index) + 1
			items[index] = profileRevisionJSON(number, profileSHA([]byte("x")), 1)
		}
		next := `"page-` + strconv.Itoa(int(request)) + `"`
		if terminal {
			next = "null"
		}
		return `{"items":[` + strings.Join(items, ",") + `],"next_cursor":` + next + `}`
	}
	for _, test := range []struct {
		name         string
		response     func(int32) string
		wantCode     string
		wantExit     int
		wantRequests int32
	}{
		{name: "initial terminal empty", response: func(int32) string { return `{"items":[],"next_cursor":null}` }, wantCode: "not_found", wantExit: 6, wantRequests: 1},
		{name: "first empty with next", response: func(int32) string { return `{"items":[],"next_cursor":"next"}` }, wantCode: "invalid_server_response", wantExit: 9, wantRequests: 1},
		{name: "later terminal empty", response: func(request int32) string {
			if request == 1 {
				return `{"items":[` + profileRevisionJSON(1, profileSHA([]byte("x")), 1) + `],"next_cursor":"next"}`
			}
			return `{"items":[],"next_cursor":null}`
		}, wantCode: "invalid_server_response", wantExit: 9, wantRequests: 2},
		{name: "repeated cursor", response: func(request int32) string {
			return `{"items":[` + profileRevisionJSON(int64(request), profileSHA([]byte("x")), 1) + `],"next_cursor":"repeat"}`
		}, wantCode: "invalid_server_response", wantExit: 9, wantRequests: 2},
		{name: "page ceiling", response: func(request int32) string {
			return `{"items":[` + profileRevisionJSON(int64(request), profileSHA([]byte("x")), 1) + `],"next_cursor":"page-` + strconv.Itoa(int(request)) + `"}`
		}, wantCode: "pagination_page_limit_exceeded", wantExit: 9, wantRequests: MaxRoastAggregatePages},
		{name: "item ceiling", response: func(request int32) string {
			return fullPage(request, false)
		}, wantCode: "pagination_limit_exceeded", wantExit: 9, wantRequests: MaxRoastAggregateItems / maxRoastPageItems},
		{name: "exact item ceiling terminal", response: func(request int32) string {
			return fullPage(request, request == MaxRoastAggregateItems/maxRoastPageItems)
		}, wantCode: "not_found", wantExit: 6, wantRequests: MaxRoastAggregateItems / maxRoastPageItems},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			client, _ := NewClient("http://127.0.0.1", "secret", time.Minute)
			client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				request := requests.Add(1)
				response := jsonHTTPResponse(http.StatusOK, test.response(request))
				response.Header.Set("X-Roast-UUID", roastUUID)
				response.Header.Set("X-Roast-Revisions-Version", "1")
				return response, nil
			})
			_, failure := client.findRoastRevision(context.Background(), roastUUID, maxRoastRevisionNumber)
			if failure == nil || failure.Code != test.wantCode || failure.ExitCode != test.wantExit {
				t.Fatalf("failure = %#v", failure)
			}
			if requests.Load() != test.wantRequests {
				t.Fatalf("requests = %d, want %d", requests.Load(), test.wantRequests)
			}
		})
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
