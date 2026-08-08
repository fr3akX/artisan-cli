package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
)

func TestOversizedImageIsRejectedBeforeLotCreateOrImageAddNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.png")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(10*1024*1024 + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	metadata := []ImageUploadManifest{{UploadIndex: 0}}
	manifest := BeanLotCreateManifest{Fields: BeanLotFields{Name: "Lot", Varietals: []string{}}, Images: metadata}
	if _, failure := client.CreateBeanLotWithImages(context.Background(), manifest, []string{path}, "create-key"); failure == nil || failure.Code != "invalid_image_file" {
		t.Fatalf("create failure = %#v", failure)
	}
	if _, failure := client.AddInventoryImages(context.Background(), mutationLotID, metadata, []string{path}, "add-key"); failure == nil || failure.Code != "invalid_image_file" {
		t.Fatalf("add failure = %#v", failure)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestInventoryImageMutationsUseExactAdminContracts(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "front.jpg")
	second := filepath.Join(dir, "side.png")
	if err := os.WriteFile(first, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}

	type recorded struct {
		method, path, key, contentType string
		body                           []byte
	}
	var requests []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, recorded{method: r.Method, path: r.URL.Path, key: r.Header.Get("Idempotency-Key"), contentType: r.Header.Get("Content-Type"), body: body})
		_, _ = io.WriteString(w, mutationLotJSON(0))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)

	caption := " Front "
	alt := " Front of bag "
	metadata := []ImageUploadManifest{
		{UploadIndex: 0, Caption: &caption, AltText: &alt, IsCover: true},
		{UploadIndex: 1},
	}
	if _, failure := client.AddInventoryImages(context.Background(), mutationLotID, metadata, []string{first, second}, "image-add-key"); failure != nil {
		t.Fatal(failure)
	}
	patch, failure := NewInventoryImagePatch(map[string]any{"caption": nil, "alt_text": " Updated alt ", "is_cover": false})
	if failure != nil {
		t.Fatal(failure)
	}
	if _, failure = client.PatchInventoryImage(context.Background(), mutationLotID, commandAPIImageID, patch, "image-patch-key"); failure != nil {
		t.Fatal(failure)
	}
	dashedSecond := "33333333-3333-4333-8333-333333333333"
	if _, failure = client.ReorderInventoryImages(context.Background(), mutationLotID, []string{commandAPIImageID, dashedSecond}, "image-order-key"); failure != nil {
		t.Fatal(failure)
	}
	if _, failure = client.DeleteInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "image-delete-key"); failure != nil {
		t.Fatal(failure)
	}

	wantMethods := []string{"POST", "PATCH", "PUT", "DELETE"}
	wantPaths := []string{
		"/api/v1/inventory/admin/bean-lots/" + mutationLotID + "/images",
		"/api/v1/inventory/admin/bean-lots/" + mutationLotID + "/images/" + commandAPIImageID,
		"/api/v1/inventory/admin/bean-lots/" + mutationLotID + "/images/order",
		"/api/v1/inventory/admin/bean-lots/" + mutationLotID + "/images/" + commandAPIImageID,
	}
	if len(requests) != 4 {
		t.Fatalf("requests = %#v", requests)
	}
	for index := range requests {
		if requests[index].method != wantMethods[index] || requests[index].path != wantPaths[index] || requests[index].key == "" {
			t.Fatalf("request %d = %#v", index, requests[index])
		}
	}
	if requests[1].contentType != "application/json" || string(requests[1].body) != `{"alt_text":"Updated alt","caption":null,"is_cover":false}` {
		t.Fatalf("patch = type %q body %q", requests[1].contentType, requests[1].body)
	}
	if string(requests[2].body) != `{"image_ids":["22222222222242228222222222222222","33333333333343338333333333333333"]}` {
		t.Fatalf("order body = %q", requests[2].body)
	}
	if len(requests[3].body) != 0 {
		t.Fatalf("delete body = %q", requests[3].body)
	}

	mediaType, parameters, err := mime.ParseMediaType(requests[0].contentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("multipart content type = %q, %v", requests[0].contentType, err)
	}
	reader := multipart.NewReader(bytes.NewReader(requests[0].body), parameters["boundary"])
	manifestPart, err := reader.NextPart()
	if err != nil || manifestPart.FormName() != "manifest" {
		t.Fatalf("manifest part = %#v, %v", manifestPart, err)
	}
	var manifest struct {
		Images []ImageUploadManifest `json:"images"`
	}
	if err := json.NewDecoder(manifestPart).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Images) != 2 || manifest.Images[0].Caption == nil || *manifest.Images[0].Caption != "Front" || manifest.Images[0].AltText == nil || *manifest.Images[0].AltText != "Front of bag" || !manifest.Images[0].IsCover {
		t.Fatalf("manifest = %#v", manifest)
	}
	for index, expected := range []struct{ filename, contentType, body string }{{"front.jpg", "image/jpeg", "jpeg"}, {"side.png", "image/png", "png"}} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		contents, _ := io.ReadAll(part)
		if part.FormName() != "images" || part.FileName() != expected.filename || part.Header.Get("Content-Type") != expected.contentType || string(contents) != expected.body {
			t.Fatalf("image part %d = filename %q type %q body %q", index, part.FileName(), part.Header.Get("Content-Type"), contents)
		}
	}
}

func TestInventoryImageMutationsRequireExactSuccessStatuses(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "image.jpg")
	if err := os.WriteFile(imagePath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, _ := NewInventoryImagePatch(map[string]any{"caption": "updated"})
	for _, test := range []struct {
		name   string
		status int
		call   func(*Client) *output.Error
	}{
		{name: "add created", status: http.StatusCreated, call: func(client *Client) *output.Error {
			_, failure := client.AddInventoryImages(context.Background(), mutationLotID, []ImageUploadManifest{{UploadIndex: 0}}, []string{imagePath}, "key")
			return failure
		}},
		{name: "patch created", status: http.StatusCreated, call: func(client *Client) *output.Error {
			_, failure := client.PatchInventoryImage(context.Background(), mutationLotID, commandAPIImageID, patch, "key")
			return failure
		}},
		{name: "order no content", status: http.StatusNoContent, call: func(client *Client) *output.Error {
			_, failure := client.ReorderInventoryImages(context.Background(), mutationLotID, []string{commandAPIImageID}, "key")
			return failure
		}},
		{name: "delete no content", status: http.StatusNoContent, call: func(client *Client) *output.Error {
			_, failure := client.DeleteInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "key")
			return failure
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(test.status)
				if test.status != http.StatusNoContent {
					_, _ = io.WriteString(w, mutationLotJSON(0))
				}
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "secret", time.Second)
			failure := test.call(client)
			if failure == nil || failure.Code != "invalid_server_response" || failure.HTTPStatus == nil || *failure.HTTPStatus != test.status {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

func TestInventoryImageUploadRetryReopensIdenticalFilesAndRejectsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.jpg")
	if err := os.WriteFile(path, []byte("same-upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := []ImageUploadManifest{{UploadIndex: 0}}
	var bodies [][]byte
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(bodies) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"busy","message":"busy","details":null}}`)
			return
		}
		_, _ = io.WriteString(w, mutationLotJSON(0))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	if _, failure := client.AddInventoryImages(context.Background(), mutationLotID, metadata, []string{path}, "same-retry-key"); failure != nil {
		t.Fatal(failure)
	}
	if len(bodies) != 3 || !bytes.Equal(bodies[0], bodies[1]) || !bytes.Equal(bodies[1], bodies[2]) || !reflect.DeepEqual(keys, []string{"same-retry-key", "same-retry-key", "same-retry-key"}) {
		t.Fatalf("retry bodies equal=%v/%v keys=%v", bytes.Equal(bodies[0], bodies[1]), bytes.Equal(bodies[1], bodies[2]), keys)
	}

	var attempts atomic.Int32
	replacedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		replacement := path + ".new"
		_ = os.WriteFile(replacement, []byte("replacement"), 0o600)
		_ = os.Rename(replacement, path)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"busy","message":"busy","details":null}}`)
	}))
	defer replacedServer.Close()
	replacedClient, _ := NewClient(replacedServer.URL, "secret", time.Second)
	if _, failure := replacedClient.AddInventoryImages(context.Background(), mutationLotID, metadata, []string{path}, "changed-retry-key"); failure == nil || failure.Code != "image_file_changed" {
		t.Fatalf("failure = %#v", failure)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestInventoryImageUploadDoesNotRetryAfterActiveSourceMutation(t *testing.T) {
	const imageSize = 1 << 20
	path := filepath.Join(t.TempDir(), "active.jpg")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), imageSize), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	client, _ := NewClient("http://127.0.0.1", "active-secret", time.Second)
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		if _, err := io.ReadFull(request.Body, make([]byte, imageSize/4)); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt(bytes.Repeat([]byte("z"), 4096), 3*imageSize/4); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, request.Body)
		_ = request.Body.Close()
		return nil, readErr
	})
	_, failure := client.AddInventoryImages(context.Background(), mutationLotID, []ImageUploadManifest{{UploadIndex: 0}}, []string{path}, "active-key")
	if failure == nil || failure.ExitCode != 2 || failure.Code != "image_file_changed" || failure.Message != "An image file changed after upload preparation" {
		t.Fatalf("failure = %#v", failure)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestInventoryImageValidationIsLocalAndStrict(t *testing.T) {
	valid := filepath.Join(t.TempDir(), "valid.jpg")
	if err := os.WriteFile(valid, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, _ := NewClient("http://127.0.0.1:1", "secret", time.Second)
	caption := strings.Repeat("x", 501)
	invalidMetadata := [][]ImageUploadManifest{
		{},
		{{UploadIndex: 1}},
		{{UploadIndex: 0, Caption: &caption}},
		{{UploadIndex: 0, IsCover: true}, {UploadIndex: 1, IsCover: true}},
	}
	for _, metadata := range invalidMetadata {
		paths := make([]string, len(metadata))
		for index := range paths {
			paths[index] = valid
		}
		if _, failure := client.AddInventoryImages(context.Background(), mutationLotID, metadata, paths, "key"); failure == nil || failure.ExitCode != 2 {
			t.Errorf("accepted metadata %#v: %#v", metadata, failure)
		}
	}
	if _, failure := client.ReorderInventoryImages(context.Background(), mutationLotID, []string{commandAPIImageID, commandAPIImageID}, "key"); failure == nil || failure.ExitCode != 2 {
		t.Fatalf("duplicate order failure = %#v", failure)
	}
	if _, failure := NewInventoryImagePatch(map[string]any{}); failure == nil {
		t.Fatal("accepted empty patch")
	}
	if _, failure := NewInventoryImagePatch(map[string]any{"is_cover": nil}); failure == nil {
		t.Fatal("accepted null cover")
	}
}

func TestDownloadInventoryImageStreamsPrivateWebPAtomically(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "download.webp")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/inventory/admin/bean-lots/"+mutationLotID+"/images/"+commandAPIImageID+"/thumbnail" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request = %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		temps, err := filepath.Glob(filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".tmp-*"))
		if err != nil || len(temps) != 1 {
			t.Errorf("active temporary files = %v, %v", temps, err)
		} else if info, statErr := os.Stat(temps[0]); statErr != nil || info.Mode().Perm()&0o077 != 0 {
			t.Errorf("temporary mode = %v, %v", info, statErr)
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = io.WriteString(w, "private-webp-bytes")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	result, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "thumbnail", destination, false)
	if failure != nil {
		t.Fatal(failure)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "private-webp-bytes" {
		t.Fatalf("contents = %q, %v", contents, err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	if result.Path != destination || result.Variant != "thumbnail" || result.Bytes != int64(len(contents)) {
		t.Fatalf("result = %#v", result)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadInventoryImageRetriesTransientStatusIntoSameAtomicDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "retry.webp")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"busy","message":"busy","details":null}}`)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		_, _ = io.WriteString(w, "retried-webp")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	if _, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false); failure != nil {
		t.Fatal(failure)
	}
	contents, _ := os.ReadFile(destination)
	if attempts.Load() != 2 || string(contents) != "retried-webp" {
		t.Fatalf("attempts=%d contents=%q", attempts.Load(), contents)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadInventoryImageSeparatesLocalStorageFailuresWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name         string
		wantRequests int32
		force        bool
		inject       func(*Client)
	}{
		{
			name: "create",
			inject: func(client *Client) {
				client.downloadOps.createTemp = func(string, string) (*os.File, error) {
					return nil, errors.New("create ENOSPC local-secret destination-secret")
				}
			},
		},
		{
			name: "protect",
			inject: func(client *Client) {
				client.downloadOps.protect = func(*os.File) error {
					return errors.New("protect local-secret destination-secret")
				}
			},
		},
		{
			name:         "writer ENOSPC",
			wantRequests: 1,
			inject: func(client *Client) {
				client.downloadOps.writer = func(*os.File) io.Writer {
					return failingDownloadWriter{err: errors.New("ENOSPC local-secret destination-secret")}
				}
			},
		},
		{
			name:         "short write",
			wantRequests: 1,
			inject: func(client *Client) {
				client.downloadOps.writer = func(*os.File) io.Writer { return shortDownloadWriter{} }
			},
		},
		{
			name:         "sync",
			wantRequests: 1,
			inject: func(client *Client) {
				client.downloadOps.syncFile = func(*os.File) error {
					return errors.New("sync local-secret destination-secret")
				}
			},
		},
		{
			name:         "close",
			wantRequests: 1,
			inject: func(client *Client) {
				client.downloadOps.closeFile = func(file *os.File) error {
					_ = file.Close()
					return errors.New("close local-secret destination-secret")
				}
			},
		},
		{
			name:         "no-replace install",
			wantRequests: 1,
			inject: func(client *Client) {
				client.downloadOps.installNoReplace = func(string, string) (bool, error) {
					return false, errors.New("install local-secret destination-secret")
				}
			},
		},
		{
			name:         "force install",
			wantRequests: 1,
			force:        true,
			inject: func(client *Client) {
				client.downloadOps.replace = func(string, string) (bool, error) {
					return false, errors.New("replace local-secret destination-secret")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "destination-secret.webp")
			if test.force {
				if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				requests.Add(1)
				w.Header().Set("Content-Type", "image/webp")
				_, _ = io.WriteString(w, "download-body")
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "local-secret", time.Second)
			test.inject(client)
			_, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, test.force)
			assertLocalStorageFailure(t, failure, "Unable to store the image download safely")
			if requests.Load() != test.wantRequests {
				t.Fatalf("requests = %d, want %d", requests.Load(), test.wantRequests)
			}
			if test.force {
				contents, err := os.ReadFile(destination)
				if err != nil || string(contents) != "existing" {
					t.Fatalf("existing destination changed: %q, %v", contents, err)
				}
			} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination unexpectedly exists: %v", err)
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadInventoryImageRetriesOnlyNetworkResponseReadFailures(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "network-retry.webp")
	var attempts atomic.Int32
	var closes atomic.Int32
	client, _ := NewClient("http://127.0.0.1", "network-secret", time.Second)
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		header := make(http.Header)
		header.Set("Content-Type", "image/webp")
		if attempt < 3 {
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: &failingDownloadReadCloser{data: []byte("partial"), err: errors.New("network read failed"), closes: &closes}}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: &failingDownloadReadCloser{data: []byte("complete"), closes: &closes}}, nil
	})
	result, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false)
	if failure != nil {
		t.Fatal(failure)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "complete" || result.Bytes != int64(len(contents)) {
		t.Fatalf("result=%#v contents=%q err=%v", result, contents, err)
	}
	if attempts.Load() != 3 || closes.Load() != 3 {
		t.Fatalf("attempts=%d closes=%d", attempts.Load(), closes.Load())
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadInventoryImageSyncsParentAfterInstallInExactOrder(t *testing.T) {
	for _, test := range []struct {
		name     string
		force    bool
		fallback bool
	}{
		{name: "no replace"},
		{name: "force replace", force: true},
		{name: "hard link fallback", fallback: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			destination := filepath.Join(dir, "durable.webp")
			if test.force {
				if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/webp")
				_, _ = io.WriteString(w, "durable")
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "secret", time.Second)
			defaults := client.downloadOps
			var events []string
			client.downloadOps.syncFile = func(file *os.File) error {
				events = append(events, "sync-file")
				return defaults.syncFile(file)
			}
			client.downloadOps.closeFile = func(file *os.File) error {
				events = append(events, "close-file")
				return defaults.closeFile(file)
			}
			client.downloadOps.syncParent = func(path string) error {
				events = append(events, "sync-parent")
				if path != dir {
					t.Fatalf("sync parent = %q, want %q", path, dir)
				}
				return defaults.syncParent(path)
			}
			if test.force {
				client.downloadOps.replace = func(from, to string) (bool, error) {
					events = append(events, "install")
					return defaults.replace(from, to)
				}
			} else if test.fallback {
				client.downloadOps.installNoReplace = func(from, to string) (bool, error) {
					events = append(events, "install")
					if err := os.Link(from, to); err != nil {
						return false, err
					}
					if err := os.Remove(from); err != nil {
						return true, err
					}
					return true, nil
				}
			} else {
				client.downloadOps.installNoReplace = func(from, to string) (bool, error) {
					events = append(events, "install")
					return defaults.installNoReplace(from, to)
				}
			}
			if _, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, test.force); failure != nil {
				t.Fatal(failure)
			}
			if !reflect.DeepEqual(events, []string{"sync-file", "close-file", "install", "sync-parent"}) {
				t.Fatalf("events = %v", events)
			}
			contents, _ := os.ReadFile(destination)
			if string(contents) != "durable" {
				t.Fatalf("destination = %q", contents)
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadInventoryImageParentSyncFailurePreservesInstalledDestination(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "no replace", true: "force"}[force], func(t *testing.T) {
			dir := t.TempDir()
			destination := filepath.Join(dir, "visible.webp")
			if force {
				if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				requests.Add(1)
				w.Header().Set("Content-Type", "image/webp")
				_, _ = io.WriteString(w, "installed")
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "sync-secret", time.Second)
			client.downloadOps.syncParent = func(string) error { return errors.New("sync sync-secret " + destination) }
			result, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, force)
			assertLocalStorageFailure(t, failure, "The image download is installed, but storage durability is uncertain")
			if result.Path != destination || result.Variant != "display" || result.Bytes != int64(len("installed")) {
				t.Fatalf("installed result = %#v", result)
			}
			contents, err := os.ReadFile(destination)
			if err != nil || string(contents) != "installed" {
				t.Fatalf("installed destination = %q, %v", contents, err)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d", requests.Load())
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadInventoryImageSyncsFallbackMetadataEvenWhenCleanupFails(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "fallback.webp")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = io.WriteString(w, "fallback")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	defaults := client.downloadOps
	var parentSynced atomic.Bool
	client.downloadOps.installNoReplace = func(from, to string) (bool, error) {
		if err := os.Link(from, to); err != nil {
			return false, err
		}
		return true, errors.New("temporary link cleanup failed")
	}
	client.downloadOps.syncParent = func(path string) error {
		parentSynced.Store(true)
		return defaults.syncParent(path)
	}
	result, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false)
	assertLocalStorageFailure(t, failure, "The image download is installed, but a local storage operation did not complete")
	if result.Path != destination || result.Bytes != int64(len("fallback")) {
		t.Fatalf("installed result = %#v", result)
	}
	if !parentSynced.Load() {
		t.Fatal("fallback metadata was not synced")
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "fallback" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadInventoryImageNoClobberPrecheckAndInstallRace(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "race.webp")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var createRacer atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		if createRacer.Load() {
			if err := os.WriteFile(destination, []byte("racer"), 0o600); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "image/webp")
		_, _ = io.WriteString(w, "downloaded")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	if _, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false); failure == nil || failure.ExitCode != 3 || failure.Code != "local_storage_error" || failure.Message != "Destination already exists; use --force to replace it" {
		t.Fatalf("precheck failure = %#v", failure)
	}
	if requests.Load() != 0 {
		t.Fatalf("precheck made %d requests", requests.Load())
	}

	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	createRacer.Store(true)
	if _, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false); failure == nil || failure.ExitCode != 3 || failure.Code != "local_storage_error" || failure.Message != "Destination already exists; use --force to replace it" {
		t.Fatalf("race failure = %#v", failure)
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != "racer" {
		t.Fatalf("race destination = %q", contents)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadInventoryImageForceReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	destination := filepath.Join(dir, "destination.webp")
	if err := os.WriteFile(target, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, destination); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = io.WriteString(w, "replacement")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	if _, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, true); failure != nil {
		t.Fatal(failure)
	}
	targetContents, _ := os.ReadFile(target)
	destinationContents, _ := os.ReadFile(destination)
	if string(targetContents) != "protected" || string(destinationContents) != "replacement" {
		t.Fatalf("target=%q destination=%q", targetContents, destinationContents)
	}
	if info, err := os.Lstat(destination); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("destination info = %v, %v", info, err)
	}
}

func TestDownloadInventoryImageRejectsInvalidResponsesAndCleansTemps(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      int
		contentType string
		body        func() io.Reader
	}{
		{name: "wrong status", status: http.StatusCreated, contentType: "image/webp", body: func() io.Reader { return strings.NewReader("x") }},
		{name: "wrong content type", status: http.StatusOK, contentType: "image/png", body: func() io.Reader { return strings.NewReader("x") }},
		{name: "content type parameters", status: http.StatusOK, contentType: "image/webp; charset=binary", body: func() io.Reader { return strings.NewReader("x") }},
		{name: "empty", status: http.StatusOK, contentType: "image/webp", body: func() io.Reader { return strings.NewReader("") }},
		{name: "oversized", status: http.StatusOK, contentType: "image/webp", body: func() io.Reader { return io.LimitReader(zeroReader{}, maxImageDownloadBytes+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "failure.webp")
			client, _ := NewClient("http://127.0.0.1", "secret", time.Second)
			client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				header := make(http.Header)
				header.Set("Content-Type", test.contentType)
				return &http.Response{StatusCode: test.status, Header: header, Body: io.NopCloser(test.body())}, nil
			})
			if _, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false); failure == nil {
				t.Fatal("invalid response succeeded")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("destination exists: %v", err)
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadInventoryImageRefusesRedirectWithoutForwardingBearer(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.Redirect(w, r, target.URL+"/credential-target", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	destination := filepath.Join(t.TempDir(), "redirect.webp")
	client, _ := NewClient(source.URL, "redirect-secret", time.Second)
	if _, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false); failure == nil || failure.Code != "redirect_refused" {
		t.Fatalf("failure = %#v", failure)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d", targetRequests.Load())
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadInventoryImageCancellationClosesResponseCleansTempAndDoesNotRetry(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "cancel.webp")
	ctx, cancel := context.WithCancel(context.Background())
	body := &cancelReadCloser{ctx: ctx}
	var requests atomic.Int32
	client, _ := NewClient("http://127.0.0.1", "secret", time.Second)
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		header := make(http.Header)
		header.Set("Content-Type", "image/webp")
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: body}, nil
	})
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, failure := client.DownloadInventoryImage(ctx, mutationLotID, commandAPIImageID, "display", destination, false)
	assertInterruptionFailure(t, failure)
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if !body.closed.Load() {
		t.Fatal("cancelled response body was not closed")
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadInventoryImageCancellationBeforeInstallRemovesOnlyTemporary(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "cancel-before-install.webp")
	ctx, cancel := context.WithCancel(context.Background())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		w.Header().Set("Content-Type", "image/webp")
		_, _ = io.WriteString(w, "complete")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	defaults := client.downloadOps
	client.downloadOps.syncFile = func(file *os.File) error {
		err := defaults.syncFile(file)
		cancel()
		return err
	}
	_, failure := client.DownloadInventoryImage(ctx, mutationLotID, commandAPIImageID, "display", destination, false)
	assertInterruptionFailure(t, failure)
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination unexpectedly exists: %v", err)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadInventoryImageExpiredDeadlineMakesZeroRequests(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "deadline.webp")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	var requests atomic.Int32
	client, _ := NewClient("http://127.0.0.1", "secret", time.Second)
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("request should not be made")
	})
	_, failure := client.DownloadInventoryImage(ctx, mutationLotID, commandAPIImageID, "display", destination, false)
	if failure == nil || failure.ExitCode != 8 || failure.Code != "network_error" || failure.Message != "Unable to communicate with the server" || failure.HTTPStatus != nil {
		t.Fatalf("failure = %#v, want network_error exit 8", failure)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
	assertNoDownloadTemps(t, destination)
}

func assertLocalStorageFailure(t *testing.T, failure *output.Error, message string) {
	t.Helper()
	if failure == nil || failure.ExitCode != 3 || failure.Code != "local_storage_error" || failure.Message != message || failure.HTTPStatus != nil {
		t.Fatalf("failure = %#v", failure)
	}
	for _, forbidden := range []string{"local-secret", "sync-secret", "destination-secret", "ENOSPC"} {
		if strings.Contains(failure.Code, forbidden) || strings.Contains(failure.Message, forbidden) {
			t.Fatalf("failure leaked %q: %#v", forbidden, failure)
		}
	}
}

func assertInterruptionFailure(t *testing.T, failure *output.Error) {
	t.Helper()
	if failure == nil || failure.ExitCode != 130 || failure.Code != "interrupted" || failure.Message != "Operation interrupted" || failure.HTTPStatus != nil {
		t.Fatalf("failure = %#v", failure)
	}
}

type failingDownloadWriter struct{ err error }

func (writer failingDownloadWriter) Write([]byte) (int, error) { return 0, writer.err }

type shortDownloadWriter struct{}

func (shortDownloadWriter) Write(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	return len(buffer) - 1, nil
}

type failingDownloadReadCloser struct {
	data   []byte
	err    error
	closes *atomic.Int32
}

func (reader *failingDownloadReadCloser) Read(buffer []byte) (int, error) {
	if len(reader.data) != 0 {
		n := copy(buffer, reader.data)
		reader.data = reader.data[n:]
		return n, nil
	}
	if reader.err != nil {
		err := reader.err
		reader.err = nil
		return 0, err
	}
	return 0, io.EOF
}

func (reader *failingDownloadReadCloser) Close() error {
	reader.closes.Add(1)
	return nil
}

const commandAPIImageID = "22222222222242228222222222222222"

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

type cancelReadCloser struct {
	ctx    context.Context
	closed atomic.Bool
}

func (body *cancelReadCloser) Read([]byte) (int, error) {
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}
func (body *cancelReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}

func assertNoDownloadTemps(t *testing.T, destination string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, err)
	}
}

func TestImageDownloadResultJSONContract(t *testing.T) {
	encoded, err := json.Marshal(ImageDownload{Path: "image.webp", Variant: "display", Bytes: 4})
	if err != nil || string(encoded) != `{"path":"image.webp","variant":"display","bytes":4}` {
		t.Fatalf("encoded = %s, %v", encoded, err)
	}
}
