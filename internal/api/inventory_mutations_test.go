package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const mutationLotID = "11111111111141118111111111111111"
const mutationStamp = "2026-08-07T12:34:56.000000Z"

func mutationLotJSON(onHand int64) string {
	return fmt.Sprintf(`{"lot_id":"%s","name":"Lot","origin":null,"processing_method":null,"crop_year":null,"state":"active","on_hand_grams":%d,"reserved_grams":0,"available_grams":%d,"unresolved_conflict_count":0,"cover_image":null,"updated_at":"%s","producer":null,"supplier":null,"external_reference":null,"received_date":null,"varietals":[],"sca_score":null,"processing_detail":null,"altitude_min_metres":null,"altitude_max_metres":null,"notes":null,"images":[],"created_at":"%s","archived_at":null,"links":{"self":"/api/v1/inventory/admin/bean-lots/%s","ledger":"/api/v1/inventory/admin/bean-lots/%s/ledger","reservations":"/api/v1/inventory/admin/bean-lots/%s/reservations"}}`, mutationLotID, onHand, onHand, mutationStamp, mutationStamp, mutationLotID, mutationLotID, mutationLotID)
}

func TestMutationMethodsUseExactRoutesBodiesAndKey(t *testing.T) {
	var mu sync.Mutex
	var methods, paths, keys, bodies, types []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		bodies = append(bodies, string(body))
		types = append(types, r.Header.Get("Content-Type"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if len(methods) == 1 {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = fmt.Fprint(w, mutationLotJSON(25))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	manifest := BeanLotCreateManifest{Fields: BeanLotFields{Name: " Lot ", Varietals: []string{" SL28 ", "Ruiru 11"}, ProcessingMethod: mutationStringPointer("washed"), ProcessingDetail: mutationStringPointer(" \t ")}, OpeningGrams: 25, OpeningReason: mutationStringPointer(" count\r\nline "), Images: []ImageUploadManifest{}}
	if _, failure := client.CreateBeanLot(context.Background(), manifest, "same-key"); failure != nil {
		t.Fatal(failure)
	}
	patch, failure := NewBeanLotPatch(map[string]any{"notes": nil, "received_date": "2026-08-07", "varietals": []any{"SL28", "Ruiru 11"}})
	if failure != nil {
		t.Fatal(failure)
	}
	if _, failure = client.PatchBeanLot(context.Background(), mutationLotID, patch, "same-key"); failure != nil {
		t.Fatal(failure)
	}
	adjustment := InventoryAdjustmentWrite{QuantityGrams: -125, Reason: "count", Reference: mutationStringPointer("sheet-1"), OccurredAt: mutationStamp}
	if _, failure = client.AdjustBeanLot(context.Background(), mutationLotID, adjustment, "same-key"); failure != nil {
		t.Fatal(failure)
	}
	if !reflect.DeepEqual(methods, []string{"POST", "PATCH", "POST"}) || !reflect.DeepEqual(paths, []string{"/api/v1/inventory/admin/bean-lots", "/api/v1/inventory/admin/bean-lots/" + mutationLotID, "/api/v1/inventory/admin/bean-lots/" + mutationLotID + "/adjustments"}) {
		t.Fatalf("methods=%v paths=%v", methods, paths)
	}
	if !reflect.DeepEqual(keys, []string{"same-key", "same-key", "same-key"}) {
		t.Fatalf("keys = %v", keys)
	}
	if !strings.HasPrefix(types[0], "multipart/form-data; boundary=") || types[1] != "application/json" || types[2] != "application/json" {
		t.Fatalf("content types = %v", types)
	}
	gotManifest := rawMutationManifest(t, bodies[0], types[0])
	wantManifest := `{"fields":{"name":"Lot","origin":null,"producer":null,"supplier":null,"external_reference":null,"received_date":null,"crop_year":null,"varietals":["SL28","Ruiru 11"],"sca_score":null,"processing_method":"washed","processing_detail":null,"altitude_min_metres":null,"altitude_max_metres":null,"notes":null},"opening_grams":25,"opening_reason":"count\nline","opening_reference":null,"images":[]}`
	if gotManifest != wantManifest {
		t.Fatalf("manifest body = %q, want %q", gotManifest, wantManifest)
	}
	if bodies[1] != `{"notes":null,"received_date":"2026-08-07","varietals":["SL28","Ruiru 11"]}` {
		t.Fatalf("patch body = %q", bodies[1])
	}
	if bodies[2] != `{"quantity_grams":-125,"reason":"count","reference":"sheet-1","occurred_at":"2026-08-07T12:34:56.000000Z"}` {
		t.Fatalf("adjust body = %q", bodies[2])
	}
}

func TestMutationRetriesReplayIdenticalJSONAndIdempotencyKey(t *testing.T) {
	var bodies, keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(contents))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(bodies) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"busy","message":"busy","details":null}}`)
			return
		}
		_, _ = io.WriteString(w, mutationLotJSON(1))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	adjustment := InventoryAdjustmentWrite{QuantityGrams: 1, Reason: "count", OccurredAt: mutationStamp}
	if _, failure := client.AdjustBeanLot(context.Background(), mutationLotID, adjustment, "retry-key"); failure != nil {
		t.Fatal(failure)
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] || !reflect.DeepEqual(keys, []string{"retry-key", "retry-key", "retry-key"}) {
		t.Fatalf("bodies=%q keys=%v", bodies, keys)
	}
}

func TestStrictMutationModelsRejectLocally(t *testing.T) {
	invalidCreates := []BeanLotCreateManifest{
		{Fields: BeanLotFields{Name: ""}, Images: []ImageUploadManifest{}},
		{Fields: BeanLotFields{Name: "Lot", ProcessingMethod: mutationStringPointer("other")}, Images: []ImageUploadManifest{}},
		{Fields: BeanLotFields{Name: "Lot", AltitudeMinMetres: mutationIntPointer(2000), AltitudeMaxMetres: mutationIntPointer(1000)}, Images: []ImageUploadManifest{}},
		{Fields: BeanLotFields{Name: "Lot"}, OpeningGrams: 1, Images: []ImageUploadManifest{}},
	}
	for _, manifest := range invalidCreates {
		if failure := ValidateBeanLotCreateManifest(manifest); failure == nil {
			t.Fatalf("accepted %#v", manifest)
		}
	}
	for _, fields := range []map[string]any{{}, {"unknown": true}, {"name": nil}, {"received_date": "08/07/2026"}, {"altitude_min_metres": int64(2), "altitude_max_metres": int64(1)}, {"processing_method": "other", "processing_detail": nil}} {
		if _, failure := NewBeanLotPatch(fields); failure == nil {
			t.Fatalf("accepted patch %#v", fields)
		}
	}
	for _, adjustment := range []InventoryAdjustmentWrite{{QuantityGrams: 0, Reason: "x", OccurredAt: mutationStamp}, {QuantityGrams: 1, Reason: " ", OccurredAt: mutationStamp}, {QuantityGrams: 1, Reason: "x", OccurredAt: "2026-08-07T12:34:56Z"}} {
		if failure := ValidateInventoryAdjustment(adjustment); failure == nil {
			t.Fatalf("accepted adjustment %#v", adjustment)
		}
	}
}

func TestStrictCreateJSONRequiresNonNullArrays(t *testing.T) {
	valid := `{"fields":{"name":"Lot","varietals":[]},"images":[]}`
	if _, failure := DecodeBeanLotCreateManifest([]byte(valid)); failure != nil {
		t.Fatalf("valid manifest failure = %v", failure)
	}
	for _, raw := range []string{
		`{"fields":{"name":"Lot"},"images":[]}`,
		`{"fields":{"name":"Lot","varietals":null},"images":[]}`,
		`{"fields":{"name":"Lot","varietals":"Typica"},"images":[]}`,
		`{"fields":{"name":"Lot","varietals":[null]},"images":[]}`,
		`{"fields":{"name":"Lot","varietals":[]}}`,
		`{"fields":{"name":"Lot","varietals":[]},"images":null}`,
		`{"fields":{"name":"Lot","varietals":[]},"images":{}}`,
		`{"fields":{"name":"Lot","varietals":[]},"images":[null]}`,
	} {
		if _, failure := DecodeBeanLotCreateManifest([]byte(raw)); failure == nil {
			t.Errorf("accepted strict create JSON %s", raw)
		}
	}
}

func TestMutationNormalizationAndRelatedProcessingRules(t *testing.T) {
	manifest, failure := DecodeBeanLotCreateManifest([]byte(`{"fields":{"name":"  Lot  ","origin":"  Kenya  ","varietals":["  SL28  "],"processing_method":"washed","processing_detail":"  \t "},"opening_reason":" first\r\nsecond ","opening_reference":"  ","images":[]}`))
	if failure != nil {
		t.Fatal(failure)
	}
	if manifest.Fields.Name != "Lot" || *manifest.Fields.Origin != "Kenya" || manifest.Fields.Varietals[0] != "SL28" || manifest.Fields.ProcessingDetail != nil || manifest.OpeningReason == nil || *manifest.OpeningReason != "first\nsecond" || manifest.OpeningReference != nil {
		t.Fatalf("normalized manifest = %#v", manifest)
	}
	if _, failure = DecodeBeanLotCreateManifest([]byte(`{"fields":{"name":"Lot","varietals":[],"processing_method":"other","processing_detail":"  "},"images":[]}`)); failure == nil {
		t.Fatal("blank detail authorized other processing")
	}
	patch, failure := DecodeBeanLotPatch([]byte(`{"origin":" Kenya ","notes":" first\rsecond ","processing_method":"washed","processing_detail":"  "}`))
	if failure != nil {
		t.Fatal(failure)
	}
	encoded, _ := json.Marshal(patch)
	if string(encoded) != `{"notes":"first\nsecond","origin":"Kenya","processing_detail":null,"processing_method":"washed"}` {
		t.Fatalf("normalized patch = %s", encoded)
	}
	if _, failure = DecodeBeanLotPatch([]byte(`{"processing_method":"other","processing_detail":" \t "}`)); failure == nil {
		t.Fatal("blank patch detail authorized other processing")
	}
}

func TestMutationDatesRejectYearZero(t *testing.T) {
	if _, failure := DecodeBeanLotCreateManifest([]byte(`{"fields":{"name":"Lot","received_date":"0000-01-01","varietals":[]},"images":[]}`)); failure == nil {
		t.Fatal("accepted year-zero create date")
	}
	if _, failure := DecodeBeanLotPatch([]byte(`{"received_date":"0000-01-01"}`)); failure == nil {
		t.Fatal("accepted year-zero patch date")
	}
	if failure := ValidateInventoryAdjustment(InventoryAdjustmentWrite{QuantityGrams: 1, Reason: "count", OccurredAt: "0000-01-01T00:00:00.000000Z"}); failure == nil {
		t.Fatal("accepted year-zero timestamp")
	}
}

func TestMutationMethodsRequireExactSuccessStatusesAndBodies(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		call   func(*Client) *output.Error
	}{
		{name: "create 200", status: http.StatusOK, call: func(client *Client) *output.Error {
			_, failure := client.CreateBeanLot(context.Background(), BeanLotCreateManifest{Fields: BeanLotFields{Name: "Lot", Varietals: []string{}}, Images: []ImageUploadManifest{}}, "key")
			return failure
		}},
		{name: "create 204", status: http.StatusNoContent, call: func(client *Client) *output.Error {
			_, failure := client.CreateBeanLot(context.Background(), BeanLotCreateManifest{Fields: BeanLotFields{Name: "Lot", Varietals: []string{}}, Images: []ImageUploadManifest{}}, "key")
			return failure
		}},
		{name: "patch 201", status: http.StatusCreated, call: func(client *Client) *output.Error {
			patch, _ := NewBeanLotPatch(map[string]any{"notes": nil})
			_, failure := client.PatchBeanLot(context.Background(), mutationLotID, patch, "key")
			return failure
		}},
		{name: "adjust 205", status: http.StatusResetContent, call: func(client *Client) *output.Error {
			_, failure := client.AdjustBeanLot(context.Background(), mutationLotID, InventoryAdjustmentWrite{QuantityGrams: 1, Reason: "count", OccurredAt: mutationStamp}, "key")
			return failure
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				if test.status != http.StatusNoContent && test.status != http.StatusResetContent {
					_, _ = io.WriteString(w, mutationLotJSON(1))
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

func TestCreateEnforcesEncodedManifestCapAtExactWireBoundary(t *testing.T) {
	var requests atomic.Int64
	var receivedManifestBytes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		receivedManifestBytes = len(rawMutationManifest(t, string(body), r.Header.Get("Content-Type")))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, mutationLotJSON(1))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	oversized := BeanLotCreateManifest{
		Fields:        BeanLotFields{Name: "Lot", Varietals: []string{}, Notes: mutationStringPointer(strings.Repeat("<", 10_000))},
		OpeningReason: mutationStringPointer(strings.Repeat("<", 2_000)), Images: []ImageUploadManifest{},
	}
	if _, failure := client.CreateBeanLot(context.Background(), oversized, "key"); failure == nil || failure.ExitCode != 2 {
		t.Fatalf("failure = %#v", failure)
	}
	if requests.Load() != 0 {
		t.Fatalf("oversized requests = %d", requests.Load())
	}

	exact, ok := exactSizeMutationManifest(maxBeanLotManifestBytes)
	if !ok {
		t.Fatal("could not construct exact-limit manifest")
	}
	if _, failure := client.CreateBeanLot(context.Background(), exact, "key"); failure != nil {
		t.Fatal(failure)
	}
	if requests.Load() != 1 || receivedManifestBytes != maxBeanLotManifestBytes {
		t.Fatalf("requests=%d manifest bytes=%d", requests.Load(), receivedManifestBytes)
	}
}

func TestCreateNormalizesDecomposedUnicodeToNFCAndRejectsCanonicalDuplicateVarietals(t *testing.T) {
	decomposed := "Cafe\u0301"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		manifest := rawMutationManifest(t, string(body), r.Header.Get("Content-Type"))
		if strings.Contains(manifest, decomposed) || !strings.Contains(manifest, "Café") {
			t.Fatalf("manifest is not NFC: %q", manifest)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, mutationLotJSON(0))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	if _, failure := client.CreateBeanLot(context.Background(), BeanLotCreateManifest{Fields: BeanLotFields{Name: decomposed, Varietals: []string{}}, Images: []ImageUploadManifest{}}, "key"); failure != nil {
		t.Fatal(failure)
	}
	manifest := BeanLotCreateManifest{Fields: BeanLotFields{Name: "Lot", Varietals: []string{"Café", decomposed}}, Images: []ImageUploadManifest{}}
	if failure := ValidateBeanLotCreateManifest(manifest); failure == nil || failure.Code != "invalid_varietals" {
		t.Fatalf("canonical duplicate failure = %#v", failure)
	}
	if _, failure := NewBeanLotPatch(map[string]any{"varietals": []any{"Café", decomposed}}); failure == nil || failure.Code != "invalid_patch_value" {
		t.Fatalf("canonical patch duplicate failure = %#v", failure)
	}
}

func exactSizeMutationManifest(size int) (BeanLotCreateManifest, bool) {
	notes := strings.Repeat("<", 10_000)
	for escaped := 1; escaped <= 2_000; escaped++ {
		reason := strings.Repeat("<", escaped)
		manifest := BeanLotCreateManifest{Fields: BeanLotFields{Name: "Lot", Varietals: []string{}, Notes: &notes}, OpeningReason: &reason, Images: []ImageUploadManifest{}}
		encoded, _ := json.Marshal(manifest)
		remainder := size - len(encoded)
		if remainder >= 0 && remainder <= 2_000-escaped {
			reason += strings.Repeat("a", remainder)
			manifest.OpeningReason = &reason
			encoded, _ = json.Marshal(manifest)
			if len(encoded) == size {
				return manifest, true
			}
		}
	}
	return BeanLotCreateManifest{}, false
}

func rawMutationManifest(t *testing.T, body, contentType string) string {
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

func mutationStringPointer(value string) *string { return &value }
func mutationIntPointer(value int64) *int64      { return &value }
