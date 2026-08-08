package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/release"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type cobraState struct {
	runtime        Runtime
	jsonMode       bool
	serverOverride string
	timeout        time.Duration
	exitCode       int
}

type cobraValidationError struct {
	failure output.Error
}

func (err *cobraValidationError) Error() string {
	return err.failure.Message
}

func setCommandExit(state *cobraState, code int) {
	state.exitCode = code
}

func normalizeLegacySingleDashArgs(args []string) []string {
	result := append([]string(nil), args...)
	rawPassthrough := inventoryRawPassthroughIndex(result)
	for i := 0; i < len(result); i++ {
		if rawPassthrough >= 0 && i >= rawPassthrough {
			break
		}
		arg := result[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		nameValue := strings.TrimLeft(arg, "-")
		name, _, hasValue := strings.Cut(nameValue, "=")
		if !strings.HasPrefix(arg, "--") && isKnownLegacySingleDashFlag(name) {
			result[i] = "-" + arg
		}
		if cobraFlagConsumesValue(name) && !hasValue && i+1 < len(result) {
			i++
		}
	}
	return result
}

func inventoryRawPassthroughIndex(args []string) int {
	command, commandIndex, ok := nextCommandToken(args, 0)
	if !ok || command != "inventory" {
		return -1
	}
	group, groupIndex, ok := nextCommandToken(args, commandIndex+1)
	if !ok {
		return -1
	}
	if group == "image" {
		return groupIndex
	}
	if group != "lot" {
		return -1
	}
	operation, operationIndex, ok := nextCommandToken(args, groupIndex+1)
	if !ok {
		return -1
	}
	switch operation {
	case "create", "update", "archive", "restore":
		return operationIndex
	default:
		return -1
	}
}

func nextCommandToken(args []string, start int) (string, int, bool) {
	for index := start; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			if index+1 < len(args) {
				return args[index+1], index + 1, true
			}
			return "", 0, false
		}
		name, _, hasValue, isFlag := splitGlobalFlag(arg)
		if !isFlag {
			return arg, index, true
		}
		if !isCobraGlobalFlag(name) {
			return arg, index, true
		}
		if (name == "server" || name == "timeout") && !hasValue && index+1 < len(args) {
			index++
		}
	}
	return "", 0, false
}

func isCobraGlobalFlag(name string) bool {
	switch name {
	case "json", "server", "timeout":
		return true
	default:
		return false
	}
}

func isKnownLegacySingleDashFlag(name string) bool {
	switch name {
	case "json", "server", "timeout", "directory", "force", "token-stdin",
		"limit", "cursor", "all", "q", "state", "availability", "conflict", "roast-uuid",
		"grams", "reason", "reference", "occurred-at", "yes", "idempotency-key",
		"client-reservation-uuid", "client-instance-uuid", "lot-id", "planned-grams",
		"actual-grams", "lot", "note":
		return true
	default:
		return false
	}
}

func canonicalLegacyArgs(cmd *cobra.Command, positionals []string) []string {
	result := make([]string, 0, cmd.Flags().NFlag()+len(positionals))
	cmd.LocalNonPersistentFlags().VisitAll(func(item *pflag.Flag) {
		if !item.Changed {
			return
		}
		if item.Value.Type() == "stringArray" {
			values, err := cmd.Flags().GetStringArray(item.Name)
			if err != nil {
				panic(err)
			}
			for _, value := range values {
				result = append(result, "--"+item.Name+"="+value)
			}
			return
		}
		result = append(result, "--"+item.Name+"="+item.Value.String())
	})
	return append(result, positionals...)
}

func newRootCommand(ctx context.Context, runtime Runtime, _ []string) (*cobra.Command, *cobraState) {
	state := &cobraState{
		runtime: runtime,
		timeout: 30 * time.Second,
	}
	root := &cobra.Command{
		Use:              "artisan",
		Short:            "Artisan green-coffee inventory command line client",
		SilenceErrors:    true,
		SilenceUsage:     true,
		TraverseChildren: true,
		Run: func(_ *cobra.Command, args []string) {
			message := "A command is required"
			if len(args) != 0 {
				message = "Unknown command: " + args[0]
			}
			setCommandExit(state, writeFailure(runtime, state.jsonMode, output.Error{
				ExitCode: usageExitCode,
				Code:     "usage",
				Message:  message,
			}))
		},
	}
	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		serverFlag := root.PersistentFlags().Lookup("server")
		if serverFlag != nil && serverFlag.Changed {
			normalized, err := config.NormalizeServerURL(state.serverOverride)
			if err != nil {
				return &cobraValidationError{failure: output.Error{
					ExitCode: usageExitCode,
					Code:     "invalid_server_url",
					Message:  "Server URL is invalid",
				}}
			}
			state.serverOverride = normalized
		}
		if state.timeout <= 0 {
			return &cobraValidationError{failure: output.Error{
				ExitCode: usageExitCode,
				Code:     "invalid_timeout",
				Message:  "Timeout must be greater than zero",
			}}
		}
		if state.timeout > MaxGlobalTimeout {
			return &cobraValidationError{failure: output.Error{
				ExitCode: usageExitCode,
				Code:     "invalid_timeout",
				Message:  "Timeout must not exceed " + MaxGlobalTimeout.String(),
			}}
		}
		return nil
	}
	root.SetOut(runtime.Out)
	root.SetErr(runtime.Err)
	root.PersistentFlags().BoolVar(&state.jsonMode, "json", false, "emit a JSON envelope")
	root.PersistentFlags().StringVar(&state.serverOverride, "server", "", "override the Artisan Server URL")
	root.PersistentFlags().DurationVar(&state.timeout, "timeout", 30*time.Second, "request timeout")

	root.AddCommand(
		newAuthCommand(ctx, state),
		newInventoryCommand(ctx, state),
		newSkillCommand(ctx, state),
		newVersionCommand(state),
	)

	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		var rendered bytes.Buffer
		previous := cmd.OutOrStdout()
		cmd.SetOut(&rendered)
		defaultHelp(cmd, args)
		cmd.SetOut(previous)
		setCommandExit(state, writeCommandHelp(runtime, state.jsonMode, rendered.String()))
	})

	return root, state
}

func newLegacyGroupCommand(
	ctx context.Context,
	state *cobraState,
	use string,
	short string,
	run func(context.Context, []string, Runtime, bool, string, time.Duration) int,
) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		Run: func(_ *cobra.Command, args []string) {
			setCommandExit(state, run(ctx, args, state.runtime, state.jsonMode, state.serverOverride, state.timeout))
		},
	}
}

func newVersionCommand(state *cobraState) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(_ *cobra.Command, args []string) {
			if len(args) != 0 {
				setCommandExit(state, writeFailure(state.runtime, state.jsonMode, output.Error{
					ExitCode: usageExitCode,
					Code:     "usage",
					Message:  "version does not accept arguments",
				}))
				return
			}
			info := release.Info()
			err := output.WriteSuccess(state.runtime.Out, state.jsonMode, info, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "artisan %s (%s)\n", info.Version, info.Commit)
				return err
			})
			if err != nil {
				setCommandExit(state, reportWriteError(state.runtime.Err, err))
			}
		},
	}
}

func newSkillCommand(ctx context.Context, state *cobraState) *cobra.Command {
	skill := &cobra.Command{
		Use:   "skill",
		Short: "Install or inspect the embedded agent skill",
		Run: func(_ *cobra.Command, _ []string) {
			setCommandExit(state, skillUsageFailure(state.runtime, state.jsonMode, "A skill command is required"))
		},
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Show the embedded agent skill",
		Run: func(_ *cobra.Command, args []string) {
			if len(args) != 0 {
				setCommandExit(state, skillUsageFailure(state.runtime, state.jsonMode, "skill show does not accept arguments"))
				return
			}
			setCommandExit(state, runSkill(ctx, []string{"show"}, state.runtime, state.jsonMode))
		},
	}
	install := &cobra.Command{
		Use:   "install --directory ROOT [--force]",
		Short: "Install the embedded agent skill",
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) != 0 {
				setCommandExit(state, skillUsageFailure(state.runtime, state.jsonMode, "skill install requires --directory ROOT"))
				return
			}
			setCommandExit(state, runSkillInstall(canonicalLegacyArgs(cmd, args), state.runtime, state.jsonMode))
		},
	}
	install.Flags().String("directory", "", "agent skill root")
	install.Flags().Bool("force", false, "replace differing content")
	skill.AddCommand(show, install)
	return skill
}

func writeCobraFailure(runtime Runtime, state *cobraState, args []string, err error) int {
	var validation *cobraValidationError
	if errors.As(err, &validation) {
		return writeFailure(runtime, state.jsonMode, validation.failure)
	}

	jsonMode := state.jsonMode
	message := "Invalid global option"
	path := knownCommandPath(args)
	if strings.HasPrefix(err.Error(), "unknown command") {
		jsonMode = cobraJSONModeForParseFailure(args)
		switch path {
		case "auth":
			message = "Unknown auth command"
		case "skill":
			message = "Unknown skill command"
		case "inventory":
			message = "Unknown inventory command"
		case "inventory lot":
			message = "Unknown inventory lot command"
		case "inventory reservation":
			message = "Unknown inventory reservation command"
		case "inventory conflict":
			message = "Unknown inventory conflict command"
		case "":
			if command := firstCommandArg(args); command != "" {
				message = "Unknown command: " + command
			}
		}
	} else {
		jsonMode = cobraJSONModeForParseFailure(args)
		if !isGlobalFlagParseError(err) {
			switch path {
			case "auth login":
				message = "Invalid auth login option"
			case "auth status":
				message = "auth status does not accept arguments"
			case "auth logout":
				message = "auth logout does not accept arguments"
			case "version":
				message = "version does not accept arguments"
			case "skill", "skill install":
				message = "skill install requires --directory ROOT"
			case "skill show":
				message = "skill show does not accept arguments"
			default:
				if inventoryMessage := inventoryCobraParseFailureMessage(path); inventoryMessage != "" {
					message = inventoryMessage
				}
			}
		}
	}
	return writeFailure(runtime, jsonMode, output.Error{
		ExitCode: usageExitCode,
		Code:     "usage",
		Message:  message,
	})
}

func cobraJSONModeForParseFailure(args []string) bool {
	command := firstCommandArg(args)
	if command != "auth" && command != "inventory" {
		return jsonModeForParseFailure(args)
	}

	jsonMode := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, value, hasValue, isFlag := splitGlobalFlag(arg)
		if !isFlag {
			continue
		}
		switch name {
		case "json":
			if !hasValue {
				jsonMode = true
				continue
			}
			if parsed, err := strconv.ParseBool(value); err == nil {
				jsonMode = parsed
			}
		default:
			if cobraFlagConsumesValue(name) && !hasValue && i+1 < len(args) {
				i++
			}
		}
	}
	return jsonMode
}

func cobraFlagConsumesValue(name string) bool {
	switch name {
	case "server", "timeout", "directory", "limit", "cursor", "q", "state", "availability", "conflict", "roast-uuid",
		"grams", "reason", "reference", "occurred-at", "idempotency-key", "client-reservation-uuid",
		"client-instance-uuid", "lot-id", "planned-grams", "actual-grams", "lot", "note":
		return true
	default:
		return false
	}
}

func isGlobalFlagParseError(err error) bool {
	text := err.Error()
	return strings.Contains(text, "--json") || strings.Contains(text, "--server") || strings.Contains(text, "--timeout")
}

func knownCommandPath(args []string) string {
	command := firstCommandArg(args)
	switch command {
	case "auth":
		for _, arg := range args {
			switch arg {
			case "login":
				return "auth login"
			case "status":
				return "auth status"
			case "logout":
				return "auth logout"
			}
		}
		return "auth"
	case "version":
		return "version"
	case "skill":
		for _, arg := range args {
			switch arg {
			case "show":
				return "skill show"
			case "install":
				return "skill install"
			}
		}
		return "skill"
	case "inventory":
		return knownInventoryCommandPath(args)
	default:
		return ""
	}
}

func firstCommandArg(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		name, _, hasValue, isFlag := splitGlobalFlag(arg)
		if !isFlag {
			return arg
		}
		if (name == "server" || name == "timeout") && !hasValue {
			i++
		}
	}
	return ""
}
