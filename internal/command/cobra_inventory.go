package command

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func addPageFlags(flags *pflag.FlagSet) {
	flags.Int("limit", 0, "Maximum items per page")
	flags.String("cursor", "", "Opaque continuation cursor")
	flags.Bool("all", false, "Read all bounded pages")
}

func runLegacyLeaf(cmd *cobra.Command, args []string, state *cobraState, run func([]string) int) {
	setCommandExit(state, run(canonicalLegacyArgs(cmd, args)))
}

func newInventoryCommand(ctx context.Context, state *cobraState) *cobra.Command {
	inventory := inventoryParentCommand("inventory", "Manage green-coffee inventory", state, "An inventory command is required", "Unknown inventory command")
	lot := inventoryParentCommand("lot", "Read and manage inventory lots", state, "An inventory lot command is required", "Unknown inventory lot command")
	reservation := inventoryParentCommand("reservation", "Manage inventory reservations", state, "An inventory reservation command is required", "Unknown inventory reservation command")
	conflict := inventoryParentCommand("conflict", "Read and resolve inventory conflicts", state, "An inventory conflict command is required", "Unknown inventory conflict command")

	lot.AddCommand(
		newInventoryLotListCommand(ctx, state),
		newInventoryLotShowCommand(ctx, state),
		newInventoryLotHistoryCommand(ctx, state, "ledger"),
		newInventoryLotHistoryCommand(ctx, state, "reservations"),
		newInventoryLotHistoryCommand(ctx, state, "conflicts"),
		newInventoryLotPassthroughCommand(ctx, state, "create"),
		newInventoryLotPassthroughCommand(ctx, state, "update"),
		newInventoryLotPassthroughCommand(ctx, state, "archive"),
		newInventoryLotPassthroughCommand(ctx, state, "restore"),
	)
	reservation.AddCommand(
		newInventoryReservationCreateCommand(ctx, state),
		newInventoryReservationTransitionCommand(ctx, state, "finalize"),
		newInventoryReservationTransitionCommand(ctx, state, "release"),
	)
	conflict.AddCommand(
		newInventoryConflictListCommand(ctx, state),
		newInventoryConflictShowCommand(ctx, state),
		newInventoryConflictResolveCommand(ctx, state),
	)
	inventory.AddCommand(lot, newInventoryAdjustCommand(ctx, state), reservation, conflict, newInventoryImagePassthroughCommand(ctx, state))
	return inventory
}

func inventoryParentCommand(use, short string, state *cobraState, missing, unknown string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Run: func(_ *cobra.Command, args []string) {
			message := missing
			if len(args) != 0 {
				message = unknown
			}
			setCommandExit(state, inventoryUsageFailure(state.runtime, state.jsonMode, message))
		},
	}
}

func newInventoryLotListCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List inventory lots",
		Args:  inventoryExactArgs(0, "Invalid inventory lot list option", "Invalid inventory lot list option"),
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runInventoryLotList(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	addPageFlags(cmd.Flags())
	cmd.Flags().String("q", "", "Search lot text")
	cmd.Flags().String("state", "", "Filter by lot state")
	cmd.Flags().String("availability", "", "Filter by availability")
	cmd.Flags().String("conflict", "", "Filter by conflict state")
	cmd.Flags().String("roast-uuid", "", "Filter by roast UUID")
	registerStaticFlagCompletion(cmd, "state", "active", "archived")
	registerStaticFlagCompletion(cmd, "availability", "positive", "zero", "negative")
	registerStaticFlagCompletion(cmd, "conflict", "open", "none")
	disableFlagFileCompletion(cmd, "limit", "cursor", "all", "q", "roast-uuid")
	return cmd
}

func newInventoryLotShowCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "show LOT_ID",
		Short:             "Show an inventory lot",
		Args:              inventoryExactArgs(1, "inventory lot show requires one LOT_ID", "inventory lot show requires one LOT_ID"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(_ *cobra.Command, args []string) {
			setCommandExit(state, runInventoryLotShow(ctx, args[0], state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	return cmd
}

func newInventoryLotHistoryCommand(ctx context.Context, state *cobraState, kind string) *cobra.Command {
	cmd := &cobra.Command{
		Use:               kind + " LOT_ID",
		Short:             "List lot " + kind,
		Args:              inventoryExactArgs(1, "inventory lot "+kind+" requires one LOT_ID", "Invalid inventory lot "+kind+" option"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			legacy := append(append([]string(nil), args...), canonicalLegacyArgs(cmd, nil)...)
			setCommandExit(state, runInventoryLotHistory(ctx, kind, legacy, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	addPageFlags(cmd.Flags())
	disableFlagFileCompletion(cmd, "limit", "cursor", "all")
	return cmd
}

func newInventoryAdjustCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "adjust LOT_ID",
		Short:             "Apply a signed inventory adjustment",
		Args:              inventoryExactArgs(1, "inventory adjust requires one LOT_ID", "Invalid inventory adjust option"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			legacy := append(append([]string(nil), args...), canonicalLegacyArgs(cmd, nil)...)
			setCommandExit(state, runInventoryAdjust(ctx, legacy, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	cmd.Flags().Int64("grams", 0, "Signed stock delta in grams")
	cmd.Flags().String("reason", "", "Adjustment reason")
	cmd.Flags().String("reference", "", "Adjustment reference")
	cmd.Flags().String("occurred-at", "", "Canonical UTC occurrence timestamp")
	cmd.Flags().Bool("yes", false, "Skip interactive confirmation")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "grams", "reason", "reference", "occurred-at", "yes", "idempotency-key")
	return cmd
}

func newInventoryReservationCreateCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an inventory reservation",
		Args:  inventoryExactArgs(0, "Invalid inventory reservation create option", "Invalid inventory reservation create option"),
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runInventoryReservationCreate(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	cmd.Flags().String("client-reservation-uuid", "", "Client reservation UUID")
	cmd.Flags().String("client-instance-uuid", "", "Client instance UUID")
	cmd.Flags().String("roast-uuid", "", "Roast UUID")
	cmd.Flags().String("lot-id", "", "Lot UUID")
	cmd.Flags().Int64("planned-grams", 0, "Planned integer grams")
	cmd.Flags().String("occurred-at", "", "Canonical UTC occurrence timestamp")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "client-reservation-uuid", "client-instance-uuid", "roast-uuid", "lot-id", "planned-grams", "occurred-at", "idempotency-key")
	return cmd
}

func newInventoryReservationTransitionCommand(ctx context.Context, state *cobraState, transition string) *cobra.Command {
	cmd := &cobra.Command{
		Use:               transition + " CLIENT_RESERVATION_UUID",
		Short:             transition + " an inventory reservation",
		Args:              inventoryExactArgs(1, "inventory reservation "+transition+" requires one CLIENT_RESERVATION_UUID", "Invalid inventory reservation "+transition+" option"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			setCommandExit(state, runInventoryReservationTransition(ctx, transition, args[0], canonicalLegacyArgs(cmd, nil), state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	if transition == "finalize" {
		cmd.Flags().Int64("actual-grams", 0, "Actual integer grams")
		disableFlagFileCompletion(cmd, "actual-grams")
	}
	cmd.Flags().String("occurred-at", "", "Canonical UTC occurrence timestamp")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "occurred-at", "idempotency-key")
	return cmd
}

func newInventoryConflictListCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List inventory conflicts for a lot",
		Args:  inventoryExactArgs(0, "Invalid inventory conflict list option", "Invalid inventory conflict list option"),
		Run: func(cmd *cobra.Command, args []string) {
			legacy := append([]string{"list"}, canonicalLegacyArgs(cmd, args)...)
			setCommandExit(state, runInventoryConflict(ctx, legacy, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	cmd.Flags().String("lot", "", "Lot UUID")
	addPageFlags(cmd.Flags())
	disableFlagFileCompletion(cmd, "lot", "limit", "cursor", "all")
	return cmd
}

func newInventoryConflictShowCommand(ctx context.Context, state *cobraState) *cobra.Command {
	return &cobra.Command{
		Use:               "show CONFLICT_ID",
		Short:             "Show an inventory conflict",
		Args:              inventoryExactArgs(1, "inventory conflict show requires one CONFLICT_ID", "inventory conflict show requires one CONFLICT_ID"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(_ *cobra.Command, args []string) {
			setCommandExit(state, runInventoryConflict(ctx, []string{"show", args[0]}, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
}

func newInventoryConflictResolveCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "resolve CONFLICT_ID",
		Short:             "Resolve an inventory conflict",
		Args:              inventoryExactArgs(1, "inventory conflict resolve requires one CONFLICT_ID", "Invalid inventory conflict resolve option"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			setCommandExit(state, runInventoryConflictResolve(ctx, args[0], canonicalLegacyArgs(cmd, nil), state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	cmd.Flags().String("note", "", "Required resolution note")
	cmd.Flags().Bool("yes", false, "Skip interactive confirmation")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "note", "yes", "idempotency-key")
	return cmd
}

func newInventoryLotPassthroughCommand(ctx context.Context, state *cobraState, operation string) *cobra.Command {
	return &cobra.Command{
		Use:                operation,
		Short:              "Manage an inventory lot",
		DisableFlagParsing: true,
		Run: func(_ *cobra.Command, args []string) {
			setCommandExit(state, runInventoryLot(ctx, append([]string{operation}, args...), state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
}

func newInventoryImagePassthroughCommand(ctx context.Context, state *cobraState) *cobra.Command {
	return &cobra.Command{
		Use:                "image",
		Short:              "Manage inventory lot images",
		DisableFlagParsing: true,
		Run: func(_ *cobra.Command, args []string) {
			setCommandExit(state, runInventoryImage(ctx, args, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
}

func inventoryExactArgs(count int, missing, extra string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == count {
			return nil
		}
		message := extra
		if len(args) < count {
			message = missing
		}
		return &cobraValidationError{failure: inventoryUsageError(message)}
	}
}

func registerStaticFlagCompletion(cmd *cobra.Command, name string, values ...string) {
	_ = cmd.RegisterFlagCompletionFunc(name, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return append([]string(nil), values...), cobra.ShellCompDirectiveNoFileComp
	})
}

func disableFlagFileCompletion(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		_ = cmd.RegisterFlagCompletionFunc(name, cobra.NoFileCompletions)
	}
}

func knownInventoryCommandPath(args []string) string {
	command, commandIndex, ok := nextCommandToken(args, 0)
	if !ok || command != "inventory" {
		return "inventory"
	}
	group, groupIndex, ok := nextCommandToken(args, commandIndex+1)
	if !ok {
		return "inventory"
	}
	switch group {
	case "adjust", "image":
		return "inventory " + group
	case "lot", "reservation", "conflict":
		leaf, _, ok := nextCommandToken(args, groupIndex+1)
		if ok && inventoryGroupHasLeaf(group, leaf) {
			return "inventory " + group + " " + leaf
		}
		return "inventory " + group
	default:
		return "inventory"
	}
}

func inventoryGroupHasLeaf(group, leaf string) bool {
	switch group {
	case "lot":
		switch leaf {
		case "list", "show", "ledger", "reservations", "conflicts", "create", "update", "archive", "restore":
			return true
		}
	case "reservation":
		switch leaf {
		case "create", "finalize", "release":
			return true
		}
	case "conflict":
		switch leaf {
		case "list", "show", "resolve":
			return true
		}
	}
	return false
}

func inventoryCobraParseFailureMessage(path string) string {
	switch path {
	case "inventory":
		return "Unknown inventory command"
	case "inventory lot":
		return "Unknown inventory lot command"
	case "inventory lot list":
		return "Invalid inventory lot list option"
	case "inventory lot show":
		return "inventory lot show requires one LOT_ID"
	case "inventory lot ledger", "inventory lot reservations", "inventory lot conflicts":
		return "Invalid " + path + " option"
	case "inventory adjust":
		return "Invalid inventory adjust option"
	case "inventory reservation":
		return "Unknown inventory reservation command"
	case "inventory reservation create", "inventory reservation finalize", "inventory reservation release":
		return "Invalid " + path + " option"
	case "inventory conflict":
		return "Unknown inventory conflict command"
	case "inventory conflict list", "inventory conflict resolve":
		return "Invalid " + path + " option"
	case "inventory conflict show":
		return "inventory conflict show requires one CONFLICT_ID"
	}
	return ""
}
