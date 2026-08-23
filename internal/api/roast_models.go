package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxRoastRevisionNumber int64 = 2_147_483_647
	maxRoastDuration       int64 = 7 * 24 * 60 * 60
	maxRoastWeightKG             = 100_000
)

var (
	canonicalRoastUUID = regexp.MustCompile(`^[0-9a-f]{12}[1-8][0-9a-f]{3}[89ab][0-9a-f]{15}$`)
	canonicalSHA256    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	errInvalidRoast    = errors.New("invalid roast response")
)

// RoastLabel is a label assigned to a private roast.
type RoastLabel struct {
	LabelUUID string `json:"label_uuid"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Archived  bool   `json:"archived"`
}

// RoastRevision describes one immutable uploaded roast profile.
type RoastRevision struct {
	RevisionNumber         int64           `json:"revision_number"`
	SHA256                 string          `json:"sha256"`
	ByteSize               int64           `json:"byte_size"`
	ParserVersion          string          `json:"parser_version"`
	ParseState             string          `json:"parse_state"`
	ParseDiagnosticCode    *string         `json:"parse_diagnostic_code"`
	ParseDiagnosticMessage *string         `json:"parse_diagnostic_message"`
	UploadedAt             string          `json:"uploaded_at"`
	Metadata               json.RawMessage `json:"metadata"`
	ReparseRecommended     bool            `json:"reparse_recommended"`
}

// RoastListItem is the bounded private roast list projection.
type RoastListItem struct {
	RoastUUID       string       `json:"roast_uuid"`
	State           string       `json:"state"`
	RoastAt         *string      `json:"roast_at"`
	Title           *string      `json:"title"`
	BatchPrefix     *string      `json:"batch_prefix"`
	BatchNumber     *int64       `json:"batch_number"`
	BatchPosition   *int64       `json:"batch_position"`
	Operator        *string      `json:"operator"`
	Machine         *string      `json:"machine"`
	MachineSetup    *string      `json:"machine_setup"`
	TemperatureUnit *string      `json:"temperature_unit"`
	DurationSeconds *float64     `json:"duration_seconds"`
	GreenWeightKG   *float64     `json:"green_weight_kg"`
	RoastedWeightKG *float64     `json:"roasted_weight_kg"`
	RevisionCount   int64        `json:"revision_count"`
	UpdatedAt       string       `json:"updated_at"`
	Labels          []RoastLabel `json:"labels"`
}

// RoastLinks contains the private archive routes bound to one roast.
type RoastLinks struct {
	Self      string `json:"self"`
	Chart     string `json:"chart"`
	Revisions string `json:"revisions"`
}

// RoastDetail is the complete safe private roast projection.
type RoastDetail struct {
	RoastListItem
	CurrentMetadata json.RawMessage `json:"current_metadata"`
	CurrentRevision *RoastRevision  `json:"current_revision"`
	Links           RoastLinks      `json:"links"`
}

// CommentView is one private roast comment or deleted-comment tombstone.
type CommentView struct {
	CommentUUID    string  `json:"comment_uuid"`
	RoastUUID      string  `json:"roast_uuid"`
	AuthorNickname string  `json:"author_nickname"`
	Body           *string `json:"body"`
	CreatedAt      string  `json:"created_at"`
	EditedAt       *string `json:"edited_at"`
	DeletedAt      *string `json:"deleted_at"`
	IsDeleted      bool    `json:"is_deleted"`
	CanEdit        bool    `json:"can_edit"`
	CanDelete      bool    `json:"can_delete"`
}

// RoastPage, RoastRevisionPage, and CommentPage retain opaque cursors.
type RoastPage struct {
	Items      []RoastListItem `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

type RoastRevisionPage struct {
	Items      []RoastRevision `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

type CommentPage struct {
	Items      []CommentView `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

func (value *RoastLabel) UnmarshalJSON(data []byte) error {
	type wire RoastLabel
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, nil, "label_uuid", "name", "color", "archived"); err != nil {
		return err
	}
	*value = RoastLabel(decoded)
	return value.validate()
}

func (value *RoastRevision) UnmarshalJSON(data []byte) error {
	type wire RoastRevision
	var decoded wire
	if err := decodeRequiredObject(data, &decoded,
		[]string{"parse_diagnostic_code", "parse_diagnostic_message"},
		"revision_number", "sha256", "byte_size", "parser_version", "parse_state",
		"parse_diagnostic_code", "parse_diagnostic_message", "uploaded_at", "metadata",
		"reparse_recommended"); err != nil {
		return err
	}
	if !isJSONObject(decoded.Metadata) {
		return errInvalidRoast
	}
	*value = RoastRevision(decoded)
	return value.validate()
}

func (value *RoastListItem) UnmarshalJSON(data []byte) error {
	type wire RoastListItem
	var decoded wire
	if err := decodeRequiredObject(data, &decoded,
		[]string{"roast_at", "title", "batch_prefix", "batch_number", "batch_position", "operator", "machine", "machine_setup", "temperature_unit", "duration_seconds", "green_weight_kg", "roasted_weight_kg"},
		"roast_uuid", "state", "roast_at", "title", "batch_prefix", "batch_number",
		"batch_position", "operator", "machine", "machine_setup", "temperature_unit",
		"duration_seconds", "green_weight_kg", "roasted_weight_kg", "revision_count",
		"updated_at", "labels"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "labels"); err != nil {
		return err
	}
	*value = RoastListItem(decoded)
	return value.validate()
}

func (value *RoastLinks) UnmarshalJSON(data []byte) error {
	type wire RoastLinks
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, nil, "self", "chart", "revisions"); err != nil {
		return err
	}
	*value = RoastLinks(decoded)
	return nil
}

func (value *RoastDetail) UnmarshalJSON(data []byte) error {
	var summary RoastListItem
	if err := json.Unmarshal(data, &summary); err != nil {
		return err
	}
	type detailFields struct {
		CurrentMetadata json.RawMessage `json:"current_metadata"`
		CurrentRevision *RoastRevision  `json:"current_revision"`
		Links           RoastLinks      `json:"links"`
	}
	var decoded detailFields
	if err := decodeRequiredObject(data, &decoded, []string{"current_revision"}, "current_metadata", "current_revision", "links"); err != nil {
		return err
	}
	if !isJSONObject(decoded.CurrentMetadata) {
		return errInvalidRoast
	}
	*value = RoastDetail{RoastListItem: summary, CurrentMetadata: decoded.CurrentMetadata, CurrentRevision: decoded.CurrentRevision, Links: decoded.Links}
	return value.validate()
}

func (value *CommentView) UnmarshalJSON(data []byte) error {
	type wire CommentView
	var decoded wire
	if err := decodeRequiredObject(data, &decoded,
		[]string{"body", "edited_at", "deleted_at"},
		"comment_uuid", "roast_uuid", "author_nickname", "body", "created_at", "edited_at",
		"deleted_at", "is_deleted", "can_edit", "can_delete"); err != nil {
		return err
	}
	*value = CommentView(decoded)
	return value.validate()
}

func (value *RoastPage) UnmarshalJSON(data []byte) error {
	type wire RoastPage
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"next_cursor"}, "items", "next_cursor"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "items"); err != nil {
		return err
	}
	*value = RoastPage(decoded)
	return validateRoastPage(value.Items, value.NextCursor)
}

func (value *RoastRevisionPage) UnmarshalJSON(data []byte) error {
	type wire RoastRevisionPage
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"next_cursor"}, "items", "next_cursor"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "items"); err != nil {
		return err
	}
	*value = RoastRevisionPage(decoded)
	return validateRoastPage(value.Items, value.NextCursor)
}

func (value *CommentPage) UnmarshalJSON(data []byte) error {
	type wire CommentPage
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"next_cursor"}, "items", "next_cursor"); err != nil {
		return err
	}
	if err := rejectNullArrayElements(data, "items"); err != nil {
		return err
	}
	*value = CommentPage(decoded)
	return validateRoastPage(value.Items, value.NextCursor)
}

func (value RoastLabel) validate() error {
	name, ok := normalizeRequestText(value.Name, 40, 160, true, false)
	if !validRoastUUID(value.LabelUUID) || !ok || name != value.Name || !oneOf(value.Color, "slate", "red", "orange", "amber", "green", "teal", "blue", "violet") {
		return errInvalidRoast
	}
	return nil
}

func (value RoastRevision) validate() error {
	if !between(value.RevisionNumber, 1, maxRoastRevisionNumber) || !canonicalSHA256.MatchString(value.SHA256) || value.ByteSize <= 0 || !validBoundedString(value.ParserVersion, 1, 64, false) || !oneOf(value.ParseState, "parsed", "failed") || !validOptionalBoundedString(value.ParseDiagnosticCode, 100, false) || !validOptionalBoundedString(value.ParseDiagnosticMessage, 500, false) || !validAwareTimestamp(value.UploadedAt) || !isJSONObject(value.Metadata) {
		return errInvalidRoast
	}
	return nil
}

func (value RoastListItem) validate() error {
	if !validRoastUUID(value.RoastUUID) || !oneOf(value.State, "awaiting_profile", "parsed", "parse_failed") || !validOptionalAwareTimestamp(value.RoastAt) || !validResponseOptionalText(value.Title, 255, 1020, false) || !validResponseOptionalText(value.BatchPrefix, 100, 400, false) || !validOptionalInt(value.BatchNumber, 0, maxRoastRevisionNumber) || !validOptionalInt(value.BatchPosition, 0, maxRoastRevisionNumber) || !validResponseOptionalText(value.Operator, 100, 400, false) || !validResponseOptionalText(value.Machine, 100, 400, false) || !validResponseOptionalText(value.MachineSetup, 200, 800, false) || !validOptionalEnum(value.TemperatureUnit, "C", "F") || !validOptionalFloat(value.DurationSeconds, float64(maxRoastDuration)) || !validOptionalFloat(value.GreenWeightKG, maxRoastWeightKG) || !validOptionalFloat(value.RoastedWeightKG, maxRoastWeightKG) || !between(value.RevisionCount, 0, maxRoastRevisionNumber) || !validAwareTimestamp(value.UpdatedAt) || value.Labels == nil {
		return errInvalidRoast
	}
	if (value.State == "awaiting_profile") != (value.RevisionCount == 0) {
		return errInvalidRoast
	}
	seenLabels := make(map[string]struct{}, len(value.Labels))
	for _, label := range value.Labels {
		if err := label.validate(); err != nil {
			return err
		}
		if _, exists := seenLabels[label.LabelUUID]; exists {
			return errInvalidRoast
		}
		seenLabels[label.LabelUUID] = struct{}{}
	}
	return nil
}

func (value RoastDetail) validate() error {
	if err := value.RoastListItem.validate(); err != nil {
		return err
	}
	if !isJSONObject(value.CurrentMetadata) {
		return errInvalidRoast
	}
	base := "/api/v1/roasts/" + value.RoastUUID
	if value.Links.Self != base || value.Links.Chart != base+"/chart" || value.Links.Revisions != base+"/revisions" {
		return errInvalidRoast
	}
	switch value.State {
	case "awaiting_profile":
		if value.RevisionCount != 0 || value.CurrentRevision != nil {
			return errInvalidRoast
		}
	case "parsed", "parse_failed":
		if value.RevisionCount < 1 || value.CurrentRevision == nil ||
			value.CurrentRevision.RevisionNumber != value.RevisionCount {
			return errInvalidRoast
		}
	default:
		return errInvalidRoast
	}
	return nil
}

func (value CommentView) validate() error {
	if !validRoastUUID(value.CommentUUID) || !validRoastUUID(value.RoastUUID) || !validBoundedString(value.AuthorNickname, 1, 100, false) || !validAwareTimestamp(value.CreatedAt) || !validOptionalAwareTimestamp(value.EditedAt) || !validOptionalAwareTimestamp(value.DeletedAt) {
		return errInvalidRoast
	}
	if value.EditedAt != nil && !awareTimestampNotBefore(*value.EditedAt, value.CreatedAt) {
		return errInvalidRoast
	}
	if value.DeletedAt != nil && (!awareTimestampNotBefore(*value.DeletedAt, value.CreatedAt) || (value.EditedAt != nil && !awareTimestampNotBefore(*value.DeletedAt, *value.EditedAt))) {
		return errInvalidRoast
	}
	if value.IsDeleted {
		if value.Body != nil || value.DeletedAt == nil || value.CanEdit {
			return errInvalidRoast
		}
	} else {
		if value.Body == nil || value.DeletedAt != nil || !validCommentBody(*value.Body) || value.CanEdit != value.CanDelete {
			return errInvalidRoast
		}
	}
	if value.CanEdit && !value.CanDelete {
		return errInvalidRoast
	}
	return nil
}

func validateRoastPage[T any](items []T, next *string) error {
	if items == nil || (next != nil && (*next == "" || len(*next) > 4096)) {
		return errInvalidRoast
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 || !utf8.Valid(raw) || validateJSONStringSurrogateEscapes(raw) != nil {
		return false
	}
	var object map[string]json.RawMessage
	return decodeOneJSON(raw, &object) == nil && object != nil && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{"))
}

func validAwareTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Year() < 1 {
		return false
	}
	// RFC3339 requires an explicit offset or Z. This check also prevents a
	// future parser relaxation from accepting timezone-naive values.
	separator := strings.LastIndexAny(value, "Zz+-")
	return separator > strings.IndexByte(value, 'T')
}

func validOptionalAwareTimestamp(value *string) bool {
	return value == nil || validAwareTimestamp(*value)
}

func awareTimestampNotBefore(value, minimum string) bool {
	parsedValue, valueErr := time.Parse(time.RFC3339Nano, value)
	parsedMinimum, minimumErr := time.Parse(time.RFC3339Nano, minimum)
	return valueErr == nil && minimumErr == nil && !parsedValue.Before(parsedMinimum)
}

func validOptionalInt(value *int64, minimum, maximum int64) bool {
	return value == nil || between(*value, minimum, maximum)
}

func validOptionalFloat(value *float64, maximum float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= maximum)
}

func validBoundedString(value string, minimum, maximum int, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < minimum || utf8.RuneCountInString(value) > maximum || len(value) > maximum*4 {
		return false
	}
	for _, character := range value {
		if character == 0x7f || (character < 0x20 && (!multiline || character != '\n')) {
			return false
		}
	}
	return true
}

func validOptionalBoundedString(value *string, maximum int, multiline bool) bool {
	return value == nil || validBoundedString(*value, 0, maximum, multiline)
}

func validCommentBody(value string) bool {
	return utf8.ValidString(value) && value != "" && utf8.RuneCountInString(value) <= 4000 &&
		len(value) <= 16000 && !strings.ContainsRune(value, '\x00') && strings.TrimSpace(value) == value
}

func validRoastUUID(value string) bool {
	return canonicalRoastUUID.MatchString(value)
}
