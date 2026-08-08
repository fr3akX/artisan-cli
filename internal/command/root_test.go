package command

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/auth"
	"github.com/fr3akX/artisan-cli/internal/config"
)

func runCommand(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	runtime := Runtime{
		In:  strings.NewReader(""),
		Out: &stdout,
		Err: &stderr,
		Getenv: func(string) string {
			return ""
		},
	}
	code := Run(context.Background(), args, runtime)
	return code, stdout.String(), stderr.String()
}

func TestVersionHuman(t *testing.T) {
	code, stdout, stderr := runCommand(t, "version")
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr)
	}
	if want := "artisan dev (unknown)\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestVersionJSON(t *testing.T) {
	code, stdout, stderr := runCommand(t, "--json", "version")
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr)
	}
	want := "{\"ok\":true,\"data\":{\"version\":\"dev\",\"commit\":\"unknown\"}}\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Fatalf("JSON stdout contains ANSI escape: %q", stdout)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "unknown")
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if want := "Unknown command: unknown\n"; stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

func TestUnknownCommandJSONIsOneStdoutEnvelope(t *testing.T) {
	code, stdout, stderr := runCommand(t, "--json", "unknown")
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	want := "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Unknown command: unknown\"}}\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if strings.Count(stdout, "\n") != 1 || strings.Contains(stdout, "\x1b[") {
		t.Fatalf("JSON stdout is not exactly one plain envelope: %q", stdout)
	}
}

func TestParseFailureUsesJSONIntentAcrossGlobalPrefix(t *testing.T) {
	code, stdout, stderr := runCommand(t, "--timeout", "nope", "--json", "version")
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	want := "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Invalid global option\"}}\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestParseFailureJSONIntentIsDeterministic(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantJSON bool
	}{
		{
			name:     "explicit false after malformed value",
			args:     []string{"--timeout", "nope", "--json=false", "version"},
			wantJSON: false,
		},
		{
			name:     "later true overrides false across malformed value",
			args:     []string{"--json=false", "--timeout", "nope", "--json", "version"},
			wantJSON: true,
		},
		{
			name:     "malformed json value does not imply json",
			args:     []string{"--json=not-a-bool", "version"},
			wantJSON: false,
		},
		{
			name:     "json after subcommand is not global",
			args:     []string{"--timeout", "nope", "version", "--json"},
			wantJSON: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, tt.args...)
			if code != 2 {
				t.Fatalf("Run() code = %d, want 2", code)
			}
			if tt.wantJSON {
				if !strings.HasPrefix(stdout, "{\"ok\":false,") || stderr != "" {
					t.Fatalf("stdout = %q, stderr = %q, want JSON failure on stdout", stdout, stderr)
				}
				return
			}
			if stdout != "" || stderr == "" {
				t.Fatalf("stdout = %q, stderr = %q, want human failure on stderr", stdout, stderr)
			}
		})
	}
}

func TestLegacySingleDashGlobalFlagsRemainAccepted(t *testing.T) {
	humanVersion := "artisan dev (unknown)\n"
	jsonVersion := "{\"ok\":true,\"data\":{\"version\":\"dev\",\"commit\":\"unknown\"}}\n"
	tests := []struct {
		name       string
		args       []string
		wantStdout string
	}{
		{name: "json", args: []string{"-json", "version"}, wantStdout: jsonVersion},
		{name: "json equals", args: []string{"-json=true", "version"}, wantStdout: jsonVersion},
		{name: "server", args: []string{"-server", "https://inventory.example", "version"}, wantStdout: humanVersion},
		{name: "server equals", args: []string{"-server=https://inventory.example", "version"}, wantStdout: humanVersion},
		{name: "timeout", args: []string{"-timeout", "5m", "version"}, wantStdout: humanVersion},
		{name: "timeout equals", args: []string{"-timeout=5m", "version"}, wantStdout: humanVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, tt.args...)
			if code != 0 || stdout != tt.wantStdout || stderr != "" {
				t.Fatalf("result = (%d, %q, %q), want (0, %q, empty)", code, stdout, stderr, tt.wantStdout)
			}
		})
	}
}

func TestGlobalFlagsAreAcceptedAfterCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "version", "--json")
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0", code)
	}
	want := "{\"ok\":true,\"data\":{\"version\":\"dev\",\"commit\":\"unknown\"}}\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestGlobalParseFailuresNeverEchoRawValues(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "secret shaped timeout human",
			args:       []string{"--timeout=test-secret-token", "version"},
			wantStderr: "Invalid global option\n",
		},
		{
			name:       "legacy single-dash secret shaped timeout human",
			args:       []string{"-timeout=test-secret-token", "version"},
			wantStderr: "Invalid global option\n",
		},
		{
			name:       "secret shaped timeout with later JSON intent",
			args:       []string{"--timeout=test-secret-token", "--json", "version"},
			wantStdout: "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Invalid global option\"}}\n",
		},
		{
			name:       "legacy single-dash secret with later JSON intent",
			args:       []string{"-timeout=test-secret-token", "-json", "version"},
			wantStdout: "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Invalid global option\"}}\n",
		},
		{
			name:       "secret shaped boolean",
			args:       []string{"--json=test-secret-token", "version"},
			wantStderr: "Invalid global option\n",
		},
		{
			name:       "secret shaped boolean with later JSON intent",
			args:       []string{"--json=test-secret-token", "--json", "version"},
			wantStdout: "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Invalid global option\"}}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, tt.args...)
			if code != usageExitCode || stdout != tt.wantStdout || stderr != tt.wantStderr {
				t.Fatalf("result = (%d, %q, %q), want (%d, %q, %q)", code, stdout, stderr, usageExitCode, tt.wantStdout, tt.wantStderr)
			}
			if strings.Contains(stdout, "test-secret-token") || strings.Contains(stderr, "test-secret-token") {
				t.Fatal("global flag failure exposed raw value")
			}
		})
	}
}

func TestExplicitGlobalServerAndTimeoutValidationAreStableUsageFailures(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "invalid server human",
			args:       []string{"--server=test-secret-token", "version"},
			wantStderr: "Server URL is invalid\n",
		},
		{
			name:       "legacy single-dash invalid server human",
			args:       []string{"-server=test-secret-token", "version"},
			wantStderr: "Server URL is invalid\n",
		},
		{
			name:       "invalid server JSON",
			args:       []string{"--json", "--server=test-secret-token", "version"},
			wantStdout: "{\"ok\":false,\"error\":{\"code\":\"invalid_server_url\",\"message\":\"Server URL is invalid\"}}\n",
		},
		{
			name:       "invalid timeout human",
			args:       []string{"--timeout=0s", "version"},
			wantStderr: "Timeout must be greater than zero\n",
		},
		{
			name:       "invalid timeout JSON",
			args:       []string{"--json", "--timeout=-1s", "version"},
			wantStdout: "{\"ok\":false,\"error\":{\"code\":\"invalid_timeout\",\"message\":\"Timeout must be greater than zero\"}}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, tt.args...)
			if code != usageExitCode || stdout != tt.wantStdout || stderr != tt.wantStderr {
				t.Fatalf("result = (%d, %q, %q), want (%d, %q, %q)", code, stdout, stderr, usageExitCode, tt.wantStdout, tt.wantStderr)
			}
			if strings.Contains(stdout, "test-secret-token") || strings.Contains(stderr, "test-secret-token") {
				t.Fatal("global validation exposed raw value")
			}
		})
	}
}

func TestGlobalTimeoutHasFiniteMaximumBoundary(t *testing.T) {
	code, stdout, stderr := runCommand(t, "--timeout=5m", "version")
	if code != 0 || stdout == "" || stderr != "" {
		t.Fatalf("boundary result = (%d, %q, %q), want success", code, stdout, stderr)
	}

	for _, jsonMode := range []bool{false, true} {
		args := []string{"--timeout=5m1ns", "version"}
		if jsonMode {
			args = append([]string{"--json"}, args...)
		}
		code, stdout, stderr = runCommand(t, args...)
		if code != usageExitCode {
			t.Fatalf("code = %d, want %d", code, usageExitCode)
		}
		if jsonMode {
			want := "{\"ok\":false,\"error\":{\"code\":\"invalid_timeout\",\"message\":\"Timeout must not exceed 5m0s\"}}\n"
			if stdout != want || stderr != "" {
				t.Fatalf("JSON output = stdout %q stderr %q", stdout, stderr)
			}
		} else if stdout != "" || stderr != "Timeout must not exceed 5m0s\n" {
			t.Fatalf("human output = stdout %q stderr %q", stdout, stderr)
		}
	}
}

func TestZeroRuntimeDoesNotPanic(t *testing.T) {
	runtime := Runtime{ConfigDir: t.TempDir()}
	if code := Run(context.Background(), []string{"version"}, runtime); code != 0 {
		t.Fatalf("version code = %d, want 0", code)
	}
	if code := Run(context.Background(), nil, runtime); code != usageExitCode {
		t.Fatalf("empty command code = %d, want %d", code, usageExitCode)
	}
	if code := Run(context.Background(), []string{"auth", "status"}, runtime); code != 3 {
		t.Fatalf("auth status code = %d, want 3", code)
	}
}

func TestGlobalFlagsAfterAuthCommandAreAccepted(t *testing.T) {
	server := identityServer(t, nil)
	defer server.Close()
	dir := t.TempDir()
	if err := config.SaveServer(dir, server.URL); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(dir).Save(commandTestToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	result := runAuthCommand(t, Runtime{ConfigDir: dir}, "auth", "status", "--json", "--timeout", "2s")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("result = %#v, want JSON success", result)
	}
	if !strings.HasPrefix(result.stdout, `{"ok":true,"data":`) {
		t.Fatalf("stdout = %q, want JSON success envelope", result.stdout)
	}
}

func TestAuthPostSubcommandGlobalFailuresAreRedactedAndUseSelectedStream(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		secret     string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "malformed timeout human",
			args:       []string{"auth", "status", "--timeout=secret-looking-value"},
			secret:     "secret-looking-value",
			wantStderr: "Invalid global option\n",
		},
		{
			name:       "invalid server JSON",
			args:       []string{"auth", "status", "--json", "--server=test-secret-token"},
			secret:     "test-secret-token",
			wantStdout: "{\"ok\":false,\"error\":{\"code\":\"invalid_server_url\",\"message\":\"Server URL is invalid\"}}\n",
		},
		{
			name:       "invalid login option with post-subcommand JSON",
			args:       []string{"auth", "login", "--json", "--token=test-secret-token"},
			secret:     "test-secret-token",
			wantStdout: "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Invalid auth login option\"}}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, tt.args...)
			if code != usageExitCode || stdout != tt.wantStdout || stderr != tt.wantStderr {
				t.Fatalf("result = (%d, %q, %q), want (%d, %q, %q)", code, stdout, stderr, usageExitCode, tt.wantStdout, tt.wantStderr)
			}
			if strings.Contains(stdout, tt.secret) || strings.Contains(stderr, tt.secret) {
				t.Fatal("auth global flag failure exposed supplied value")
			}
		})
	}
}
