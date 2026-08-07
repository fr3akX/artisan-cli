package command

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const commandLotID = "11111111111141118111111111111111"
const commandImageID = "22222222222242228222222222222222"
const commandConflictID = "55555555555545558555555555555555"
const commandEntryID = "33333333333343338333333333333333"
const commandReservationID = "44444444444444448444444444444444"
const commandRoastID = "66666666666646668666666666666666"
const commandClientID = "77777777777747778777777777777777"
const commandUserID = "88888888888848888888888888888888"
const commandTimestamp = "2026-08-04T12:00:00.000000Z"

func commandInventorySummary(id, name string, available int) string {
	return fmt.Sprintf(`{"lot_id":%q,"name":%q,"origin":null,"processing_method":null,"crop_year":null,"state":"active","on_hand_grams":%d,"reserved_grams":0,"available_grams":%d,"unresolved_conflict_count":0,"cover_image":null,"updated_at":%q}`,
		id, name, available, available, commandTimestamp)
}

func commandInventoryImageJSON() string {
	return `{"image_id":"` + commandImageID + `","caption":"front","alt_text":null,"position":0,"is_cover":true,"display_width":1600,"display_height":1200,"thumbnail_width":480,"thumbnail_height":360,"display_url":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `/images/` + commandImageID + `/display","thumbnail_url":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `/images/` + commandImageID + `/thumbnail"}`
}

func commandInventorySummaryFull() string {
	return `{"lot_id":"` + commandLotID + `","name":"Lot","origin":"Ethiopia","processing_method":"washed","crop_year":2026,"state":"active","on_hand_grams":5000,"reserved_grams":1250,"available_grams":3750,"unresolved_conflict_count":2,"cover_image":` + commandInventoryImageJSON() + `,"updated_at":"` + commandTimestamp + `"}`
}

func commandLedgerJSON() string {
	return `{"entry_id":"` + commandEntryID + `","operation":"reservation","lot_id":"` + commandLotID + `","roast_uuid":"` + commandRoastID + `","reservation_id":"` + commandReservationID + `","on_hand_delta":-250,"reserved_delta":1250,"resulting_on_hand_grams":5000,"resulting_reserved_grams":1250,"resulting_available_grams":3750,"reason":"planned","reference":null,"actor_kind":"desktop","occurred_at":"` + commandTimestamp + `","created_at":"` + commandTimestamp + `"}`
}

func commandReservationJSON() string {
	return `{"reservation_id":"` + commandReservationID + `","client_reservation_uuid":"` + commandEntryID + `","lot_id":"` + commandLotID + `","roast_uuid":"` + commandRoastID + `","client_instance_uuid":"` + commandClientID + `","state":"finalized","planned_grams":1250,"actual_grams":1200,"reserved_at":"` + commandTimestamp + `","completed_at":null,"created_at":"` + commandTimestamp + `","updated_at":"` + commandTimestamp + `","open_conflict_id":"` + commandConflictID + `"}`
}

func commandResolvedConflictJSON() string {
	return `{"conflict_id":"` + commandConflictID + `","lot_id":"` + commandLotID + `","source_ledger_entry_id":"` + commandEntryID + `","roast_uuid":"` + commandRoastID + `","reservation_id":"` + commandReservationID + `","trigger_operation":"consumption","available_grams_snapshot":-25,"state":"resolved","resolution_note":"counted","resolved_by_user_id":"` + commandUserID + `","resolved_at":"` + commandTimestamp + `","created_at":"` + commandTimestamp + `"}`
}

func inventoryRuntime(t *testing.T, serverURL string) Runtime {
	t.Helper()
	return Runtime{ConfigDir: t.TempDir(), Getenv: func(name string) string {
		switch name {
		case "ARTISAN_SERVER_URL":
			return serverURL
		case "ARTISAN_SERVER_TOKEN":
			return commandTestToken
		default:
			return ""
		}
	}}
}

func TestInventoryLotListHumanUsesExactFiltersAndDoesNotTruncate(t *testing.T) {
	var query url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/inventory/admin/bean-lots" {
			t.Errorf("path = %q", r.URL.Path)
		}
		query = r.URL.Query()
		_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"next+/= cursor"}`, commandInventorySummary(commandLotID, "Very long lot name", 2147483647))
	}))
	defer server.Close()

	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "inventory", "lot", "list",
		"--limit", "17", "--cursor", "bound+/= cursor", "--q", `%_\\ café`, "--state", "active",
		"--availability", "positive", "--conflict", "none", "--roast-uuid", "22222222-2222-4222-8222-222222222222")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("result = %#v", result)
	}
	wantQuery := url.Values{"limit": {"17"}, "cursor": {"bound+/= cursor"}, "q": {`%_\\ café`}, "state": {"active"}, "availability": {"positive"}, "conflict": {"none"}, "roast_uuid": {commandImageID}}
	if !reflect.DeepEqual(query, wantQuery) {
		t.Fatalf("query = %#v, want %#v", query, wantQuery)
	}
	for _, exact := range []string{commandLotID, "2147483647", "next+/= cursor"} {
		if !strings.Contains(result.stdout, exact) {
			t.Fatalf("stdout %q does not contain %q", result.stdout, exact)
		}
	}
}

func TestInventoryLotListJSONRetainsPageAndCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"next"}`, commandInventorySummaryFull())
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "lot", "list")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("result = %#v", result)
	}
	assertInventoryJSONSuccess(t, result.stdout, inventoryExpectedPage([]any{inventoryExpectedSummaryFull()}, "next"))
}

func TestInventoryLotListAllFollowsOpaqueCursorsAndReturnsNullCursor(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		if cursor == "" {
			_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"opaque+/= cursor"}`, commandInventorySummaryFull())
			return
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":null}`, commandInventorySummary(commandImageID, "Second", 20))
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "lot", "list", "--all")
	if result.code != 0 {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(cursors, []string{"", "opaque+/= cursor"}) {
		t.Fatalf("cursors = %#v", cursors)
	}
	assertInventoryJSONSuccess(t, result.stdout, inventoryExpectedPage([]any{
		inventoryExpectedSummaryFull(),
		inventoryExpectedSummaryNullable(commandImageID, "Second", 20),
	}, nil))
}

func TestInventoryJSONContractsForDetailAndHistoryCommands(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		response string
		wantData any
	}{
		{name: "lot detail", args: []string{"inventory", "lot", "show", commandLotID}, response: commandLotDetailFullJSON(), wantData: inventoryExpectedLotDetail()},
		{name: "ledger page", args: []string{"inventory", "lot", "ledger", commandLotID}, response: `{"items":[` + commandLedgerJSON() + `],"next_cursor":"ledger-next"}`, wantData: inventoryExpectedPage([]any{inventoryExpectedLedger()}, "ledger-next")},
		{name: "reservation page", args: []string{"inventory", "lot", "reservations", commandLotID}, response: `{"items":[` + commandReservationJSON() + `],"next_cursor":"reservation-next"}`, wantData: inventoryExpectedPage([]any{inventoryExpectedReservation()}, "reservation-next")},
		{name: "lot conflict page", args: []string{"inventory", "lot", "conflicts", commandLotID}, response: `{"items":[` + commandResolvedConflictJSON() + `],"next_cursor":"conflict-next"}`, wantData: inventoryExpectedPage([]any{inventoryExpectedResolvedConflict()}, "conflict-next")},
		{name: "conflict show", args: []string{"inventory", "conflict", "show", commandConflictID}, response: commandConflictJSON(), wantData: inventoryExpectedOpenConflict()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, tt.response)
			}))
			defer server.Close()
			args := append([]string{"--json"}, tt.args...)
			result := runAuthCommand(t, inventoryRuntime(t, server.URL), args...)
			if result.code != 0 || result.stderr != "" {
				t.Fatalf("result = %#v", result)
			}
			assertInventoryJSONSuccess(t, result.stdout, tt.wantData)
		})
	}
}

func TestInventoryJSONContractAssertionsDetectMutatedTagsTypesAndKeys(t *testing.T) {
	want := map[string]any{"ok": true, "data": inventoryExpectedPage([]any{inventoryExpectedSummaryFull()}, "next")}
	mutations := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "extra trusted key", mutate: func(value map[string]any) { value["future"] = true }},
		{name: "wrong top-level tag", mutate: func(value map[string]any) { value["result"] = value["data"]; delete(value, "data") }},
		{name: "wrong boolean type", mutate: func(value map[string]any) { value["ok"] = "true" }},
		{name: "integer serialized as float", mutate: func(value map[string]any) {
			value["data"].(map[string]any)["items"].([]any)[0].(map[string]any)["on_hand_grams"] = json.Number("5000.0")
		}},
		{name: "integer serialized as string", mutate: func(value map[string]any) {
			value["data"].(map[string]any)["items"].([]any)[0].(map[string]any)["on_hand_grams"] = "5000"
		}},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			mutated := cloneInventoryJSONValue(t, want).(map[string]any)
			tt.mutate(mutated)
			if reflect.DeepEqual(mutated, want) {
				t.Fatal("contract comparison accepted intentional mutation")
			}
		})
	}
}

func TestInventoryLotListAllPaginationFailureEmitsNoPartialData(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"same"}`, commandInventorySummary(commandLotID, "must-not-leak", 5000))
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "lot", "list", "--all")
	if result.code != 9 || result.stderr != "" || requests != 2 {
		t.Fatalf("result/requests = %#v/%d", result, requests)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	assertInventoryJSONKeys(t, envelope, "error", "ok")
	if envelope["ok"] != false || strings.Contains(result.stdout, "must-not-leak") || strings.Contains(result.stdout, commandLotID) || strings.Count(strings.TrimSpace(result.stdout), "\n") != 0 {
		t.Fatalf("partial or multiple output envelopes: %q", result.stdout)
	}
	errorObject, valid := envelope["error"].(map[string]any)
	if !valid {
		t.Fatalf("error = %#v", envelope["error"])
	}
	assertInventoryJSONKeys(t, errorObject, "code", "message")
	if errorObject["code"] != "invalid_server_response" {
		t.Fatalf("error = %#v", errorObject)
	}
}

func assertInventoryJSONKeys(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v; object = %#v", got, want, object)
	}
}

func assertInventoryJSONSuccess(t *testing.T, payload string, wantData any) {
	t.Helper()
	got := decodeInventoryJSONDocument(t, payload)
	want := map[string]any{"ok": true, "data": wantData}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON contract mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func decodeInventoryJSONDocument(t *testing.T, payload string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("json.Decode() error = %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("JSON output contains trailing value: %v, %#v", err, trailing)
	}
	return decoded
}

func cloneInventoryJSONValue(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var clone any
	if err := decoder.Decode(&clone); err != nil {
		t.Fatalf("json.Decode() error = %v", err)
	}
	return clone
}

func inventoryExpectedPage(items []any, nextCursor any) map[string]any {
	return map[string]any{"items": items, "next_cursor": nextCursor}
}

func inventoryExpectedImage() map[string]any {
	return map[string]any{
		"image_id": commandImageID, "caption": "front", "alt_text": nil,
		"position": json.Number("0"), "is_cover": true,
		"display_width": json.Number("1600"), "display_height": json.Number("1200"),
		"thumbnail_width": json.Number("480"), "thumbnail_height": json.Number("360"),
		"display_url":   "/api/v1/inventory/admin/bean-lots/" + commandLotID + "/images/" + commandImageID + "/display",
		"thumbnail_url": "/api/v1/inventory/admin/bean-lots/" + commandLotID + "/images/" + commandImageID + "/thumbnail",
	}
}

func inventoryExpectedSummaryFull() map[string]any {
	return map[string]any{
		"lot_id": commandLotID, "name": "Lot", "origin": "Ethiopia", "processing_method": "washed",
		"crop_year": json.Number("2026"), "state": "active", "on_hand_grams": json.Number("5000"),
		"reserved_grams": json.Number("1250"), "available_grams": json.Number("3750"),
		"unresolved_conflict_count": json.Number("2"), "cover_image": inventoryExpectedImage(), "updated_at": commandTimestamp,
	}
}

func inventoryExpectedSummaryNullable(id, name string, available int) map[string]any {
	grams := json.Number(fmt.Sprintf("%d", available))
	return map[string]any{
		"lot_id": id, "name": name, "origin": nil, "processing_method": nil, "crop_year": nil,
		"state": "active", "on_hand_grams": grams, "reserved_grams": json.Number("0"),
		"available_grams": grams, "unresolved_conflict_count": json.Number("0"), "cover_image": nil, "updated_at": commandTimestamp,
	}
}

func inventoryExpectedLotDetail() map[string]any {
	value := inventoryExpectedSummaryFull()
	for key, item := range map[string]any{
		"producer": "Producer", "supplier": nil, "external_reference": "EXT-1", "received_date": "2026-08-01",
		"varietals": []any{"Heirloom", "74110"}, "sca_score": "87.50", "processing_detail": "Raised beds",
		"altitude_min_metres": json.Number("1900"), "altitude_max_metres": nil, "notes": "seasonal",
		"images": []any{inventoryExpectedImage()}, "created_at": commandTimestamp, "archived_at": nil,
		"links": map[string]any{
			"self":         "/api/v1/inventory/admin/bean-lots/" + commandLotID,
			"ledger":       "/api/v1/inventory/admin/bean-lots/" + commandLotID + "/ledger",
			"reservations": "/api/v1/inventory/admin/bean-lots/" + commandLotID + "/reservations",
		},
	} {
		value[key] = item
	}
	return value
}

func inventoryExpectedLedger() map[string]any {
	return map[string]any{
		"entry_id": commandEntryID, "operation": "reservation", "lot_id": commandLotID,
		"roast_uuid": commandRoastID, "reservation_id": commandReservationID,
		"on_hand_delta": json.Number("-250"), "reserved_delta": json.Number("1250"),
		"resulting_on_hand_grams": json.Number("5000"), "resulting_reserved_grams": json.Number("1250"),
		"resulting_available_grams": json.Number("3750"), "reason": "planned", "reference": nil,
		"actor_kind": "desktop", "occurred_at": commandTimestamp, "created_at": commandTimestamp,
	}
}

func inventoryExpectedReservation() map[string]any {
	return map[string]any{
		"reservation_id": commandReservationID, "client_reservation_uuid": commandEntryID,
		"lot_id": commandLotID, "roast_uuid": commandRoastID, "client_instance_uuid": commandClientID,
		"state": "finalized", "planned_grams": json.Number("1250"), "actual_grams": json.Number("1200"),
		"reserved_at": commandTimestamp, "completed_at": nil, "created_at": commandTimestamp,
		"updated_at": commandTimestamp, "open_conflict_id": commandConflictID,
	}
}

func inventoryExpectedResolvedConflict() map[string]any {
	return map[string]any{
		"conflict_id": commandConflictID, "lot_id": commandLotID, "source_ledger_entry_id": commandEntryID,
		"roast_uuid": commandRoastID, "reservation_id": commandReservationID, "trigger_operation": "consumption",
		"available_grams_snapshot": json.Number("-25"), "state": "resolved", "resolution_note": "counted",
		"resolved_by_user_id": commandUserID, "resolved_at": commandTimestamp, "created_at": commandTimestamp,
	}
}

func inventoryExpectedOpenConflict() map[string]any {
	return map[string]any{
		"conflict_id": commandConflictID, "lot_id": commandLotID, "source_ledger_entry_id": commandEntryID,
		"roast_uuid": nil, "reservation_id": nil, "trigger_operation": "manual_adjustment",
		"available_grams_snapshot": json.Number("-1"), "state": "open", "resolution_note": nil,
		"resolved_by_user_id": nil, "resolved_at": nil, "created_at": commandTimestamp,
	}
}

func TestInventoryReadCommandsUseAdminRoutesAndNormalizeIDs(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/ledger"):
			_, _ = fmt.Fprint(w, `{"items":[],"next_cursor":null}`)
		case strings.HasSuffix(r.URL.Path, "/reservations"):
			_, _ = fmt.Fprint(w, `{"items":[],"next_cursor":null}`)
		case strings.HasSuffix(r.URL.Path, "/conflicts"):
			_, _ = fmt.Fprint(w, `{"items":[],"next_cursor":null}`)
		case strings.Contains(r.URL.Path, "/conflicts/"):
			_, _ = fmt.Fprint(w, commandConflictJSON())
		default:
			_, _ = fmt.Fprint(w, commandLotDetailJSON())
		}
	}))
	defer server.Close()
	dashedLot := "11111111-1111-4111-8111-111111111111"
	dashedConflict := "55555555-5555-4555-8555-555555555555"
	commands := [][]string{
		{"inventory", "lot", "show", dashedLot},
		{"inventory", "lot", "ledger", dashedLot},
		{"inventory", "lot", "reservations", dashedLot},
		{"inventory", "lot", "conflicts", dashedLot},
		{"inventory", "conflict", "list", "--lot", dashedLot},
		{"inventory", "conflict", "show", dashedConflict},
	}
	for _, args := range commands {
		result := runAuthCommand(t, inventoryRuntime(t, server.URL), args...)
		if result.code != 0 {
			t.Fatalf("Run(%v) = %#v", args, result)
		}
	}
	want := []string{
		"/api/v1/inventory/admin/bean-lots/" + commandLotID,
		"/api/v1/inventory/admin/bean-lots/" + commandLotID + "/ledger",
		"/api/v1/inventory/admin/bean-lots/" + commandLotID + "/reservations",
		"/api/v1/inventory/admin/bean-lots/" + commandLotID + "/conflicts",
		"/api/v1/inventory/admin/bean-lots/" + commandLotID + "/conflicts",
		"/api/v1/inventory/admin/conflicts/" + commandConflictID,
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestInventoryReadUsageAndUpgradeFailuresHaveStableExits(t *testing.T) {
	result := runAuthCommand(t, Runtime{}, "inventory", "conflict", "list")
	if result.code != 2 || !strings.Contains(result.stderr, "--lot") {
		t.Fatalf("usage result = %#v", result)
	}
	for _, args := range [][]string{
		{"inventory", "lot", "show", "not-a-uuid"},
		{"inventory", "lot", "list", "--limit", "101"},
		{"inventory", "lot", "ledger", commandLotID, "--limit", "-1"},
	} {
		result = runAuthCommand(t, Runtime{}, args...)
		if result.code != 2 {
			t.Fatalf("Run(%v) code = %d, want local usage exit 2; result = %#v", args, result.code, result)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"detail":"Not Found"}`)
	}))
	defer server.Close()
	result = runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "lot", "list")
	if result.code != 9 || !strings.Contains(result.stdout, `"code":"server_upgrade_required"`) {
		t.Fatalf("upgrade result = %#v", result)
	}
}

func commandConflictJSON() string {
	return `{"conflict_id":"` + commandConflictID + `","lot_id":"` + commandLotID + `","source_ledger_entry_id":"` + commandEntryID + `","roast_uuid":null,"reservation_id":null,"trigger_operation":"manual_adjustment","available_grams_snapshot":-1,"state":"open","resolution_note":null,"resolved_by_user_id":null,"resolved_at":null,"created_at":"` + commandTimestamp + `"}`
}

func commandLotDetailFullJSON() string {
	return strings.TrimSuffix(commandInventorySummaryFull(), "}") + `,"producer":"Producer","supplier":null,"external_reference":"EXT-1","received_date":"2026-08-01","varietals":["Heirloom","74110"],"sca_score":"87.50","processing_detail":"Raised beds","altitude_min_metres":1900,"altitude_max_metres":null,"notes":"seasonal","images":[` + commandInventoryImageJSON() + `],"created_at":"` + commandTimestamp + `","archived_at":null,"links":{"self":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `","ledger":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `/ledger","reservations":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `/reservations"}}`
}

func commandLotDetailJSON() string {
	return strings.TrimSuffix(commandInventorySummary(commandLotID, "Lot", 5000), "}") + `,"producer":null,"supplier":null,"external_reference":null,"received_date":null,"varietals":[],"sca_score":null,"processing_detail":null,"altitude_min_metres":null,"altitude_max_metres":null,"notes":null,"images":[],"created_at":"` + commandTimestamp + `","archived_at":null,"links":{"self":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `","ledger":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `/ledger","reservations":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `/reservations"}}`
}
