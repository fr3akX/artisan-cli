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
	"golang.org/x/text/unicode/norm"
)

// BeanLotFields is the exact metadata object nested in BeanLotCreateManifest.
type BeanLotFields struct {
	Name               string   `json:"name"`
	Origin             *string  `json:"origin"`
	Producer           *string  `json:"producer"`
	Supplier           *string  `json:"supplier"`
	ExternalReference  *string  `json:"external_reference"`
	ReceivedDate       *string  `json:"received_date"`
	CropYear           *int64   `json:"crop_year"`
	PricePerKgEURCents *int64   `json:"price_per_kg_eur_cents"`
	Varietals          []string `json:"varietals"`
	SCAScore           *string  `json:"sca_score"`
	ProcessingMethod   *string  `json:"processing_method"`
	ProcessingDetail   *string  `json:"processing_detail"`
	AltitudeMinMetres  *int64   `json:"altitude_min_metres"`
	AltitudeMaxMetres  *int64   `json:"altitude_max_metres"`
	Notes              *string  `json:"notes"`
}

// ImageUploadManifest binds zero-based upload order to per-image metadata.
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

const maxBeanLotManifestBytes = 65_536

func (manifest *BeanLotCreateManifest) UnmarshalJSON(data []byte) error {
	type wire struct {
		Fields           BeanLotFields   `json:"fields"`
		OpeningGrams     int64           `json:"opening_grams"`
		OpeningReason    *string         `json:"opening_reason"`
		OpeningReference *string         `json:"opening_reference"`
		Images           json.RawMessage `json:"images"`
	}
	var decoded wire
	if err := decodeStrictMutationJSON(data, &decoded); err != nil {
		return err
	}
	var images []ImageUploadManifest
	if err := decodeRequiredArray(decoded.Images, &images); err != nil {
		return err
	}
	*manifest = BeanLotCreateManifest{
		Fields: decoded.Fields, OpeningGrams: decoded.OpeningGrams, OpeningReason: decoded.OpeningReason,
		OpeningReference: decoded.OpeningReference, Images: images,
	}
	return nil
}

func (fields *BeanLotFields) UnmarshalJSON(data []byte) error {
	type wire struct {
		Name               string          `json:"name"`
		Origin             *string         `json:"origin"`
		Producer           *string         `json:"producer"`
		Supplier           *string         `json:"supplier"`
		ExternalReference  *string         `json:"external_reference"`
		ReceivedDate       *string         `json:"received_date"`
		CropYear           *int64          `json:"crop_year"`
		PricePerKgEURCents *int64          `json:"price_per_kg_eur_cents"`
		Varietals          json.RawMessage `json:"varietals"`
		SCAScore           json.RawMessage `json:"sca_score"`
		ProcessingMethod   *string         `json:"processing_method"`
		ProcessingDetail   *string         `json:"processing_detail"`
		AltitudeMinMetres  *int64          `json:"altitude_min_metres"`
		AltitudeMaxMetres  *int64          `json:"altitude_max_metres"`
		Notes              *string         `json:"notes"`
	}
	var decoded wire
	if err := decodeStrictMutationJSON(data, &decoded); err != nil {
		return err
	}
	var varietals []string
	if err := decodeRequiredArray(decoded.Varietals, &varietals); err != nil {
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
		PricePerKgEURCents: decoded.PricePerKgEURCents, Varietals: varietals, SCAScore: score, ProcessingMethod: decoded.ProcessingMethod,
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
		"external_reference": "nullable-string", "received_date": "date", "crop_year": "integer", "price_per_kg_eur_cents": "price", "varietals": "varietals",
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
	_, failure := normalizeBeanLotCreateManifest(manifest)
	return failure
}

func normalizeBeanLotCreateManifest(manifest BeanLotCreateManifest) (BeanLotCreateManifest, *output.Error) {
	name, ok := normalizeRequestText(manifest.Fields.Name, 200, 800, true, false)
	if !ok {
		return manifest, mutationUsage("invalid_name", "Bean lot name is required and must be valid")
	}
	manifest.Fields.Name = name
	optionalFields := []struct {
		value      **string
		codePoints int
		bytesLimit int
		multiline  bool
	}{
		{&manifest.Fields.Origin, 100, 400, false},
		{&manifest.Fields.Producer, 200, 800, false},
		{&manifest.Fields.Supplier, 200, 800, false},
		{&manifest.Fields.ExternalReference, 200, 800, false},
		{&manifest.Fields.ProcessingDetail, 200, 800, false},
		{&manifest.Fields.Notes, 10000, 40000, true},
		{&manifest.OpeningReason, 2000, 8000, true},
		{&manifest.OpeningReference, 200, 800, false},
	}
	for _, field := range optionalFields {
		normalized, valid := normalizeOptionalRequestText(*field.value, field.codePoints, field.bytesLimit, field.multiline)
		if !valid {
			return manifest, mutationUsage("invalid_text", "Bean lot text field is invalid")
		}
		*field.value = normalized
	}
	if manifest.Fields.ReceivedDate != nil && !validDate(*manifest.Fields.ReceivedDate) {
		return manifest, mutationUsage("invalid_date", "Received date must use YYYY-MM-DD")
	}
	if manifest.Fields.CropYear != nil && !between(*manifest.Fields.CropYear, 1000, 9999) {
		return manifest, mutationUsage("invalid_crop_year", "Crop year must be between 1000 and 9999")
	}
	if manifest.Fields.PricePerKgEURCents != nil && !between(*manifest.Fields.PricePerKgEURCents, 0, maxPricePerKgEURCents) {
		return manifest, mutationUsage("invalid_price_per_kg_eur", "Price per kg must be between 0 and 2147483647 cents")
	}
	if manifest.Fields.SCAScore != nil && !validRequestScore(*manifest.Fields.SCAScore) {
		return manifest, mutationUsage("invalid_sca_score", "SCA score must be between 0.00 and 100.00 with two decimal places")
	}
	if !validOptionalEnum(manifest.Fields.ProcessingMethod, "washed", "natural", "honey", "pulped-natural", "wet-hulled", "anaerobic", "experimental", "other") {
		return manifest, mutationUsage("invalid_processing", "Processing method is invalid")
	}
	if manifest.Fields.ProcessingMethod != nil && *manifest.Fields.ProcessingMethod == "other" && manifest.Fields.ProcessingDetail == nil {
		return manifest, mutationUsage("invalid_processing", "Processing detail is required when processing method is other")
	}
	if !validAltitude(manifest.Fields.AltitudeMinMetres) || !validAltitude(manifest.Fields.AltitudeMaxMetres) || (manifest.Fields.AltitudeMinMetres != nil && manifest.Fields.AltitudeMaxMetres != nil && *manifest.Fields.AltitudeMinMetres > *manifest.Fields.AltitudeMaxMetres) {
		return manifest, mutationUsage("invalid_altitude", "Altitude must be between 0 and 9000 and minimum must not exceed maximum")
	}
	if manifest.Fields.Varietals == nil || len(manifest.Fields.Varietals) > 16 {
		return manifest, mutationUsage("invalid_varietals", "Varietals must be a non-null array of at most 16 items")
	}
	manifest.Fields.Varietals = append([]string{}, manifest.Fields.Varietals...)
	seen := make(map[string]struct{}, len(manifest.Fields.Varietals))
	for index, varietal := range manifest.Fields.Varietals {
		normalized, valid := normalizeRequestText(varietal, 100, 400, true, false)
		if !valid {
			return manifest, mutationUsage("invalid_varietals", "Varietals must be nonblank valid text")
		}
		if _, exists := seen[normalized]; exists {
			return manifest, mutationUsage("invalid_varietals", "Varietals must be unique")
		}
		seen[normalized] = struct{}{}
		manifest.Fields.Varietals[index] = normalized
	}
	if !between(manifest.OpeningGrams, 0, maxInventoryGrams) || (manifest.OpeningGrams > 0 && manifest.OpeningReason == nil) {
		return manifest, mutationUsage("invalid_opening_balance", "Positive opening grams require an opening reason")
	}
	images, imageFailure := normalizeImageUploadManifest(manifest.Images, false)
	if imageFailure != nil {
		return manifest, imageFailure
	}
	manifest.Images = images
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return manifest, mutationUsage("invalid_manifest", "Unable to encode bean lot manifest")
	}
	if len(encoded) > maxBeanLotManifestBytes {
		return manifest, mutationUsage("manifest_too_large", "Encoded bean lot manifest exceeds 65,536 bytes")
	}
	return manifest, nil
}

// ValidateInventoryAdjustment validates exact integer grams and canonical time.
func ValidateInventoryAdjustment(adjustment InventoryAdjustmentWrite) *output.Error {
	_, failure := normalizeInventoryAdjustment(adjustment)
	return failure
}

// NormalizeInventoryAdjustment returns the exact canonical adjustment sent on the wire.
func NormalizeInventoryAdjustment(adjustment InventoryAdjustmentWrite) (InventoryAdjustmentWrite, *output.Error) {
	return normalizeInventoryAdjustment(adjustment)
}

func normalizeInventoryAdjustment(adjustment InventoryAdjustmentWrite) (InventoryAdjustmentWrite, *output.Error) {
	if adjustment.QuantityGrams == 0 || !validGrams(adjustment.QuantityGrams) {
		return adjustment, mutationUsage("invalid_grams", "Adjustment grams must be a nonzero integer within the supported range")
	}
	reason, ok := normalizeRequestText(adjustment.Reason, 2000, 8000, true, true)
	if !ok {
		return adjustment, mutationUsage("invalid_reason", "Adjustment reason must be nonblank valid text")
	}
	adjustment.Reason = reason
	reference, ok := normalizeOptionalRequestText(adjustment.Reference, 200, 800, false)
	if !ok {
		return adjustment, mutationUsage("invalid_reference", "Adjustment reference is invalid")
	}
	adjustment.Reference = reference
	if !validTimestamp(adjustment.OccurredAt) {
		return adjustment, mutationUsage("invalid_timestamp", "Occurred-at must use the canonical UTC timestamp format")
	}
	return adjustment, nil
}

func (c *Client) CreateBeanLot(ctx context.Context, manifest BeanLotCreateManifest, key string) (BeanLotDetail, *output.Error) {
	return c.CreateBeanLotWithImages(ctx, manifest, nil, key)
}

// CreateBeanLotWithImages creates a lot with ordered replayable image files.
func (c *Client) CreateBeanLotWithImages(ctx context.Context, manifest BeanLotCreateManifest, imagePaths []string, key string) (BeanLotDetail, *output.Error) {
	manifest, failure := normalizeBeanLotCreateManifest(manifest)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	if len(imagePaths) != len(manifest.Images) {
		return BeanLotDetail{}, mutationUsage("invalid_images", "Every image declaration must have exactly one file")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return BeanLotDetail{}, mutationUsage("invalid_manifest", "Unable to encode bean lot manifest")
	}
	body, err := newManifestMultipartBody(ctx, encoded, imagePaths...)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return BeanLotDetail{}, contextOrNetworkFailure(ctx)
		}
		return BeanLotDetail{}, multipartPreparationFailure(err)
	}
	var lot BeanLotDetail
	failure = c.doInventoryAdminJSON(ctx, Request{Method: http.MethodPost, Path: inventoryAdminRoot + "/bean-lots", Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusCreated}, &lot, false)
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
	failure = c.doInventoryAdminJSON(ctx, Request{Method: http.MethodPatch, Path: inventoryAdminRoot + "/bean-lots/" + lotID, Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK}, &lot, true)
	if failure == nil && lot.LotID != lotID {
		return BeanLotDetail{}, invalidServerResponse(http.StatusOK)
	}
	return lot, failure
}

func (c *Client) AdjustBeanLot(ctx context.Context, rawLotID string, adjustment InventoryAdjustmentWrite, key string) (BeanLotDetail, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	adjustment, failure = normalizeInventoryAdjustment(adjustment)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	body, err := newJSONBody(adjustment)
	if err != nil {
		return BeanLotDetail{}, mutationUsage("invalid_adjustment", "Unable to encode inventory adjustment")
	}
	var lot BeanLotDetail
	failure = c.doInventoryAdminJSON(ctx, Request{Method: http.MethodPost, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/adjustments", Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK}, &lot, true)
	if failure == nil && lot.LotID != lotID {
		return BeanLotDetail{}, invalidServerResponse(http.StatusOK)
	}
	return lot, failure
}

// DecodeBeanLotCreateManifest strictly decodes and normalizes one JSON object.
func DecodeBeanLotCreateManifest(data []byte) (BeanLotCreateManifest, *output.Error) {
	var manifest BeanLotCreateManifest
	if err := decodeStrictMutationJSON(data, &manifest); err != nil {
		return manifest, mutationUsage("invalid_json", "Bean lot create JSON does not match the strict request model")
	}
	return normalizeBeanLotCreateManifest(manifest)
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

func decodeRequiredArray(data json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || trimmed[0] != '[' {
		return errors.New("required array is missing or invalid")
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(trimmed, &elements); err != nil {
		return err
	}
	for _, element := range elements {
		if bytes.Equal(bytes.TrimSpace(element), []byte("null")) {
			return errors.New("array elements cannot be null")
		}
	}
	return json.Unmarshal(trimmed, destination)
}

func canonicalPatchValue(name, kind string, value any) (any, bool) {
	if value == nil {
		if strings.HasPrefix(kind, "nullable-") || kind == "date" || kind == "integer" || kind == "price" || kind == "varietals" || kind == "score" || kind == "processing" || kind == "altitude" {
			return nil, true
		}
		return nil, false
	}
	switch kind {
	case "string", "nullable-string":
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		codePoints, bytesLimit, multiline := patchTextLimits(name)
		normalized, valid := normalizeRequestText(text, codePoints, bytesLimit, kind == "string", multiline)
		if !valid {
			return nil, false
		}
		if normalized == "" && kind == "nullable-string" {
			return nil, true
		}
		return normalized, true
	case "date":
		text, ok := value.(string)
		return text, ok && validDate(text)
	case "integer":
		integer, ok := exactInt64(value)
		return integer, ok && between(integer, 1000, 9999)
	case "altitude":
		integer, ok := exactInt64(value)
		return integer, ok && between(integer, 0, 9000)
	case "price":
		integer, ok := exactInt64(value)
		return integer, ok && between(integer, 0, maxPricePerKgEURCents)
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
			if !textOK {
				return nil, false
			}
			normalized, valid := normalizeRequestText(text, 100, 400, true, false)
			if !valid {
				return nil, false
			}
			if _, exists := seen[normalized]; exists {
				return nil, false
			}
			seen[normalized] = struct{}{}
			result[index] = normalized
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

func normalizeOptionalRequestText(value *string, codePoints, bytesLimit int, multiline bool) (*string, bool) {
	if value == nil {
		return nil, true
	}
	normalized, ok := normalizeRequestText(*value, codePoints, bytesLimit, false, multiline)
	if !ok {
		return nil, false
	}
	if normalized == "" {
		return nil, true
	}
	return &normalized, true
}

func normalizeRequestText(value string, codePoints, bytesLimit int, required, multiline bool) (string, bool) {
	if !utf8.ValidString(value) {
		return "", false
	}
	// Match the server's normalization order: canonicalize line endings, apply
	// Unicode NFC, trim surrounding whitespace, reject controls, then enforce
	// bounds against the normalized wire value.
	normalized := strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	normalized = norm.NFC.String(normalized)
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return "", !required
	}
	for _, character := range normalized {
		point := uint32(character)
		permittedMultiline := multiline && (character == '\n' || character == '\t')
		if (point <= 0x1f || (point >= 0x7f && point <= 0x9f)) && !permittedMultiline {
			return "", false
		}
	}
	if utf8.RuneCountInString(normalized) > codePoints || len(normalized) > bytesLimit {
		return "", false
	}
	return normalized, true
}

func mutationUsage(code, message string) *output.Error {
	return &output.Error{ExitCode: 2, Code: code, Message: message}
}
