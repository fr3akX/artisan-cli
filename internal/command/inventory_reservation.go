package command

import (
	"context"
	"flag"
	"io"
	"strconv"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/output"
)

func runInventoryReservation(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if len(args) == 0 {
		return inventoryUsageFailure(runtime, jsonMode, "An inventory reservation command is required")
	}
	switch args[0] {
	case "create":
		return runInventoryReservationCreate(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "finalize", "release":
		if len(args) < 2 {
			return inventoryUsageFailure(runtime, jsonMode, "inventory reservation "+args[0]+" requires one CLIENT_RESERVATION_UUID")
		}
		return runInventoryReservationTransition(ctx, args[0], args[1], args[2:], runtime, jsonMode, serverOverride, timeout)
	default:
		return inventoryUsageFailure(runtime, jsonMode, "Unknown inventory reservation command")
	}
}

func runInventoryReservationCreate(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory reservation create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	request := api.ReservationCreate{}
	flags.StringVar(&request.ClientReservationUUID, "client-reservation-uuid", "", "client reservation UUID")
	flags.StringVar(&request.ClientInstanceUUID, "client-instance-uuid", "", "client instance UUID")
	flags.StringVar(&request.RoastUUID, "roast-uuid", "", "roast UUID")
	flags.StringVar(&request.LotID, "lot-id", "", "lot UUID")
	flags.Int64Var(&request.PlannedGrams, "planned-grams", 0, "planned integer grams")
	flags.StringVar(&request.OccurredAt, "occurred-at", "", "canonical UTC occurrence timestamp")
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory reservation create option")
	}
	visited := visitedFlagNames(flags)
	for _, required := range []string{"client-reservation-uuid", "client-instance-uuid", "roast-uuid", "lot-id", "planned-grams", "occurred-at"} {
		if !visited[required] {
			return inventoryUsageFailure(runtime, jsonMode, "inventory reservation create requires every reservation field")
		}
	}
	if failure := api.ValidateReservationCreate(request); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	response, apiFailure := client.CreateInventoryReservation(ctx, request, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, response, func(w io.Writer) error { return writeReservationMutation(w, response) })
}

func runInventoryReservationTransition(ctx context.Context, transition, clientReservationUUID string, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeInventoryUUID(clientReservationUUID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	flags := flag.NewFlagSet("artisan inventory reservation "+transition, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	occurredAt := flags.String("occurred-at", "", "canonical UTC occurrence timestamp")
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	var actualGrams int64
	if transition == "finalize" {
		flags.Int64Var(&actualGrams, "actual-grams", 0, "actual integer grams")
	}
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory reservation "+transition+" option")
	}
	visited := visitedFlagNames(flags)
	if !visited["occurred-at"] {
		return inventoryUsageFailure(runtime, jsonMode, "inventory reservation "+transition+" requires --occurred-at")
	}
	var finalize api.ReservationFinalize
	var release api.ReservationRelease
	if transition == "finalize" {
		finalize.OccurredAt = *occurredAt
		if visited["actual-grams"] {
			finalize.ActualGrams = &actualGrams
		}
		if failure := api.ValidateReservationFinalize(finalize); failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
	} else {
		release.OccurredAt = *occurredAt
		if failure := api.ValidateReservationRelease(release); failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
	}
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	var response api.ReservationMutationResponse
	var apiFailure *output.Error
	if transition == "finalize" {
		response, apiFailure = client.FinalizeInventoryReservation(ctx, clientReservationUUID, finalize, key)
	} else {
		response, apiFailure = client.ReleaseInventoryReservation(ctx, clientReservationUUID, release, key)
	}
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, response, func(w io.Writer) error { return writeReservationMutation(w, response) })
}

func writeReservationMutation(w io.Writer, response api.ReservationMutationResponse) error {
	reservation := response.Reservation
	if err := output.WriteDetails(w, []output.DetailField{
		{Label: "Reservation ID", Value: reservation.ReservationID},
		{Label: "Client reservation UUID", Value: reservation.ClientReservationUUID},
		{Label: "Lot ID", Value: reservation.LotID},
		{Label: "Roast UUID", Value: reservation.RoastUUID},
		{Label: "Client instance UUID", Value: reservation.ClientInstanceUUID},
		{Label: "State", Value: reservation.State},
		{Label: "Planned grams", Value: strconv.FormatInt(reservation.PlannedGrams, 10)},
		{Label: "Actual grams", Value: optionalInt(reservation.ActualGrams)},
		{Label: "Reserved at", Value: reservation.ReservedAt},
		{Label: "Completed at", Value: optionalString(reservation.CompletedAt)},
		{Label: "Created at", Value: reservation.CreatedAt},
		{Label: "Updated at", Value: reservation.UpdatedAt},
		{Label: "Open conflict ID", Value: optionalString(reservation.OpenConflictID)},
		{Label: "On hand grams", Value: strconv.FormatInt(response.Balance.OnHandGrams, 10)},
		{Label: "Reserved grams", Value: strconv.FormatInt(response.Balance.ReservedGrams, 10)},
		{Label: "Available grams", Value: strconv.FormatInt(response.Balance.AvailableGrams, 10)},
		{Label: "Unresolved conflicts", Value: strconv.FormatInt(response.Balance.UnresolvedConflictCount, 10)},
		{Label: "Idempotent replay", Value: strconv.FormatBool(response.IdempotentReplay)},
	}); err != nil {
		return err
	}
	if response.Conflict == nil {
		return nil
	}
	return writeConflictDetail(w, *response.Conflict)
}
