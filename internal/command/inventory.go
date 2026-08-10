package command

import (
	"context"
	"time"
)

func runInventory(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if len(args) == 0 {
		return inventoryUsageFailure(runtime, jsonMode, "An inventory command is required")
	}
	switch args[0] {
	case "lot":
		return runInventoryLot(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "conflict":
		return runInventoryConflict(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "reservation":
		return runInventoryReservation(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "image":
		return runInventoryImage(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "adjust":
		return runInventoryAdjust(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "totals":
		return runInventoryTotals(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	default:
		return inventoryUsageFailure(runtime, jsonMode, "Unknown inventory command")
	}
}

func inventoryUsageFailure(runtime Runtime, jsonMode bool, message string) int {
	return writeFailure(runtime, jsonMode, inventoryUsageError(message))
}
