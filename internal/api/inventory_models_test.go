package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const inventoryLotID = "11111111111141118111111111111111"
const inventoryImageID = "22222222222242228222222222222222"
const inventoryEntryID = "33333333333343338333333333333333"
const inventoryReservationID = "44444444444444448444444444444444"
const inventoryConflictID = "55555555555545558555555555555555"
const inventoryTimestamp = "2026-08-04T12:00:00.000000Z"

func validImageJSON() string {
	return `{
		"image_id":"` + inventoryImageID + `","caption":null,"alt_text":"cover","position":0,"is_cover":true,
		"display_width":1600,"display_height":1200,"thumbnail_width":480,"thumbnail_height":360,
		"display_url":"/api/v1/inventory/admin/bean-lots/` + inventoryLotID + `/images/` + inventoryImageID + `/display",
		"thumbnail_url":"/api/v1/inventory/admin/bean-lots/` + inventoryLotID + `/images/` + inventoryImageID + `/thumbnail"
	}`
}

func validSummaryJSON() string {
	return `{
		"lot_id":"` + inventoryLotID + `","name":"Ethiopia Guji","origin":"Ethiopia","processing_method":"washed",
		"crop_year":2026,"state":"active","on_hand_grams":5000,"reserved_grams":1250,"available_grams":3750,
		"unresolved_conflict_count":0,"cover_image":` + validImageJSON() + `,"updated_at":"` + inventoryTimestamp + `"
	}`
}

func validDetailJSON() string {
	summary := strings.TrimSuffix(validSummaryJSON(), "}")
	return summary + `,
		"producer":null,"supplier":"Supplier","external_reference":null,"received_date":"2026-08-01",
		"varietals":["Heirloom"],"sca_score":"87.50","processing_detail":null,
		"altitude_min_metres":1900,"altitude_max_metres":2100,"notes":null,"images":[` + validImageJSON() + `],
		"created_at":"` + inventoryTimestamp + `","archived_at":null,
		"links":{"self":"/api/v1/inventory/admin/bean-lots/` + inventoryLotID + `","ledger":"/api/v1/inventory/admin/bean-lots/` + inventoryLotID + `/ledger","reservations":"/api/v1/inventory/admin/bean-lots/` + inventoryLotID + `/reservations"}
	}`
}

func TestInventoryModelsAcceptCanonicalFixtureAndUnknownAdditiveFields(t *testing.T) {
	payload := strings.TrimSuffix(validDetailJSON(), "}") + `,"future_field":{"safe":true}}`
	var lot BeanLotDetail
	if err := json.Unmarshal([]byte(payload), &lot); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if lot.LotID != inventoryLotID || lot.OnHandGrams != 5000 || lot.SCAScore == nil || *lot.SCAScore != "87.50" {
		t.Fatalf("decoded lot = %#v", lot)
	}
	encoded, err := json.Marshal(lot)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "future_field") {
		t.Fatal("unknown response field was retained")
	}
}

func TestInventoryModelsRejectMalformedConsumedFields(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "missing required nullable", old: `"origin":"Ethiopia",`, new: ""},
		{name: "dashed response UUID", old: inventoryLotID, new: "11111111-1111-4111-8111-111111111111"},
		{name: "uppercase response UUID", old: inventoryLotID, new: "ABCDEFABCDEFABCDEFABCDEFABCDEFAB"},
		{name: "invalid timestamp", old: inventoryTimestamp, new: "2026-08-04T12:00:00Z"},
		{name: "noninteger grams", old: `"on_hand_grams":5000`, new: `"on_hand_grams":5000.5`},
		{name: "balance invariant", old: `"available_grams":3750`, new: `"available_grams":3749`},
		{name: "invalid state", old: `"state":"active"`, new: `"state":"retired"`},
		{name: "invalid processing", old: `"processing_method":"washed"`, new: `"processing_method":"magic"`},
		{name: "wrong image root", old: "/api/v1/inventory/admin/bean-lots/", new: "/api/v1/browser/bean-lots/"},
		{name: "wrong detail link", old: `"ledger":"/api/v1/inventory/admin`, new: `"ledger":"/api/v1/browser`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := strings.Replace(validDetailJSON(), tt.old, tt.new, 1)
			var lot BeanLotDetail
			if err := json.Unmarshal([]byte(payload), &lot); err == nil {
				t.Fatal("json.Unmarshal() succeeded, want validation failure")
			}
		})
	}
}

func TestBeanLotDetailRejectsDuplicateImagePositions(t *testing.T) {
	second := strings.ReplaceAll(validImageJSON(), inventoryImageID, inventoryEntryID)
	payload := strings.Replace(validDetailJSON(), `"images":[`+validImageJSON()+`]`, `"images":[`+validImageJSON()+`,`+second+`]`, 1)
	var lot BeanLotDetail
	if err := json.Unmarshal([]byte(payload), &lot); err == nil {
		t.Fatal("json.Unmarshal() succeeded with duplicate positions")
	}
}

func TestInventoryHistoryModelsValidateEnumsIDsTimestampsAndBalances(t *testing.T) {
	fixtures := []struct {
		name    string
		payload string
		target  any
	}{
		{
			name:    "ledger",
			payload: `{"entry_id":"` + inventoryEntryID + `","operation":"manual_adjustment","lot_id":"` + inventoryLotID + `","roast_uuid":null,"reservation_id":null,"on_hand_delta":100,"reserved_delta":0,"resulting_on_hand_grams":5100,"resulting_reserved_grams":1250,"resulting_available_grams":3850,"reason":"count","reference":null,"actor_kind":"desktop","occurred_at":"` + inventoryTimestamp + `","created_at":"` + inventoryTimestamp + `","future":1}`,
			target:  &InventoryLedgerEntry{},
		},
		{
			name:    "reservation",
			payload: `{"reservation_id":"` + inventoryReservationID + `","client_reservation_uuid":"` + inventoryEntryID + `","lot_id":"` + inventoryLotID + `","roast_uuid":"` + inventoryImageID + `","client_instance_uuid":"` + inventoryConflictID + `","state":"reserved","planned_grams":1250,"actual_grams":null,"reserved_at":"` + inventoryTimestamp + `","completed_at":null,"created_at":"` + inventoryTimestamp + `","updated_at":"` + inventoryTimestamp + `","open_conflict_id":null}`,
			target:  &InventoryReservation{},
		},
		{
			name:    "conflict",
			payload: `{"conflict_id":"` + inventoryConflictID + `","lot_id":"` + inventoryLotID + `","source_ledger_entry_id":"` + inventoryEntryID + `","roast_uuid":null,"reservation_id":null,"trigger_operation":"manual_adjustment","available_grams_snapshot":-1,"state":"open","resolution_note":null,"resolved_by_user_id":null,"resolved_at":null,"created_at":"` + inventoryTimestamp + `"}`,
			target:  &InventoryConflict{},
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(fixture.payload), fixture.target); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
		})
	}

	var ledger InventoryLedgerEntry
	badBalance := `{"entry_id":"` + inventoryEntryID + `","operation":"manual_adjustment","lot_id":"` + inventoryLotID + `","roast_uuid":null,"reservation_id":null,"on_hand_delta":100,"reserved_delta":0,"resulting_on_hand_grams":5100,"resulting_reserved_grams":1250,"resulting_available_grams":3849,"reason":null,"reference":null,"actor_kind":"desktop","occurred_at":"` + inventoryTimestamp + `","created_at":"` + inventoryTimestamp + `"}`
	if err := json.Unmarshal([]byte(badBalance), &ledger); err == nil {
		t.Fatal("ledger accepted an invalid resulting balance")
	}
	invalidEnums := []struct {
		name    string
		payload string
		target  any
	}{
		{name: "ledger operation", payload: strings.Replace(fixtures[0].payload, `"operation":"manual_adjustment"`, `"operation":"unknown"`, 1), target: &InventoryLedgerEntry{}},
		{name: "ledger actor", payload: strings.Replace(fixtures[0].payload, `"actor_kind":"desktop"`, `"actor_kind":"robot"`, 1), target: &InventoryLedgerEntry{}},
		{name: "reservation state", payload: strings.Replace(fixtures[1].payload, `"state":"reserved"`, `"state":"pending"`, 1), target: &InventoryReservation{}},
		{name: "conflict state", payload: strings.Replace(fixtures[2].payload, `"state":"open"`, `"state":"ignored"`, 1), target: &InventoryConflict{}},
	}
	for _, invalid := range invalidEnums {
		t.Run("invalid "+invalid.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(invalid.payload), invalid.target); err == nil {
				t.Fatal("json.Unmarshal() accepted an invalid enum")
			}
		})
	}
}

func TestInventoryModelsRejectNullForEveryRequiredNonNullField(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		fields  []string
		new     func() any
	}{
		{name: "image", payload: validImageJSON(), fields: []string{"image_id", "position", "is_cover", "display_width", "display_height", "thumbnail_width", "thumbnail_height", "display_url", "thumbnail_url"}, new: func() any { return &InventoryImage{} }},
		{name: "summary", payload: validSummaryJSON(), fields: []string{"lot_id", "name", "state", "on_hand_grams", "reserved_grams", "available_grams", "unresolved_conflict_count", "updated_at"}, new: func() any { return &BeanLotSummary{} }},
		{name: "detail", payload: validDetailJSON(), fields: []string{"varietals", "images", "created_at", "links"}, new: func() any { return &BeanLotDetail{} }},
		{name: "ledger", payload: validLedgerJSON(), fields: []string{"entry_id", "operation", "lot_id", "on_hand_delta", "reserved_delta", "resulting_on_hand_grams", "resulting_reserved_grams", "resulting_available_grams", "actor_kind", "occurred_at", "created_at"}, new: func() any { return &InventoryLedgerEntry{} }},
		{name: "reservation", payload: validReservationJSON(), fields: []string{"reservation_id", "client_reservation_uuid", "lot_id", "roast_uuid", "client_instance_uuid", "state", "planned_grams", "reserved_at", "created_at", "updated_at"}, new: func() any { return &InventoryReservation{} }},
		{name: "conflict", payload: validConflictJSON(), fields: []string{"conflict_id", "lot_id", "source_ledger_entry_id", "trigger_operation", "available_grams_snapshot", "state", "created_at"}, new: func() any { return &InventoryConflict{} }},
	}
	for _, tt := range tests {
		for _, field := range tt.fields {
			t.Run(tt.name+"/"+field, func(t *testing.T) {
				payload := mutateInventoryJSONField(t, tt.payload, field, nil, false)
				if err := json.Unmarshal(payload, tt.new()); err == nil {
					t.Fatalf("accepted null required non-null field %q", field)
				}
			})
		}
	}

	t.Run("detail/varietals null element", func(t *testing.T) {
		payload := strings.Replace(validDetailJSON(), `"varietals":["Heirloom"]`, `"varietals":[null]`, 1)
		if err := json.Unmarshal([]byte(payload), &BeanLotDetail{}); err == nil {
			t.Fatal("accepted null varietal element")
		}
	})
	t.Run("detail/null nested link", func(t *testing.T) {
		payload := strings.Replace(validDetailJSON(), `"self":"/api/v1/inventory/admin`, `"self":null,"discard":"/api/v1/inventory/admin`, 1)
		if err := json.Unmarshal([]byte(payload), &BeanLotDetail{}); err == nil {
			t.Fatal("accepted null required link")
		}
	})
	for _, tt := range []struct {
		name    string
		payload string
		target  any
	}{
		{name: "summary nullable explicit null", payload: mutateInventoryJSONString(t, validSummaryJSON(), "origin", nil, false), target: &BeanLotSummary{}},
		{name: "detail nullable explicit null", payload: mutateInventoryJSONString(t, validDetailJSON(), "producer", nil, false), target: &BeanLotDetail{}},
		{name: "ledger nullable explicit null", payload: mutateInventoryJSONString(t, validLedgerJSON(), "reference", nil, false), target: &InventoryLedgerEntry{}},
		{name: "reservation nullable explicit null", payload: mutateInventoryJSONString(t, validReservationJSON(), "actual_grams", nil, false), target: &InventoryReservation{}},
		{name: "conflict nullable explicit null", payload: mutateInventoryJSONString(t, validConflictJSON(), "resolved_at", nil, false), target: &InventoryConflict{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tt.payload), tt.target); err != nil {
				t.Fatalf("explicit nullable field rejected: %v", err)
			}
		})
	}
	for _, field := range []string{"producer", "varietals"} {
		t.Run("detail missing distinct/"+field, func(t *testing.T) {
			payload := mutateInventoryJSONField(t, validDetailJSON(), field, nil, true)
			if err := json.Unmarshal(payload, &BeanLotDetail{}); err == nil || !strings.Contains(err.Error(), "missing required field") {
				t.Fatalf("missing field error = %v", err)
			}
		})
	}
}

func TestInventoryPagesRejectNullItemsAndElementsButAllowNullCursor(t *testing.T) {
	pageTests := []struct {
		name    string
		item    string
		newPage func() any
	}{
		{name: "lots", item: validSummaryJSON(), newPage: func() any { return &BeanLotPage{} }},
		{name: "ledger", item: validLedgerJSON(), newPage: func() any { return &InventoryLedgerEntryPage{} }},
		{name: "reservations", item: validReservationJSON(), newPage: func() any { return &InventoryReservationPage{} }},
		{name: "conflicts", item: validConflictJSON(), newPage: func() any { return &InventoryConflictPage{} }},
	}
	for _, tt := range pageTests {
		t.Run(tt.name+"/null items", func(t *testing.T) {
			if err := json.Unmarshal([]byte(`{"items":null,"next_cursor":null}`), tt.newPage()); err == nil {
				t.Fatal("accepted null items")
			}
		})
		t.Run(tt.name+"/null element", func(t *testing.T) {
			if err := json.Unmarshal([]byte(`{"items":[null],"next_cursor":null}`), tt.newPage()); err == nil {
				t.Fatal("accepted null item element")
			}
		})
		t.Run(tt.name+"/null cursor", func(t *testing.T) {
			if err := json.Unmarshal([]byte(`{"items":[`+tt.item+`],"next_cursor":null}`), tt.newPage()); err != nil {
				t.Fatalf("nullable cursor rejected: %v", err)
			}
		})
	}
}

func mutateInventoryJSONField(t *testing.T, payload, field string, value any, remove bool) []byte {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(payload), &object); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	if _, exists := object[field]; !exists {
		t.Fatalf("fixture missing field %q", field)
	}
	if remove {
		delete(object, field)
	} else {
		object[field] = value
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("fixture encode: %v", err)
	}
	return encoded
}

func mutateInventoryJSONString(t *testing.T, payload, field string, value any, remove bool) string {
	return string(mutateInventoryJSONField(t, payload, field, value, remove))
}

func validLedgerJSON() string {
	return `{"entry_id":"` + inventoryEntryID + `","operation":"manual_adjustment","lot_id":"` + inventoryLotID + `","roast_uuid":null,"reservation_id":null,"on_hand_delta":100,"reserved_delta":0,"resulting_on_hand_grams":5100,"resulting_reserved_grams":1250,"resulting_available_grams":3850,"reason":"count","reference":null,"actor_kind":"desktop","occurred_at":"` + inventoryTimestamp + `","created_at":"` + inventoryTimestamp + `"}`
}

func validReservationJSON() string {
	return `{"reservation_id":"` + inventoryReservationID + `","client_reservation_uuid":"` + inventoryEntryID + `","lot_id":"` + inventoryLotID + `","roast_uuid":"` + inventoryImageID + `","client_instance_uuid":"` + inventoryConflictID + `","state":"reserved","planned_grams":1250,"actual_grams":null,"reserved_at":"` + inventoryTimestamp + `","completed_at":null,"created_at":"` + inventoryTimestamp + `","updated_at":"` + inventoryTimestamp + `","open_conflict_id":null}`
}

func validConflictJSON() string {
	return `{"conflict_id":"` + inventoryConflictID + `","lot_id":"` + inventoryLotID + `","source_ledger_entry_id":"` + inventoryEntryID + `","roast_uuid":null,"reservation_id":null,"trigger_operation":"manual_adjustment","available_grams_snapshot":-1,"state":"open","resolution_note":null,"resolved_by_user_id":null,"resolved_at":null,"created_at":"` + inventoryTimestamp + `"}`
}

func TestInventoryReservationStateNullAndTimestampInvariants(t *testing.T) {
	completed := "2026-08-04T12:00:01.000000Z"
	valid := []string{
		validReservationJSON(),
		reservationStateJSON("finalized", int64Pointer(1200), &completed),
		reservationStateJSON("released", nil, &completed),
	}
	for index, payload := range valid {
		if err := json.Unmarshal([]byte(payload), &InventoryReservation{}); err != nil {
			t.Errorf("valid[%d]: %v", index, err)
		}
	}
	invalid := []string{
		reservationStateJSON("reserved", int64Pointer(1200), nil),
		reservationStateJSON("reserved", nil, &completed),
		reservationStateJSON("finalized", nil, &completed),
		reservationStateJSON("finalized", int64Pointer(1200), nil),
		reservationStateJSON("released", int64Pointer(1200), &completed),
		reservationStateJSON("released", nil, nil),
		reservationStateJSON("finalized", int64Pointer(1200), stringPointer("2026-08-04T11:59:59.000000Z")),
	}
	for index, payload := range invalid {
		if err := json.Unmarshal([]byte(payload), &InventoryReservation{}); err == nil {
			t.Errorf("accepted invalid reservation[%d]: %s", index, payload)
		}
	}
}

func TestInventoryConflictStateSnapshotAndTimestampInvariants(t *testing.T) {
	resolved := strings.ReplaceAll(validConflictJSON(), `"state":"open","resolution_note":null,"resolved_by_user_id":null,"resolved_at":null`, `"state":"resolved","resolution_note":"counted","resolved_by_user_id":"`+inventoryImageID+`","resolved_at":"2026-08-04T12:00:01.000000Z"`)
	if err := json.Unmarshal([]byte(resolved), &InventoryConflict{}); err != nil {
		t.Fatalf("valid resolved conflict: %v", err)
	}
	minimum := strings.Replace(validConflictJSON(), `"available_grams_snapshot":-1`, `"available_grams_snapshot":-2147483647`, 1)
	if err := json.Unmarshal([]byte(minimum), &InventoryConflict{}); err != nil {
		t.Fatalf("valid minimum conflict snapshot: %v", err)
	}
	invalid := []string{
		strings.Replace(validConflictJSON(), `"available_grams_snapshot":-1`, `"available_grams_snapshot":0`, 1),
		strings.Replace(validConflictJSON(), `"available_grams_snapshot":-1`, `"available_grams_snapshot":-2147483648`, 1),
		strings.Replace(validConflictJSON(), `"resolution_note":null`, `"resolution_note":"counted"`, 1),
		strings.Replace(validConflictJSON(), `"resolved_by_user_id":null`, `"resolved_by_user_id":"`+inventoryImageID+`"`, 1),
		strings.Replace(validConflictJSON(), `"resolved_at":null`, `"resolved_at":"`+inventoryTimestamp+`"`, 1),
		strings.Replace(resolved, `"resolution_note":"counted"`, `"resolution_note":null`, 1),
		strings.Replace(resolved, `"resolved_by_user_id":"`+inventoryImageID+`"`, `"resolved_by_user_id":null`, 1),
		strings.Replace(resolved, `"resolved_at":"2026-08-04T12:00:01.000000Z"`, `"resolved_at":null`, 1),
		strings.Replace(resolved, `"resolved_at":"2026-08-04T12:00:01.000000Z"`, `"resolved_at":"2026-08-04T11:59:59.000000Z"`, 1),
	}
	for index, payload := range invalid {
		if err := json.Unmarshal([]byte(payload), &InventoryConflict{}); err == nil {
			t.Errorf("accepted invalid conflict[%d]: %s", index, payload)
		}
	}
}

func reservationStateJSON(state string, actual *int64, completed *string) string {
	payload := validReservationJSON()
	payload = strings.Replace(payload, `"state":"reserved"`, `"state":"`+state+`"`, 1)
	if actual != nil {
		payload = strings.Replace(payload, `"actual_grams":null`, `"actual_grams":`+fmt.Sprint(*actual), 1)
	}
	if completed != nil {
		payload = strings.Replace(payload, `"completed_at":null`, `"completed_at":"`+*completed+`"`, 1)
	}
	return payload
}

func int64Pointer(value int64) *int64 { return &value }

func TestInventoryPagesRequireItemsAndCursorWhileToleratingUnknownFields(t *testing.T) {
	var page BeanLotPage
	payload := `{"items":[` + validSummaryJSON() + `],"next_cursor":"opaque+/= cursor","future":true}`
	if err := json.Unmarshal([]byte(payload), &page); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if page.NextCursor == nil || *page.NextCursor != "opaque+/= cursor" || len(page.Items) != 1 {
		t.Fatalf("page = %#v", page)
	}
	for _, malformed := range []string{`{"next_cursor":null}`, `{"items":[]}`, `{"items":[],"next_cursor":""}`} {
		if err := json.Unmarshal([]byte(malformed), &page); err == nil {
			t.Fatalf("json.Unmarshal(%s) succeeded", malformed)
		}
	}
}
