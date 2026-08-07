package command

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const commandLotID = "11111111111141118111111111111111"
const commandImageID = "22222222222242228222222222222222"
const commandConflictID = "55555555555545558555555555555555"
const commandTimestamp = "2026-08-04T12:00:00.000000Z"

func commandInventorySummary(id, name string, available int) string {
	return fmt.Sprintf(`{"lot_id":%q,"name":%q,"origin":null,"processing_method":null,"crop_year":null,"state":"active","on_hand_grams":%d,"reserved_grams":0,"available_grams":%d,"unresolved_conflict_count":0,"cover_image":null,"updated_at":%q}`,
		id, name, available, available, commandTimestamp)
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
		_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"next"}`, commandInventorySummary(commandLotID, "Lot", 5000))
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "lot", "list")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("result = %#v", result)
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor *string          `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !envelope.OK || len(envelope.Data.Items) != 1 || envelope.Data.NextCursor == nil || *envelope.Data.NextCursor != "next" {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestInventoryLotListAllFollowsOpaqueCursorsAndReturnsNullCursor(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		if cursor == "" {
			_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"opaque+/= cursor"}`, commandInventorySummary(commandLotID, "First", 10))
			return
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":null}`, commandInventorySummary("22222222222242228222222222222222", "Second", 20))
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "lot", "list", "--all")
	if result.code != 0 {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(cursors, []string{"", "opaque+/= cursor"}) {
		t.Fatalf("cursors = %#v", cursors)
	}
	if !strings.Contains(result.stdout, `"next_cursor":null`) || strings.Count(result.stdout, `"lot_id"`) != 2 {
		t.Fatalf("stdout = %q", result.stdout)
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
	return `{"conflict_id":"` + commandConflictID + `","lot_id":"` + commandLotID + `","source_ledger_entry_id":"33333333333343338333333333333333","roast_uuid":null,"reservation_id":null,"trigger_operation":"manual_adjustment","available_grams_snapshot":-1,"state":"open","resolution_note":null,"resolved_by_user_id":null,"resolved_at":null,"created_at":"` + commandTimestamp + `"}`
}

func commandLotDetailJSON() string {
	return strings.TrimSuffix(commandInventorySummary(commandLotID, "Lot", 5000), "}") + `,"producer":null,"supplier":null,"external_reference":null,"received_date":null,"varietals":[],"sca_score":null,"processing_detail":null,"altitude_min_metres":null,"altitude_max_metres":null,"notes":null,"images":[],"created_at":"` + commandTimestamp + `","archived_at":null,"links":{"self":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `","ledger":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `/ledger","reservations":"/api/v1/inventory/admin/bean-lots/` + commandLotID + `/reservations"}}`
}
