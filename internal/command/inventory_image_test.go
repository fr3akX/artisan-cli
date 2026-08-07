package command

import (
	"encoding/json"
	"fmt"
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
)

func TestInventoryImageAddUsesIndexedMetadataAndOrderedPositionalFiles(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "front.jpg")
	second := filepath.Join(dir, "side.png")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotManifest map[string]any
	var filenames, contentTypes, contents []string
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotPath = r.URL.Path
		gotKey = r.Header.Get("Idempotency-Key")
		_, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(r.Body, parameters["boundary"])
		part, err := reader.NextPart()
		if err != nil || part.FormName() != "manifest" {
			t.Fatalf("manifest part = %#v, %v", part, err)
		}
		if err := json.NewDecoder(part).Decode(&gotManifest); err != nil {
			t.Fatal(err)
		}
		for {
			part, err = reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(part)
			filenames = append(filenames, part.FileName())
			contentTypes = append(contentTypes, part.Header.Get("Content-Type"))
			contents = append(contents, string(body))
		}
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()

	runtime := inventoryRuntime(t, server.URL)
	runtime.IsTerminal = func(int) bool { t.Fatal("image add prompted"); return false }
	result := runAuthCommand(t, runtime, "inventory", "image", "add",
		"--caption", "0= Front ", "--alt-text", "0= Front of bag ", "--cover", "0",
		"--caption", "1=Side=angle", "--idempotency-key", "image-add-key", commandLotID, first, second)
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("result = %#v", result)
	}
	if gotPath != "/api/v1/inventory/admin/bean-lots/"+commandLotID+"/images" || gotKey != "image-add-key" {
		t.Fatalf("path=%q key=%q", gotPath, gotKey)
	}
	if !reflect.DeepEqual(filenames, []string{"front.jpg", "side.png"}) || !reflect.DeepEqual(contentTypes, []string{"image/jpeg", "image/png"}) || !reflect.DeepEqual(contents, []string{"first", "second"}) {
		t.Fatalf("files=%v types=%v contents=%v", filenames, contentTypes, contents)
	}
	images := gotManifest["images"].([]any)
	firstMetadata := images[0].(map[string]any)
	secondMetadata := images[1].(map[string]any)
	if firstMetadata["upload_index"] != float64(0) || firstMetadata["caption"] != "Front" || firstMetadata["alt_text"] != "Front of bag" || firstMetadata["is_cover"] != true || secondMetadata["caption"] != "Side=angle" || secondMetadata["alt_text"] != nil {
		t.Fatalf("manifest = %#v", gotManifest)
	}
}

func TestInventoryLotCreateSupportsRepeatedImageDeclarations(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "front.jpeg")
	second := filepath.Join(dir, "side.PNG")
	for _, item := range []struct{ path, body string }{{first, "first"}, {second, "second"}} {
		if err := os.WriteFile(item.path, []byte(item.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var manifest map[string]any
	var imageParts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/inventory/admin/bean-lots" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, parameters, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		reader := multipart.NewReader(r.Body, parameters["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewDecoder(part).Decode(&manifest); err != nil {
			t.Fatal(err)
		}
		for {
			part, err = reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			imageParts++
			_, _ = io.Copy(io.Discard, part)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "inventory", "lot", "create",
		"--name", "Image lot", "--image", first, "--image", second,
		"--image-caption", "0=Front", "--image-alt-text", "1=Side view", "--image-cover", "1")
	if result.code != 0 {
		t.Fatalf("result = %#v", result)
	}
	images := manifest["images"].([]any)
	if len(images) != 2 || imageParts != 2 || images[0].(map[string]any)["caption"] != "Front" || images[1].(map[string]any)["alt_text"] != "Side view" || images[1].(map[string]any)["is_cover"] != true {
		t.Fatalf("manifest=%#v imageParts=%d", manifest, imageParts)
	}
}

func TestInventoryLotCreateFromJSONPairsExactManifestMetadataWithImageDeclarations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "front.jpg")
	if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	var manifest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, parameters, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		part, _ := multipart.NewReader(r.Body, parameters["boundary"]).NextPart()
		body, _ := io.ReadAll(part)
		manifest = string(body)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.In = strings.NewReader(`{"fields":{"name":"JSON image lot","varietals":[]},"images":[{"upload_index":0,"caption":"Front","alt_text":null,"is_cover":true}]}`)
	result := runAuthCommand(t, runtime, "inventory", "lot", "create", "--from-json", "-", "--image", path)
	if result.code != 0 || !strings.Contains(manifest, `"caption":"Front"`) {
		t.Fatalf("result=%#v manifest=%q", result, manifest)
	}
}

func TestInventoryImageUpdateReorderAndDeleteContracts(t *testing.T) {
	var methods, paths, bodies, keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(body))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	update := runAuthCommand(t, runtime, "inventory", "image", "update", "--clear-caption", "--alt-text", " New alt ", "--cover=false", "--idempotency-key", "update-key", commandLotID, commandImageID)
	reorder := runAuthCommand(t, runtime, "inventory", "image", "reorder", "--idempotency-key", "order-key", commandLotID, commandImageID, commandEntryID)
	deleteRuntime := inventoryRuntime(t, server.URL)
	deleteRuntime.IsTerminal = func(int) bool { return false }
	removed := runAuthCommand(t, deleteRuntime, "inventory", "image", "delete", "--yes", "--idempotency-key", "delete-key", commandLotID, commandImageID)
	if update.code != 0 || reorder.code != 0 || removed.code != 0 {
		t.Fatalf("update=%#v reorder=%#v delete=%#v", update, reorder, removed)
	}
	if !reflect.DeepEqual(methods, []string{"PATCH", "PUT", "DELETE"}) || !reflect.DeepEqual(bodies, []string{`{"alt_text":"New alt","caption":null,"is_cover":false}`, `{"image_ids":["22222222222242228222222222222222","33333333333343338333333333333333"]}`, ""}) || !reflect.DeepEqual(keys, []string{"update-key", "order-key", "delete-key"}) {
		t.Fatalf("methods=%v paths=%v bodies=%q keys=%v", methods, paths, bodies, keys)
	}
}

func TestInventoryImageDeleteDeclineAndMissingYesMakeZeroRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	interactive := inventoryRuntime(t, server.URL)
	interactive.In = strings.NewReader("no\n")
	interactive.IsTerminal = func(int) bool { return true }
	declined := runAuthCommand(t, interactive, "inventory", "image", "delete", commandLotID, commandImageID)
	noninteractive := inventoryRuntime(t, server.URL)
	noninteractive.IsTerminal = func(int) bool { return false }
	missing := runAuthCommand(t, noninteractive, "--json", "inventory", "image", "delete", commandLotID, commandImageID)
	if declined.code != 10 || missing.code != 10 || !strings.Contains(missing.stdout, `"code":"confirmation_required"`) || requests.Load() != 0 {
		t.Fatalf("declined=%#v missing=%#v requests=%d", declined, missing, requests.Load())
	}
	for _, exact := range []string{commandLotID, commandImageID, "Delete"} {
		if !strings.Contains(declined.stderr, exact) {
			t.Fatalf("prompt %q missing %q", declined.stderr, exact)
		}
	}
}

func TestInventoryImageDownloadSelectsVariantAndWritesOnlyFile(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "thumb.webp")
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path = r.URL.Path
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = io.WriteString(w, "webp-private")
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "image", "download", "--variant", "thumbnail", commandLotID, commandImageID, destination)
	if result.code != 0 || result.stderr != "" || strings.Contains(result.stdout, "webp-private") {
		t.Fatalf("result = %#v", result)
	}
	if path != "/api/v1/inventory/admin/bean-lots/"+commandLotID+"/images/"+commandImageID+"/thumbnail" {
		t.Fatalf("path = %q", path)
	}
	body, err := os.ReadFile(destination)
	if err != nil || string(body) != "webp-private" {
		t.Fatalf("file = %q, %v", body, err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope["data"].(map[string]any)
	if data["path"] != destination || data["variant"] != "thumbnail" || data["bytes"] != float64(len(body)) {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestInventoryImageUsageDocumentsUnambiguousIndexedMetadataSyntax(t *testing.T) {
	for _, test := range []struct {
		args []string
		want []string
	}{
		{args: []string{"inventory", "image", "add", "--help"}, want: []string{"image add [OPTIONS] LOT_ID FILE...", "--caption INDEX=TEXT", "--alt-text INDEX=TEXT", "--cover INDEX", "zero-based"}},
		{args: []string{"inventory", "lot", "create", "--help"}, want: []string{"--image FILE", "--image-caption INDEX=TEXT", "--image-alt-text INDEX=TEXT", "--image-cover INDEX", "zero-based"}},
	} {
		result := runAuthCommand(t, Runtime{}, test.args...)
		if result.code != 0 || result.stderr != "" {
			t.Fatalf("args=%v result=%#v", test.args, result)
		}
		for _, want := range test.want {
			if !strings.Contains(result.stdout, want) {
				t.Errorf("help %q missing %q", result.stdout, want)
			}
		}
	}

	invalid := [][]string{
		{"inventory", "image", "add", commandLotID},
		{"inventory", "image", "add", "--caption", "front", commandLotID, "image.jpg"},
		{"inventory", "image", "add", "--caption", "2=front", commandLotID, "first.jpg"},
		{"inventory", "image", "add", "--caption", "0=first", "--caption", "0=second", commandLotID, "first.jpg"},
		{"inventory", "image", "reorder", commandLotID},
		{"inventory", "image", "download", "--variant", "original", commandLotID, commandImageID, "out.webp"},
		{"inventory", "lot", "create", "--name", "Lot", "--image-caption", "0=orphaned"},
	}
	for _, args := range invalid {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != 2 {
			t.Errorf("args=%v result=%#v", args, result)
		}
	}
}

func TestInventoryLotCreateHelpIsCompleteAndExact(t *testing.T) {
	const want = `Usage: artisan inventory lot create [OPTIONS]

Lot fields:
  --name TEXT                         lot name (required unless --from-json)
  --origin TEXT                       origin
  --producer TEXT                     producer
  --supplier TEXT                     supplier
  --external-reference TEXT           external reference
  --received-date YYYY-MM-DD           received date
  --crop-year YEAR                    crop year
  --varietal TEXT                     varietal (repeatable)
  --sca-score SCORE                   SCA score
  --processing-method TEXT            processing method
  --processing-detail TEXT            processing detail
  --altitude-min-metres METRES        minimum altitude
  --altitude-max-metres METRES        maximum altitude
  --notes TEXT                        notes

Opening inventory:
  --opening-grams GRAMS               opening grams
  --opening-reason TEXT               opening reason
  --opening-reference TEXT            opening reference

Input and replay:
  --from-json FILE|-                  strict request JSON (lot fields and image metadata)
  --idempotency-key KEY               advanced idempotency key

Images are declared in order with repeatable flags. Metadata uses an explicit zero-based declaration INDEX:
  --image FILE                        JPEG/PNG image file (repeatable, maximum eight)
  --image-caption INDEX=TEXT          caption for one image (repeatable)
  --image-alt-text INDEX=TEXT         alt text for one image (repeatable)
  --image-cover INDEX                 mark one image as the cover
`
	result := runAuthCommand(t, Runtime{}, "inventory", "lot", "create", "--help")
	if result.code != 0 || result.stderr != "" || result.stdout != want {
		t.Fatalf("result = %#v\nwant help:\n%s", result, want)
	}
}

func TestInventoryImageHelpHonorsJSONOutputContract(t *testing.T) {
	result := runAuthCommand(t, Runtime{}, "--json", "inventory", "image", "add", "--help")
	if result.code != 0 || result.stderr != "" || !strings.HasPrefix(result.stdout, `{"ok":true,"data":{"usage":`) || strings.Count(result.stdout, "\n") != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestInventoryImageInvalidLocalFilesAndLimitsFailBeforeConfiguration(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.jpg")
	if err := os.WriteFile(valid, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooMany := []string{"inventory", "image", "add", commandLotID}
	for index := 0; index < 9; index++ {
		tooMany = append(tooMany, valid)
	}
	for _, args := range [][]string{
		{"inventory", "image", "add", commandLotID, filepath.Join(dir, "missing.jpg")},
		{"inventory", "image", "add", commandLotID, filepath.Join(dir, "wrong.webp")},
		tooMany,
	} {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != 2 {
			t.Errorf("args=%v result=%#v", args, result)
		}
	}
}
