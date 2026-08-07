package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const (
	inventoryAdminRoot = "/api/v1/inventory/admin"
	// MaxInventoryAggregateItems is the finite item ceiling for every --all traversal.
	MaxInventoryAggregateItems = 10_000
)

// LotListOptions contains every server-supported bean-lot list filter.
type LotListOptions struct {
	Limit        int
	Cursor       string
	Query        string
	State        string
	Availability string
	Conflict     string
	RoastUUID    string
}

// PageOptions controls a history list page.
type PageOptions struct {
	Limit  int
	Cursor string
}

func (c *Client) ListBeanLots(ctx context.Context, options LotListOptions) (BeanLotPage, *output.Error) {
	query, failure := lotListQuery(options)
	if failure != nil {
		return BeanLotPage{}, failure
	}
	var page BeanLotPage
	failure = c.doInventoryRead(ctx, inventoryAdminRoot+"/bean-lots", query, false, &page)
	return page, failure
}

func (c *Client) ListAllBeanLots(ctx context.Context, options LotListOptions) (BeanLotPage, *output.Error) {
	items, failure := collectInventoryPages(options.Cursor, MaxInventoryAggregateItems, func(cursor string) ([]BeanLotSummary, *string, *output.Error) {
		pageOptions := options
		pageOptions.Cursor = cursor
		page, pageFailure := c.ListBeanLots(ctx, pageOptions)
		return page.Items, page.NextCursor, pageFailure
	})
	return BeanLotPage{Items: items, NextCursor: nil}, failure
}

func (c *Client) BeanLot(ctx context.Context, rawLotID string) (BeanLotDetail, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	var lot BeanLotDetail
	failure = c.doInventoryRead(ctx, inventoryAdminRoot+"/bean-lots/"+lotID, nil, true, &lot)
	return lot, failure
}

func (c *Client) BeanLotLedger(ctx context.Context, rawLotID string, options PageOptions) (InventoryLedgerEntryPage, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return InventoryLedgerEntryPage{}, failure
	}
	query, failure := pageQuery(options)
	if failure != nil {
		return InventoryLedgerEntryPage{}, failure
	}
	var page InventoryLedgerEntryPage
	failure = c.doInventoryRead(ctx, inventoryAdminRoot+"/bean-lots/"+lotID+"/ledger", query, true, &page)
	return page, failure
}

func (c *Client) AllBeanLotLedger(ctx context.Context, rawLotID string, options PageOptions) (InventoryLedgerEntryPage, *output.Error) {
	items, failure := collectInventoryPages(options.Cursor, MaxInventoryAggregateItems, func(cursor string) ([]InventoryLedgerEntry, *string, *output.Error) {
		pageOptions := options
		pageOptions.Cursor = cursor
		page, pageFailure := c.BeanLotLedger(ctx, rawLotID, pageOptions)
		return page.Items, page.NextCursor, pageFailure
	})
	return InventoryLedgerEntryPage{Items: items, NextCursor: nil}, failure
}

func (c *Client) BeanLotReservations(ctx context.Context, rawLotID string, options PageOptions) (InventoryReservationPage, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return InventoryReservationPage{}, failure
	}
	query, failure := pageQuery(options)
	if failure != nil {
		return InventoryReservationPage{}, failure
	}
	var page InventoryReservationPage
	failure = c.doInventoryRead(ctx, inventoryAdminRoot+"/bean-lots/"+lotID+"/reservations", query, true, &page)
	return page, failure
}

func (c *Client) AllBeanLotReservations(ctx context.Context, rawLotID string, options PageOptions) (InventoryReservationPage, *output.Error) {
	items, failure := collectInventoryPages(options.Cursor, MaxInventoryAggregateItems, func(cursor string) ([]InventoryReservation, *string, *output.Error) {
		pageOptions := options
		pageOptions.Cursor = cursor
		page, pageFailure := c.BeanLotReservations(ctx, rawLotID, pageOptions)
		return page.Items, page.NextCursor, pageFailure
	})
	return InventoryReservationPage{Items: items, NextCursor: nil}, failure
}

func (c *Client) BeanLotConflicts(ctx context.Context, rawLotID string, options PageOptions) (InventoryConflictPage, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return InventoryConflictPage{}, failure
	}
	query, failure := pageQuery(options)
	if failure != nil {
		return InventoryConflictPage{}, failure
	}
	var page InventoryConflictPage
	failure = c.doInventoryRead(ctx, inventoryAdminRoot+"/bean-lots/"+lotID+"/conflicts", query, true, &page)
	return page, failure
}

func (c *Client) AllBeanLotConflicts(ctx context.Context, rawLotID string, options PageOptions) (InventoryConflictPage, *output.Error) {
	items, failure := collectInventoryPages(options.Cursor, MaxInventoryAggregateItems, func(cursor string) ([]InventoryConflict, *string, *output.Error) {
		pageOptions := options
		pageOptions.Cursor = cursor
		page, pageFailure := c.BeanLotConflicts(ctx, rawLotID, pageOptions)
		return page.Items, page.NextCursor, pageFailure
	})
	return InventoryConflictPage{Items: items, NextCursor: nil}, failure
}

func (c *Client) InventoryConflict(ctx context.Context, rawConflictID string) (InventoryConflict, *output.Error) {
	conflictID, failure := normalizeInventoryUUID(rawConflictID)
	if failure != nil {
		return InventoryConflict{}, failure
	}
	var conflict InventoryConflict
	failure = c.doInventoryRead(ctx, inventoryAdminRoot+"/conflicts/"+conflictID, nil, true, &conflict)
	return conflict, failure
}

func (c *Client) doInventoryRead(ctx context.Context, path string, query url.Values, entityRoute bool, destination any) *output.Error {
	failure := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: query}, destination)
	if failure == nil || failure.HTTPStatus == nil || *failure.HTTPStatus != http.StatusNotFound {
		return failure
	}
	if entityRoute && (failure.Code == "bean_lot_not_found" || failure.Code == "inventory_conflict_not_found") {
		return failure
	}
	return &output.Error{
		ExitCode:   9,
		Code:       "server_upgrade_required",
		Message:    "The server does not provide the inventory administration API; upgrade Artisan Server",
		HTTPStatus: statusPointer(http.StatusNotFound),
	}
}

// ValidateLotListOptions validates local flags without making a request.
func ValidateLotListOptions(options LotListOptions) *output.Error {
	_, failure := lotListQuery(options)
	return failure
}

// ValidatePageOptions validates local pagination flags without making a request.
func ValidatePageOptions(options PageOptions) *output.Error {
	_, failure := pageQuery(options)
	return failure
}

// NormalizeInventoryUUID returns the canonical compact lowercase server form.
func NormalizeInventoryUUID(raw string) (string, *output.Error) {
	return normalizeInventoryUUID(raw)
}

func lotListQuery(options LotListOptions) (url.Values, *output.Error) {
	query, failure := pageQuery(PageOptions{Limit: options.Limit, Cursor: options.Cursor})
	if failure != nil {
		return nil, failure
	}
	if options.Query != "" {
		query.Set("q", options.Query)
	}
	if options.State != "" {
		if !oneOf(options.State, "active", "archived") {
			return nil, inventoryUsageFailure("invalid_state", "State must be active or archived")
		}
		query.Set("state", options.State)
	}
	if options.Availability != "" {
		if !oneOf(options.Availability, "positive", "zero", "negative") {
			return nil, inventoryUsageFailure("invalid_availability", "Availability must be positive, zero, or negative")
		}
		query.Set("availability", options.Availability)
	}
	if options.Conflict != "" {
		if !oneOf(options.Conflict, "open", "none") {
			return nil, inventoryUsageFailure("invalid_conflict_filter", "Conflict must be open or none")
		}
		query.Set("conflict", options.Conflict)
	}
	if options.RoastUUID != "" {
		roastUUID, uuidFailure := normalizeInventoryUUID(options.RoastUUID)
		if uuidFailure != nil {
			return nil, uuidFailure
		}
		query.Set("roast_uuid", roastUUID)
	}
	return query, nil
}

func pageQuery(options PageOptions) (url.Values, *output.Error) {
	query := make(url.Values)
	if options.Limit != 0 {
		if options.Limit < 1 || options.Limit > 100 {
			return nil, inventoryUsageFailure("invalid_limit", "Limit must be between 1 and 100")
		}
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.Cursor != "" {
		if len(options.Cursor) > 4096 {
			return nil, inventoryUsageFailure("invalid_cursor", "Cursor must be between 1 and 4096 bytes")
		}
		query.Set("cursor", options.Cursor)
	}
	return query, nil
}

func normalizeInventoryUUID(raw string) (string, *output.Error) {
	value := raw
	if len(value) == 36 {
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return "", inventoryUsageFailure("invalid_uuid", "UUID must be compact or standard dashed form")
		}
		value = value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:]
		value = strings.ToLower(value)
	}
	if !canonicalInventoryUUID.MatchString(value) {
		return "", inventoryUsageFailure("invalid_uuid", "UUID must be compact lowercase or standard dashed form")
	}
	return value, nil
}

func collectInventoryPages[T any](initialCursor string, maxItems int, fetch func(string) ([]T, *string, *output.Error)) ([]T, *output.Error) {
	if maxItems <= 0 {
		return nil, inventoryUsageFailure("invalid_pagination_limit", "Pagination limit must be positive")
	}
	items := make([]T, 0)
	seen := make(map[string]struct{})
	cursor := initialCursor
	for {
		if _, exists := seen[cursor]; exists {
			return nil, &output.Error{ExitCode: 9, Code: "invalid_server_response", Message: "The server repeated an inventory pagination cursor"}
		}
		seen[cursor] = struct{}{}
		pageItems, nextCursor, failure := fetch(cursor)
		if failure != nil {
			return nil, failure
		}
		if len(pageItems) > maxItems-len(items) {
			return nil, &output.Error{ExitCode: 9, Code: "pagination_limit_exceeded", Message: "Inventory pagination exceeded the 10000 item safety limit"}
		}
		items = append(items, pageItems...)
		if nextCursor == nil {
			return items, nil
		}
		cursor = *nextCursor
	}
}

func inventoryUsageFailure(code, message string) *output.Error {
	return &output.Error{ExitCode: 2, Code: code, Message: message}
}
