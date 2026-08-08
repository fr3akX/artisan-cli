package command

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func addCobraLotFieldFlags(flags *pflag.FlagSet) {
	flags.String("name", "", "Lot name")
	flags.String("origin", "", "Origin")
	flags.String("producer", "", "Producer")
	flags.String("supplier", "", "Supplier")
	flags.String("external-reference", "", "External reference")
	flags.String("received-date", "", "Received date (YYYY-MM-DD)")
	flags.Int64("crop-year", 0, "Crop year")
	flags.StringArray("varietal", nil, "Varietal; repeat for multiple values")
	flags.String("sca-score", "", "SCA score")
	flags.String("processing-method", "", "Processing method")
	flags.String("processing-detail", "", "Processing detail")
	flags.Int64("altitude-min-metres", 0, "Minimum altitude in metres")
	flags.Int64("altitude-max-metres", 0, "Maximum altitude in metres")
	flags.String("notes", "", "Notes")
}

func newInventoryLotCreateCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an inventory lot",
		Long:  "Create an inventory lot. Image captions and alt text use zero-based INDEX=TEXT declarations.",
		Args:  inventoryExactArgs(0, "Invalid inventory lot create option", "Invalid inventory lot create option"),
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runInventoryLotCreate(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	addCobraLotFieldFlags(cmd.Flags())
	cmd.Flags().Int64("opening-grams", 0, "Opening inventory in integer grams")
	cmd.Flags().String("opening-reason", "", "Opening inventory reason")
	cmd.Flags().String("opening-reference", "", "Opening inventory reference")
	cmd.Flags().String("from-json", "", "Strict request JSON file or - for standard input")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	cmd.Flags().StringArray("image", nil, "JPEG/PNG image file; repeat in upload order (maximum eight)")
	cmd.Flags().StringArray("image-caption", nil, "Image caption as zero-based INDEX=TEXT; repeat for multiple images")
	cmd.Flags().StringArray("image-alt-text", nil, "Image alt text as zero-based INDEX=TEXT; repeat for multiple images")
	cmd.Flags().String("image-cover", "", "Zero-based cover image index")
	_ = cmd.MarkFlagFilename("from-json")
	_ = cmd.MarkFlagFilename("image", "jpg", "jpeg", "png")
	_ = cmd.RegisterFlagCompletionFunc("from-json", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"-"}, cobra.ShellCompDirectiveDefault
	})
	disableFlagFileCompletion(cmd,
		"name", "origin", "producer", "supplier", "external-reference", "received-date", "crop-year", "varietal",
		"sca-score", "processing-method", "processing-detail", "altitude-min-metres", "altitude-max-metres", "notes",
		"opening-grams", "opening-reason", "opening-reference", "idempotency-key", "image-caption", "image-alt-text", "image-cover",
	)
	return cmd
}

func newInventoryLotUpdateCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update LOT_ID",
		Short:             "Update an inventory lot",
		Args:              inventoryExactArgs(1, "inventory lot update requires one LOT_ID", "Invalid inventory lot update option"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			setCommandExit(state, runInventoryLotUpdate(ctx, args[0], canonicalLegacyArgs(cmd, nil), state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	addCobraLotFieldFlags(cmd.Flags())
	cmd.Flags().StringArray("clear", nil, "Nullable field to clear; repeat for multiple fields")
	cmd.Flags().String("from-json", "", "Strict request JSON file or - for standard input")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	_ = cmd.MarkFlagFilename("from-json")
	_ = cmd.RegisterFlagCompletionFunc("from-json", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"-"}, cobra.ShellCompDirectiveDefault
	})
	disableFlagFileCompletion(cmd,
		"name", "origin", "producer", "supplier", "external-reference", "received-date", "crop-year", "varietal",
		"sca-score", "processing-method", "processing-detail", "altitude-min-metres", "altitude-max-metres", "notes",
		"clear", "idempotency-key",
	)
	return cmd
}

func newInventoryLotStateCommand(ctx context.Context, state *cobraState, operation string) *cobra.Command {
	cmd := &cobra.Command{
		Use:               operation + " LOT_ID",
		Short:             operation + " an inventory lot",
		Args:              inventoryExactArgs(1, "inventory lot "+operation+" requires one LOT_ID", "Invalid inventory lot "+operation+" option"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			setCommandExit(state, runInventoryLotState(ctx, operation, args[0], canonicalLegacyArgs(cmd, nil), state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	if operation == "archive" {
		cmd.Flags().Bool("yes", false, "Skip interactive confirmation")
		disableFlagFileCompletion(cmd, "yes")
	}
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "idempotency-key")
	return cmd
}
