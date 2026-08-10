package command

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCobraLotCreateRepeatedVarietalsPreserveOrder(t *testing.T) {
	var manifest map[string]any
	var key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key = r.Header.Get("Idempotency-Key")
		mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content type = %q, %v", r.Header.Get("Content-Type"), err)
		}
		part, err := multipart.NewReader(r.Body, parameters["boundary"]).NextPart()
		if err != nil || part.FormName() != "manifest" || part.FileName() != "" {
			t.Fatalf("part = %#v, %v", part, err)
		}
		if err := json.NewDecoder(part).Decode(&manifest); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.IsTerminal = func(int) bool { t.Fatal("create checked terminal state"); return false }
	result := runAuthCommand(t, runtime, "inventory", "lot", "create",
		"--name", " New Lot ", "--description", "  Cafe\u0301 story\r\nSecond paragraph  ", "--origin", " Kenya ", "--producer", "Producer", "--supplier", "Supplier",
		"--external-reference", "ext-1", "--received-date", "2026-08-07", "--crop-year", "2026",
		"--varietal", " SL28 ", "--varietal", "Ruiru 11", "--sca-score", "87.50", "--processing-method", "washed",
		"--processing-detail", " \t ", "--altitude-min-metres", "1800", "--altitude-max-metres", "2000",
		"--notes", " notes ", "--opening-grams", "2500", "--opening-reason", " count\r\nline ", "--opening-reference", " sheet ",
		"--idempotency-key", "create-key")
	if result.code != 0 || result.stderr != "" || key != "create-key" {
		t.Fatalf("result=%#v key=%q", result, key)
	}
	fields := manifest["fields"].(map[string]any)
	if fields["name"] != "New Lot" || fields["origin"] != "Kenya" || fields["received_date"] != "2026-08-07" || fields["sca_score"] != "87.50" || fields["processing_detail"] != nil || fields["notes"] != "notes" {
		t.Fatalf("fields = %#v", fields)
	}
	if fields["description"] != "Café story\nSecond paragraph" {
		t.Fatalf("description = %#v", fields["description"])
	}
	if manifest["opening_reason"] != "count\nline" || manifest["opening_reference"] != "sheet" {
		t.Fatalf("opening fields = %#v", manifest)
	}
	if got := fields["varietals"]; fmt.Sprint(got) != "[SL28 Ruiru 11]" || manifest["opening_grams"] != float64(2500) || fmt.Sprint(manifest["images"]) != "[]" {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, balance := range []string{"5000", "1250", "3750"} {
		if !strings.Contains(result.stdout, balance) {
			t.Fatalf("output %q missing balance %q", result.stdout, balance)
		}
	}
}

func TestInventoryLotPricePerKgCreateUpdateAndClearBodies(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		body := string(contents)
		if r.Method == http.MethodPost {
			body = rawCommandMutationManifest(t, body, r.Header.Get("Content-Type"))
			w.WriteHeader(http.StatusCreated)
		}
		bodies = append(bodies, body)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()

	runtime := inventoryRuntime(t, server.URL)
	runtime.IsTerminal = func(int) bool { t.Fatal("price mutation checked terminal state"); return false }
	results := []commandResult{
		runAuthCommand(t, runtime, "inventory", "lot", "create", "--name", "Lot", "--idempotency-key", "create-omitted"),
		runAuthCommand(t, runtime, "inventory", "lot", "create", "--name", "Lot", "--price-per-kg-eur", "12.34", "--idempotency-key", "create-price"),
		runAuthCommand(t, runtime, "inventory", "lot", "update", commandLotID, "--notes", "note", "--idempotency-key", "update-omitted"),
		runAuthCommand(t, runtime, "inventory", "lot", "update", commandLotID, "--price-per-kg-eur", "0", "--idempotency-key", "update-zero"),
		runAuthCommand(t, runtime, "inventory", "lot", "update", commandLotID, "--clear", "price-per-kg-eur", "--idempotency-key", "clear-hyphen"),
		runAuthCommand(t, runtime, "inventory", "lot", "update", commandLotID, "--clear", "price_per_kg_eur", "--idempotency-key", "clear-underscore"),
	}
	for index, result := range results {
		if result.code != 0 || result.stderr != "" {
			t.Fatalf("result %d = %#v", index, result)
		}
		if !strings.Contains(result.stdout, "€12.34") {
			t.Fatalf("result %d did not render authoritative returned price: %q", index, result.stdout)
		}
	}

	const omitted = `{"fields":{"name":"Lot","origin":null,"producer":null,"supplier":null,"external_reference":null,"received_date":null,"crop_year":null,"price_per_kg_eur_cents":null,"varietals":[],"sca_score":null,"processing_method":null,"processing_detail":null,"altitude_min_metres":null,"altitude_max_metres":null,"description":null,"notes":null},"opening_grams":0,"opening_reason":null,"opening_reference":null,"images":[]}`
	const priced = `{"fields":{"name":"Lot","origin":null,"producer":null,"supplier":null,"external_reference":null,"received_date":null,"crop_year":null,"price_per_kg_eur_cents":1234,"varietals":[],"sca_score":null,"processing_method":null,"processing_detail":null,"altitude_min_metres":null,"altitude_max_metres":null,"description":null,"notes":null},"opening_grams":0,"opening_reason":null,"opening_reference":null,"images":[]}`
	want := []string{omitted, priced, `{"notes":"note"}`, `{"price_per_kg_eur_cents":0}`, `{"price_per_kg_eur_cents":null}`, `{"price_per_kg_eur_cents":null}`}
	if fmt.Sprint(bodies) != fmt.Sprint(want) {
		t.Fatalf("bodies = %q, want %q", bodies, want)
	}
}

func TestInventoryLotDescriptionUpdateClearAndConflictAreLocal(t *testing.T) {
	var bodies []string
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(contents))
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()

	runtime := inventoryRuntime(t, server.URL)
	set := runAuthCommand(t, runtime, "inventory", "lot", "update", commandLotID, "--description", " New public story ", "--idempotency-key", "set-description")
	clear := runAuthCommand(t, runtime, "inventory", "lot", "update", commandLotID, "--clear", "description", "--idempotency-key", "clear-description")
	if set.code != 0 || set.stderr != "" || clear.code != 0 || clear.stderr != "" {
		t.Fatalf("set=%#v clear=%#v", set, clear)
	}
	wantBodies := []string{`{"description":"New public story"}`, `{"description":null}`}
	if !reflect.DeepEqual(bodies, wantBodies) {
		t.Fatalf("bodies = %q, want %q", bodies, wantBodies)
	}

	conflict := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "--json", "inventory", "lot", "update", commandLotID, "--description", "story", "--clear", "description")
	if conflict.code != 2 || !strings.Contains(conflict.stdout, `"code":"conflicting_field"`) {
		t.Fatalf("conflict before configuration = %#v", conflict)
	}
	if requests.Load() != 2 {
		t.Fatalf("conflict sent request; requests = %d", requests.Load())
	}
}

func TestInventoryLotDescriptionInvalidValuesAreLocal(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	for name, value := range map[string]string{
		"too many runes": strings.Repeat("é", 2001),
		"NUL":            "public\x00story",
		"C1 control":     "public\u0085story",
	} {
		t.Run(name, func(t *testing.T) {
			for _, args := range [][]string{
				{"inventory", "lot", "create", "--name", "Lot", "--description", value},
				{"inventory", "lot", "update", commandLotID, "--description", value},
			} {
				result := runAuthCommand(t, inventoryRuntime(t, server.URL), append([]string{"--json"}, args...)...)
				if result.code != 2 {
					t.Errorf("args %q result = %#v", args[:3], result)
				}
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid descriptions sent %d requests", requests.Load())
	}
}

func TestInventoryLotPriceClearConflictAndInvalidDecimalsAreLocal(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()

	withoutConfig := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "--json", "inventory", "lot", "update", commandLotID, "--price-per-kg-eur", "12.34", "--clear", "price-per-kg-eur")
	if withoutConfig.code != 2 || !strings.Contains(withoutConfig.stdout, `"code":"conflicting_field"`) {
		t.Fatalf("conflict before configuration result = %#v", withoutConfig)
	}

	for _, alias := range []string{"price-per-kg-eur", "price_per_kg_eur"} {
		result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "lot", "update", commandLotID, "--price-per-kg-eur", "12.34", "--clear", alias)
		if result.code != 2 || !strings.Contains(result.stdout, `"code":"conflicting_field"`) {
			t.Errorf("conflict alias %q result = %#v", alias, result)
		}
	}

	invalid := []string{"", " 1", "1 ", "+1", "-1", "00", "01", ".1", "1.", "1.234", "1,00", "1_00", "1e2", "NaN", "١", "21474836.48"}
	for _, raw := range invalid {
		for _, command := range [][]string{
			{"inventory", "lot", "create", "--name", "Lot", "--price-per-kg-eur", raw},
			{"inventory", "lot", "update", commandLotID, "--price-per-kg-eur", raw},
		} {
			args := append([]string{"--json"}, command...)
			result := runAuthCommand(t, inventoryRuntime(t, server.URL), args...)
			if result.code != 2 || !strings.Contains(result.stdout, `"code":"invalid_price_per_kg_eur"`) {
				t.Errorf("args %q result = %#v", command, result)
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("local validation sent %d requests", requests.Load())
	}
}

func rawCommandMutationManifest(t *testing.T, body, contentType string) string {
	t.Helper()
	_, parameters, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	part, err := multipart.NewReader(strings.NewReader(body), parameters["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(part)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestCobraLotUpdateRepeatedClearsAfterPositionalAndStateCommandsAreExact(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(contents))
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	noPrompt := inventoryRuntime(t, server.URL)
	noPrompt.IsTerminal = func(int) bool { t.Fatal("update or restore checked terminal state"); return false }
	update := runAuthCommand(t, noPrompt, "inventory", "lot", "update", commandLotID, "--name", " Renamed ",
		"--clear", "origin", "--clear", "producer", "--clear", "supplier", "--clear", "external-reference",
		"--clear", "received-date", "--clear", "crop-year", "--clear", "varietals", "--clear", "sca-score",
		"--clear", "processing-method", "--clear", "processing-detail", "--clear", "altitude-min-metres",
		"--clear", "altitude-max-metres", "--clear", "notes", "--idempotency-key", "update-key")
	restore := runAuthCommand(t, noPrompt, "inventory", "lot", "restore", commandLotID, "--idempotency-key", "restore-key")
	archiveRuntime := inventoryRuntime(t, server.URL)
	archiveRuntime.IsTerminal = func(int) bool { return false }
	archive := runAuthCommand(t, archiveRuntime, "inventory", "lot", "archive", commandLotID, "--yes", "--idempotency-key", "archive-key")
	if update.code != 0 || restore.code != 0 || archive.code != 0 {
		t.Fatalf("results update=%#v restore=%#v archive=%#v", update, restore, archive)
	}
	want := []string{`{"altitude_max_metres":null,"altitude_min_metres":null,"crop_year":null,"external_reference":null,"name":"Renamed","notes":null,"origin":null,"processing_detail":null,"processing_method":null,"producer":null,"received_date":null,"sca_score":null,"supplier":null,"varietals":null}`, `{"state":"active"}`, `{"state":"archived"}`}
	if fmt.Sprint(bodies) != fmt.Sprint(want) {
		t.Fatalf("bodies = %q, want %q", bodies, want)
	}
}

func TestInventoryLotArchiveCanonicalizesConfirmationAndStatePaths(t *testing.T) {
	const rawLotID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	const canonicalLotID = "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		paths = append(paths, r.URL.EscapedPath())
		_, _ = fmt.Fprint(w, strings.ReplaceAll(commandLotDetailFullJSON(), commandLotID, canonicalLotID))
	}))
	defer server.Close()

	archiveRuntime := inventoryRuntime(t, server.URL)
	archiveRuntime.In = strings.NewReader("yes\n")
	archiveRuntime.IsTerminal = func(int) bool { return true }
	archived := runAuthCommand(t, archiveRuntime, "inventory", "lot", "archive", rawLotID, "--idempotency-key", "archive-key")
	wantPrompt := "Archive lot " + canonicalLotID + "? Type yes to continue: "
	if archived.code != 0 || archived.stderr != wantPrompt {
		t.Fatalf("archive = %#v, want prompt %q", archived, wantPrompt)
	}

	restoreRuntime := inventoryRuntime(t, server.URL)
	restoreRuntime.IsTerminal = func(int) bool { t.Fatal("restore checked terminal state"); return false }
	restored := runAuthCommand(t, restoreRuntime, "inventory", "lot", "restore", rawLotID, "--idempotency-key", "restore-key")
	if restored.code != 0 {
		t.Fatalf("restore = %#v", restored)
	}
	wantPaths := []string{
		"/api/v1/inventory/admin/bean-lots/" + canonicalLotID,
		"/api/v1/inventory/admin/bean-lots/" + canonicalLotID,
	}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("paths = %q, want %q", paths, wantPaths)
	}
}

func TestInventoryLotArchiveDeclineAndNonTTYMissingYesMakeNoRequest(t *testing.T) {
	const rawLotID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	const canonicalLotID = "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		_, _ = fmt.Fprint(w, strings.ReplaceAll(commandLotDetailFullJSON(), commandLotID, canonicalLotID))
	}))
	defer server.Close()
	interactive := inventoryRuntime(t, server.URL)
	interactive.In = strings.NewReader("no\n")
	interactive.IsTerminal = func(int) bool { return true }
	declined := runAuthCommand(t, interactive, "inventory", "lot", "archive", rawLotID)
	wantPrompt := "Archive lot " + canonicalLotID + "? Type yes to continue: "
	if declined.code != 10 || declined.stderr != wantPrompt+"Confirmation declined\n" {
		t.Fatalf("declined = %#v, want canonical prompt %q", declined, wantPrompt)
	}
	nonTTY := inventoryRuntime(t, server.URL)
	nonTTY.IsTerminal = func(int) bool { return false }
	missing := runAuthCommand(t, nonTTY, "--json", "inventory", "lot", "archive", rawLotID)
	if missing.code != 10 || !strings.Contains(missing.stdout, `"code":"confirmation_required"`) {
		t.Fatalf("missing = %#v", missing)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestInventoryLotCreateFromJSONIsBoundedStrictAndCanonical(t *testing.T) {
	var manifest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, parameters, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		part, err := multipart.NewReader(r.Body, parameters["boundary"]).NextPart()
		if err != nil {
			t.Fatal(err)
		}
		contents, _ := io.ReadAll(part)
		manifest = string(contents)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.In = strings.NewReader(` { "fields": { "name": "  JSON Lot  ", "origin": " Kenya ", "varietals": [], "sca_score": 87, "processing_method": "washed", "processing_detail": "  " }, "opening_grams": 0, "images": [] } `)
	result := runAuthCommand(t, runtime, "inventory", "lot", "create", "--from-json", "-", "--idempotency-key", "json-key")
	if result.code != 0 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(manifest, `"name":"JSON Lot"`) || !strings.Contains(manifest, `"origin":"Kenya"`) || !strings.Contains(manifest, `"processing_detail":null`) || !strings.Contains(manifest, `"sca_score":"87.00"`) || !strings.Contains(manifest, `"varietals":[]`) || !strings.Contains(manifest, `"images":[]`) {
		t.Fatalf("canonical manifest = %q", manifest)
	}

	secret := "PRIVATE_JSON_SOURCE_CONTENT"
	bad := inventoryRuntime(t, server.URL)
	bad.In = strings.NewReader(`{"fields":{"name":"Lot","varietals":[]},"images":[],"` + secret + `":true}`)
	rejected := runAuthCommand(t, bad, "inventory", "lot", "create", "--from-json", "-")
	if rejected.code != 2 || strings.Contains(rejected.stdout+rejected.stderr, secret) {
		t.Fatalf("rejected = %#v", rejected)
	}
	oversized := inventoryRuntime(t, server.URL)
	oversized.In = strings.NewReader(strings.Repeat("x", maxMutationJSONBytes+1))
	tooLarge := runAuthCommand(t, oversized, "inventory", "lot", "create", "--from-json", "-")
	if tooLarge.code != 2 || !strings.Contains(tooLarge.stderr, "1 MiB") {
		t.Fatalf("tooLarge = %#v", tooLarge)
	}
}

func TestInventoryLotWritePresenceSensitiveEmptyFlagsRejectLocally(t *testing.T) {
	for _, args := range [][]string{
		{"inventory", "lot", "create", "--from-json", ""},
		{"inventory", "lot", "create", "--from-json", "", "--name", "Lot"},
		{"inventory", "lot", "update", commandLotID, "--from-json", ""},
		{"inventory", "lot", "update", commandLotID, "--from-json", "", "--notes", "note"},
		{"inventory", "lot", "create", "--name", "Lot", "--idempotency-key", ""},
		{"inventory", "lot", "update", commandLotID, "--notes", "note", "--idempotency-key", ""},
		{"inventory", "lot", "archive", commandLotID, "--yes", "--idempotency-key", ""},
		{"inventory", "lot", "restore", commandLotID, "--idempotency-key", ""},
	} {
		result := runAuthCommand(t, Runtime{In: strings.NewReader(`{"fields":{"name":"Lot","varietals":[]},"images":[]}`), ConfigDir: t.TempDir()}, args...)
		if result.code != 2 {
			t.Errorf("args %q result = %#v", args, result)
		}
	}
}

func TestInventoryLotCreateRejectsEncodedManifestCapBeforeConfigOrNetwork(t *testing.T) {
	args := []string{"inventory", "lot", "create", "--name", "Lot", "--notes", strings.Repeat("<", 10_000), "--opening-reason", strings.Repeat("<", 2_000)}
	local := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
	if local.code != 2 {
		t.Fatalf("local result = %#v", local)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), args...)
	if result.code != 2 || requests.Load() != 0 {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
}

func TestInventoryLotWriteRejectsInvalidLocalInputBeforeConfigurationOrNetwork(t *testing.T) {
	tests := [][]string{
		{"inventory", "lot", "create", "--name", "Lot", "--from-json", "-"},
		{"inventory", "lot", "create", "--name", "Lot", "--opening-grams", "1.5"},
		{"inventory", "lot", "create", "--name", "Lot", "--processing-method", "other"},
		{"inventory", "lot", "create", "--name", "Lot", "--processing-method", "other", "--processing-detail", "  "},
		{"inventory", "lot", "create", "--name", "Lot", "--altitude-min-metres", "2000", "--altitude-max-metres", "1000"},
		{"inventory", "lot", "create", "--name", "Lot", "--received-date", "0000-01-01"},
		{"inventory", "lot", "update", commandLotID, "--received-date", "0000-01-01"},
		{"inventory", "lot", "update", commandLotID, "--clear", "unknown"},
		{"inventory", "lot", "update", commandLotID, "--clear", "name"},
		{"inventory", "lot", "update", commandLotID, "--clear", "state"},
	}
	for _, args := range tests {
		runtime := Runtime{In: strings.NewReader(`{"fields":{"name":"from json"},"images":[]}`), ConfigDir: t.TempDir()}
		result := runAuthCommand(t, runtime, args...)
		if result.code != 2 {
			t.Errorf("args %v result = %#v", args, result)
		}
	}
}
