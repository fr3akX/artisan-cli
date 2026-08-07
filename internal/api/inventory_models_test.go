package api

import (
	"encoding/json"
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
