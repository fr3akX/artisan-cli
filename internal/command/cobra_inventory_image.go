package command

import (
	"context"

	"github.com/spf13/cobra"
)

func newInventoryImageCommand(ctx context.Context, state *cobraState) *cobra.Command {
	image := inventoryParentCommand("image", "Manage inventory lot images", state, "An inventory image command is required", "Unknown inventory image command")
	image.AddCommand(
		newInventoryImageAddCommand(ctx, state),
		newInventoryImageUpdateCommand(ctx, state),
		newInventoryImageReorderCommand(ctx, state),
		newInventoryImageDeleteCommand(ctx, state),
		newInventoryImageDownloadCommand(ctx, state),
	)
	return image
}

func newInventoryImageAddCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add LOT_ID FILE...",
		Short: "Add images to an inventory lot",
		Long:  "Add images to an inventory lot. Captions and alt text use zero-based INDEX=TEXT declarations.",
		Args:  inventoryMinimumArgs(2, "Invalid inventory image add option; use image add [OPTIONS] LOT_ID FILE..."),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveDefault
		},
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runInventoryImageAdd(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	cmd.Flags().StringArray("caption", nil, "Caption as zero-based INDEX=TEXT; repeat for multiple files")
	cmd.Flags().StringArray("alt-text", nil, "Alt text as zero-based INDEX=TEXT; repeat for multiple files")
	cmd.Flags().String("cover", "", "Zero-based cover file index")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "caption", "alt-text", "cover", "idempotency-key")
	return cmd
}

func newInventoryImageUpdateCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update LOT_ID IMAGE_ID",
		Short:             "Update inventory image metadata",
		Args:              inventoryExactArgs(2, "Invalid inventory image update option", "Invalid inventory image update option"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runInventoryImageUpdate(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	cmd.Flags().String("caption", "", "Caption")
	cmd.Flags().String("alt-text", "", "Alt text")
	cmd.Flags().Bool("clear-caption", false, "Clear caption")
	cmd.Flags().Bool("clear-alt-text", false, "Clear alt text")
	cmd.Flags().Bool("cover", false, "Cover state")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "caption", "alt-text", "clear-caption", "clear-alt-text", "cover", "idempotency-key")
	return cmd
}

func newInventoryImageReorderCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "reorder LOT_ID IMAGE_ID...",
		Short:             "Reorder inventory lot images",
		Args:              inventoryMinimumArgs(2, "inventory image reorder requires LOT_ID and the complete IMAGE_ID list"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runInventoryImageReorder(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "idempotency-key")
	return cmd
}

func newInventoryImageDeleteCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "delete LOT_ID IMAGE_ID",
		Short:             "Delete an inventory lot image",
		Args:              inventoryExactArgs(2, "inventory image delete requires LOT_ID and IMAGE_ID", "inventory image delete requires LOT_ID and IMAGE_ID"),
		ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runInventoryImageDelete(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	cmd.Flags().Bool("yes", false, "Skip interactive confirmation")
	cmd.Flags().String("idempotency-key", "", "Advanced idempotency key")
	disableFlagFileCompletion(cmd, "yes", "idempotency-key")
	return cmd
}

func newInventoryImageDownloadCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download LOT_ID IMAGE_ID DESTINATION",
		Short: "Download an inventory lot image",
		Args:  inventoryExactArgs(3, "inventory image download requires LOT_ID IMAGE_ID DESTINATION", "inventory image download requires LOT_ID IMAGE_ID DESTINATION"),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) < 2 || len(args) >= 3 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveDefault
		},
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runInventoryImageDownload(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	cmd.Flags().String("variant", "display", "Image variant: display or thumbnail")
	cmd.Flags().Bool("force", false, "Replace an existing destination")
	registerStaticFlagCompletion(cmd, "variant", "display", "thumbnail")
	disableFlagFileCompletion(cmd, "force")
	return cmd
}

func inventoryMinimumArgs(count int, message string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) >= count {
			return nil
		}
		return &cobraValidationError{failure: inventoryUsageError(message)}
	}
}
