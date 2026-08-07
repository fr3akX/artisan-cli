package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
)

func runInventoryAdjust(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if len(args) == 0 {
		return inventoryUsageFailure(runtime, jsonMode, "inventory adjust requires one LOT_ID")
	}
	lotID := args[0]
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	flags := flag.NewFlagSet("artisan inventory adjust", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	grams := flags.Int64("grams", 0, "signed integer grams")
	reason := flags.String("reason", "", "adjustment reason")
	reference := flags.String("reference", "", "adjustment reference")
	occurredAt := flags.String("occurred-at", "", "canonical UTC occurrence timestamp")
	yes := flags.Bool("yes", false, "skip interactive confirmation")
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args[1:]); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory adjust option")
	}
	visited := visitedFlagNames(flags)
	if !visited["grams"] || !visited["reason"] {
		return inventoryUsageFailure(runtime, jsonMode, "inventory adjust requires --grams and --reason")
	}
	if !visited["occurred-at"] {
		*occurredAt = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	adjustment := api.InventoryAdjustmentWrite{QuantityGrams: *grams, Reason: *reason, OccurredAt: *occurredAt}
	if visited["reference"] {
		adjustment.Reference = reference
	}
	if failure := api.ValidateInventoryAdjustment(adjustment); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if visited["idempotency-key"] {
		if err := api.ValidateIdempotencyKey(*idempotencyKey); err != nil {
			return inventoryUsageFailure(runtime, jsonMode, "Idempotency key is invalid")
		}
	}
	change := strconv.FormatInt(*grams, 10)
	if *grams > 0 {
		change = "+" + change
	}
	approved, code := confirmMutation(runtime, jsonMode, *yes, fmt.Sprintf("Adjust lot %s by %s grams?", lotID, change))
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
	lot, apiFailure := client.AdjustBeanLot(ctx, lotID, adjustment, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}
