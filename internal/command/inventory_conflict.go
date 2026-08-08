package command

import (
	"context"
	"flag"
	"io"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/output"
)

func runInventoryConflictResolve(ctx context.Context, conflictID string, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeInventoryUUID(conflictID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	flags := flag.NewFlagSet("artisan inventory conflict resolve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	note := flags.String("note", "", "required resolution note")
	yes := flags.Bool("yes", false, "skip interactive confirmation")
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory conflict resolve option")
	}
	visited := visitedFlagNames(flags)
	if !visited["note"] {
		return inventoryUsageFailure(runtime, jsonMode, "inventory conflict resolve requires --note")
	}
	request, requestFailure := api.NormalizeInventoryConflictResolution(api.InventoryConflictResolutionWrite{ResolutionNote: *note})
	if requestFailure != nil {
		return writeFailure(runtime, jsonMode, *requestFailure)
	}
	if visited["idempotency-key"] {
		if err := api.ValidateIdempotencyKey(*idempotencyKey); err != nil {
			return inventoryUsageFailure(runtime, jsonMode, "Idempotency key is invalid")
		}
	}
	canonicalConflictID, _ := api.NormalizeInventoryUUID(conflictID)
	approved, code := confirmMutation(runtime, jsonMode, *yes, "Resolve conflict "+canonicalConflictID+" with note: "+output.EscapeVisible(request.ResolutionNote)+"?")
	if !approved {
		return code
	}
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	conflict, apiFailure := client.ResolveInventoryConflict(ctx, conflictID, request, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, conflict, func(w io.Writer) error { return writeConflictDetail(w, conflict) })
}
