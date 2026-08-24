package command

import (
	"context"
	"strings"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/spf13/cobra"
)

func newRoastCommand(ctx context.Context, state *cobraState) *cobra.Command {
	roast := roastParentCommand("roast", "Read private roasts and post review comments", state, "A roast command is required", "Unknown roast command")
	chart := roastParentCommand("chart", "Download validated roast chart data", state, "A roast chart command is required", "Unknown roast chart command")
	profile := roastParentCommand("profile", "Download immutable roast profiles", state, "A roast profile command is required", "Unknown roast profile command")
	comment := roastParentCommand("comment", "Read private roast comments", state, "A roast comment command is required", "Unknown roast comment command")
	review := roastParentCommand("review", "Post one revision-bound roast review", state, "A roast review command is required", "Unknown roast review command")

	chart.AddCommand(newRoastChartDownloadCommand(ctx, state))
	profile.AddCommand(newRoastProfileDownloadCommand(ctx, state))
	comment.AddCommand(newRoastCommentListCommand(ctx, state))
	review.AddCommand(newRoastReviewPostCommand(ctx, state))
	roast.AddCommand(newRoastListCommand(ctx, state), newRoastShowCommand(ctx, state), newRoastRevisionsCommand(ctx, state), chart, profile, comment, review)
	return roast
}

func roastParentCommand(use, short string, state *cobraState, missing, unknown string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short,
		Run: func(_ *cobra.Command, args []string) {
			message := missing
			if len(args) != 0 {
				message = unknown
			}
			setCommandExit(state, roastUsageFailure(state.runtime, state.jsonMode, message))
		},
	}
}

func newRoastListCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use: "list", Short: "List private roasts",
		Args: roastExactArgs(0, "roast list does not accept arguments"), ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			runLegacyLeaf(cmd, args, state, func(canonical []string) int {
				return runRoastList(ctx, canonical, state.runtime, state.jsonMode, state.serverOverride, state.timeout)
			})
		},
	}
	addPageFlags(cmd.Flags())
	cmd.Flags().String("search", "", "Search roast text")
	cmd.Flags().String("roast-at-from", "", "Earliest roast time in RFC3339 form")
	cmd.Flags().String("roast-at-to", "", "Latest roast time in RFC3339 form")
	cmd.Flags().String("machine", "", "Filter by machine text")
	cmd.Flags().String("state", "", "Filter by roast state")
	cmd.Flags().String("label-id", "", "Filter by label UUID")
	registerStaticFlagCompletion(cmd, "state", "awaiting_profile", "parsed", "parse_failed")
	disableFlagFileCompletion(cmd, "limit", "cursor", "all", "search", "roast-at-from", "roast-at-to", "machine", "label-id")
	return cmd
}

func newRoastShowCommand(ctx context.Context, state *cobraState) *cobra.Command {
	return &cobra.Command{
		Use: "show ROAST_UUID", Short: "Show a private roast",
		Args: roastExactArgs(1, "roast show requires one ROAST_UUID"), ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(_ *cobra.Command, args []string) {
			setCommandExit(state, runRoastShow(ctx, args[0], state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
}

func newRoastRevisionsCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use: "revisions ROAST_UUID", Short: "List immutable roast revisions",
		Args: roastExactArgs(1, "roast revisions requires one ROAST_UUID"), ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			setCommandExit(state, runRoastRevisions(ctx, args[0], canonicalLegacyArgs(cmd, nil), state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	addPageFlags(cmd.Flags())
	disableFlagFileCompletion(cmd, "limit", "cursor", "all")
	return cmd
}

func newRoastChartDownloadCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use: "download ROAST_UUID DESTINATION", Short: "Download the current validated chart JSON",
		Args:              roastExactArgs(2, "roast chart download requires ROAST_UUID DESTINATION"),
		ValidArgsFunction: fileCompletionAfter(1),
		Run: func(cmd *cobra.Command, args []string) {
			force, _ := cmd.Flags().GetBool("force")
			setCommandExit(state, runRoastChartDownload(ctx, args[0], args[1], force, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	cmd.Flags().Bool("force", false, "Replace an existing destination")
	disableFlagFileCompletion(cmd, "force")
	return cmd
}

func newRoastProfileDownloadCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use: "download ROAST_UUID REVISION_NUMBER DESTINATION", Short: "Download one immutable raw profile",
		Args:              roastExactArgs(3, "roast profile download requires ROAST_UUID REVISION_NUMBER DESTINATION"),
		ValidArgsFunction: fileCompletionAfter(2),
		Run: func(cmd *cobra.Command, args []string) {
			force, _ := cmd.Flags().GetBool("force")
			setCommandExit(state, runRoastProfileDownload(ctx, args[0], args[1], args[2], force, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	cmd.Flags().Bool("force", false, "Replace an existing destination")
	disableFlagFileCompletion(cmd, "force")
	return cmd
}

func newRoastCommentListCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use: "list ROAST_UUID", Short: "List private roast comments",
		Args: roastExactArgs(1, "roast comment list requires one ROAST_UUID"), ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			setCommandExit(state, runRoastComments(ctx, args[0], canonicalLegacyArgs(cmd, nil), state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	addPageFlags(cmd.Flags())
	disableFlagFileCompletion(cmd, "limit", "cursor", "all")
	return cmd
}

func newRoastReviewPostCommand(ctx context.Context, state *cobraState) *cobra.Command {
	cmd := &cobra.Command{
		Use: "post ROAST_UUID", Short: "Post one fixed revision-bound review",
		Args: roastExactArgs(1, "roast review post requires one ROAST_UUID"), ValidArgsFunction: cobra.NoFileCompletions,
		Run: func(cmd *cobra.Command, args []string) {
			revisionSHA, _ := cmd.Flags().GetString("revision-sha256")
			template, _ := cmd.Flags().GetString("template-version")
			bodyFile, _ := cmd.Flags().GetString("body-file")
			if !requiredChangedStringFlag(cmd, "revision-sha256", revisionSHA) || !requiredChangedStringFlag(cmd, "template-version", template) || !requiredChangedStringFlag(cmd, "body-file", bodyFile) {
				setCommandExit(state, roastUsageFailure(state.runtime, state.jsonMode, "roast review post requires --revision-sha256 SHA256, --template-version VERSION, and --body-file FILE"))
				return
			}
			setCommandExit(state, runRoastReviewPost(ctx, args[0], revisionSHA, template, bodyFile, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
	cmd.Flags().String("revision-sha256", "", "Immutable revision SHA-256")
	cmd.Flags().String("template-version", "", "Fixed review template version")
	cmd.Flags().String("body-file", "", "Review body file")
	disableFlagFileCompletion(cmd, "revision-sha256")
	registerStaticFlagCompletion(cmd, "template-version", api.ReviewTemplateVersion)
	_ = cmd.RegisterFlagCompletionFunc("body-file", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveDefault
	})
	return cmd
}

func requiredChangedStringFlag(cmd *cobra.Command, name, value string) bool {
	flag := cmd.Flags().Lookup(name)
	return flag != nil && flag.Changed && strings.TrimSpace(value) != ""
}

func roastExactArgs(count int, message string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == count {
			return nil
		}
		return &cobraValidationError{failure: roastUsageError(message)}
	}
}

func fileCompletionAfter(nonFileCount int) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) == nonFileCount {
			return nil, cobra.ShellCompDirectiveDefault
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func knownRoastCommandPath(args []string) string {
	command, commandIndex, ok := nextCommandToken(args, 0)
	if !ok || command != "roast" {
		return "roast"
	}
	group, groupIndex, ok := nextCommandToken(args, commandIndex+1)
	if !ok {
		return "roast"
	}
	switch group {
	case "list", "show", "revisions":
		return "roast " + group
	case "chart", "profile", "comment", "review":
		leaf, _, ok := nextCommandToken(args, groupIndex+1)
		if ok && ((group == "chart" || group == "profile") && leaf == "download" || group == "comment" && leaf == "list" || group == "review" && leaf == "post") {
			return "roast " + group + " " + leaf
		}
		return "roast " + group
	default:
		return "roast"
	}
}

func roastCobraParseFailureMessage(path string) string {
	switch path {
	case "roast":
		return "Unknown roast command"
	case "roast chart":
		return "Unknown roast chart command"
	case "roast profile":
		return "Unknown roast profile command"
	case "roast comment":
		return "Unknown roast comment command"
	case "roast review":
		return "Unknown roast review command"
	case "roast list":
		return "Invalid roast list option"
	case "roast show":
		return "roast show requires one ROAST_UUID"
	case "roast revisions":
		return "Invalid roast revisions option"
	case "roast chart download":
		return "roast chart download requires ROAST_UUID DESTINATION"
	case "roast profile download":
		return "roast profile download requires ROAST_UUID REVISION_NUMBER DESTINATION"
	case "roast comment list":
		return "Invalid roast comment list option"
	case "roast review post":
		return "Invalid roast review post option"
	default:
		return ""
	}
}
