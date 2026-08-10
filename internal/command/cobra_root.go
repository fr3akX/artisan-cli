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
	path := knownCommandPath(result)
	result = shieldInventoryImageDashPositionals(result, path)
	for i := 0; i < len(result); i++ {
		arg := result[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		nameValue := strings.TrimLeft(arg, "-")
		name, _, hasValue := strings.Cut(nameValue, "=")
		if !strings.HasPrefix(arg, "--") && isKnownLegacySingleDashFlagForPath(name, path) {
			result[i] = "-" + arg
		}
		if cobraFlagConsumesValueForPath(name, path) && !hasValue && i+1 < len(result) {
			i++
		}
	}
	return result
}

type inventoryImageArgPartition struct {
	options     []string
	positionals []string
	optionCount int
	ok          bool
}

func shieldInventoryImageDashPositionals(args []string, path string) []string {
	if path != "inventory image add" && path != "inventory image download" {
		return args
	}
	_, commandIndex, ok := nextCommandToken(args, 0)
	if !ok {
		return args
	}
	_, groupIndex, ok := nextCommandToken(args, commandIndex+1)
	if !ok {
		return args
	}
	_, leafIndex, ok := nextCommandToken(args, groupIndex+1)
	if !ok {
		return args
	}
	partition := partitionInventoryImageArgs(args[leafIndex+1:], path, 0, 0)
	if !partition.ok {
		return args
	}
	hasDashPositional := false
	for _, positional := range partition.positionals {
		if strings.HasPrefix(positional, "-") {
			hasDashPositional = true
			break
		}
	}
	if !hasDashPositional {
		return args
	}
	result := append([]string(nil), args[:leafIndex+1]...)
	result = append(result, partition.options...)
	result = append(result, "--")
	return append(result, partition.positionals...)
}

func partitionInventoryImageArgs(args []string, path string, index, positionalCount int) inventoryImageArgPartition {
	if path == "inventory image download" && positionalCount > 3 {
		return inventoryImageArgPartition{}
	}
	if index == len(args) {
		valid := positionalCount >= 2
		if path == "inventory image download" {
			valid = positionalCount == 3
		}
		return inventoryImageArgPartition{ok: valid}
	}
	if args[index] == "--" {
		count := positionalCount + len(args) - index - 1
		valid := count >= 2
		if path == "inventory image download" {
			valid = count == 3
		}
		if !valid {
			return inventoryImageArgPartition{}
		}
		return inventoryImageArgPartition{positionals: append([]string(nil), args[index+1:]...), ok: true}
	}

	raw := args[index]
	name, _, hasValue, isFlag := splitGlobalFlag(raw)
	known, consumesValue := inventoryImageRouteFlag(path, name)
	if !isFlag {
		return prependInventoryImagePositional(raw, partitionInventoryImageArgs(args, path, index+1, positionalCount+1))
	}
	shieldAfter := 1
	if path == "inventory image download" {
		shieldAfter = 2
	}
	if !known {
		if positionalCount >= shieldAfter && !strings.HasPrefix(raw, "--") {
			return prependInventoryImagePositional(raw, partitionInventoryImageArgs(args, path, index+1, positionalCount+1))
		}
		return prependInventoryImageOption([]string{raw}, partitionInventoryImageArgs(args, path, index+1, positionalCount))
	}

	next := index + 1
	optionTokens := []string{raw}
	if consumesValue && !hasValue && next < len(args) {
		optionTokens = append(optionTokens, args[next])
		next++
	}
	best := prependInventoryImageOption(optionTokens, partitionInventoryImageArgs(args, path, next, positionalCount))
	reservedHelp := name == "help" || name == "h"
	reservedBoolean := !consumesValue && (strings.HasPrefix(raw, "--") || index == len(args)-1)
	if path == "inventory image download" && positionalCount >= shieldAfter && !strings.HasPrefix(raw, "--") && !reservedHelp && !reservedBoolean {
		asPositional := prependInventoryImagePositional(raw, partitionInventoryImageArgs(args, path, index+1, positionalCount+1))
		if asPositional.ok && (!best.ok || asPositional.optionCount >= best.optionCount) {
			best = asPositional
		}
	}
	return best
}

func inventoryImageRouteFlag(path, name string) (known, consumesValue bool) {
	switch name {
	case "json", "help", "h":
		return true, false
	case "server", "timeout":
		return true, true
	}
	if path == "inventory image add" {
		switch name {
		case "caption", "alt-text", "cover", "idempotency-key":
			return true, true
		}
	}
	if path == "inventory image download" {
		switch name {
		case "variant":
			return true, true
		case "force":
			return true, false
		}
	}
	return false, false
}

func prependInventoryImageOption(tokens []string, partition inventoryImageArgPartition) inventoryImageArgPartition {
	if !partition.ok {
		return partition
	}
	partition.options = append(append([]string(nil), tokens...), partition.options...)
	partition.optionCount++
	return partition
}

func prependInventoryImagePositional(value string, partition inventoryImageArgPartition) inventoryImageArgPartition {
	if !partition.ok {
		return partition
	}
	partition.positionals = append([]string{value}, partition.positionals...)
	return partition
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

func isKnownLegacySingleDashFlagForPath(name, path string) bool {
	if isCobraGlobalFlag(name) {
		return true
	}
	if strings.HasPrefix(path, "inventory lot create") {
		return cobraLotFieldFlag(name) || stringIn(name, "opening-grams", "opening-reason", "opening-reference", "from-json", "idempotency-key", "image", "image-caption", "image-alt-text", "image-cover")
	}
	if strings.HasPrefix(path, "inventory lot update") {
		return cobraLotFieldFlag(name) || stringIn(name, "clear", "from-json", "idempotency-key")
	}
	if path == "inventory lot archive" {
		return stringIn(name, "yes", "idempotency-key")
	}
	if path == "inventory lot restore" || path == "inventory image reorder" {
		return name == "idempotency-key"
	}
	switch path {
	case "inventory image add":
		return stringIn(name, "caption", "alt-text", "cover", "idempotency-key")
	case "inventory image update":
		return stringIn(name, "caption", "alt-text", "clear-caption", "clear-alt-text", "cover", "idempotency-key")
	case "inventory image delete":
		return stringIn(name, "yes", "idempotency-key")
	case "inventory image download":
		return stringIn(name, "variant", "force")
	}
	return isKnownLegacySingleDashFlag(name)
}

func isKnownLegacySingleDashFlag(name string) bool {
	return stringIn(name,
		"json", "server", "timeout", "directory", "force", "token-stdin",
		"limit", "cursor", "all", "q", "state", "availability", "conflict", "roast-uuid",
		"grams", "reason", "reference", "occurred-at", "yes", "idempotency-key",
		"client-reservation-uuid", "client-instance-uuid", "lot-id", "planned-grams",
		"actual-grams", "lot", "note",
	)
}

func cobraLotFieldFlag(name string) bool {
	return stringIn(name,
		"name", "origin", "producer", "supplier", "external-reference", "received-date", "crop-year", "price-per-kg-eur", "varietal",
		"sca-score", "processing-method", "processing-detail", "altitude-min-metres", "altitude-max-metres", "notes",
	)
}

func stringIn(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func cobraFlagConsumesValueForPath(name, path string) bool {
	if name == "cover" && path == "inventory image update" {
		return false
	}
	if isKnownLegacySingleDashFlagForPath(name, path) {
		switch name {
		case "json", "force", "token-stdin", "all", "yes", "clear-caption", "clear-alt-text":
			return false
		}
		return true
	}
	return false
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
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().BoolVar(&state.jsonMode, "json", false, "emit a JSON envelope")
	root.PersistentFlags().StringVar(&state.serverOverride, "server", "", "override the Artisan Server URL")
	root.PersistentFlags().DurationVar(&state.timeout, "timeout", 30*time.Second, "request timeout")

	root.AddCommand(
		newAuthCommand(ctx, state),
		newInventoryCommand(ctx, state),
		newSkillCommand(ctx, state),
		newCompletionCommand(root, state),
		newVersionCommand(state),
	)
	root.InitDefaultHelpCmd()
	for _, child := range root.Commands() {
		if child.Name() == "help" {
			child.Hidden = true
		}
	}

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

func newCompletionCommand(root *cobra.Command, state *cobraState) *cobra.Command {
	completion := &cobra.Command{
		Use:   "completion",
		Short: "Generate a shell completion program",
		Run: func(_ *cobra.Command, args []string) {
			message := "A completion command is required"
			if len(args) != 0 {
				message = "Unknown completion command"
			}
			setCommandExit(state, writeFailure(state.runtime, state.jsonMode, output.Error{
				ExitCode: usageExitCode,
				Code:     "usage",
				Message:  message,
			}))
		},
	}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		shell := shell
		leaf := &cobra.Command{
			Use:   shell,
			Short: "Generate " + shell + " completion",
			Args:  cobra.NoArgs,
			Run: func(_ *cobra.Command, _ []string) {
				var err error
				switch shell {
				case "bash":
					err = root.GenBashCompletionV2(state.runtime.Out, true)
				case "zsh":
					err = root.GenZshCompletion(state.runtime.Out)
				case "fish":
					err = root.GenFishCompletion(state.runtime.Out, true)
				case "powershell":
					err = root.GenPowerShellCompletionWithDesc(state.runtime.Out)
				}
				if err != nil {
					setCommandExit(state, reportWriteError(state.runtime.Err, err))
					return
				}
				setCommandExit(state, 0)
			},
		}
		completion.AddCommand(leaf)
	}
	return completion
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
		Run: func(_ *cobra.Command, args []string) {
			message := "A skill command is required"
			if len(args) != 0 {
				message = "Unknown skill command"
			}
			setCommandExit(state, skillUsageFailure(state.runtime, state.jsonMode, message))
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
		case "completion":
			message = "Unknown completion command"
		case "inventory":
			message = "Unknown inventory command"
		case "inventory lot":
			message = "Unknown inventory lot command"
		case "inventory reservation":
			message = "Unknown inventory reservation command"
		case "inventory conflict":
			message = "Unknown inventory conflict command"
		case "inventory image":
			message = "Unknown inventory image command"
		case "":
			if command := firstCommandArg(args); command != "" {
				message = "Unknown command: " + command
			}
		}
	} else {
		jsonMode = cobraJSONModeForParseFailure(args)
		if !isGlobalFlagParseError(err) {
			switch path {
			case "auth":
				message = "Unknown auth command"
			case "auth login":
				message = "Invalid auth login option"
			case "auth status":
				message = "auth status does not accept arguments"
			case "auth logout":
				message = "auth logout does not accept arguments"
			case "version":
				message = "version does not accept arguments"
			case "skill":
				message = "Unknown skill command"
			case "skill install":
				message = "skill install requires --directory ROOT"
			case "skill show":
				message = "skill show does not accept arguments"
			case "completion":
				message = "Unknown completion command"
			case "completion bash", "completion zsh", "completion fish", "completion powershell":
				message = path + " does not accept arguments"
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
		"client-instance-uuid", "lot-id", "planned-grams", "actual-grams", "lot", "note",
		"name", "origin", "producer", "supplier", "external-reference", "received-date", "crop-year", "price-per-kg-eur", "varietal",
		"sca-score", "processing-method", "processing-detail", "altitude-min-metres", "altitude-max-metres", "notes",
		"opening-grams", "opening-reason", "opening-reference", "from-json", "image", "image-caption", "image-alt-text",
		"image-cover", "clear", "caption", "alt-text", "variant":
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
	command, commandIndex, ok := nextRouteCommandToken(args, 0)
	if !ok {
		return ""
	}
	switch command {
	case "auth":
		if child, _, ok := nextRouteCommandToken(args, commandIndex+1); ok && stringIn(child, "login", "status", "logout") {
			return "auth " + child
		}
		return "auth"
	case "version":
		return "version"
	case "completion":
		if child, _, ok := nextRouteCommandToken(args, commandIndex+1); ok && stringIn(child, "bash", "zsh", "fish", "powershell") {
			return "completion " + child
		}
		return "completion"
	case "skill":
		if child, _, ok := nextRouteCommandToken(args, commandIndex+1); ok && stringIn(child, "show", "install") {
			return "skill " + child
		}
		return "skill"
	case "inventory":
		return knownInventoryCommandPath(args)
	default:
		return ""
	}
}

func nextRouteCommandToken(args []string, start int) (string, int, bool) {
	for index := start; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
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
