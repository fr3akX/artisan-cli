package command

import (
	"context"
	"errors"
	"time"

	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/securefile"
)

const (
	authStateLockFileName = ".auth-state.lock"
	authStateLockMaxWait  = 30 * time.Second
)

func acquireAuthStateLock(ctx context.Context, configDir string) (func() error, *output.Error) {
	dir, err := config.ResolveDir(configDir)
	if err == nil {
		var release func() error
		release, err = securefile.AcquirePrivateLock(ctx, dir, authStateLockFileName, authStateLockMaxWait)
		if err == nil {
			return release, nil
		}
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return nil, interruptionFailure()
	}
	return nil, &output.Error{ExitCode: 3, Code: "configuration_error", Message: "Configuration is missing or unsafe"}
}

func interruptionFailure() *output.Error {
	return &output.Error{ExitCode: 130, Code: "interrupted", Message: "Operation interrupted"}
}
