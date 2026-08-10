package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
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

func TestListBeanLotsUsesReadRootWithoutIdentityPreflightAndEscapesFilters(t *testing.T) {
	var paths []string
	var gotRawQuery string
	client := inventoryAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		gotRawQuery = r.URL.RawQuery
		writeInventoryJSON(w, `{"items":[],"next_cursor":"next +/= cursor"}`)
	})
	page, failure := client.ListBeanLots(context.Background(), LotListOptions{
		Limit: 17, Cursor: "bound +/= cursor", Query: `%_\\ café`, State: "archived",
		Availability: "negative", Conflict: "open", RoastUUID: "22222222-2222-4222-8222-222222222222",
	})
	if failure != nil {
		t.Fatalf("ListBeanLots() failure = %#v", failure)
	}
	if !reflect.DeepEqual(paths, []string{"/api/v1/inventory/read/bean-lots"}) {
		t.Fatalf("paths = %#v; list must use the read root without an identity preflight", paths)
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

func TestListDesktopBeanLotsUsesExactReducedRouteAndPaginationQuery(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client := inventoryAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		writeInventoryJSON(w, `{"items":[{"lot_id":"11111111111141118111111111111111","name":"Member lot","origin":"Ethiopia","varietals":["Heirloom"],"processing_method":"washed","crop_year":2026,"on_hand_grams":5000,"reserved_grams":1250,"available_grams":3750,"unresolved_conflict_count":2,"future_field":true}],"next_cursor":"opaque +/="}`)
	})
	page, failure := client.ListDesktopBeanLots(context.Background(), PageOptions{Limit: 17, Cursor: "bound +/="})
	if failure != nil {
		t.Fatalf("ListDesktopBeanLots() failure = %#v", failure)
	}
	if gotPath != "/api/v1/inventory/bean-lots" || !reflect.DeepEqual(gotQuery, url.Values{"limit": {"17"}, "cursor": {"bound +/="}}) {
		t.Fatalf("path=%q query=%#v", gotPath, gotQuery)
	}
	if len(page.Items) != 1 || page.Items[0].Name != "Member lot" || page.Items[0].AvailableGrams != 3750 || page.NextCursor == nil || *page.NextCursor != "opaque +/=" {
		t.Fatalf("page = %#v", page)
	}
}

func TestListAllDesktopBeanLotsFollowsOpaqueCursorsAndBoundsTraversal(t *testing.T) {
	var cursors []string
	client := inventoryAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		if len(cursors) == 1 {
			writeInventoryJSON(w, `{"items":[],"next_cursor":"opaque cursor"}`)
			return
		}
		writeInventoryJSON(w, `{"items":[],"next_cursor":null}`)
	})
	page, failure := client.ListAllDesktopBeanLots(context.Background(), PageOptions{Limit: 1})
	if failure != nil || page.NextCursor != nil || !reflect.DeepEqual(cursors, []string{"", "opaque cursor"}) {
		t.Fatalf("page=%#v failure=%#v cursors=%#v", page, failure, cursors)
	}
}

func TestDesktopBeanLotPageRejectsMissingInvalidAndPrivateFullProjectionFields(t *testing.T) {
	valid := `{"items":[{"lot_id":"11111111111141118111111111111111","name":"Member lot","origin":null,"varietals":[],"processing_method":null,"crop_year":null,"on_hand_grams":5,"reserved_grams":2,"available_grams":3,"unresolved_conflict_count":0}],"next_cursor":null}`
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing varietals", body: strings.Replace(valid, `,"varietals":[]`, "", 1)},
		{name: "null varietals", body: strings.Replace(valid, `"varietals":[]`, `"varietals":null`, 1)},
		{name: "inconsistent balance", body: strings.Replace(valid, `"available_grams":3`, `"available_grams":4`, 1)},
		{name: "invalid processing", body: strings.Replace(valid, `"processing_method":null`, `"processing_method":"magic"`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := inventoryAPIClient(t, func(w http.ResponseWriter, _ *http.Request) { writeInventoryJSON(w, test.body) })
			if _, failure := client.ListDesktopBeanLots(context.Background(), PageOptions{}); failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %#v", failure)
			}
		})
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

func TestInventoryReadRoutesNormalizeDashedAndCompactUUIDsAndUseReadRoot(t *testing.T) {
	var paths []string
	client := inventoryAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case r.URL.Path == "/api/v1/inventory/read/bean-lots/"+inventoryLotID:
			writeInventoryJSON(w, strings.ReplaceAll(validDetailJSON(), inventoryAdminRoot, inventoryReadRoot))
		case r.URL.Path == "/api/v1/inventory/read/conflicts/"+inventoryConflictID:
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
		"/api/v1/inventory/read/bean-lots/" + inventoryLotID,
		"/api/v1/inventory/read/bean-lots/" + inventoryLotID + "/ledger",
		"/api/v1/inventory/read/bean-lots/" + inventoryLotID + "/reservations",
		"/api/v1/inventory/read/bean-lots/" + inventoryLotID + "/conflicts",
		"/api/v1/inventory/read/conflicts/" + inventoryConflictID,
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

func TestCollectInventoryPagesEnforcesIndependentPageCeiling(t *testing.T) {
	if MaxInventoryAggregatePages != 1000 {
		t.Fatalf("MaxInventoryAggregatePages = %d, want documented conservative ceiling 1000", MaxInventoryAggregatePages)
	}

	t.Run("exact ceiling may terminate", func(t *testing.T) {
		calls := 0
		items, failure := collectInventoryPages("", MaxInventoryAggregateItems, func(string) ([]int, *string, *output.Error) {
			calls++
			if calls == MaxInventoryAggregatePages {
				return nil, nil, nil
			}
			return nil, stringPointer(fmt.Sprintf("cursor-%d", calls)), nil
		})
		if failure != nil || len(items) != 0 || calls != MaxInventoryAggregatePages {
			t.Fatalf("collect = %#v, %#v, calls=%d", items, failure, calls)
		}
	})

	t.Run("unique empty pages are bounded before next request", func(t *testing.T) {
		calls := 0
		items, failure := collectInventoryPages("", MaxInventoryAggregateItems, func(string) ([]int, *string, *output.Error) {
			calls++
			return nil, stringPointer(fmt.Sprintf("cursor-%d", calls)), nil
		})
		if failure == nil || failure.Code != "pagination_page_limit_exceeded" || items != nil || calls != MaxInventoryAggregatePages {
			t.Fatalf("collect = %#v, %#v, calls=%d", items, failure, calls)
		}
	})

	t.Run("failure cancels traversal without partial output", func(t *testing.T) {
		calls := 0
		canceled := &output.Error{ExitCode: 8, Code: "request_canceled", Message: "canceled"}
		items, failure := collectInventoryPages("", MaxInventoryAggregateItems, func(string) ([]int, *string, *output.Error) {
			calls++
			if calls == 3 {
				return nil, nil, canceled
			}
			return []int{calls}, stringPointer(fmt.Sprintf("cursor-%d", calls)), nil
		})
		if items != nil || failure != canceled || calls != 3 {
			t.Fatalf("collect = %#v, %#v, calls=%d", items, failure, calls)
		}
	})
}

func TestInventoryTotalsUsesReadRootWithOnlyValidatedFilters(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client := inventoryAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		writeInventoryJSON(w, `{"lot_count":3,"on_hand_grams":1000,"reserved_grams":250,"available_grams":750,"on_hand_value_eur_cents":1234,"priced_lot_count":2,"unpriced_lot_count":1}`)
	})
	options := InventoryTotalsOptions{
		Query: "guji", State: "active", Availability: "negative", Conflict: "open",
		RoastUUID: "11111111-1111-4111-8111-111111111111",
	}
	totals, failure := client.InventoryTotals(context.Background(), options)
	if failure != nil {
		t.Fatalf("InventoryTotals() failure = %#v", failure)
	}
	if gotPath != "/api/v1/inventory/read/bean-lots/totals" {
		t.Fatalf("path = %q", gotPath)
	}
	want := url.Values{
		"q": {"guji"}, "state": {"active"}, "availability": {"negative"}, "conflict": {"open"},
		"roast_uuid": {inventoryLotID},
	}
	if !reflect.DeepEqual(gotQuery, want) {
		t.Fatalf("query = %#v, want %#v", gotQuery, want)
	}
	for _, forbidden := range []string{"limit", "cursor", "all"} {
		if gotQuery.Has(forbidden) {
			t.Fatalf("totals query contains pagination option %q", forbidden)
		}
	}
	if totals.LotCount != 3 || totals.OnHandValueEURCents == nil || *totals.OnHandValueEURCents != 1234 {
		t.Fatalf("totals = %#v", totals)
	}
}

func TestValidateInventoryTotalsOptionsRejectsInvalidFiltersWithoutRequest(t *testing.T) {
	requests := 0
	client := inventoryAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeInventoryJSON(w, `{}`)
	})
	for _, options := range []InventoryTotalsOptions{
		{State: "deleted"}, {Availability: "scarce"}, {Conflict: "resolved"}, {RoastUUID: "invalid"},
	} {
		if failure := ValidateInventoryTotalsOptions(options); failure == nil || failure.ExitCode != 2 {
			t.Errorf("ValidateInventoryTotalsOptions(%#v) = %#v", options, failure)
		}
		if _, failure := client.InventoryTotals(context.Background(), options); failure == nil || failure.ExitCode != 2 {
			t.Errorf("InventoryTotals(%#v) = %#v", options, failure)
		}
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestMissingReadNamespaceMapsToServerUpgradeWithoutMaskingEntityNotFound(t *testing.T) {
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
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, tt.body)
			})
			_, failure := client.BeanLot(context.Background(), inventoryLotID)
			if failure == nil || failure.Code != tt.wantCode {
				t.Fatalf("failure = %#v, want code %q", failure, tt.wantCode)
			}
			if tt.wantCode == "server_upgrade_required" && failure.Message != "The server does not provide the inventory read API; upgrade Artisan Server" {
				t.Fatalf("message = %q", failure.Message)
			}
		})
	}
}

func stringPointer(value string) *string { return &value }
