package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const inventoryReservationsRoot = "/api/v1/inventory/reservations"

// ReservationCreate is the exact existing desktop reservation request.
type ReservationCreate struct {
	ClientReservationUUID string `json:"client_reservation_uuid"`
	ClientInstanceUUID    string `json:"client_instance_uuid"`
	RoastUUID             string `json:"roast_uuid"`
	LotID                 string `json:"lot_id"`
	PlannedGrams          int64  `json:"planned_grams"`
	OccurredAt            string `json:"occurred_at"`
}

// ReservationFinalize is the exact existing desktop finalize request. A nil
// ActualGrams is sent as JSON null so the server, rather than the CLI, applies
// its established planned-weight behavior.
type ReservationFinalize struct {
	ActualGrams *int64 `json:"actual_grams"`
	OccurredAt  string `json:"occurred_at"`
}

// ReservationRelease is the exact existing desktop release request.
type ReservationRelease struct {
	OccurredAt string `json:"occurred_at"`
}

// InventoryBalance is the authoritative balance nested in reservation mutations.
type InventoryBalance struct {
	LotID                   string `json:"lot_id"`
	OnHandGrams             int64  `json:"on_hand_grams"`
	ReservedGrams           int64  `json:"reserved_grams"`
	AvailableGrams          int64  `json:"available_grams"`
	UnresolvedConflictCount int64  `json:"unresolved_conflict_count"`
}

// ReservationMutationResponse is the exact desktop reservation mutation projection.
type ReservationMutationResponse struct {
	Reservation      DesktopInventoryReservation `json:"reservation"`
	Balance          InventoryBalance            `json:"balance"`
	Conflict         *InventoryConflict          `json:"conflict"`
	IdempotentReplay bool                        `json:"idempotent_replay"`
}

// InventoryConflictResolutionWrite is the strict admin conflict resolution request.
type InventoryConflictResolutionWrite struct {
	ResolutionNote string `json:"resolution_note"`
}

func (value *InventoryBalance) UnmarshalJSON(data []byte) error {
	type wire InventoryBalance
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, nil, "lot_id", "on_hand_grams", "reserved_grams", "available_grams", "unresolved_conflict_count"); err != nil {
		return err
	}
	*value = InventoryBalance(decoded)
	return value.validate()
}

func (value *ReservationMutationResponse) UnmarshalJSON(data []byte) error {
	type wire ReservationMutationResponse
	var decoded wire
	if err := decodeRequiredObject(data, &decoded, []string{"conflict"}, "reservation", "balance", "conflict", "idempotent_replay"); err != nil {
		return err
	}
	*value = ReservationMutationResponse(decoded)
	return value.validate()
}

func (value InventoryBalance) validate() error {
	if !validUUID(value.LotID) || !validGrams(value.OnHandGrams) || !between(value.ReservedGrams, 0, maxInventoryGrams) || !validGrams(value.AvailableGrams) || value.AvailableGrams != value.OnHandGrams-value.ReservedGrams || !between(value.UnresolvedConflictCount, 0, maxInventoryGrams) {
		return errors.New("invalid inventory balance")
	}
	return nil
}

func (value ReservationMutationResponse) validate() error {
	if value.Reservation.LotID != value.Balance.LotID {
		return errors.New("inconsistent reservation balance")
	}
	if value.Conflict == nil {
		if value.Reservation.OpenConflictID != nil {
			return errors.New("missing reservation conflict")
		}
		return nil
	}
	if value.Conflict.State != "open" || value.Reservation.OpenConflictID == nil || *value.Reservation.OpenConflictID != value.Conflict.ConflictID || value.Conflict.LotID != value.Reservation.LotID || value.Conflict.RoastUUID == nil || *value.Conflict.RoastUUID != value.Reservation.RoastUUID || value.Conflict.ReservationID == nil || *value.Conflict.ReservationID != value.Reservation.ReservationID {
		return errors.New("inconsistent reservation conflict")
	}
	return nil
}

// ValidateReservationCreate validates and canonicalizes all locally knowable fields.
func ValidateReservationCreate(request ReservationCreate) *output.Error {
	_, failure := normalizeReservationCreate(request)
	return failure
}

func normalizeReservationCreate(request ReservationCreate) (ReservationCreate, *output.Error) {
	identifiers := []*string{&request.ClientReservationUUID, &request.ClientInstanceUUID, &request.RoastUUID, &request.LotID}
	for _, identifier := range identifiers {
		canonical, failure := normalizeInventoryUUID(*identifier)
		if failure != nil {
			return request, failure
		}
		*identifier = canonical
	}
	if !between(request.PlannedGrams, 1, maxInventoryGrams) {
		return request, mutationUsage("invalid_grams", "Planned grams must be a positive integer within the supported range")
	}
	if !validTimestamp(request.OccurredAt) {
		return request, mutationUsage("invalid_timestamp", "Occurred-at must use the canonical UTC timestamp format")
	}
	return request, nil
}

// ValidateReservationFinalize validates exact optional grams and occurrence time.
func ValidateReservationFinalize(request ReservationFinalize) *output.Error {
	_, failure := normalizeReservationFinalize(request)
	return failure
}

func normalizeReservationFinalize(request ReservationFinalize) (ReservationFinalize, *output.Error) {
	if request.ActualGrams != nil && !between(*request.ActualGrams, 1, maxInventoryGrams) {
		return request, mutationUsage("invalid_grams", "Actual grams must be a positive integer within the supported range")
	}
	if !validTimestamp(request.OccurredAt) {
		return request, mutationUsage("invalid_timestamp", "Occurred-at must use the canonical UTC timestamp format")
	}
	return request, nil
}

// ValidateReservationRelease validates the required occurrence time.
func ValidateReservationRelease(request ReservationRelease) *output.Error {
	if !validTimestamp(request.OccurredAt) {
		return mutationUsage("invalid_timestamp", "Occurred-at must use the canonical UTC timestamp format")
	}
	return nil
}

// ValidateInventoryConflictResolution validates the required note.
func ValidateInventoryConflictResolution(request InventoryConflictResolutionWrite) *output.Error {
	_, failure := normalizeInventoryConflictResolution(request)
	return failure
}

// NormalizeInventoryConflictResolution validates and returns the exact wire note.
func NormalizeInventoryConflictResolution(request InventoryConflictResolutionWrite) (InventoryConflictResolutionWrite, *output.Error) {
	return normalizeInventoryConflictResolution(request)
}

func normalizeInventoryConflictResolution(request InventoryConflictResolutionWrite) (InventoryConflictResolutionWrite, *output.Error) {
	note, ok := normalizeRequestText(request.ResolutionNote, 2000, 8000, true, true)
	if !ok {
		return request, mutationUsage("invalid_resolution_note", "Resolution note must be nonblank valid text")
	}
	request.ResolutionNote = note
	return request, nil
}

func (c *Client) CreateInventoryReservation(ctx context.Context, request ReservationCreate, key string) (ReservationMutationResponse, *output.Error) {
	request, failure := normalizeReservationCreate(request)
	if failure != nil {
		return ReservationMutationResponse{}, failure
	}
	response, failure := c.doReservationMutation(ctx, inventoryReservationsRoot, request, key, http.StatusCreated)
	if failure != nil {
		return ReservationMutationResponse{}, failure
	}
	reservation := response.Reservation
	if reservation.ClientReservationUUID != request.ClientReservationUUID || reservation.ClientInstanceUUID != request.ClientInstanceUUID || reservation.RoastUUID != request.RoastUUID || reservation.LotID != request.LotID || reservation.PlannedGrams != request.PlannedGrams || reservation.ReservedAt != request.OccurredAt {
		return ReservationMutationResponse{}, invalidServerResponse(http.StatusCreated)
	}
	return response, nil
}

func (c *Client) FinalizeInventoryReservation(ctx context.Context, rawClientReservationUUID string, request ReservationFinalize, key string) (ReservationMutationResponse, *output.Error) {
	clientReservationUUID, failure := normalizeInventoryUUID(rawClientReservationUUID)
	if failure != nil {
		return ReservationMutationResponse{}, failure
	}
	request, failure = normalizeReservationFinalize(request)
	if failure != nil {
		return ReservationMutationResponse{}, failure
	}
	response, failure := c.doReservationMutation(ctx, inventoryReservationsRoot+"/"+clientReservationUUID+"/finalize", request, key, http.StatusOK)
	if failure != nil {
		return ReservationMutationResponse{}, failure
	}
	reservation := response.Reservation
	if reservation.ClientReservationUUID != clientReservationUUID || reservation.State != "finalized" || reservation.CompletedAt == nil || *reservation.CompletedAt != request.OccurredAt || (request.ActualGrams != nil && (reservation.ActualGrams == nil || *reservation.ActualGrams != *request.ActualGrams)) {
		return ReservationMutationResponse{}, invalidServerResponse(http.StatusOK)
	}
	return response, nil
}

func (c *Client) ReleaseInventoryReservation(ctx context.Context, rawClientReservationUUID string, request ReservationRelease, key string) (ReservationMutationResponse, *output.Error) {
	clientReservationUUID, failure := normalizeInventoryUUID(rawClientReservationUUID)
	if failure != nil {
		return ReservationMutationResponse{}, failure
	}
	if failure = ValidateReservationRelease(request); failure != nil {
		return ReservationMutationResponse{}, failure
	}
	response, failure := c.doReservationMutation(ctx, inventoryReservationsRoot+"/"+clientReservationUUID+"/release", request, key, http.StatusOK)
	if failure != nil {
		return ReservationMutationResponse{}, failure
	}
	reservation := response.Reservation
	if reservation.ClientReservationUUID != clientReservationUUID || reservation.State != "released" || reservation.CompletedAt == nil || *reservation.CompletedAt != request.OccurredAt {
		return ReservationMutationResponse{}, invalidServerResponse(http.StatusOK)
	}
	return response, nil
}

func (c *Client) doReservationMutation(ctx context.Context, path string, request any, key string, status int) (ReservationMutationResponse, *output.Error) {
	body, err := newJSONBody(request)
	if err != nil {
		return ReservationMutationResponse{}, mutationUsage("invalid_reservation", "Unable to encode inventory reservation")
	}
	var response ReservationMutationResponse
	failure := c.Do(ctx, Request{Method: http.MethodPost, Path: path, Body: body, IdempotencyKey: key, ExpectedStatus: status}, &response)
	return response, failure
}

func (c *Client) ResolveInventoryConflict(ctx context.Context, rawConflictID string, request InventoryConflictResolutionWrite, key string) (InventoryConflict, *output.Error) {
	conflictID, failure := normalizeInventoryUUID(rawConflictID)
	if failure != nil {
		return InventoryConflict{}, failure
	}
	request, failure = normalizeInventoryConflictResolution(request)
	if failure != nil {
		return InventoryConflict{}, failure
	}
	body, err := newJSONBody(request)
	if err != nil {
		return InventoryConflict{}, mutationUsage("invalid_resolution", "Unable to encode inventory conflict resolution")
	}
	var conflict InventoryConflict
	failure = c.doInventoryAdminJSON(ctx, Request{Method: http.MethodPost, Path: inventoryAdminRoot + "/conflicts/" + conflictID + "/resolve", Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK}, &conflict, true)
	if failure != nil {
		return InventoryConflict{}, failure
	}
	if conflict.ConflictID != conflictID || conflict.State != "resolved" || conflict.ResolutionNote == nil || *conflict.ResolutionNote != request.ResolutionNote {
		return InventoryConflict{}, invalidServerResponse(http.StatusOK)
	}
	return conflict, nil
}
