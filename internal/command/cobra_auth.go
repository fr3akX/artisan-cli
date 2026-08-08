package command

import (
	"context"

	"github.com/spf13/cobra"
)

func newAuthCommand(ctx context.Context, state *cobraState) *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Authentication and saved credentials",
		Long:  "Authenticate with Artisan Server and manage the saved credential.",
		Example: `  artisan auth login --server https://artisan.example
  printf '%s\n' "$ARTISAN_TOKEN" | artisan auth login --server https://artisan.example --token-stdin
  artisan auth status
  artisan auth logout`,
		Run: func(_ *cobra.Command, _ []string) {
			setCommandExit(state, authUsageFailure(state.runtime, state.jsonMode, "An auth command is required"))
		},
	}

	login := &cobra.Command{
		Use:   "login",
		Short: "Authenticate and save the credential",
		Long:  "Authenticate using a hidden token prompt or a token read from standard input, then save the verified credential.",
		Example: `  artisan auth login --server https://artisan.example
  printf '%s\n' "$ARTISAN_TOKEN" | artisan auth login --server https://artisan.example --token-stdin`,
		Run: func(cmd *cobra.Command, args []string) {
			setCommandExit(state, runAuthLogin(
				ctx,
				canonicalLegacyArgs(cmd, args),
				state.runtime,
				state.jsonMode,
				state.serverOverride,
				state.timeout,
			))
		},
	}
	login.Flags().Bool("token-stdin", false, "read the token from standard input")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the authenticated identity",
		Run: func(_ *cobra.Command, args []string) {
			if len(args) != 0 {
				setCommandExit(state, authUsageFailure(state.runtime, state.jsonMode, "auth status does not accept arguments"))
				return
			}
			setCommandExit(state, runAuthStatus(ctx, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}

	logout := &cobra.Command{
		Use:   "logout",
		Short: "Remove the saved credential",
		Run: func(_ *cobra.Command, args []string) {
			if len(args) != 0 {
				setCommandExit(state, authUsageFailure(state.runtime, state.jsonMode, "auth logout does not accept arguments"))
				return
			}
			setCommandExit(state, runAuthLogout(ctx, state.runtime, state.jsonMode))
		},
	}

	auth.AddCommand(login, status, logout)
	return auth
}
