package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

const maxInventoryGrams int64 = 2_147_483_647

var (
	canonicalInventoryUUID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	canonicalScore         = regexp.MustCompile(`^(?:0|[1-9][0-9]?|100)\.[0-9]{2}$`)
	canonicalDate          = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// InventoryImage is a private inventory image projection.
type InventoryImage struct {
	ImageID         string  `json:"image_id"`
	Caption         *string `json:"caption"`
	AltText         *string `json:"alt_text"`
	Position        int64   `json:"position"`
	IsCover         bool    `json:"is_cover"`
	DisplayWidth    int64   `json:"display_width"`
	DisplayHeight   int64   `json:"display_height"`
	ThumbnailWidth  int64   `json:"thumbnail_width"`
	ThumbnailHeight int64   `json:"thumbnail_height"`
	DisplayURL      string  `json:"display_url"`
	ThumbnailURL    string  `json:"thumbnail_url"`
}

// BeanLotSummary is the projection returned by lot lists.
type BeanLotSummary struct {
	LotID                   string          `json:"lot_id"`
	Name                    string          `json:"name"`
	Origin                  *string         `json:"origin"`
	ProcessingMethod        *string         `json:"processing_method"`
	CropYear                *int64          `json:"crop_year"`
	State                   string          `json:"state"`
	OnHandGrams             int64           `json:"on_hand_grams"`
	ReservedGrams           int64           `json:"reserved_grams"`
	AvailableGrams          int64           `json:"available_grams"`
	UnresolvedConflictCount int64           `json:"unresolved_conflict_count"`
	CoverImage              *InventoryImage `json:"cover_image"`
	UpdatedAt               string          `json:"updated_at"`
}

// BeanLotLinks contains the admin read routes for a lot.
type BeanLotLinks struct {
	Self         string `json:"self"`
	Ledger       string `json:"ledger"`
	Reservations string `json:"reservations"`
}

// BeanLotDetail is the complete lot read projection.
type BeanLotDetail struct {
	BeanLotSummary
	Producer          *string          `json:"producer"`
	Supplier          *string          `json:"supplier"`
	ExternalReference *string          `json:"external_reference"`
	ReceivedDate      *string          `json:"received_date"`
	Varietals         []string         `json:"varietals"`
	SCAScore          *string          `json:"sca_score"`
	ProcessingDetail  *string          `json:"processing_detail"`
	AltitudeMinMetres *int64           `json:"altitude_min_metres"`
	AltitudeMaxMetres *int64           `json:"altitude_max_metres"`
	Notes             *string          `json:"notes"`
	Images            []InventoryImage `json:"images"`
	CreatedAt         string           `json:"created_at"`
	ArchivedAt        *string          `json:"archived_at"`
	Links             BeanLotLinks     `json:"links"`
}

// InventoryLedgerEntry is an immutable inventory accounting event.
type InventoryLedgerEntry struct {
	EntryID                 string  `json:"entry_id"`
	Operation               string  `json:"operation"`
	LotID                   string  `json:"lot_id"`
	RoastUUID               *string `json:"roast_uuid"`
	ReservationID           *string `json:"reservation_id"`
	OnHandDelta             int64   `json:"on_hand_delta"`
	ReservedDelta           int64   `json:"reserved_delta"`
	ResultingOnHandGrams    int64   `json:"resulting_on_hand_grams"`
	ResultingReservedGrams  int64   `json:"resulting_reserved_grams"`
	ResultingAvailableGrams int64   `json:"resulting_available_grams"`
	Reason                  *string `json:"reason"`
	Reference               *string `json:"reference"`
	ActorKind               string  `json:"actor_kind"`
	OccurredAt              string  `json:"occurred_at"`
	CreatedAt               string  `json:"created_at"`
}

// InventoryReservation is a lot reservation read projection.
type InventoryReservation struct {
	ReservationID         string  `json:"reservation_id"`
	ClientReservationUUID string  `json:"client_reservation_uuid"`
	LotID                 string  `json:"lot_id"`
	RoastUUID             string  `json:"roast_uuid"`
	ClientInstanceUUID    string  `json:"client_instance_uuid"`
	State                 string  `json:"state"`
	PlannedGrams          int64   `json:"planned_grams"`
	ActualGrams           *int64  `json:"actual_grams"`
	ReservedAt            string  `json:"reserved_at"`
	CompletedAt           *string `json:"completed_at"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
	OpenConflictID        *string `json:"open_conflict_id"`
}

// InventoryConflict is an inventory consistency conflict projection.
type InventoryConflict struct {
	ConflictID             string  `json:"conflict_id"`
	LotID                  string  `json:"lot_id"`
	SourceLedgerEntryID    string  `json:"source_ledger_entry_id"`
	RoastUUID              *string `json:"roast_uuid"`
	ReservationID          *string `json:"reservation_id"`
	TriggerOperation       string  `json:"trigger_operation"`
	AvailableGramsSnapshot int64   `json:"available_grams_snapshot"`
	State                  string  `json:"state"`
	ResolutionNote         *string `json:"resolution_note"`
	ResolvedByUserID       *string `json:"resolved_by_user_id"`
	ResolvedAt             *string `json:"resolved_at"`
	CreatedAt              string  `json:"created_at"`
}

// Page response types retain the server's opaque continuation cursor.
type BeanLotPage struct {
	Items      []BeanLotSummary `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

type InventoryLedgerEntryPage struct {
	Items      []InventoryLedgerEntry `json:"items"`
	NextCursor *string                `json:"next_cursor"`
}

type InventoryReservationPage struct {
	Items      []InventoryReservation `json:"items"`
	NextCursor *string                `json:"next_cursor"`
}

type InventoryConflictPage struct {
	Items      []InventoryConflict `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

func (value *InventoryImage) UnmarshalJSON(data []byte) error {
	type wire InventoryImage
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"caption", "alt_text"}, "image_id", "caption", "alt_text", "position", "is_cover", "display_width", "display_height", "thumbnail_width", "thumbnail_height", "display_url", "thumbnail_url"); err != nil {
		return err
	}
	*value = InventoryImage(decoded)
	return value.validate("")
}

func (value *BeanLotSummary) UnmarshalJSON(data []byte) error {
	type wire BeanLotSummary
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"origin", "processing_method", "crop_year", "cover_image"}, "lot_id", "name", "origin", "processing_method", "crop_year", "state", "on_hand_grams", "reserved_grams", "available_grams", "unresolved_conflict_count", "cover_image", "updated_at"); err != nil {
		return err
	}
	*value = BeanLotSummary(decoded)
	return value.validate()
}

func (value *BeanLotLinks) UnmarshalJSON(data []byte) error {
	type wire BeanLotLinks
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, nil, "self", "ledger", "reservations"); err != nil {
		return err
	}
	*value = BeanLotLinks(decoded)
	return nil
}

func (value *BeanLotDetail) UnmarshalJSON(data []byte) error {
	var summary BeanLotSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return err
	}
	type detailFields struct {
		Producer          *string          `json:"producer"`
		Supplier          *string          `json:"supplier"`
		ExternalReference *string          `json:"external_reference"`
		ReceivedDate      *string          `json:"received_date"`
		Varietals         []string         `json:"varietals"`
		SCAScore          *string          `json:"sca_score"`
		ProcessingDetail  *string          `json:"processing_detail"`
		AltitudeMinMetres *int64           `json:"altitude_min_metres"`
		AltitudeMaxMetres *int64           `json:"altitude_max_metres"`
		Notes             *string          `json:"notes"`
		Images            []InventoryImage `json:"images"`
		CreatedAt         string           `json:"created_at"`
		ArchivedAt        *string          `json:"archived_at"`
		Links             BeanLotLinks     `json:"links"`
	}
	var decoded detailFields
	if err := decodeRequiredObject(data, &decoded,
		[]string{"producer", "supplier", "external_reference", "received_date", "sca_score", "processing_detail", "altitude_min_metres", "altitude_max_metres", "notes", "archived_at"},
		"producer", "supplier", "external_reference", "received_date", "varietals", "sca_score", "processing_detail", "altitude_min_metres", "altitude_max_metres", "notes", "images", "created_at", "archived_at", "links"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "varietals", "images"); err != nil {
		return err
	}
	*value = BeanLotDetail{
		BeanLotSummary: summary, Producer: decoded.Producer, Supplier: decoded.Supplier,
		ExternalReference: decoded.ExternalReference, ReceivedDate: decoded.ReceivedDate,
		Varietals: decoded.Varietals, SCAScore: decoded.SCAScore, ProcessingDetail: decoded.ProcessingDetail,
		AltitudeMinMetres: decoded.AltitudeMinMetres, AltitudeMaxMetres: decoded.AltitudeMaxMetres,
		Notes: decoded.Notes, Images: decoded.Images, CreatedAt: decoded.CreatedAt,
		ArchivedAt: decoded.ArchivedAt, Links: decoded.Links,
	}
	return value.validate()
}

func (value *InventoryLedgerEntry) UnmarshalJSON(data []byte) error {
	type wire InventoryLedgerEntry
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"roast_uuid", "reservation_id", "reason", "reference"}, "entry_id", "operation", "lot_id", "roast_uuid", "reservation_id", "on_hand_delta", "reserved_delta", "resulting_on_hand_grams", "resulting_reserved_grams", "resulting_available_grams", "reason", "reference", "actor_kind", "occurred_at", "created_at"); err != nil {
		return err
	}
	*value = InventoryLedgerEntry(decoded)
	return value.validate()
}

func (value *InventoryReservation) UnmarshalJSON(data []byte) error {
	type wire InventoryReservation
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"actual_grams", "completed_at", "open_conflict_id"}, "reservation_id", "client_reservation_uuid", "lot_id", "roast_uuid", "client_instance_uuid", "state", "planned_grams", "actual_grams", "reserved_at", "completed_at", "created_at", "updated_at", "open_conflict_id"); err != nil {
		return err
	}
	*value = InventoryReservation(decoded)
	return value.validate()
}

func (value *InventoryConflict) UnmarshalJSON(data []byte) error {
	type wire InventoryConflict
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"roast_uuid", "reservation_id", "resolution_note", "resolved_by_user_id", "resolved_at"}, "conflict_id", "lot_id", "source_ledger_entry_id", "roast_uuid", "reservation_id", "trigger_operation", "available_grams_snapshot", "state", "resolution_note", "resolved_by_user_id", "resolved_at", "created_at"); err != nil {
		return err
	}
	*value = InventoryConflict(decoded)
	return value.validate()
}

func (value *BeanLotPage) UnmarshalJSON(data []byte) error {
	type wire BeanLotPage
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"next_cursor"}, "items", "next_cursor"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "items"); err != nil {
		return err
	}
	*value = BeanLotPage(decoded)
	return validatePage(value.Items, value.NextCursor)
}

func (value *InventoryLedgerEntryPage) UnmarshalJSON(data []byte) error {
	type wire InventoryLedgerEntryPage
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"next_cursor"}, "items", "next_cursor"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "items"); err != nil {
		return err
	}
	*value = InventoryLedgerEntryPage(decoded)
	return validatePage(value.Items, value.NextCursor)
}

func (value *InventoryReservationPage) UnmarshalJSON(data []byte) error {
	type wire InventoryReservationPage
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"next_cursor"}, "items", "next_cursor"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "items"); err != nil {
		return err
	}
	*value = InventoryReservationPage(decoded)
	return validatePage(value.Items, value.NextCursor)
}

func (value *InventoryConflictPage) UnmarshalJSON(data []byte) error {
	type wire InventoryConflictPage
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"next_cursor"}, "items", "next_cursor"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "items"); err != nil {
		return err
	}
	*value = InventoryConflictPage(decoded)
	return validatePage(value.Items, value.NextCursor)
}

func decodeRequiredObject(data []byte, destination any, nullable []string, required ...string) error {
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return errors.New("invalid inventory object")
	}
	nullableSet := make(map[string]struct{}, len(nullable))
	for _, field := range nullable {
		nullableSet[field] = struct{}{}
	}
	for _, field := range required {
		raw, ok := fields[field]
		if !ok {
			return fmt.Errorf("missing required field %s", field)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if _, allowed := nullableSet[field]; !allowed {
				return fmt.Errorf("required field %s must not be null", field)
			}
		}
	}
	return json.Unmarshal(data, destination)
}

func rejectNullArrayElements(data []byte, names ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range names {
		var elements []json.RawMessage
		if err := json.Unmarshal(fields[name], &elements); err != nil {
			return err
		}
		for _, element := range elements {
			if bytes.Equal(bytes.TrimSpace(element), []byte("null")) {
				return fmt.Errorf("array field %s must not contain null", name)
			}
		}
	}
	return nil
}

func (value InventoryImage) validate(expectedLot string) error {
	if !validUUID(value.ImageID) || value.Position < 0 || value.Position >= 8 || !between(value.DisplayWidth, 1, 12000) || !between(value.DisplayHeight, 1, 12000) || !between(value.ThumbnailWidth, 1, 12000) || !between(value.ThumbnailHeight, 1, 12000) {
		return errors.New("invalid inventory image")
	}
	basePattern := regexp.MustCompile(`^/api/v1/inventory/admin/bean-lots/([0-9a-f]{32})/images/([0-9a-f]{32})/(display|thumbnail)$`)
	display := basePattern.FindStringSubmatch(value.DisplayURL)
	thumbnail := basePattern.FindStringSubmatch(value.ThumbnailURL)
	if len(display) != 4 || len(thumbnail) != 4 || display[3] != "display" || thumbnail[3] != "thumbnail" || display[1] != thumbnail[1] || display[2] != value.ImageID || thumbnail[2] != value.ImageID || !validUUID(display[1]) || (expectedLot != "" && display[1] != expectedLot) {
		return errors.New("invalid inventory image link")
	}
	return nil
}

func (value BeanLotSummary) validate() error {
	if !validUUID(value.LotID) || !oneOf(value.State, "active", "archived") || !validOptionalEnum(value.ProcessingMethod, "washed", "natural", "honey", "pulped-natural", "wet-hulled", "anaerobic", "experimental", "other") || !validGrams(value.OnHandGrams) || !between(value.ReservedGrams, 0, maxInventoryGrams) || !validGrams(value.AvailableGrams) || value.AvailableGrams != value.OnHandGrams-value.ReservedGrams || !between(value.UnresolvedConflictCount, 0, maxInventoryGrams) || !validTimestamp(value.UpdatedAt) {
		return errors.New("invalid bean lot summary")
	}
	if value.CropYear != nil && !between(*value.CropYear, 1000, 9999) {
		return errors.New("invalid crop year")
	}
	if value.CoverImage != nil {
		if !value.CoverImage.IsCover || value.CoverImage.validate(value.LotID) != nil {
			return errors.New("invalid cover image")
		}
	}
	return nil
}

func (value BeanLotDetail) validate() error {
	if err := value.BeanLotSummary.validate(); err != nil {
		return err
	}
	if value.ReceivedDate != nil && !validDate(*value.ReceivedDate) {
		return errors.New("invalid received date")
	}
	if value.SCAScore != nil {
		if !canonicalScore.MatchString(*value.SCAScore) {
			return errors.New("invalid SCA score")
		}
		score, _ := strconv.ParseFloat(*value.SCAScore, 64)
		if score > 100 {
			return errors.New("invalid SCA score")
		}
	}
	if value.AltitudeMinMetres != nil && !between(*value.AltitudeMinMetres, 0, 9000) {
		return errors.New("invalid altitude")
	}
	if value.AltitudeMaxMetres != nil && !between(*value.AltitudeMaxMetres, 0, 9000) {
		return errors.New("invalid altitude")
	}
	if value.Varietals == nil || value.Images == nil || !validTimestamp(value.CreatedAt) || (value.ArchivedAt != nil && !validTimestamp(*value.ArchivedAt)) {
		return errors.New("invalid bean lot detail")
	}
	positions := make(map[int64]struct{}, len(value.Images))
	var detailCover *InventoryImage
	for index := range value.Images {
		image := &value.Images[index]
		if err := image.validate(value.LotID); err != nil {
			return err
		}
		if _, exists := positions[image.Position]; exists {
			return errors.New("duplicate image position")
		}
		positions[image.Position] = struct{}{}
		if image.IsCover {
			if detailCover != nil {
				return errors.New("multiple cover images")
			}
			detailCover = image
		}
	}
	if !sameInventoryImage(value.CoverImage, detailCover) {
		return errors.New("inconsistent cover image")
	}
	base := "/api/v1/inventory/admin/bean-lots/" + value.LotID
	if value.Links.Self != base || value.Links.Ledger != base+"/ledger" || value.Links.Reservations != base+"/reservations" {
		return errors.New("invalid bean lot links")
	}
	return nil
}

func (value InventoryLedgerEntry) validate() error {
	if !validUUID(value.EntryID) || !validUUID(value.LotID) || !validOptionalUUID(value.RoastUUID) || !validOptionalUUID(value.ReservationID) || !oneOf(value.Operation, "opening_balance", "manual_adjustment", "reservation", "reservation_release", "consumption") || !oneOf(value.ActorKind, "browser", "desktop") || !validGrams(value.OnHandDelta) || !validGrams(value.ReservedDelta) || !validGrams(value.ResultingOnHandGrams) || !between(value.ResultingReservedGrams, 0, maxInventoryGrams) || !validGrams(value.ResultingAvailableGrams) || value.ResultingAvailableGrams != value.ResultingOnHandGrams-value.ResultingReservedGrams || !validTimestamp(value.OccurredAt) || !validTimestamp(value.CreatedAt) {
		return errors.New("invalid inventory ledger entry")
	}
	return nil
}

func (value InventoryReservation) validate() error {
	if !validUUID(value.ReservationID) || !validUUID(value.ClientReservationUUID) || !validUUID(value.LotID) || !validUUID(value.RoastUUID) || !validUUID(value.ClientInstanceUUID) || !validOptionalUUID(value.OpenConflictID) || !oneOf(value.State, "reserved", "finalized", "released") || !between(value.PlannedGrams, 1, maxInventoryGrams) || (value.ActualGrams != nil && !between(*value.ActualGrams, 1, maxInventoryGrams)) || !validTimestamp(value.ReservedAt) || !validTimestamp(value.CreatedAt) || !validTimestamp(value.UpdatedAt) || (value.CompletedAt != nil && !validTimestamp(*value.CompletedAt)) {
		return errors.New("invalid inventory reservation")
	}
	switch value.State {
	case "reserved":
		if value.ActualGrams != nil || value.CompletedAt != nil {
			return errors.New("invalid reserved reservation")
		}
	case "finalized":
		if value.ActualGrams == nil || value.CompletedAt == nil {
			return errors.New("invalid finalized reservation")
		}
	case "released":
		if value.ActualGrams != nil || value.CompletedAt == nil {
			return errors.New("invalid released reservation")
		}
	}
	if value.CompletedAt != nil && !timestampNotBefore(*value.CompletedAt, value.ReservedAt) {
		return errors.New("reservation completed before it was reserved")
	}
	return nil
}

func (value InventoryConflict) validate() error {
	if !validUUID(value.ConflictID) || !validUUID(value.LotID) || !validUUID(value.SourceLedgerEntryID) || !validOptionalUUID(value.RoastUUID) || !validOptionalUUID(value.ReservationID) || !validOptionalUUID(value.ResolvedByUserID) || !oneOf(value.TriggerOperation, "opening_balance", "manual_adjustment", "reservation", "reservation_release", "consumption") || !oneOf(value.State, "open", "resolved") || !between(value.AvailableGramsSnapshot, -maxInventoryGrams, -1) || !validTimestamp(value.CreatedAt) || (value.ResolvedAt != nil && !validTimestamp(*value.ResolvedAt)) {
		return errors.New("invalid inventory conflict")
	}
	if value.State == "open" {
		if value.ResolutionNote != nil || value.ResolvedByUserID != nil || value.ResolvedAt != nil {
			return errors.New("invalid open inventory conflict")
		}
		return nil
	}
	if value.ResolutionNote == nil || value.ResolvedByUserID == nil || value.ResolvedAt == nil {
		return errors.New("invalid resolved inventory conflict")
	}
	note, valid := normalizeRequestText(*value.ResolutionNote, 2000, 8000, true, true)
	if !valid || note != *value.ResolutionNote || !timestampNotBefore(*value.ResolvedAt, value.CreatedAt) {
		return errors.New("invalid resolved inventory conflict")
	}
	return nil
}

func validatePage[T any](items []T, next *string) error {
	if items == nil || (next != nil && (*next == "" || len(*next) > 4096)) {
		return errors.New("invalid inventory page")
	}
	return nil
}

func sameInventoryImage(left, right *InventoryImage) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ImageID == right.ImageID && optionalStringsEqual(left.Caption, right.Caption) && optionalStringsEqual(left.AltText, right.AltText) && left.Position == right.Position && left.IsCover == right.IsCover && left.DisplayWidth == right.DisplayWidth && left.DisplayHeight == right.DisplayHeight && left.ThumbnailWidth == right.ThumbnailWidth && left.ThumbnailHeight == right.ThumbnailHeight && left.DisplayURL == right.DisplayURL && left.ThumbnailURL == right.ThumbnailURL
}

func optionalStringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validUUID(value string) bool                { return canonicalInventoryUUID.MatchString(value) }
func validOptionalUUID(value *string) bool       { return value == nil || validUUID(*value) }
func validGrams(value int64) bool                { return between(value, -maxInventoryGrams, maxInventoryGrams) }
func between(value, minimum, maximum int64) bool { return value >= minimum && value <= maximum }
func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
func validOptionalEnum(value *string, allowed ...string) bool {
	return value == nil || oneOf(*value, allowed...)
}
func validTimestamp(value string) bool {
	parsed, err := time.Parse("2006-01-02T15:04:05.000000Z", value)
	return err == nil && parsed.Year() >= 1 && parsed.Format("2006-01-02T15:04:05.000000Z") == value
}
func timestampNotBefore(value, minimum string) bool {
	parsedValue, valueErr := time.Parse("2006-01-02T15:04:05.000000Z", value)
	parsedMinimum, minimumErr := time.Parse("2006-01-02T15:04:05.000000Z", minimum)
	return valueErr == nil && minimumErr == nil && !parsedValue.Before(parsedMinimum)
}

func validDate(value string) bool {
	if !canonicalDate.MatchString(value) {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Year() >= 1 && parsed.Format("2006-01-02") == value
}
