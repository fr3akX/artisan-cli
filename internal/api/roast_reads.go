package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const (
	roastAPIRoot = "/api/v1/roasts"
	// MaxRoastAggregateItems is the finite item ceiling for every roast --all traversal.
	MaxRoastAggregateItems = 10_000
	// MaxRoastAggregatePages bounds requests and cursor memory for every roast --all traversal.
	MaxRoastAggregatePages = 1_000
	maxRoastCursorBytes    = 512
)

// RoastListOptions contains every server-supported private roast list filter.
type RoastListOptions struct {
	Limit       int
	Cursor      string
	Search      string
	RoastAtFrom string
	RoastAtTo   string
	Machine     string
	State       string
	LabelID     string
}

// NormalizeRoastUUID returns the canonical compact lowercase server form.
func NormalizeRoastUUID(raw string) (string, *output.Error) {
	const code = "invalid_roast_uuid"
	const message = "Roast UUID must be compact or standard dashed form"
	if raw != strings.ToLower(raw) {
		return "", inventoryUsageFailure(code, message)
	}
	value, failure := normalizeCompactUUID(raw, code, message)
	if failure != nil {
		return "", failure
	}
	if !validRoastUUID(value) {
		return "", inventoryUsageFailure(code, message)
	}
	return value, nil
}

// ValidateRoastListOptions validates local list flags without making a request.
func ValidateRoastListOptions(options RoastListOptions) *output.Error {
	_, failure := roastListQuery(options)
	return failure
}

func (c *Client) ListRoasts(ctx context.Context, options RoastListOptions) (RoastPage, *output.Error) {
	query, failure := roastListQuery(options)
	if failure != nil {
		return RoastPage{}, failure
	}
	var page RoastPage
	failure = c.doRoastRead(ctx, roastAPIRoot, query, false, nil, &page)
	return page, failure
}

func (c *Client) ListAllRoasts(ctx context.Context, options RoastListOptions) (RoastPage, *output.Error) {
	items, failure := collectRoastPages(options.Cursor, MaxRoastAggregateItems, func(cursor string) ([]RoastListItem, *string, *output.Error) {
		pageOptions := options
		pageOptions.Cursor = cursor
		page, pageFailure := c.ListRoasts(ctx, pageOptions)
		return page.Items, page.NextCursor, pageFailure
	})
	return RoastPage{Items: items, NextCursor: nil}, failure
}

func (c *Client) Roast(ctx context.Context, rawRoastUUID string) (RoastDetail, *output.Error) {
	roastUUID, failure := NormalizeRoastUUID(rawRoastUUID)
	if failure != nil {
		return RoastDetail{}, failure
	}
	var roast RoastDetail
	failure = c.doRoastRead(ctx, roastAPIRoot+"/"+roastUUID, nil, true, nil, &roast)
	if failure == nil && roast.RoastUUID != roastUUID {
		return RoastDetail{}, invalidServerResponse(http.StatusOK)
	}
	return roast, failure
}

func (c *Client) RoastRevisions(ctx context.Context, rawRoastUUID string, options PageOptions) (RoastRevisionPage, *output.Error) {
	roastUUID, failure := NormalizeRoastUUID(rawRoastUUID)
	if failure != nil {
		return RoastRevisionPage{}, failure
	}
	query, failure := roastPageQuery(options)
	if failure != nil {
		return RoastRevisionPage{}, failure
	}
	var page RoastRevisionPage
	failure = c.doRoastRead(ctx, roastAPIRoot+"/"+roastUUID+"/revisions", query, true, roastRevisionHeaderValidator(roastUUID), &page)
	return page, failure
}

func (c *Client) AllRoastRevisions(ctx context.Context, rawRoastUUID string, options PageOptions) (RoastRevisionPage, *output.Error) {
	items, failure := collectRoastPages(options.Cursor, MaxRoastAggregateItems, func(cursor string) ([]RoastRevision, *string, *output.Error) {
		pageOptions := options
		pageOptions.Cursor = cursor
		page, pageFailure := c.RoastRevisions(ctx, rawRoastUUID, pageOptions)
		return page.Items, page.NextCursor, pageFailure
	})
	return RoastRevisionPage{Items: items, NextCursor: nil}, failure
}

func (c *Client) RoastComments(ctx context.Context, rawRoastUUID string, options PageOptions) (CommentPage, *output.Error) {
	roastUUID, failure := NormalizeRoastUUID(rawRoastUUID)
	if failure != nil {
		return CommentPage{}, failure
	}
	query, failure := roastPageQuery(options)
	if failure != nil {
		return CommentPage{}, failure
	}
	var page CommentPage
	failure = c.doRoastRead(ctx, roastAPIRoot+"/"+roastUUID+"/comments", query, true, nil, &page)
	if failure == nil {
		for _, comment := range page.Items {
			if comment.RoastUUID != roastUUID {
				return CommentPage{}, invalidServerResponse(http.StatusOK)
			}
		}
	}
	return page, failure
}

func (c *Client) AllRoastComments(ctx context.Context, rawRoastUUID string, options PageOptions) (CommentPage, *output.Error) {
	items, failure := collectRoastPages(options.Cursor, MaxRoastAggregateItems, func(cursor string) ([]CommentView, *string, *output.Error) {
		pageOptions := options
		pageOptions.Cursor = cursor
		page, pageFailure := c.RoastComments(ctx, rawRoastUUID, pageOptions)
		return page.Items, page.NextCursor, pageFailure
	})
	return CommentPage{Items: items, NextCursor: nil}, failure
}

func (c *Client) doRoastRead(ctx context.Context, path string, query url.Values, preserveEntityNotFound bool, validateResponse ResponseValidator, destination any) *output.Error {
	failure := c.Do(ctx, Request{
		Method: http.MethodGet, Path: path, Query: query, ExpectedStatus: http.StatusOK,
		ValidateResponse: validateResponse,
	}, destination)
	return classifyRoastAPIFailure(failure, preserveEntityNotFound)
}

func classifyRoastAPIFailure(failure *output.Error, preserveEntityNotFound bool) *output.Error {
	if failure == nil || failure.HTTPStatus == nil || *failure.HTTPStatus != http.StatusNotFound {
		return failure
	}
	if preserveEntityNotFound && failure.Code == "not_found" {
		return failure
	}
	return &output.Error{
		ExitCode: 9, Code: "server_upgrade_required",
		Message:    "The server does not provide the roast archive API; upgrade Artisan Server",
		HTTPStatus: statusPointer(http.StatusNotFound),
	}
}

func roastRevisionHeaderValidator(roastUUID string) ResponseValidator {
	return func(_ int, header http.Header) *output.Error {
		roastValues := header.Values("X-Roast-UUID")
		versionValues := header.Values("X-Roast-Revisions-Version")
		if len(roastValues) == 0 && len(versionValues) == 0 {
			return nil
		}
		if len(roastValues) != 1 || roastValues[0] != roastUUID || len(versionValues) != 1 || versionValues[0] != "1" {
			return invalidServerResponse(http.StatusOK)
		}
		return nil
	}
}

func roastListQuery(options RoastListOptions) (url.Values, *output.Error) {
	query, failure := roastPageQuery(PageOptions{Limit: options.Limit, Cursor: options.Cursor})
	if failure != nil {
		return nil, failure
	}
	if options.Search != "" {
		if strings.ContainsRune(options.Search, '\x00') || !validRoastFilterText(options.Search, 200) {
			return nil, invalidRoastFilter()
		}
		query.Set("search", options.Search)
	}
	if options.Machine != "" {
		if !validRoastFilterText(options.Machine, 100) {
			return nil, invalidRoastFilter()
		}
		query.Set("machine", options.Machine)
	}
	if options.State != "" {
		if !oneOf(options.State, "awaiting_profile", "parsed", "parse_failed") {
			return nil, invalidRoastFilter()
		}
		query.Set("state", options.State)
	}
	if options.LabelID != "" {
		labelID, uuidFailure := normalizeCompactUUID(options.LabelID, "invalid_roast_filter", "Roast filters are invalid")
		if uuidFailure != nil {
			return nil, uuidFailure
		}
		if !validRoastUUID(labelID) {
			return nil, invalidRoastFilter()
		}
		query.Set("label_id", labelID)
	}
	from, failure := normalizeRoastFilterTime(options.RoastAtFrom)
	if failure != nil {
		return nil, failure
	}
	to, failure := normalizeRoastFilterTime(options.RoastAtTo)
	if failure != nil {
		return nil, failure
	}
	if from != nil {
		query.Set("roast_at_from", from.UTC().Format(time.RFC3339Nano))
	}
	if to != nil {
		query.Set("roast_at_to", to.UTC().Format(time.RFC3339Nano))
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, invalidRoastFilter()
	}
	return query, nil
}

func roastPageQuery(options PageOptions) (url.Values, *output.Error) {
	query := make(url.Values)
	if options.Limit != 0 {
		if options.Limit < 1 || options.Limit > 100 {
			return nil, invalidRoastFilter()
		}
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.Cursor != "" {
		if len(options.Cursor) > maxRoastCursorBytes {
			return nil, invalidRoastFilter()
		}
		query.Set("cursor", options.Cursor)
	}
	return query, nil
}

func normalizeRoastFilterTime(raw string) (*time.Time, *output.Error) {
	if raw == "" {
		return nil, nil
	}
	if !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return nil, invalidRoastFilter()
	}
	if !validAwareTimestamp(raw) {
		return nil, invalidRoastFilter()
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, invalidRoastFilter()
	}
	return &parsed, nil
}

func validRoastFilterText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func collectRoastPages[T any](initialCursor string, maxItems int, fetch func(string) ([]T, *string, *output.Error)) ([]T, *output.Error) {
	if maxItems <= 0 {
		return nil, invalidRoastFilter()
	}
	items := make([]T, 0)
	seen := make(map[string]struct{}, MaxRoastAggregatePages)
	cursor := initialCursor
	pages := 0
	for {
		if pages >= MaxRoastAggregatePages {
			return nil, &output.Error{ExitCode: 9, Code: "pagination_page_limit_exceeded", Message: "Roast pagination exceeded the 1000 page safety limit"}
		}
		if _, exists := seen[cursor]; exists {
			return nil, &output.Error{ExitCode: 9, Code: "invalid_server_response", Message: "The server repeated a roast pagination cursor"}
		}
		seen[cursor] = struct{}{}
		pages++
		pageItems, nextCursor, failure := fetch(cursor)
		if failure != nil {
			return nil, failure
		}
		if nextCursor != nil && len(pageItems) == 0 {
			return nil, &output.Error{ExitCode: 9, Code: "invalid_server_response", Message: "The server returned an empty roast page with a continuation cursor"}
		}
		if len(pageItems) > maxItems-len(items) {
			return nil, &output.Error{ExitCode: 9, Code: "pagination_limit_exceeded", Message: "Roast pagination exceeded the 10000 item safety limit"}
		}
		items = append(items, pageItems...)
		if nextCursor == nil {
			return items, nil
		}
		cursor = *nextCursor
	}
}

func invalidRoastFilter() *output.Error {
	return &output.Error{ExitCode: 2, Code: "invalid_roast_filter", Message: "Roast filters are invalid"}
}
