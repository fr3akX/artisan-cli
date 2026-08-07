package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
)

func inventoryAPIClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, "inventory-test-token", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func writeInventoryJSON(w http.ResponseWriter, payload string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, payload)
}

func TestListBeanLotsSendsExactFiltersAndEscapesValues(t *testing.T) {
	var gotPath, gotRawQuery string
	client := inventoryAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRawQuery = r.URL.Path, r.URL.RawQuery
		writeInventoryJSON(w, `{"items":[],"next_cursor":"next +/= cursor"}`)
	})
	page, failure := client.ListBeanLots(context.Background(), LotListOptions{
		Limit: 17, Cursor: "bound +/= cursor", Query: `%_\\ café`, State: "archived",
		Availability: "negative", Conflict: "open", RoastUUID: "22222222-2222-4222-8222-222222222222",
	})
	if failure != nil {
		t.Fatalf("ListBeanLots() failure = %#v", failure)
	}
	if gotPath != "/api/v1/inventory/admin/bean-lots" {
		t.Fatalf("path = %q", gotPath)
	}
	got, err := url.ParseQuery(gotRawQuery)
	if err != nil {
		t.Fatalf("ParseQuery() error = %v", err)
	}
	want := url.Values{
		"limit": {"17"}, "cursor": {"bound +/= cursor"}, "q": {`%_\\ café`}, "state": {"archived"},
		"availability": {"negative"}, "conflict": {"open"}, "roast_uuid": {inventoryImageID},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("query = %#v, want %#v", got, want)
	}
	if page.NextCursor == nil || *page.NextCursor != "next +/= cursor" {
		t.Fatalf("next cursor = %#v", page.NextCursor)
	}
}

func TestListAllBeanLotsPreservesFiltersWhenBindingEachCursor(t *testing.T) {
	var queries []url.Values
	client := inventoryAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		if r.URL.Query().Get("cursor") == "" {
			writeInventoryJSON(w, `{"items":[],"next_cursor":"bound cursor"}`)
			return
		}
		writeInventoryJSON(w, `{"items":[],"next_cursor":null}`)
	})
	page, failure := client.ListAllBeanLots(context.Background(), LotListOptions{Limit: 1, Query: "%", State: "active", Availability: "positive", Conflict: "none", RoastUUID: inventoryImageID})
	if failure != nil || page.NextCursor != nil {
		t.Fatalf("ListAllBeanLots() = %#v, %#v", page, failure)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %#v", queries)
	}
	for index, query := range queries {
		for key, want := range map[string]string{"limit": "1", "q": "%", "state": "active", "availability": "positive", "conflict": "none", "roast_uuid": inventoryImageID} {
			if query.Get(key) != want {
				t.Fatalf("query %d %s = %q, want %q", index, key, query.Get(key), want)
			}
		}
	}
	if queries[0].Get("cursor") != "" || queries[1].Get("cursor") != "bound cursor" {
		t.Fatalf("cursor binding = %#v", queries)
	}
}

func TestInventoryReadRoutesNormalizeDashedAndCompactUUIDs(t *testing.T) {
	var paths []string
	client := inventoryAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case r.URL.Path == "/api/v1/inventory/admin/bean-lots/"+inventoryLotID:
			writeInventoryJSON(w, validDetailJSON())
		case r.URL.Path == "/api/v1/inventory/admin/conflicts/"+inventoryConflictID:
			writeInventoryJSON(w, `{"conflict_id":"`+inventoryConflictID+`","lot_id":"`+inventoryLotID+`","source_ledger_entry_id":"`+inventoryEntryID+`","roast_uuid":null,"reservation_id":null,"trigger_operation":"manual_adjustment","available_grams_snapshot":-1,"state":"open","resolution_note":null,"resolved_by_user_id":null,"resolved_at":null,"created_at":"`+inventoryTimestamp+`"}`)
		default:
			writeInventoryJSON(w, `{"items":[],"next_cursor":null}`)
		}
	})
	dashedLot := "11111111-1111-4111-8111-111111111111"
	if _, failure := client.BeanLot(context.Background(), dashedLot); failure != nil {
		t.Fatalf("BeanLot() failure = %#v", failure)
	}
	if _, failure := client.BeanLotLedger(context.Background(), dashedLot, PageOptions{}); failure != nil {
		t.Fatalf("BeanLotLedger() failure = %#v", failure)
	}
	if _, failure := client.BeanLotReservations(context.Background(), inventoryLotID, PageOptions{}); failure != nil {
		t.Fatalf("BeanLotReservations() failure = %#v", failure)
	}
	if _, failure := client.BeanLotConflicts(context.Background(), dashedLot, PageOptions{}); failure != nil {
		t.Fatalf("BeanLotConflicts() failure = %#v", failure)
	}
	dashedConflict := "55555555-5555-4555-8555-555555555555"
	if _, failure := client.InventoryConflict(context.Background(), dashedConflict); failure != nil {
		t.Fatalf("InventoryConflict() failure = %#v", failure)
	}
	want := []string{
		"/api/v1/inventory/admin/bean-lots/" + inventoryLotID,
		"/api/v1/inventory/admin/bean-lots/" + inventoryLotID + "/ledger",
		"/api/v1/inventory/admin/bean-lots/" + inventoryLotID + "/reservations",
		"/api/v1/inventory/admin/bean-lots/" + inventoryLotID + "/conflicts",
		"/api/v1/inventory/admin/conflicts/" + inventoryConflictID,
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestInventoryReadRejectsInvalidLocalUUIDWithoutRequest(t *testing.T) {
	requests := 0
	client := inventoryAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeInventoryJSON(w, validDetailJSON())
	})
	_, failure := client.BeanLot(context.Background(), "not-an-id")
	if failure == nil || failure.ExitCode != 2 || failure.Code != "invalid_uuid" {
		t.Fatalf("failure = %#v", failure)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestCollectInventoryPagesCompletesInOrderAndDetectsCursorLoopAndBound(t *testing.T) {
	pages := map[string]struct {
		items []int
		next  *string
	}{
		"start":  {items: []int{1, 2}, next: stringPointer("second")},
		"second": {items: []int{3}, next: nil},
	}
	items, failure := collectInventoryPages("start", 3, func(cursor string) ([]int, *string, *output.Error) {
		page := pages[cursor]
		return page.items, page.next, nil
	})
	if failure != nil || !reflect.DeepEqual(items, []int{1, 2, 3}) {
		t.Fatalf("collect = %#v, %#v", items, failure)
	}

	_, failure = collectInventoryPages("", 10, func(cursor string) ([]int, *string, *output.Error) {
		return []int{1}, stringPointer("same"), nil
	})
	if failure == nil || failure.Code != "invalid_server_response" {
		t.Fatalf("loop failure = %#v", failure)
	}

	calls := 0
	_, failure = collectInventoryPages("", 2, func(cursor string) ([]int, *string, *output.Error) {
		calls++
		return []int{1, 2, 3}, nil, nil
	})
	if failure == nil || failure.Code != "pagination_limit_exceeded" || calls != 1 {
		t.Fatalf("bound failure/calls = %#v/%d", failure, calls)
	}
}

func TestMissingAdminNamespaceMapsToServerUpgradeWithoutMaskingEntityNotFound(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{name: "namespace default 404", body: `{"detail":"Not Found"}`, wantCode: "server_upgrade_required"},
		{name: "entity API 404", body: `{"error":{"code":"bean_lot_not_found","message":"Bean lot not found","details":null}}`, wantCode: "bean_lot_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := inventoryAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				writeInventoryJSON(w, tt.body)
			})
			_, failure := client.BeanLot(context.Background(), inventoryLotID)
			if failure == nil || failure.Code != tt.wantCode {
				t.Fatalf("failure = %#v, want code %q", failure, tt.wantCode)
			}
		})
	}
}

func stringPointer(value string) *string { return &value }
