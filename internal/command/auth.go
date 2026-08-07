package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/output"
)

const (
	maxTokenInputBytes = 64 * 1024
	loginTokenSentinel = "artisan-cli-login-token-placeholder"
)

func runAuth(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if len(args) == 0 {
		return authUsageFailure(runtime, jsonMode, "An auth command is required")
	}

	switch args[0] {
	case "login":
		return runAuthLogin(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "status":
		if len(args) != 1 {
			return authUsageFailure(runtime, jsonMode, "auth status does not accept arguments")
		}
		return runAuthStatus(ctx, runtime, jsonMode, serverOverride, timeout)
	case "logout":
		if len(args) != 1 {
			return authUsageFailure(runtime, jsonMode, "auth logout does not accept arguments")
		}
		return runAuthLogout(runtime, jsonMode)
	default:
		return authUsageFailure(runtime, jsonMode, "Unknown auth command")
	}
}

func runAuthLogin(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan auth login", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tokenStdin := flags.Bool("token-stdin", false, "read the token from standard input")
	if err := flags.Parse(args); err != nil {
		return authUsageFailure(runtime, jsonMode, "Invalid auth login option")
	}
	if len(flags.Args()) != 0 {
		return authUsageFailure(runtime, jsonMode, "auth login does not accept arguments")
	}
	if err := recoverLoginTransaction(runtime.ConfigDir); err != nil {
		return writeFailure(runtime, jsonMode, configurationLoadFailure(err))
	}

	serverURL, persistServer, failure := resolveLoginServer(runtime, serverOverride)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	token, failure := readLoginToken(runtime, *tokenStdin)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}

	client, err := api.NewClient(serverURL, token, timeout)
	if err != nil {
		return writeFailure(runtime, jsonMode, clientConfigurationFailure(err))
	}
	identity, apiFailure := client.Identity(ctx)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	if failure := persistLogin(runtime.ConfigDir, token, persistServer); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}

	if err := output.WriteSuccess(runtime.Out, jsonMode, identity, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "Authenticated as %s for %s (%s) with role %s\n",
			identity.User.Nickname,
			identity.Organization.Name,
			identity.Organization.Slug,
			identity.Role,
		)
		return err
	}); err != nil {
		return reportWriteError(runtime.Err, err)
	}
	return 0
}

func runAuthStatus(ctx context.Context, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if err := recoverLoginTransaction(runtime.ConfigDir); err != nil {
		return writeFailure(runtime, jsonMode, configurationLoadFailure(err))
	}
	values, err := loadEffectiveConfiguration(runtime, serverOverride)
	if err != nil {
		return writeFailure(runtime, jsonMode, configurationLoadFailure(err))
	}
	client, err := api.NewClient(values.ServerURL, values.Token, timeout)
	if err != nil {
		return writeFailure(runtime, jsonMode, clientConfigurationFailure(err))
	}
	identity, failure := client.Identity(ctx)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}

	if err := output.WriteSuccess(runtime.Out, jsonMode, identity, func(w io.Writer) error {
		if _, err := fmt.Fprintf(w, "User: %s\n", identity.User.Nickname); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Organization: %s (%s)\n", identity.Organization.Name, identity.Organization.Slug); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "Role: %s\n", identity.Role)
		return err
	}); err != nil {
		return reportWriteError(runtime.Err, err)
	}
	return 0
}

func runAuthLogout(runtime Runtime, jsonMode bool) int {
	if err := recoverLoginTransaction(runtime.ConfigDir); err != nil {
		return writeFailure(runtime, jsonMode, configurationLoadFailure(err))
	}
	if failure := persistLogout(runtime.ConfigDir); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	data := struct {
		LoggedOut bool `json:"logged_out"`
	}{LoggedOut: true}
	if err := output.WriteSuccess(runtime.Out, jsonMode, data, func(w io.Writer) error {
		_, err := fmt.Fprintln(w, "Logged out")
		return err
	}); err != nil {
		return reportWriteError(runtime.Err, err)
	}
	return 0
}

func resolveLoginServer(runtime Runtime, serverOverride string) (serverURL, persistServer string, failure *output.Error) {
	values, err := loadConfiguration(runtime.ConfigDir, runtime.Getenv, serverOverride, true)
	if err != nil {
		result := configurationLoadFailure(err)
		return "", "", &result
	}
	if serverOverride != "" {
		persistServer = values.ServerURL
	}
	return values.ServerURL, persistServer, nil
}

func loadEffectiveConfiguration(runtime Runtime, serverOverride string) (config.Values, error) {
	return loadConfiguration(runtime.ConfigDir, runtime.Getenv, serverOverride, false)
}

func loadConfiguration(configDir string, getenv func(string) string, serverOverride string, login bool) (config.Values, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return config.Load(configDir, func(name string) string {
		if name == "ARTISAN_SERVER_URL" && serverOverride != "" {
			return serverOverride
		}
		if login && name == "ARTISAN_SERVER_TOKEN" {
			return loginTokenSentinel
		}
		return getenv(name)
	})
}

func readLoginToken(runtime Runtime, fromStdin bool) (string, *output.Error) {
	if fromStdin {
		token, err := readBoundedTokenLine(runtime.In)
		if err != nil {
			return "", &output.Error{
				ExitCode: usageExitCode,
				Code:     "invalid_credentials",
				Message:  "Standard input must contain one nonblank token line",
			}
		}
		return token, nil
	}

	fdSource, ok := runtime.In.(interface{ Fd() uintptr })
	if !ok || runtime.IsTerminal == nil || runtime.ReadPassword == nil {
		return "", tokenStdinRequiredFailure()
	}
	fd := int(fdSource.Fd())
	if !runtime.IsTerminal(fd) {
		return "", tokenStdinRequiredFailure()
	}
	if _, err := fmt.Fprint(runtime.Err, "Token: "); err != nil {
		return "", &output.Error{ExitCode: 1, Code: "output_error", Message: "Unable to write the token prompt"}
	}
	secret, err := runtime.ReadPassword(fd)
	_, newlineErr := fmt.Fprintln(runtime.Err)
	if err != nil {
		return "", &output.Error{ExitCode: 1, Code: "credential_input_error", Message: "Unable to read the token"}
	}
	if newlineErr != nil {
		return "", &output.Error{ExitCode: 1, Code: "output_error", Message: "Unable to write the token prompt"}
	}
	if len(secret) > maxTokenInputBytes || strings.TrimSpace(string(secret)) == "" || strings.ContainsAny(string(secret), "\r\n") {
		return "", &output.Error{ExitCode: usageExitCode, Code: "invalid_credentials", Message: "Token must be a nonblank single line"}
	}
	return string(secret), nil
}

func tokenStdinRequiredFailure() *output.Error {
	return &output.Error{
		ExitCode: usageExitCode,
		Code:     "credential_input_required",
		Message:  "Token input is not a terminal; use --token-stdin",
	}
}

func readBoundedTokenLine(reader io.Reader) (string, error) {
	if reader == nil {
		return "", errors.New("missing input")
	}
	contents, err := io.ReadAll(io.LimitReader(reader, maxTokenInputBytes+3))
	if err != nil || len(contents) > maxTokenInputBytes+2 {
		return "", errors.New("invalid input")
	}
	if strings.HasSuffix(string(contents), "\r\n") {
		contents = contents[:len(contents)-2]
	} else if len(contents) > 0 && (contents[len(contents)-1] == '\r' || contents[len(contents)-1] == '\n') {
		contents = contents[:len(contents)-1]
	}
	token := string(contents)
	if len(token) > maxTokenInputBytes || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("invalid token")
	}
	return token, nil
}

func persistLogin(configDir, token, serverURL string) *output.Error {
	if serverURL != "" {
		return persistExplicitLogin(configDir, token, serverURL, nil)
	}
	return persistStoredToken(configDir, token)
}

func configurationLoadFailure(error) output.Error {
	return output.Error{ExitCode: 3, Code: "configuration_error", Message: "Configuration is missing or unsafe"}
}

func clientConfigurationFailure(err error) output.Error {
	if strings.HasPrefix(err.Error(), "invalid_timeout:") {
		return output.Error{ExitCode: usageExitCode, Code: "invalid_timeout", Message: "Timeout must be greater than zero"}
	}
	return output.Error{ExitCode: usageExitCode, Code: "invalid_configuration", Message: "Server URL and token must be valid"}
}

func authUsageFailure(runtime Runtime, jsonMode bool, message string) int {
	return writeFailure(runtime, jsonMode, output.Error{
		ExitCode: usageExitCode,
		Code:     "usage",
		Message:  message,
	})
}
