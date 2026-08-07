package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fr3akX/artisan-cli/internal/output"
)

// BeanLotFields is the exact metadata object nested in BeanLotCreateManifest.
type BeanLotFields struct {
	Name              string   `json:"name"`
	Origin            *string  `json:"origin"`
	Producer          *string  `json:"producer"`
	Supplier          *string  `json:"supplier"`
	ExternalReference *string  `json:"external_reference"`
	ReceivedDate      *string  `json:"received_date"`
	CropYear          *int64   `json:"crop_year"`
	Varietals         []string `json:"varietals"`
	SCAScore          *string  `json:"sca_score"`
	ProcessingMethod  *string  `json:"processing_method"`
	ProcessingDetail  *string  `json:"processing_detail"`
	AltitudeMinMetres *int64   `json:"altitude_min_metres"`
	AltitudeMaxMetres *int64   `json:"altitude_max_metres"`
	Notes             *string  `json:"notes"`
}

// ImageUploadManifest reserves the create seam extended with file bodies in Task 8.
type ImageUploadManifest struct {
	UploadIndex int64   `json:"upload_index"`
	Caption     *string `json:"caption"`
	AltText     *string `json:"alt_text"`
	IsCover     bool    `json:"is_cover"`
}

// BeanLotCreateManifest is the strict server create manifest.
type BeanLotCreateManifest struct {
	Fields           BeanLotFields         `json:"fields"`
	OpeningGrams     int64                 `json:"opening_grams"`
	OpeningReason    *string               `json:"opening_reason"`
	OpeningReference *string               `json:"opening_reference"`
	Images           []ImageUploadManifest `json:"images"`
}

// BeanLotPatch retains field presence, including explicit JSON null clears.
type BeanLotPatch struct {
	fields map[string]any
}

func (fields *BeanLotFields) UnmarshalJSON(data []byte) error {
	type wire struct {
		Name              string          `json:"name"`
		Origin            *string         `json:"origin"`
		Producer          *string         `json:"producer"`
		Supplier          *string         `json:"supplier"`
		ExternalReference *string         `json:"external_reference"`
		ReceivedDate      *string         `json:"received_date"`
		CropYear          *int64          `json:"crop_year"`
		Varietals         []string        `json:"varietals"`
		SCAScore          json.RawMessage `json:"sca_score"`
		ProcessingMethod  *string         `json:"processing_method"`
		ProcessingDetail  *string         `json:"processing_detail"`
		AltitudeMinMetres *int64          `json:"altitude_min_metres"`
		AltitudeMaxMetres *int64          `json:"altitude_max_metres"`
		Notes             *string         `json:"notes"`
	}
	var decoded wire
	if err := decodeStrictMutationJSON(data, &decoded); err != nil {
		return err
	}
	var score *string
	if len(decoded.SCAScore) != 0 && !bytes.Equal(bytes.TrimSpace(decoded.SCAScore), []byte("null")) {
		canonical, ok := canonicalScoreJSON(decoded.SCAScore)
		if !ok {
			return errors.New("invalid SCA score")
		}
		score = &canonical
	}
	*fields = BeanLotFields{
		Name: decoded.Name, Origin: decoded.Origin, Producer: decoded.Producer, Supplier: decoded.Supplier,
		ExternalReference: decoded.ExternalReference, ReceivedDate: decoded.ReceivedDate, CropYear: decoded.CropYear,
		Varietals: decoded.Varietals, SCAScore: score, ProcessingMethod: decoded.ProcessingMethod,
		ProcessingDetail: decoded.ProcessingDetail, AltitudeMinMetres: decoded.AltitudeMinMetres,
		AltitudeMaxMetres: decoded.AltitudeMaxMetres, Notes: decoded.Notes,
	}
	return nil
}

// InventoryAdjustmentWrite is the strict manual adjustment request.
type InventoryAdjustmentWrite struct {
	QuantityGrams int64   `json:"quantity_grams"`
	Reason        string  `json:"reason"`
	Reference     *string `json:"reference"`
	OccurredAt    string  `json:"occurred_at"`
}

func (patch BeanLotPatch) MarshalJSON() ([]byte, error) { return json.Marshal(patch.fields) }

// HasField reports whether a sparse patch explicitly contains a field.
func (patch BeanLotPatch) HasField(name string) bool {
	_, exists := patch.fields[name]
	return exists
}

// NewBeanLotPatch validates a sparse patch and preserves explicit nulls.
func NewBeanLotPatch(fields map[string]any) (BeanLotPatch, *output.Error) {
	if len(fields) == 0 {
		return BeanLotPatch{}, mutationUsage("invalid_patch", "Bean lot update requires at least one field")
	}
	allowed := map[string]string{
		"name": "string", "origin": "nullable-string", "producer": "nullable-string", "supplier": "nullable-string",
		"external_reference": "nullable-string", "received_date": "date", "crop_year": "integer", "varietals": "varietals",
		"sca_score": "score", "processing_method": "processing", "processing_detail": "nullable-string",
		"altitude_min_metres": "altitude", "altitude_max_metres": "altitude", "notes": "nullable-string", "state": "state",
	}
	canonical := make(map[string]any, len(fields))
	for name, value := range fields {
		kind, ok := allowed[name]
		if !ok {
			return BeanLotPatch{}, mutationUsage("invalid_patch_field", "Bean lot update contains an unknown field")
		}
		converted, ok := canonicalPatchValue(name, kind, value)
		if !ok {
			return BeanLotPatch{}, mutationUsage("invalid_patch_value", "Bean lot update contains an invalid field value")
		}
		canonical[name] = converted
	}
	if value, exists := canonical["name"]; exists && value == nil {
		return BeanLotPatch{}, mutationUsage("invalid_patch_value", "Bean lot name cannot be cleared")
	}
	if value, exists := canonical["state"]; exists && value == nil {
		return BeanLotPatch{}, mutationUsage("invalid_patch_value", "Bean lot state cannot be cleared")
	}
	if method, hasMethod := canonical["processing_method"]; hasMethod {
		if detail, hasDetail := canonical["processing_detail"]; hasDetail && method == "other" && detail == nil {
			return BeanLotPatch{}, mutationUsage("invalid_processing", "Processing detail is required when processing method is other")
		}
	}
	if minimum, hasMinimum := nullableInt(canonical, "altitude_min_metres"); hasMinimum {
		if maximum, hasMaximum := nullableInt(canonical, "altitude_max_metres"); hasMaximum && minimum != nil && maximum != nil && *minimum > *maximum {
			return BeanLotPatch{}, mutationUsage("invalid_altitude", "Altitude minimum must not exceed altitude maximum")
		}
	}
	return BeanLotPatch{fields: canonical}, nil
}

// ValidateBeanLotCreateManifest validates locally knowable strict server rules.
func ValidateBeanLotCreateManifest(manifest BeanLotCreateManifest) *output.Error {
	if !validRequestText(manifest.Fields.Name, 200, 800, true, false) {
		return mutationUsage("invalid_name", "Bean lot name is required and must be valid")
	}
	for value, limits := range map[*string][2]int{
		manifest.Fields.Origin: {100, 400}, manifest.Fields.Producer: {200, 800}, manifest.Fields.Supplier: {200, 800},
		manifest.Fields.ExternalReference: {200, 800}, manifest.Fields.ProcessingDetail: {200, 800}, manifest.OpeningReference: {200, 800},
	} {
		if value != nil && !validRequestText(*value, limits[0], limits[1], false, false) {
			return mutationUsage("invalid_text", "Bean lot text field is invalid")
		}
	}
	for value, limits := range map[*string][2]int{manifest.Fields.Notes: {10000, 40000}, manifest.OpeningReason: {2000, 8000}} {
		if value != nil && !validRequestText(*value, limits[0], limits[1], false, true) {
			return mutationUsage("invalid_text", "Bean lot multiline text field is invalid")
		}
	}
	if manifest.Fields.ReceivedDate != nil && !validDate(*manifest.Fields.ReceivedDate) {
		return mutationUsage("invalid_date", "Received date must use YYYY-MM-DD")
	}
	if manifest.Fields.CropYear != nil && !between(*manifest.Fields.CropYear, 1000, 9999) {
		return mutationUsage("invalid_crop_year", "Crop year must be between 1000 and 9999")
	}
	if manifest.Fields.SCAScore != nil && !validRequestScore(*manifest.Fields.SCAScore) {
		return mutationUsage("invalid_sca_score", "SCA score must be between 0.00 and 100.00 with two decimal places")
	}
	if !validOptionalEnum(manifest.Fields.ProcessingMethod, "washed", "natural", "honey", "pulped-natural", "wet-hulled", "anaerobic", "experimental", "other") {
		return mutationUsage("invalid_processing", "Processing method is invalid")
	}
	if manifest.Fields.ProcessingMethod != nil && *manifest.Fields.ProcessingMethod == "other" && manifest.Fields.ProcessingDetail == nil {
		return mutationUsage("invalid_processing", "Processing detail is required when processing method is other")
	}
	if !validAltitude(manifest.Fields.AltitudeMinMetres) || !validAltitude(manifest.Fields.AltitudeMaxMetres) || (manifest.Fields.AltitudeMinMetres != nil && manifest.Fields.AltitudeMaxMetres != nil && *manifest.Fields.AltitudeMinMetres > *manifest.Fields.AltitudeMaxMetres) {
		return mutationUsage("invalid_altitude", "Altitude must be between 0 and 9000 and minimum must not exceed maximum")
	}
	if manifest.Fields.Varietals == nil {
		manifest.Fields.Varietals = []string{}
	}
	if len(manifest.Fields.Varietals) > 16 {
		return mutationUsage("invalid_varietals", "At most 16 varietals may be supplied")
	}
	seen := make(map[string]struct{}, len(manifest.Fields.Varietals))
	for _, varietal := range manifest.Fields.Varietals {
		if !validRequestText(varietal, 100, 400, true, false) {
			return mutationUsage("invalid_varietals", "Varietals must be nonblank valid text")
		}
		identity := strings.TrimSpace(varietal)
		if _, exists := seen[identity]; exists {
			return mutationUsage("invalid_varietals", "Varietals must be unique")
		}
		seen[identity] = struct{}{}
	}
	if !between(manifest.OpeningGrams, 0, maxInventoryGrams) || (manifest.OpeningGrams > 0 && (manifest.OpeningReason == nil || strings.TrimSpace(*manifest.OpeningReason) == "")) {
		return mutationUsage("invalid_opening_balance", "Positive opening grams require an opening reason")
	}
	if manifest.Images == nil || len(manifest.Images) != 0 {
		return mutationUsage("invalid_images", "Lot creation supports zero images at this command boundary")
	}
	return nil
}

// ValidateInventoryAdjustment validates exact integer grams and canonical time.
func ValidateInventoryAdjustment(adjustment InventoryAdjustmentWrite) *output.Error {
	if adjustment.QuantityGrams == 0 || !validGrams(adjustment.QuantityGrams) {
		return mutationUsage("invalid_grams", "Adjustment grams must be a nonzero integer within the supported range")
	}
	if !validRequestText(adjustment.Reason, 2000, 8000, true, true) {
		return mutationUsage("invalid_reason", "Adjustment reason must be nonblank valid text")
	}
	if adjustment.Reference != nil && !validRequestText(*adjustment.Reference, 200, 800, false, false) {
		return mutationUsage("invalid_reference", "Adjustment reference is invalid")
	}
	if !validTimestamp(adjustment.OccurredAt) {
		return mutationUsage("invalid_timestamp", "Occurred-at must use the canonical UTC timestamp format")
	}
	return nil
}

func (c *Client) CreateBeanLot(ctx context.Context, manifest BeanLotCreateManifest, key string) (BeanLotDetail, *output.Error) {
	if manifest.Fields.Varietals == nil {
		manifest.Fields.Varietals = []string{}
	}
	if manifest.Images == nil {
		manifest.Images = []ImageUploadManifest{}
	}
	if failure := ValidateBeanLotCreateManifest(manifest); failure != nil {
		return BeanLotDetail{}, failure
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return BeanLotDetail{}, mutationUsage("invalid_manifest", "Unable to encode bean lot manifest")
	}
	body, err := NewManifestMultipartBody(encoded)
	if err != nil {
		return BeanLotDetail{}, &output.Error{ExitCode: 1, Code: "request_body_error", Message: "Unable to prepare the request body"}
	}
	var lot BeanLotDetail
	failure := c.Do(ctx, Request{Method: http.MethodPost, Path: inventoryAdminRoot + "/bean-lots", Body: body, IdempotencyKey: key}, &lot)
	return lot, failure
}

func (c *Client) PatchBeanLot(ctx context.Context, rawLotID string, patch BeanLotPatch, key string) (BeanLotDetail, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	if len(patch.fields) == 0 {
		return BeanLotDetail{}, mutationUsage("invalid_patch", "Bean lot update requires at least one field")
	}
	body, err := newJSONBody(patch)
	if err != nil {
		return BeanLotDetail{}, mutationUsage("invalid_patch", "Unable to encode bean lot update")
	}
	var lot BeanLotDetail
	failure = c.Do(ctx, Request{Method: http.MethodPatch, Path: inventoryAdminRoot + "/bean-lots/" + lotID, Body: body, IdempotencyKey: key}, &lot)
	return lot, failure
}

func (c *Client) AdjustBeanLot(ctx context.Context, rawLotID string, adjustment InventoryAdjustmentWrite, key string) (BeanLotDetail, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	if failure = ValidateInventoryAdjustment(adjustment); failure != nil {
		return BeanLotDetail{}, failure
	}
	body, err := newJSONBody(adjustment)
	if err != nil {
		return BeanLotDetail{}, mutationUsage("invalid_adjustment", "Unable to encode inventory adjustment")
	}
	var lot BeanLotDetail
	failure = c.Do(ctx, Request{Method: http.MethodPost, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/adjustments", Body: body, IdempotencyKey: key}, &lot)
	return lot, failure
}

// DecodeBeanLotCreateManifest strictly decodes one JSON object and canonicalizes defaults.
func DecodeBeanLotCreateManifest(data []byte) (BeanLotCreateManifest, *output.Error) {
	var manifest BeanLotCreateManifest
	if err := decodeStrictMutationJSON(data, &manifest); err != nil {
		return manifest, mutationUsage("invalid_json", "Bean lot create JSON does not match the strict request model")
	}
	if manifest.Fields.Varietals == nil {
		manifest.Fields.Varietals = []string{}
	}
	if manifest.Images == nil {
		manifest.Images = []ImageUploadManifest{}
	}
	return manifest, ValidateBeanLotCreateManifest(manifest)
}

// DecodeBeanLotPatch strictly decodes one sparse JSON object.
func DecodeBeanLotPatch(data []byte) (BeanLotPatch, *output.Error) {
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return BeanLotPatch{}, mutationUsage("invalid_json", "Bean lot update JSON must be one object")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return BeanLotPatch{}, mutationUsage("invalid_json", "Bean lot update JSON must contain one object")
	}
	return NewBeanLotPatch(fields)
}

func newJSONBody(value any) (func() (io.ReadCloser, string, error), error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return func() (io.ReadCloser, string, error) {
		return io.NopCloser(bytes.NewReader(encoded)), "application/json", nil
	}, nil
}

func decodeStrictMutationJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func canonicalPatchValue(name, kind string, value any) (any, bool) {
	if value == nil {
		if strings.HasPrefix(kind, "nullable-") || kind == "date" || kind == "integer" || kind == "varietals" || kind == "score" || kind == "processing" || kind == "altitude" {
			return nil, true
		}
		return nil, false
	}
	switch kind {
	case "string", "nullable-string":
		text, ok := value.(string)
		codePoints, bytesLimit, multiline := patchTextLimits(name)
		return text, ok && validRequestText(text, codePoints, bytesLimit, kind == "string", multiline)
	case "date":
		text, ok := value.(string)
		return text, ok && validDate(text)
	case "integer":
		integer, ok := exactInt64(value)
		return integer, ok && between(integer, 1000, 9999)
	case "altitude":
		integer, ok := exactInt64(value)
		return integer, ok && between(integer, 0, 9000)
	case "varietals":
		items, ok := value.([]any)
		if !ok {
			if strings, stringOK := value.([]string); stringOK {
				items = make([]any, len(strings))
				for index := range strings {
					items[index] = strings[index]
				}
				ok = true
			}
		}
		if !ok || len(items) > 16 {
			return nil, false
		}
		result := make([]string, len(items))
		seen := make(map[string]struct{}, len(items))
		for index, item := range items {
			text, textOK := item.(string)
			if !textOK || !validRequestText(text, 100, 400, true, false) {
				return nil, false
			}
			identity := strings.TrimSpace(text)
			if _, exists := seen[identity]; exists {
				return nil, false
			}
			seen[identity] = struct{}{}
			result[index] = text
		}
		return result, true
	case "score":
		switch score := value.(type) {
		case string:
			return score, validRequestScore(score)
		case json.Number:
			integer, err := score.Int64()
			return strconv.FormatInt(integer, 10) + ".00", err == nil && integer >= 0 && integer <= 100
		case int:
			return strconv.Itoa(score) + ".00", score >= 0 && score <= 100
		case int64:
			return strconv.FormatInt(score, 10) + ".00", score >= 0 && score <= 100
		default:
			return nil, false
		}
	case "processing":
		text, ok := value.(string)
		return text, ok && oneOf(text, "washed", "natural", "honey", "pulped-natural", "wet-hulled", "anaerobic", "experimental", "other")
	case "state":
		text, ok := value.(string)
		return text, ok && oneOf(text, "active", "archived")
	default:
		return nil, false
	}
}

func exactInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int64:
		return number, true
	case json.Number:
		integer, err := number.Int64()
		return integer, err == nil
	default:
		return 0, false
	}
}

func nullableInt(fields map[string]any, name string) (*int64, bool) {
	value, exists := fields[name]
	if !exists {
		return nil, false
	}
	if value == nil {
		return nil, true
	}
	integer, ok := value.(int64)
	if !ok {
		return nil, true
	}
	return &integer, true
}

func validAltitude(value *int64) bool { return value == nil || between(*value, 0, 9000) }

func canonicalScoreJSON(raw []byte) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, validRequestScore(text)
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return "", false
	}
	integer, err := number.Int64()
	if err != nil || integer < 0 || integer > 100 {
		return "", false
	}
	return strconv.FormatInt(integer, 10) + ".00", true
}

func validRequestScore(value string) bool {
	if !canonicalScore.MatchString(value) {
		return false
	}
	score, err := strconv.ParseFloat(value, 64)
	return err == nil && score <= 100
}

func patchTextLimits(name string) (int, int, bool) {
	switch name {
	case "origin":
		return 100, 400, false
	case "notes":
		return 10000, 40000, true
	default:
		return 200, 800, false
	}
}

func validRequestText(value string, codePoints, bytesLimit int, required, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > codePoints || len(value) > bytesLimit {
		return false
	}
	for _, character := range value {
		point := uint32(character)
		permittedMultiline := multiline && (character == '\n' || character == '\t')
		if (point <= 0x1f || (point >= 0x7f && point <= 0x9f)) && !permittedMultiline {
			return false
		}
	}
	return !required || strings.TrimSpace(value) != ""
}

func mutationUsage(code, message string) *output.Error {
	return &output.Error{ExitCode: 2, Code: code, Message: message}
}
