package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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
	manifest := BeanLotCreateManifest{Fields: BeanLotFields{Name: "Lot", Varietals: []string{"SL28", "Ruiru 11"}}, OpeningGrams: 25, OpeningReason: mutationStringPointer("count"), Images: []ImageUploadManifest{}}
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

func mutationStringPointer(value string) *string { return &value }
func mutationIntPointer(value int64) *int64      { return &value }
