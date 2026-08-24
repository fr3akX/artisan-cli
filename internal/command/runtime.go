package command

import (
	"context"
	"io"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/output"
)

type roastChartDownloadFunc func(context.Context, *api.Client, string, string, bool) (api.RoastChartDownload, *output.Error)
type roastProfileDownloadFunc func(context.Context, *api.Client, string, int64, string, bool) (api.RoastProfileDownload, *output.Error)

type roastDownloadHooks struct {
	chart   roastChartDownloadFunc
	profile roastProfileDownloadFunc
}

type roastDownloadHooksContextKey struct{}

func withRoastDownloadHooks(ctx context.Context, hooks roastDownloadHooks) context.Context {
	return context.WithValue(ctx, roastDownloadHooksContextKey{}, hooks)
}

func roastDownloadHooksFromContext(ctx context.Context) roastDownloadHooks {
	hooks, _ := ctx.Value(roastDownloadHooksContextKey{}).(roastDownloadHooks)
	return hooks
}

// Runtime contains process resources used by commands.
type Runtime struct {
	In           io.Reader
	Out          io.Writer
	Err          io.Writer
	Getenv       func(string) string
	ConfigDir    string
	IsTerminal   func(fd int) bool
	ReadPassword func(fd int) ([]byte, error)
}
