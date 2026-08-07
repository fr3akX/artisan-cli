// Package command parses and executes artisan commands.
package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/release"
)

const usageExitCode = 2

// Run parses args, executes a command, and returns the process exit code.
func Run(ctx context.Context, args []string, runtime Runtime) int {
	flags := flag.NewFlagSet("artisan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonMode := flags.Bool("json", false, "emit a JSON envelope")
	server := flags.String("server", "", "override the Artisan Server URL")
	timeout := flags.Duration("timeout", 30*time.Second, "request timeout")
	if err := flags.Parse(args); err != nil {
		return writeFailure(runtime, jsonModeForParseFailure(args), output.Error{
			ExitCode: usageExitCode,
			Code:     "usage",
			Message:  err.Error(),
		})
	}
	remaining := flags.Args()
	if len(remaining) == 0 {
		return writeFailure(runtime, *jsonMode, output.Error{
			ExitCode: usageExitCode,
			Code:     "usage",
			Message:  "A command is required",
		})
	}

	switch remaining[0] {
	case "auth":
		return runAuth(ctx, remaining[1:], runtime, *jsonMode, *server, *timeout)
	case "version":
		if len(remaining) != 1 {
			return writeFailure(runtime, *jsonMode, output.Error{
				ExitCode: usageExitCode,
				Code:     "usage",
				Message:  "version does not accept arguments",
			})
		}
		info := release.Info()
		err := output.WriteSuccess(runtime.Out, *jsonMode, info, func(w io.Writer) error {
			_, err := fmt.Fprintf(w, "artisan %s (%s)\n", info.Version, info.Commit)
			return err
		})
		if err != nil {
			return reportWriteError(runtime.Err, err)
		}
		return 0
	default:
		return writeFailure(runtime, *jsonMode, output.Error{
			ExitCode: usageExitCode,
			Code:     "usage",
			Message:  "Unknown command: " + remaining[0],
		})
	}
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
