package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
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

func TestRoastFilterCanonicalizesValidRFC3339Boundaries(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "2026-01-01T00:00:00Z", want: "2026-01-01T00:00:00Z"},
		{raw: "2024-02-29T23:59:59.1+23:59", want: "2024-02-29T00:00:59.1Z"},
		{raw: "2026-12-31T23:59:59.999999999-23:59", want: "2027-01-01T23:58:59.999999999Z"},
		{raw: "2026-01-01T00:00:00+00:00", want: "2026-01-01T00:00:00Z"},
		{raw: "0001-01-01T00:01:00.123456789+00:01", want: "0001-01-01T00:00:00.123456789Z"},
		{raw: "9999-12-31T23:58:59.123456789-00:01", want: "9999-12-31T23:59:59.123456789Z"},
	}
	for _, tt := range tests {
		query, failure := roastListQuery(RoastListOptions{RoastAtFrom: tt.raw})
		if failure != nil || query.Get("roast_at_from") != tt.want {
			t.Errorf("roastListQuery(%q) = %q, %#v; want %q", tt.raw, query.Get("roast_at_from"), failure, tt.want)
		}
	}
}

func TestRoastFilterRejectsUTCYearRolloverBeforeRequest(t *testing.T) {
	requests := 0
	client := roastAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeRoastJSON(w, `{"items":[],"next_cursor":null}`)
	})
	for _, raw := range []string{
		"0001-01-01T00:00:00.123456789+00:01",
		"9999-12-31T23:59:59.123456789-00:01",
	} {
		options := RoastListOptions{RoastAtFrom: raw}
		if failure := ValidateRoastListOptions(options); failure == nil || failure.Code != "invalid_roast_filter" || failure.ExitCode != 2 {
			t.Errorf("ValidateRoastListOptions(%q) = %#v", raw, failure)
		}
		if _, failure := client.ListRoasts(context.Background(), options); failure == nil || failure.Code != "invalid_roast_filter" || failure.ExitCode != 2 {
			t.Errorf("ListRoasts(%q) = %#v", raw, failure)
		}
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
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
		{Limit: -1}, {Limit: 101}, {Cursor: strings.Repeat("x", 513)},
		{Search: strings.Repeat("x", 201)}, {Search: "review\x00hidden"}, {Machine: strings.Repeat("x", 101)}, {Machine: "roaster\x00hidden"},
		{State: "deleted"}, {LabelID: "invalid"},
		{RoastAtFrom: "2026-08-23T12:00:00"},
		{RoastAtFrom: "2026-08-23T12:00:00+24:00"},
		{RoastAtFrom: "2026-08-23T12:00:00+01:60"},
		{RoastAtFrom: "2026-08-23T12:00:00,1Z"},
		{RoastAtFrom: "2026-08-23T12:00:00.1234567890Z"},
		{RoastAtFrom: "2026-08-23t12:00:00Z"},
		{RoastAtFrom: "2026-08-23T12:00:00z"},
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

func TestRoastTextFiltersDoNotRejectOtherControlsWithoutContractEvidence(t *testing.T) {
	var got url.Values
	client := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		writeRoastJSON(w, `{"items":[],"next_cursor":null}`)
	})
	options := RoastListOptions{Search: "review\x01term", Machine: "roaster\x1fmodel"}
	if _, failure := client.ListRoasts(context.Background(), options); failure != nil {
		t.Fatalf("ListRoasts() failure = %#v", failure)
	}
	if got.Get("search") != options.Search || got.Get("machine") != options.Machine {
		t.Fatalf("query = %#v", got)
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
			writeRoastJSON(w, `{"items":[`+validRoastListItemJSON()+`],"next_cursor":"next"}`)
			return
		}
		writeRoastJSON(w, `{"items":[`+validRoastListItemJSON()+`],"next_cursor":null}`)
	})
	page, failure := client.ListAllRoasts(context.Background(), RoastListOptions{Limit: 1, Search: "review", State: "parsed"})
	if failure != nil || page.NextCursor != nil || len(page.Items) != 2 || len(listQueries) != 2 {
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
	if failure == nil || failure.Code != "invalid_server_response" {
		t.Fatalf("item bound failure = %#v", failure)
	}
	calls := 0
	_, failure = collectRoastPages("", MaxRoastAggregateItems, func(string) ([]int, *string, *output.Error) {
		calls++
		return []int{calls}, stringPointer(fmt.Sprintf("cursor-%d", calls)), nil
	})
	if failure == nil || failure.Code != "pagination_page_limit_exceeded" || calls != MaxRoastAggregatePages {
		t.Fatalf("page bound failure=%#v calls=%d", failure, calls)
	}
}

func TestPublicRoastAggregateMethodsEnforceExactItemBoundary(t *testing.T) {
	tests := []struct {
		name string
		item string
		call func(*Client) (int, *output.Error)
	}{
		{
			name: "roasts", item: validRoastListItemJSON(),
			call: func(client *Client) (int, *output.Error) {
				page, failure := client.ListAllRoasts(context.Background(), RoastListOptions{})
				return len(page.Items), failure
			},
		},
		{
			name: "revisions", item: validRoastRevisionJSON(),
			call: func(client *Client) (int, *output.Error) {
				page, failure := client.AllRoastRevisions(context.Background(), roastUUID, PageOptions{})
				return len(page.Items), failure
			},
		},
		{
			name: "comments", item: validDeletedCommentJSON(),
			call: func(client *Client) (int, *output.Error) {
				page, failure := client.AllRoastComments(context.Background(), roastUUID, PageOptions{})
				return len(page.Items), failure
			},
		},
	}
	for _, tt := range tests {
		for _, total := range []int{MaxRoastAggregateItems, MaxRoastAggregateItems + 1} {
			t.Run(tt.name+"/"+strconv.Itoa(total), func(t *testing.T) {
				requests := 0
				client := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
					requests++
					pageNumber := 0
					if cursor := r.URL.Query().Get("cursor"); cursor != "" {
						var err error
						pageNumber, err = strconv.Atoi(cursor)
						if err != nil {
							t.Fatalf("cursor = %q", cursor)
						}
					}
					start := pageNumber * 100
					count := total - start
					if count > 100 {
						count = 100
					}
					if count < 1 {
						t.Fatalf("unexpected aggregate request %d for total %d", requests, total)
					}
					next := "null"
					if start+count < total {
						next = strconv.Quote(strconv.Itoa(pageNumber + 1))
					}
					writeRoastJSON(w, `{"items":[`+repeatRoastJSONItem(tt.item, count)+`],"next_cursor":`+next+`}`)
				})
				count, failure := tt.call(client)
				if total == MaxRoastAggregateItems {
					if failure != nil || count != MaxRoastAggregateItems || requests != 100 {
						t.Fatalf("count=%d failure=%#v requests=%d", count, failure, requests)
					}
					return
				}
				if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 || count != 0 || requests != 101 {
					t.Fatalf("count=%d failure=%#v requests=%d", count, failure, requests)
				}
			})
		}
	}
}

func repeatRoastJSONItem(item string, count int) string {
	if count == 1 {
		return item
	}
	return strings.Repeat(item+",", count-1) + item
}

func TestAllRoastRevisionAndCommentReadsReturnNoCursor(t *testing.T) {
	calls := map[string]int{}
	client := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Query().Get("cursor") == "" {
			switch {
			case strings.HasSuffix(r.URL.Path, "/revisions"):
				writeRoastJSON(w, `{"items":[`+validRoastRevisionJSON()+`],"next_cursor":"next"}`)
			case strings.HasSuffix(r.URL.Path, "/comments"):
				writeRoastJSON(w, `{"items":[`+validDeletedCommentJSON()+`],"next_cursor":"next"}`)
			}
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/revisions"):
			writeRoastJSON(w, `{"items":[`+validRoastRevisionJSON()+`],"next_cursor":null}`)
		case strings.HasSuffix(r.URL.Path, "/comments"):
			writeRoastJSON(w, `{"items":[`+validDeletedCommentJSON()+`],"next_cursor":null}`)
		}
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

func TestRoastAggregateReadsRejectEmptyContinuationPagesWithoutFollowing(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) *output.Error
	}{
		{name: "roasts", call: func(client *Client) *output.Error {
			_, failure := client.ListAllRoasts(context.Background(), RoastListOptions{})
			return failure
		}},
		{name: "revisions", call: func(client *Client) *output.Error {
			_, failure := client.AllRoastRevisions(context.Background(), roastUUID, PageOptions{})
			return failure
		}},
		{name: "comments", call: func(client *Client) *output.Error {
			_, failure := client.AllRoastComments(context.Background(), roastUUID, PageOptions{})
			return failure
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			client := roastAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests++
				writeRoastJSON(w, `{"items":[],"next_cursor":"next"}`)
			})
			failure := tt.call(client)
			if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
				t.Fatalf("failure = %#v", failure)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestRoastResponseCursorMatchesRequestByteContract(t *testing.T) {
	requestClient := roastAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := len(r.URL.Query().Get("cursor")); got != 512 {
			t.Fatalf("request cursor bytes = %d, want 512", got)
		}
		writeRoastJSON(w, `{"items":[`+validRoastListItemJSON()+`],"next_cursor":null}`)
	})
	if _, failure := requestClient.ListRoasts(context.Background(), RoastListOptions{Cursor: strings.Repeat("x", 512)}); failure != nil {
		t.Fatalf("512-byte request cursor rejected: %#v", failure)
	}

	pages := []struct {
		name string
		item string
		call func(*Client) (*string, *output.Error)
	}{
		{name: "roasts", item: validRoastListItemJSON(), call: func(client *Client) (*string, *output.Error) {
			page, failure := client.ListRoasts(context.Background(), RoastListOptions{})
			return page.NextCursor, failure
		}},
		{name: "revisions", item: validRoastRevisionJSON(), call: func(client *Client) (*string, *output.Error) {
			page, failure := client.RoastRevisions(context.Background(), roastUUID, PageOptions{})
			return page.NextCursor, failure
		}},
		{name: "comments", item: validDeletedCommentJSON(), call: func(client *Client) (*string, *output.Error) {
			page, failure := client.RoastComments(context.Background(), roastUUID, PageOptions{})
			return page.NextCursor, failure
		}},
	}
	for _, page := range pages {
		for _, length := range []int{512, 513} {
			t.Run(page.name+"/"+fmt.Sprint(length), func(t *testing.T) {
				client := roastAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
					writeRoastJSON(w, `{"items":[`+page.item+`],"next_cursor":"`+strings.Repeat("x", length)+`"}`)
				})
				nextCursor, failure := page.call(client)
				if length == 512 {
					if failure != nil || nextCursor == nil || len(*nextCursor) != 512 {
						t.Fatalf("next cursor = %#v, failure = %#v", nextCursor, failure)
					}
					return
				}
				if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
					t.Fatalf("failure = %#v", failure)
				}
			})
		}
	}
}

func TestRoastReadsRejectDuplicateJSONKeysAsInvalidServerResponse(t *testing.T) {
	client := roastAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeRoastJSON(w, `{"items":[],"items":[],"next_cursor":null}`)
	})
	if _, failure := client.ListRoasts(context.Background(), RoastListOptions{}); failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
		t.Fatalf("failure = %#v", failure)
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

func TestRoastReadsAllowConfiguredServerURLInArbitraryResponseData(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/roasts":
			item := strings.Replace(validRoastListItemJSON(), `"title":"Review roast"`, `"title":"`+server.URL+`"`, 1)
			writeRoastJSON(w, `{"items":[`+item+`],"next_cursor":null}`)
		case "/api/v1/roasts/" + roastUUID:
			detail := strings.Replace(validRoastDetailJSON(), `"roast":"private"`, `"roast":"`+server.URL+`"`, 1)
			writeRoastJSON(w, detail)
		case "/api/v1/roasts/" + roastUUID + "/comments":
			comment := strings.Replace(validDeletedCommentJSON(), `"body":null`, `"body":"`+server.URL+`"`, 1)
			comment = strings.Replace(comment, `"deleted_at":"`+roastTimestamp2+`"`, `"deleted_at":null`, 1)
			comment = strings.Replace(comment, `"is_deleted":true`, `"is_deleted":false`, 1)
			writeRoastJSON(w, `{"items":[`+comment+`],"next_cursor":null}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "url-data-token", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, failure := client.ListRoasts(context.Background(), RoastListOptions{}); failure != nil {
		t.Fatalf("title containing server URL rejected: %#v", failure)
	}
	if _, failure := client.Roast(context.Background(), roastUUID); failure != nil {
		t.Fatalf("metadata containing server URL rejected: %#v", failure)
	}
	if _, failure := client.RoastComments(context.Background(), roastUUID, PageOptions{}); failure != nil {
		t.Fatalf("comment containing server URL rejected: %#v", failure)
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
