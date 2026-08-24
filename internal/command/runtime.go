package command

import (
	"context"
	"io"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/output"
)

type roastChartDownloadFunc func(context.Context, *api.Client, string, string, bool) (api.RoastChartDownload, *output.Error)
type roastProfileDownloadFunc func(context.Context, *api.Client, string, int64, string, bool) (api.RoastProfileDownload, *output.Error)

// Runtime contains process resources used by commands.
type Runtime struct {
	In           io.Reader
	Out          io.Writer
	Err          io.Writer
	Getenv       func(string) string
	ConfigDir    string
	IsTerminal   func(fd int) bool
	ReadPassword func(fd int) ([]byte, error)

	// Deterministic command-level download result seams. Production runtimes
	// leave these nil and call the authenticated API client directly.
	roastChartDownload   roastChartDownloadFunc
	roastProfileDownload roastProfileDownloadFunc
}
