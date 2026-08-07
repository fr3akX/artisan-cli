// Package command parses and executes artisan commands.
package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/release"
)

const usageExitCode = 2

// Run parses args, executes a command, and returns the process exit code.
func Run(ctx context.Context, args []string, runtime Runtime) int {
	_ = ctx

	flags := flag.NewFlagSet("artisan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonMode := flags.Bool("json", false, "emit a JSON envelope")
	server := flags.String("server", "", "override the Artisan Server URL")
	timeout := flags.Duration("timeout", 30*time.Second, "request timeout")
	if err := flags.Parse(args); err != nil {
		return writeFailure(runtime, *jsonMode, output.Error{
			ExitCode: usageExitCode,
			Code:     "usage",
			Message:  err.Error(),
		})
	}
	_ = server
	_ = timeout

	remaining := flags.Args()
	if len(remaining) == 0 {
		return writeFailure(runtime, *jsonMode, output.Error{
			ExitCode: usageExitCode,
			Code:     "usage",
			Message:  "A command is required",
		})
	}

	switch remaining[0] {
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
