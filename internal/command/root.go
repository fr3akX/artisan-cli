// Package command parses and executes artisan commands.
package command

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const (
	usageExitCode = 2
	// MaxGlobalTimeout is the finite upper bound for all command network activity.
	MaxGlobalTimeout = 5 * time.Minute
)

// Run parses args, executes a command, and returns the process exit code.
func Run(ctx context.Context, args []string, runtime Runtime) int {
	runtime = normalizeRuntime(runtime)
	args = normalizeLegacySingleDashArgs(args)
	root, state := newRootCommand(ctx, runtime, args)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		return writeCobraFailure(runtime, state, args, err)
	}
	return state.exitCode
}

// jsonModeForParseFailure determines the final valid --json setting in the
// complete global-option prefix. The flag package stops at the first malformed
// value, which may be before a later --json option that still expresses the
// caller's output intent.
func jsonModeForParseFailure(args []string) bool {
	jsonMode := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}

		name, value, hasValue, isFlag := splitGlobalFlag(arg)
		if !isFlag {
			break
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
		case "server", "timeout":
			if !hasValue && i+1 < len(args) {
				i++
			}
		}
	}
	return jsonMode
}

func splitGlobalFlag(arg string) (name, value string, hasValue, isFlag bool) {
	if len(arg) < 2 || arg[0] != '-' {
		return "", "", false, false
	}

	name = arg[1:]
	if strings.HasPrefix(name, "-") {
		name = name[1:]
	}
	name, value, hasValue = strings.Cut(name, "=")
	return name, value, hasValue, true
}

func normalizeRuntime(runtime Runtime) Runtime {
	if runtime.In == nil {
		runtime.In = strings.NewReader("")
	}
	if runtime.Out == nil {
		runtime.Out = io.Discard
	}
	if runtime.Err == nil {
		runtime.Err = io.Discard
	}
	if runtime.Getenv == nil {
		runtime.Getenv = func(string) string { return "" }
	}
	return runtime
}

func writeFailure(runtime Runtime, jsonMode bool, failure output.Error) int {
	writer := runtime.Err
	if jsonMode {
		writer = runtime.Out
	}
	if err := output.WriteFailure(writer, jsonMode, failure); err != nil {
		return reportWriteError(runtime.Err, err)
	}
	return failure.ExitCode
}

func reportWriteError(w io.Writer, err error) int {
	_, _ = fmt.Fprintf(w, "failed to write output: %v\n", err)
	return 1
}
