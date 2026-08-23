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

func roastAPIClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, "roast-test-token", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func writeRoastJSON(w http.ResponseWriter, payload string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, payload)
}

func TestRoastUUIDNormalizationPreservesInventoryIdentityContract(t *testing.T) {
	for _, raw := range []string{roastUUID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"} {
		got, failure := NormalizeRoastUUID(raw)
		if failure != nil || got != roastUUID {
			t.Fatalf("NormalizeRoastUUID(%q) = %q, %#v", raw, got, failure)
		}
	}
	for _, raw := range []string{"", "not-an-id", strings.ToUpper(roastUUID), "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"} {
		if got, failure := NormalizeRoastUUID(raw); failure == nil || failure.Code != "invalid_roast_uuid" || failure.ExitCode != 2 || got != "" {
			t.Fatalf("NormalizeRoastUUID(%q) = %q, %#v", raw, got, failure)
		}
	}
	inventoryGot, inventoryFailure := NormalizeInventoryUUID("11111111-1111-4111-8111-111111111111")
	if inventoryFailure != nil || inventoryGot != inventoryLotID {
		t.Fatalf("inventory normalization changed: %q, %#v", inventoryGot, inventoryFailure)
	}
	if _, failure := NormalizeInventoryUUID("invalid"); failure == nil || failure.Code != "invalid_uuid" {
		t.Fatalf("inventory error contract changed: %#v", failure)
	}
}

func TestListRoastsMapsEveryFilterAndCanonicalizesTimes(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	var gotAuthorization string
	client := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		gotAuthorization = r.Header.Get("Authorization")
		writeRoastJSON(w, `{"items":[`+validRoastListItemJSON()+`],"next_cursor":"next +/="}`)
	})
	page, failure := client.ListRoasts(context.Background(), RoastListOptions{
		Limit: 37, Cursor: "opaque +/=", Search: `%_\\ café`,
		RoastAtFrom: "2026-08-23T14:34:56+02:00", RoastAtTo: "2026-08-23T13:00:00Z",
		Machine: "Loring S70", State: "parsed", LabelID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
	})
	if failure != nil {
		t.Fatalf("ListRoasts() failure = %#v", failure)
	}
	if gotPath != "/api/v1/roasts" {
		t.Fatalf("path = %q", gotPath)
	}
	want := url.Values{
		"limit": {"37"}, "cursor": {"opaque +/="}, "search": {`%_\\ café`},
		"roast_at_from": {"2026-08-23T12:34:56Z"}, "roast_at_to": {"2026-08-23T13:00:00Z"},
		"machine": {"Loring S70"}, "state": {"parsed"}, "label_id": {labelUUID},
	}
	if !reflect.DeepEqual(gotQuery, want) {
		t.Fatalf("query = %#v, want %#v", gotQuery, want)
	}
	if gotAuthorization != "Bearer roast-test-token" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if len(page.Items) != 1 || page.Items[0].RoastUUID != roastUUID || page.NextCursor == nil || *page.NextCursor != "next +/=" {
		t.Fatalf("page = %#v", page)
	}
}

func TestListRoastsOmitsZeroValues(t *testing.T) {
	var query url.Values
	client := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		writeRoastJSON(w, `{"items":[],"next_cursor":null}`)
	})
	if _, failure := client.ListRoasts(context.Background(), RoastListOptions{}); failure != nil {
		t.Fatalf("ListRoasts() failure = %#v", failure)
	}
	if len(query) != 0 {
		t.Fatalf("query = %#v, want empty", query)
	}
}

func TestValidateRoastListOptionsRejectsInvalidFiltersBeforeNetwork(t *testing.T) {
	requests := 0
	client := roastAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeRoastJSON(w, `{"items":[],"next_cursor":null}`)
	})
	invalid := []RoastListOptions{
		{Limit: -1}, {Limit: 101}, {Cursor: strings.Repeat("x", 4097)},
		{Search: strings.Repeat("x", 201)}, {Machine: strings.Repeat("x", 101)},
		{State: "deleted"}, {LabelID: "invalid"},
		{RoastAtFrom: "2026-08-23T12:00:00"},
		{RoastAtFrom: "2026-08-23T12:00:00+00:00", RoastAtTo: "2026-08-23T11:59:59Z"},
	}
	for _, options := range invalid {
		if failure := ValidateRoastListOptions(options); failure == nil || failure.ExitCode != 2 || failure.Code != "invalid_roast_filter" {
			t.Errorf("ValidateRoastListOptions(%#v) = %#v", options, failure)
		}
		if _, failure := client.ListRoasts(context.Background(), options); failure == nil || failure.ExitCode != 2 {
			t.Errorf("ListRoasts(%#v) = %#v", options, failure)
		}
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestRoastReadMethodsUseExactPathsNormalizeUUIDAndBindEntities(t *testing.T) {
	var paths []string
	client := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/v1/roasts/" + roastUUID:
			writeRoastJSON(w, validRoastDetailJSON())
		case "/api/v1/roasts/" + roastUUID + "/revisions":
			w.Header().Set("X-Roast-UUID", roastUUID)
			w.Header().Set("X-Roast-Revisions-Version", "1")
			writeRoastJSON(w, `{"items":[`+validRoastRevisionJSON()+`],"next_cursor":null}`)
		case "/api/v1/roasts/" + roastUUID + "/comments":
			writeRoastJSON(w, `{"items":[`+validDeletedCommentJSON()+`],"next_cursor":null}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	})
	dashed := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, failure := client.Roast(context.Background(), dashed); failure != nil {
		t.Fatalf("Roast() failure = %#v", failure)
	}
	if _, failure := client.RoastRevisions(context.Background(), dashed, PageOptions{Limit: 2, Cursor: "revision cursor"}); failure != nil {
		t.Fatalf("RoastRevisions() failure = %#v", failure)
	}
	if _, failure := client.RoastComments(context.Background(), dashed, PageOptions{Limit: 3, Cursor: "comment cursor"}); failure != nil {
		t.Fatalf("RoastComments() failure = %#v", failure)
	}
	want := []string{
		"/api/v1/roasts/" + roastUUID,
		"/api/v1/roasts/" + roastUUID + "/revisions?cursor=revision+cursor&limit=2",
		"/api/v1/roasts/" + roastUUID + "/comments?cursor=comment+cursor&limit=3",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestRoastReadsRejectMismatchedEntityAndRevisionHeaders(t *testing.T) {
	otherUUID := "eeeeeeeeeeee4eee8eeeeeeeeeeeeeee"
	tests := []struct {
		name string
		call func(*Client) *output.Error
		body string
		head func(http.Header)
	}{
		{name: "detail roast mismatch", body: strings.Replace(validRoastDetailJSON(), roastUUID, otherUUID, 1), call: func(c *Client) *output.Error { _, f := c.Roast(context.Background(), roastUUID); return f }},
		{name: "comment roast mismatch", body: `{"items":[` + strings.Replace(validDeletedCommentJSON(), roastUUID, otherUUID, 1) + `],"next_cursor":null}`, call: func(c *Client) *output.Error {
			_, f := c.RoastComments(context.Background(), roastUUID, PageOptions{})
			return f
		}},
		{name: "revision roast header mismatch", body: `{"items":[],"next_cursor":null}`, head: func(h http.Header) { h.Set("X-Roast-UUID", otherUUID); h.Set("X-Roast-Revisions-Version", "1") }, call: func(c *Client) *output.Error {
			_, f := c.RoastRevisions(context.Background(), roastUUID, PageOptions{})
			return f
		}},
		{name: "revision version mismatch", body: `{"items":[],"next_cursor":null}`, head: func(h http.Header) { h.Set("X-Roast-UUID", roastUUID); h.Set("X-Roast-Revisions-Version", "2") }, call: func(c *Client) *output.Error {
			_, f := c.RoastRevisions(context.Background(), roastUUID, PageOptions{})
			return f
		}},
		{name: "partial revision headers", body: `{"items":[],"next_cursor":null}`, head: func(h http.Header) { h.Set("X-Roast-UUID", roastUUID) }, call: func(c *Client) *output.Error {
			_, f := c.RoastRevisions(context.Background(), roastUUID, PageOptions{})
			return f
		}},
		{name: "duplicate revision header", body: `{"items":[],"next_cursor":null}`, head: func(h http.Header) {
			h.Add("X-Roast-UUID", roastUUID)
			h.Add("X-Roast-UUID", roastUUID)
			h.Set("X-Roast-Revisions-Version", "1")
		}, call: func(c *Client) *output.Error {
			_, f := c.RoastRevisions(context.Background(), roastUUID, PageOptions{})
			return f
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := roastAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.head != nil {
					tt.head(w.Header())
				}
				writeRoastJSON(w, tt.body)
			})
			failure := tt.call(client)
			if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

func TestRoastRevisionHeadersMayBeJointlyAbsentForCompatibleLegacyServer(t *testing.T) {
	client := roastAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeRoastJSON(w, `{"items":[],"next_cursor":null}`)
	})
	if _, failure := client.RoastRevisions(context.Background(), roastUUID, PageOptions{}); failure != nil {
		t.Fatalf("jointly absent optional headers rejected: %#v", failure)
	}
}

func TestAllRoastReadsFollowCursorsPreserveFiltersAndBoundTraversal(t *testing.T) {
	var listQueries []url.Values
	client := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		listQueries = append(listQueries, r.URL.Query())
		if r.URL.Query().Get("cursor") == "" {
			writeRoastJSON(w, `{"items":[],"next_cursor":"next"}`)
			return
		}
		writeRoastJSON(w, `{"items":[],"next_cursor":null}`)
	})
	page, failure := client.ListAllRoasts(context.Background(), RoastListOptions{Limit: 1, Search: "review", State: "parsed"})
	if failure != nil || page.NextCursor != nil || len(listQueries) != 2 {
		t.Fatalf("page=%#v failure=%#v queries=%#v", page, failure, listQueries)
	}
	for _, query := range listQueries {
		if query.Get("limit") != "1" || query.Get("search") != "review" || query.Get("state") != "parsed" {
			t.Fatalf("filters not preserved: %#v", query)
		}
	}

	_, failure = collectRoastPages("", MaxRoastAggregateItems, func(string) ([]int, *string, *output.Error) {
		return []int{1}, stringPointer("repeat"), nil
	})
	if failure == nil || failure.Code != "invalid_server_response" {
		t.Fatalf("repeated cursor failure = %#v", failure)
	}
	_, failure = collectRoastPages("", 2, func(string) ([]int, *string, *output.Error) {
		return []int{1, 2, 3}, nil, nil
	})
	if failure == nil || failure.Code != "pagination_limit_exceeded" {
		t.Fatalf("item bound failure = %#v", failure)
	}
	calls := 0
	_, failure = collectRoastPages("", MaxRoastAggregateItems, func(string) ([]int, *string, *output.Error) {
		calls++
		return nil, stringPointer(fmt.Sprintf("cursor-%d", calls)), nil
	})
	if failure == nil || failure.Code != "pagination_page_limit_exceeded" || calls != MaxRoastAggregatePages {
		t.Fatalf("page bound failure=%#v calls=%d", failure, calls)
	}
}

func TestAllRoastRevisionAndCommentReadsReturnNoCursor(t *testing.T) {
	calls := map[string]int{}
	client := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Query().Get("cursor") == "" {
			writeRoastJSON(w, `{"items":[],"next_cursor":"next"}`)
			return
		}
		writeRoastJSON(w, `{"items":[],"next_cursor":null}`)
	})
	revisions, failure := client.AllRoastRevisions(context.Background(), roastUUID, PageOptions{})
	if failure != nil || revisions.NextCursor != nil {
		t.Fatalf("AllRoastRevisions() = %#v, %#v", revisions, failure)
	}
	comments, failure := client.AllRoastComments(context.Background(), roastUUID, PageOptions{})
	if failure != nil || comments.NextCursor != nil {
		t.Fatalf("AllRoastComments() = %#v, %#v", comments, failure)
	}
	if calls["/api/v1/roasts/"+roastUUID+"/revisions"] != 2 || calls["/api/v1/roasts/"+roastUUID+"/comments"] != 2 {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestRoastReadsRejectRedirectMalformedResponseAndSecretReflection(t *testing.T) {
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantCode string
	}{
		{name: "redirect", wantCode: "redirect_refused", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
		}},
		{name: "malformed", wantCode: "invalid_server_response", handler: func(w http.ResponseWriter, _ *http.Request) { writeRoastJSON(w, `{"items":[]}`) }},
		{name: "multiple documents", wantCode: "invalid_server_response", handler: func(w http.ResponseWriter, _ *http.Request) { writeRoastJSON(w, `{"items":[],"next_cursor":null}{}`) }},
		{name: "token reflection", wantCode: "invalid_server_response", handler: func(w http.ResponseWriter, _ *http.Request) {
			writeRoastJSON(w, `{"items":[],"next_cursor":null,"future":"roast-test-token"}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := roastAPIClient(t, tt.handler)
			_, failure := client.ListRoasts(context.Background(), RoastListOptions{})
			if failure == nil || failure.Code != tt.wantCode || strings.Contains(failure.Message, "roast-test-token") {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

func TestRoast404ClassificationPreservesEntityAbsenceAndClassifiesMissingAPI(t *testing.T) {
	tests := []struct {
		name     string
		call     func(*Client) *output.Error
		body     string
		wantCode string
	}{
		{name: "top-level route absent", body: `{"detail":"Not Found"}`, wantCode: "server_upgrade_required", call: func(c *Client) *output.Error {
			_, f := c.ListRoasts(context.Background(), RoastListOptions{})
			return f
		}},
		{name: "entity absent", body: `{"error":{"code":"not_found","message":"Not found","details":null}}`, wantCode: "not_found", call: func(c *Client) *output.Error { _, f := c.Roast(context.Background(), roastUUID); return f }},
		{name: "nested endpoint absent", body: `{"detail":"Not Found"}`, wantCode: "server_upgrade_required", call: func(c *Client) *output.Error {
			_, f := c.RoastComments(context.Background(), roastUUID, PageOptions{})
			return f
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := roastAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, tt.body)
			})
			failure := tt.call(client)
			if failure == nil || failure.Code != tt.wantCode {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}
