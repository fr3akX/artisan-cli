package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/auth"
	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/securefile"
)

const commandTestToken = "test-secret-token"

const commandIdentityJSON = `{
	"user":{"id":"11111111-1111-4111-8111-111111111111","email":"owner@example.com","nickname":"Owner"},
	"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"My Roastery","slug":"my-roastery"},
	"role":"admin"
}`

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("test read failure") }

type commandResult struct {
	code   int
	stdout string
	stderr string
}

func runAuthCommand(t *testing.T, runtime Runtime, args ...string) commandResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if runtime.In == nil {
		runtime.In = strings.NewReader("")
	}
	runtime.Out = &stdout
	runtime.Err = &stderr
	if runtime.Getenv == nil {
		runtime.Getenv = func(string) string { return "" }
	}
	code := Run(context.Background(), args, runtime)
	return commandResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func identityServer(t *testing.T, handler func(*http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler != nil {
			handler(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, commandIdentityJSON)
	}))
}

func assertTokenRedacted(t *testing.T, result commandResult) {
	t.Helper()
	if strings.Contains(result.stdout, commandTestToken) || strings.Contains(result.stderr, commandTestToken) {
		t.Fatal("command output exposed the bearer credential")
	}
}

func TestAuthLoginFromStdinVerifiesThenPersistsNormalizedServerAndToken(t *testing.T) {
	configDir := t.TempDir()
	var verified atomic.Bool
	server := identityServer(t, func(r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/auth/me" {
			t.Errorf("request = %s %s, want GET /api/v1/auth/me", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+commandTestToken {
			t.Error("request did not contain the expected bearer credential")
		}
		verified.Store(true)
	})
	defer server.Close()

	result := runAuthCommand(t, Runtime{
		In:        strings.NewReader(commandTestToken + "\n"),
		ConfigDir: configDir,
	}, "--server", server.URL+"/", "--timeout", "2s", "auth", "login", "--token-stdin")

	if result.code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", result.code, result.stderr)
	}
	if !verified.Load() {
		t.Fatal("login did not verify the token")
	}
	if result.stdout != "Authenticated as Owner for My Roastery (my-roastery) with role admin\n" || result.stderr != "" {
		t.Fatalf("stdout = %q, stderr = %q", result.stdout, result.stderr)
	}
	values, err := config.Load(configDir, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if values.ServerURL != server.URL {
		t.Fatalf("stored server = %q, want normalized server", values.ServerURL)
	}
	if values.Token != commandTestToken {
		t.Fatal("stored credential does not match submitted credential")
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginJSONNeverIncludesToken(t *testing.T) {
	server := identityServer(t, nil)
	defer server.Close()
	result := runAuthCommand(t, Runtime{
		In:        strings.NewReader(commandTestToken + "\n"),
		ConfigDir: t.TempDir(),
	}, "--json", "--server", server.URL, "auth", "login", "--token-stdin")

	if result.code != 0 || result.stderr != "" {
		t.Fatalf("result code/stderr = %d/%q, want success", result.code, result.stderr)
	}
	want := `{"ok":true,"data":{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"owner@example.com","nickname":"Owner"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"My Roastery","slug":"my-roastery"},"role":"admin"}}` + "\n"
	if result.stdout != want {
		t.Fatalf("stdout = %q, want identity envelope", result.stdout)
	}
	assertTokenRedacted(t, result)
}

func TestAuthConfigurationFailuresUseStableExitThree(t *testing.T) {
	tests := []struct {
		name     string
		jsonMode bool
		setup    func(t *testing.T, dir string) func(string) string
	}{
		{name: "missing human"},
		{name: "missing JSON", jsonMode: true},
		{
			name: "malformed stored human",
			setup: func(t *testing.T, dir string) func(string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not-json\n"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return nil
			},
		},
		{
			name: "unsafe stored credential human",
			setup: func(t *testing.T, dir string) func(string) string {
				t.Helper()
				if err := config.SaveServer(dir, "http://127.0.0.1:43210"); err != nil {
					t.Fatalf("SaveServer() error = %v", err)
				}
				credentialPath := filepath.Join(dir, "credentials.json")
				if err := os.WriteFile(credentialPath, []byte(`{"token":"stored"}`+"\n"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				if err := os.Chmod(credentialPath, 0o644); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
				return nil
			},
		},
		{
			name: "malformed environment token human",
			setup: func(t *testing.T, _ string) func(string) string {
				return func(name string) string {
					switch name {
					case "ARTISAN_SERVER_URL":
						return "http://127.0.0.1:43210"
					case "ARTISAN_SERVER_TOKEN":
						return "bad\ntoken"
					default:
						return ""
					}
				}
			},
		},
		{
			name:     "invalid environment JSON",
			jsonMode: true,
			setup: func(t *testing.T, _ string) func(string) string {
				return func(name string) string {
					switch name {
					case "ARTISAN_SERVER_URL":
						return "test-secret-token"
					case "ARTISAN_SERVER_TOKEN":
						return commandTestToken
					default:
						return ""
					}
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			var getenv func(string) string
			if tt.setup != nil {
				getenv = tt.setup(t, dir)
			}
			args := []string{"auth", "status"}
			if tt.jsonMode {
				args = append([]string{"--json"}, args...)
			}
			result := runAuthCommand(t, Runtime{ConfigDir: dir, Getenv: getenv}, args...)
			if result.code != 3 {
				t.Fatalf("Run() code = %d, want 3", result.code)
			}
			if tt.jsonMode {
				want := "{\"ok\":false,\"error\":{\"code\":\"configuration_error\",\"message\":\"Configuration is missing or unsafe\"}}\n"
				if result.stdout != want || result.stderr != "" {
					t.Fatalf("JSON result = %#v, want stable config envelope", result)
				}
			} else if result.stdout != "" || result.stderr != "Configuration is missing or unsafe\n" {
				t.Fatalf("human result = %#v, want stable config failure", result)
			}
			assertTokenRedacted(t, result)
		})
	}
}

func TestAuthLoginRequiresServerWhenNoneConfigured(t *testing.T) {
	configDir := t.TempDir()
	result := runAuthCommand(t, Runtime{
		In:        strings.NewReader(commandTestToken + "\n"),
		ConfigDir: configDir,
	}, "auth", "login", "--token-stdin")

	if result.code != 3 || result.stderr != "Configuration is missing or unsafe\n" {
		t.Fatalf("result = %#v, want stable configuration failure", result)
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("login without a server changed configuration")
	}
	if _, err := os.Stat(filepath.Join(configDir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("login without a server changed credentials")
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginReadsPasswordOnlyFromTerminal(t *testing.T) {
	configDir := t.TempDir()
	server := identityServer(t, nil)
	defer server.Close()
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer input.Close()
	defer output.Close()
	var detectedFD, readFD int

	result := runAuthCommand(t, Runtime{
		In:        input,
		ConfigDir: configDir,
		IsTerminal: func(fd int) bool {
			detectedFD = fd
			return true
		},
		ReadPassword: func(fd int) ([]byte, error) {
			readFD = fd
			return []byte(commandTestToken), nil
		},
	}, "--server", server.URL, "auth", "login")

	if result.code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", result.code, result.stderr)
	}
	if detectedFD != int(input.Fd()) || readFD != int(input.Fd()) {
		t.Fatalf("terminal/read fds = %d/%d, want stdin fd", detectedFD, readFD)
	}
	if result.stderr != "Token: \n" {
		t.Fatalf("stderr = %q, want hidden-input prompt", result.stderr)
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginRejectsNonTerminalWithoutTokenStdin(t *testing.T) {
	called := false
	result := runAuthCommand(t, Runtime{
		ConfigDir: t.TempDir(),
		IsTerminal: func(int) bool {
			return false
		},
		ReadPassword: func(int) ([]byte, error) {
			called = true
			return []byte(commandTestToken), nil
		},
	}, "--server", "http://127.0.0.1:1", "auth", "login")

	if result.code != usageExitCode || !strings.Contains(result.stderr, "--token-stdin") {
		t.Fatalf("result = %#v, want nonterminal usage failure", result)
	}
	if called {
		t.Fatal("ReadPassword was called for a nonterminal input")
	}
}

func TestAuthLoginRejectsTokenOptionWithoutExposure(t *testing.T) {
	tests := [][]string{
		{"--server", "http://127.0.0.1:1", "auth", "login", "--token=" + commandTestToken},
		{"--server", "http://127.0.0.1:1", "auth", "login", "--token-stdin=" + commandTestToken},
	}
	for _, args := range tests {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != usageExitCode || !strings.Contains(result.stderr, "auth login option") {
			t.Fatalf("result code/output did not report invalid option: code=%d stderr=%q", result.code, result.stderr)
		}
		assertTokenRedacted(t, result)
	}
}

func TestReadBoundedTokenLineCoverage(t *testing.T) {
	accepted := []struct {
		name  string
		input string
		want  string
	}{
		{name: "EOF without newline", input: "token", want: "token"},
		{name: "LF", input: "token\n", want: "token"},
		{name: "CR", input: "token\r", want: "token"},
		{name: "CRLF", input: "token\r\n", want: "token"},
		{name: "exact bound", input: strings.Repeat("x", maxTokenInputBytes), want: strings.Repeat("x", maxTokenInputBytes)},
		{name: "exact bound with CRLF", input: strings.Repeat("x", maxTokenInputBytes) + "\r\n", want: strings.Repeat("x", maxTokenInputBytes)},
	}
	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readBoundedTokenLine(strings.NewReader(tt.input))
			if err != nil || got != tt.want {
				t.Fatalf("readBoundedTokenLine() length/error = %d/%v, want length %d", len(got), err, len(tt.want))
			}
		})
	}
	for _, tt := range []struct {
		name   string
		reader io.Reader
	}{
		{name: "overflow", reader: strings.NewReader(strings.Repeat("x", maxTokenInputBytes+1))},
		{name: "extra line", reader: strings.NewReader("token\nextra")},
		{name: "reader error", reader: errorReader{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := readBoundedTokenLine(tt.reader); err == nil {
				t.Fatal("readBoundedTokenLine() succeeded, want failure")
			}
		})
	}
}

func TestAuthLoginTokenStdinRejectsBlankMultilineAndOversized(t *testing.T) {
	tests := []struct {
		name  string
		input io.Reader
	}{
		{name: "blank", input: strings.NewReader(" \r\n")},
		{name: "multiline", input: strings.NewReader(commandTestToken + "\nsecond-line\n")},
		{name: "oversized", input: strings.NewReader(strings.Repeat("x", 64*1024+1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runAuthCommand(t, Runtime{In: tt.input, ConfigDir: t.TempDir()},
				"--server", "http://127.0.0.1:1", "auth", "login", "--token-stdin")
			if result.code != usageExitCode {
				t.Fatalf("Run() code = %d, want %d", result.code, usageExitCode)
			}
			assertTokenRedacted(t, result)
		})
	}
}

func TestAuthLoginDoesNotPersistBeforeSuccessfulIdentity(t *testing.T) {
	configDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"code":"authentication_required","message":"Authentication required"}}`)
	}))
	defer server.Close()

	result := runAuthCommand(t, Runtime{In: strings.NewReader(commandTestToken + "\n"), ConfigDir: configDir},
		"--server", server.URL, "auth", "login", "--token-stdin")
	if result.code != 4 {
		t.Fatalf("Run() code = %d, want 4", result.code)
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		if _, err := os.Stat(filepath.Join(configDir, name)); !os.IsNotExist(err) {
			t.Fatalf("failed login created %s", name)
		}
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginEnvironmentServerIsNotPersisted(t *testing.T) {
	configDir := t.TempDir()
	server := identityServer(t, nil)
	defer server.Close()
	result := runAuthCommand(t, Runtime{
		In:        strings.NewReader(commandTestToken + "\n"),
		ConfigDir: configDir,
		Getenv: func(name string) string {
			if name == "ARTISAN_SERVER_URL" {
				return server.URL
			}
			return ""
		},
	}, "auth", "login", "--token-stdin")

	if result.code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", result.code, result.stderr)
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("environment server override was persisted")
	}
	storedToken, err := auth.NewFileStore(configDir).Load()
	if err != nil || storedToken != commandTestToken {
		t.Fatal("successful login did not persist the submitted credential")
	}
}

func TestAuthLoginCredentialPersistenceFailureLeavesServerUnchanged(t *testing.T) {
	configDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(configDir, "credentials.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	server := identityServer(t, nil)
	defer server.Close()

	result := runAuthCommand(t, Runtime{In: strings.NewReader(commandTestToken + "\n"), ConfigDir: configDir},
		"--server", server.URL, "auth", "login", "--token-stdin")
	if result.code == 0 {
		t.Fatal("Run() succeeded despite credential persistence failure")
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("credential persistence failure partially saved the server")
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginServerPersistenceFailureRollsBackNewCredential(t *testing.T) {
	configDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(configDir, "config.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	server := identityServer(t, nil)
	defer server.Close()

	result := runAuthCommand(t, Runtime{In: strings.NewReader(commandTestToken + "\n"), ConfigDir: configDir},
		"--server", server.URL, "auth", "login", "--token-stdin")
	if result.code == 0 {
		t.Fatal("Run() succeeded despite server persistence failure")
	}
	if _, err := os.Stat(filepath.Join(configDir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("server persistence failure left a new credential behind")
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginServerPersistenceFailureRestoresPreviousCredential(t *testing.T) {
	configDir := t.TempDir()
	const previousToken = "previous-credential"
	store := auth.NewFileStore(configDir)
	if err := store.Save(previousToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(configDir, "config.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	server := identityServer(t, nil)
	defer server.Close()

	result := runAuthCommand(t, Runtime{In: strings.NewReader(commandTestToken + "\n"), ConfigDir: configDir},
		"--server", server.URL, "auth", "login", "--token-stdin")
	if result.code == 0 {
		t.Fatal("Run() succeeded despite server persistence failure")
	}
	storedToken, err := store.Load()
	if err != nil || storedToken != previousToken {
		t.Fatal("server persistence failure did not restore the previous credential")
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginRejectsIdentityContainingToken(t *testing.T) {
	configDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":%q},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`, commandTestToken)
	}))
	defer server.Close()

	result := runAuthCommand(t, Runtime{In: strings.NewReader(commandTestToken + "\n"), ConfigDir: configDir},
		"--server", server.URL, "auth", "login", "--token-stdin")
	if result.code == 0 {
		t.Fatal("Run() accepted an identity containing the bearer credential")
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginPropagatesGlobalTimeout(t *testing.T) {
	configDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, commandIdentityJSON)
	}))
	defer server.Close()

	result := runAuthCommand(t, Runtime{In: strings.NewReader(commandTestToken + "\n"), ConfigDir: configDir},
		"--server", server.URL, "--timeout", "1ms", "auth", "login", "--token-stdin")
	if result.code != 8 {
		t.Fatalf("Run() code = %d, want network failure exit 8", result.code)
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		if _, err := os.Stat(filepath.Join(configDir, name)); !os.IsNotExist(err) {
			t.Fatalf("timed out login created %s", name)
		}
	}
	assertTokenRedacted(t, result)
}

func TestAuthStatusHumanAndJSON(t *testing.T) {
	server := identityServer(t, nil)
	defer server.Close()
	configDir := t.TempDir()
	if err := config.SaveServer(configDir, server.URL); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(configDir).Save(commandTestToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	human := runAuthCommand(t, Runtime{ConfigDir: configDir}, "auth", "status")
	if human.code != 0 || human.stderr != "" {
		t.Fatalf("human result = %#v", human)
	}
	if human.stdout != "User: Owner\nOrganization: My Roastery (my-roastery)\nRole: admin\n" {
		t.Fatalf("human stdout = %q", human.stdout)
	}
	jsonResult := runAuthCommand(t, Runtime{ConfigDir: configDir}, "--json", "auth", "status")
	if jsonResult.code != 0 || jsonResult.stderr != "" {
		t.Fatalf("JSON result = %#v", jsonResult)
	}
	wantJSON := `{"ok":true,"data":{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"owner@example.com","nickname":"Owner"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"My Roastery","slug":"my-roastery"},"role":"admin"}}` + "\n"
	if jsonResult.stdout != wantJSON {
		t.Fatalf("JSON stdout = %q, want identity envelope", jsonResult.stdout)
	}
	assertTokenRedacted(t, human)
	assertTokenRedacted(t, jsonResult)
}

func TestAuthStatusGlobalServerOverridesStoredWithoutPersisting(t *testing.T) {
	var storedHits atomic.Int32
	storedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		storedHits.Add(1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer storedServer.Close()
	overrideServer := identityServer(t, nil)
	defer overrideServer.Close()
	configDir := t.TempDir()
	if err := config.SaveServer(configDir, storedServer.URL); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(configDir).Save(commandTestToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	result := runAuthCommand(t, Runtime{
		ConfigDir: configDir,
		Getenv: func(name string) string {
			if name == "ARTISAN_SERVER_URL" {
				return "test-secret-token"
			}
			return ""
		},
	}, "--server", overrideServer.URL, "auth", "status")
	if result.code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", result.code, result.stderr)
	}
	if storedHits.Load() != 0 {
		t.Fatal("status contacted the stored server despite a global override")
	}
	contents, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(contents), storedServer.URL) || strings.Contains(string(contents), overrideServer.URL) {
		t.Fatal("one-command server override rewrote stored configuration")
	}
}

func TestAuthLogoutWithEnvironmentTokenRemovesOnlyStoredToken(t *testing.T) {
	configDir := t.TempDir()
	if err := config.SaveServer(configDir, "http://127.0.0.1:43210"); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(configDir).Save(commandTestToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	getenv := func(name string) string {
		if name == "ARTISAN_SERVER_TOKEN" {
			return "environment-credential"
		}
		return ""
	}
	result := runAuthCommand(t, Runtime{
		ConfigDir: configDir,
		Getenv:    getenv,
	}, "auth", "logout")
	if result.code != 0 {
		t.Fatalf("Run() code = %d, want 0", result.code)
	}
	if _, err := auth.NewFileStore(configDir).Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("logout did not remove stored credential")
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.json")); err != nil {
		t.Fatal("logout removed stored server")
	}
	values, err := config.Load(configDir, getenv)
	if err != nil || values.Token != "environment-credential" || values.Source.Token != config.OriginEnvironment {
		t.Fatal("logout altered the effective environment credential")
	}
	assertCommittedCheckpointMatchesCurrent(t, configDir)
}

func TestAuthLogoutIsIdempotentAndLeavesServerConfiguration(t *testing.T) {
	configDir := t.TempDir()
	serverURL := "http://127.0.0.1:43210"
	if err := config.SaveServer(configDir, serverURL); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(configDir).Save(commandTestToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	for i := 0; i < 2; i++ {
		result := runAuthCommand(t, Runtime{ConfigDir: configDir}, "auth", "logout")
		if result.code != 0 || result.stdout != "Logged out\n" || result.stderr != "" {
			t.Fatalf("logout %d result = %#v", i+1, result)
		}
		assertTokenRedacted(t, result)
	}
	if _, err := os.Stat(filepath.Join(configDir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("logout left credentials on disk")
	}
	contents, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil || !strings.Contains(string(contents), serverURL) {
		t.Fatal("logout removed or changed server configuration")
	}
}

func TestExplicitLoginTransactionRestoresPriorStateOnStageFailure(t *testing.T) {
	stages := []string{loginStageJournalWritten, loginStageTokenSaved, loginStageServerSaved, loginStageBeforeCommit}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			const oldServer = "http://127.0.0.1:41001"
			const oldToken = "old-credential"
			if err := config.SaveServer(dir, oldServer); err != nil {
				t.Fatalf("SaveServer() error = %v", err)
			}
			if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			failure := persistExplicitLogin(dir, commandTestToken, "http://127.0.0.1:41002", func(got string) error {
				if got == stage {
					return errors.New("injected stage failure")
				}
				return nil
			})
			if failure == nil {
				t.Fatal("persistExplicitLogin() succeeded, want failure")
			}
			assertStoredAuthState(t, dir, oldServer, oldToken)
			assertCommittedCheckpointMatchesCurrent(t, dir)
		})
	}
}

func TestExplicitLoginTransactionRestoresPriorAbsence(t *testing.T) {
	dir := t.TempDir()
	failure := persistExplicitLogin(dir, commandTestToken, "http://127.0.0.1:41502", func(stage string) error {
		if stage == loginStageTokenSaved {
			return errors.New("injected failure")
		}
		return nil
	})
	if failure == nil {
		t.Fatal("persistExplicitLogin() succeeded, want failure")
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transaction did not restore absence of %s", name)
		}
	}
	assertCommittedCheckpointMatchesCurrent(t, dir)
}

func TestExplicitLoginTransactionRecoversCrashAtEveryStage(t *testing.T) {
	stages := []string{loginStageJournalWritten, loginStageTokenSaved, loginStageServerSaved, loginStageBeforeCommit}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			const oldServer = "http://127.0.0.1:42001"
			const oldToken = "old-credential"
			if err := config.SaveServer(dir, oldServer); err != nil {
				t.Fatalf("SaveServer() error = %v", err)
			}
			if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			failure := persistExplicitLogin(dir, commandTestToken, "http://127.0.0.1:42002", func(got string) error {
				if got == stage {
					return errSimulatedLoginCrash
				}
				return nil
			})
			if failure == nil {
				t.Fatal("persistExplicitLogin() succeeded, want simulated crash failure")
			}
			journalInfo, err := os.Stat(filepath.Join(dir, loginTransactionFileName))
			if err != nil {
				t.Fatal("simulated crash did not leave recovery journal")
			}
			if journalInfo.Mode().Perm()&0o077 != 0 {
				t.Fatalf("journal mode = %#o, grants group/other access", journalInfo.Mode().Perm())
			}
			if err := recoverLoginTransaction(dir); err != nil {
				t.Fatalf("recoverLoginTransaction() error = %v", err)
			}
			if stage == loginStageServerSaved || stage == loginStageBeforeCommit {
				assertStoredAuthState(t, dir, "http://127.0.0.1:42002", commandTestToken)
			} else {
				assertStoredAuthState(t, dir, oldServer, oldToken)
			}
			assertCommittedCheckpointMatchesCurrent(t, dir)
		})
	}
}

func TestExplicitLoginTransactionLeavesJournalWhenRollbackFailsAndNextRunRecovers(t *testing.T) {
	dir := t.TempDir()
	const oldServer = "http://127.0.0.1:43001"
	const oldToken = "old-credential"
	if err := config.SaveServer(dir, oldServer); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	store := auth.NewFileStore(dir)
	if err := store.Save(oldToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	credentialPath := filepath.Join(dir, "credentials.json")
	failure := persistExplicitLogin(dir, commandTestToken, "http://127.0.0.1:43002", func(stage string) error {
		if stage != loginStageTokenSaved {
			return nil
		}
		if err := os.Remove(credentialPath); err != nil {
			return err
		}
		if err := os.Mkdir(credentialPath, 0o700); err != nil {
			return err
		}
		return errors.New("injected failure with blocked rollback")
	})
	if failure == nil {
		t.Fatal("persistExplicitLogin() succeeded, want failure")
	}
	if strings.Contains(failure.Code, commandTestToken) || strings.Contains(failure.Message, commandTestToken) {
		t.Fatal("transaction failure exposed newly supplied credential")
	}
	if _, err := os.Stat(filepath.Join(dir, loginTransactionFileName)); err != nil {
		t.Fatal("failed rollback did not retain recovery journal")
	}
	if current, err := store.Load(); err == nil && current == commandTestToken {
		t.Fatal("newly supplied credential remained usable after failed rollback")
	}
	if err := os.Remove(credentialPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove blocker: %v", err)
	}
	if err := recoverLoginTransaction(dir); err != nil {
		t.Fatalf("recoverLoginTransaction() error = %v", err)
	}
	assertStoredAuthState(t, dir, oldServer, oldToken)
}

func TestAuthCommandAutomaticallyRecoversPendingLoginTransaction(t *testing.T) {
	dir := t.TempDir()
	const oldServer = "http://127.0.0.1:43501"
	const oldToken = "old-credential"
	if err := config.SaveServer(dir, oldServer); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	failure := persistExplicitLogin(dir, commandTestToken, "http://127.0.0.1:43502", func(stage string) error {
		if stage == loginStageTokenSaved {
			return errSimulatedLoginCrash
		}
		return nil
	})
	if failure == nil {
		t.Fatal("persistExplicitLogin() succeeded, want simulated crash")
	}
	result := runAuthCommand(t, Runtime{ConfigDir: dir}, "auth", "logout")
	if result.code != 0 {
		t.Fatalf("logout code = %d, want 0", result.code)
	}
	server, err := config.LoadStoredServer(dir)
	if err != nil || server != oldServer {
		t.Fatal("auth command did not recover prior server before logout")
	}
	if _, err := auth.NewFileStore(dir).Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("logout did not remove recovered stored credential")
	}
	assertCommittedCheckpointMatchesCurrent(t, dir)
}

func TestLoginTransactionJournalStrictDecode(t *testing.T) {
	dir := t.TempDir()
	contents := []byte(`{"version":1,"server_present":false,"server_url":"","token_present":false,"token":"","unknown":true}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, loginTransactionFileName), contents, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := recoverLoginTransaction(dir); err == nil {
		t.Fatal("recoverLoginTransaction() accepted unknown journal field")
	}
	if _, err := os.Stat(filepath.Join(dir, loginTransactionFileName)); err != nil {
		t.Fatal("invalid journal was removed instead of retained for inspection/recovery")
	}
}

func TestLoginTransactionJournalRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "journal-target")
	contents := []byte(`{"version":1,"server_present":false,"server_url":"","token_present":false,"token":""}` + "\n")
	if err := os.WriteFile(target, contents, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, loginTransactionFileName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := recoverLoginTransaction(dir); err == nil {
		t.Fatal("recoverLoginTransaction() followed a journal symlink")
	}
}

func TestExplicitLoginTransactionSuccessLeavesCheckpointAndCleansTemps(t *testing.T) {
	dir := t.TempDir()
	failure := persistExplicitLogin(dir, commandTestToken, "http://127.0.0.1:44002", nil)
	if failure != nil {
		t.Fatalf("persistExplicitLogin() failure code = %q", failure.Code)
	}
	assertStoredAuthState(t, dir, "http://127.0.0.1:44002", commandTestToken)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("transaction temporary remains: %s", entry.Name())
		}
	}
	assertCommittedCheckpointMatchesCurrent(t, dir)
}

func assertStoredAuthState(t *testing.T, dir, wantServer, wantToken string) {
	t.Helper()
	server, err := config.LoadStoredServer(dir)
	if err != nil || server != wantServer {
		t.Fatalf("stored server mismatch or error: %v", err)
	}
	token, err := auth.NewFileStore(dir).Load()
	if err != nil || token != wantToken {
		t.Fatal("stored credential mismatch or load error")
	}
	if token == commandTestToken && wantToken != commandTestToken {
		t.Fatal("newly supplied credential remained usable after failure")
	}
}

func TestAuthRejectsUnknownSubcommandsAndArguments(t *testing.T) {
	tests := [][]string{
		{"auth"},
		{"auth", "unknown"},
		{"auth", "status", "extra"},
		{"auth", "logout", "extra"},
	}
	for _, args := range tests {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != usageExitCode {
			t.Fatalf("Run(%v) code = %d, want %d", args, result.code, usageExitCode)
		}
	}
}

func TestLoginTransactionRejectsMalformedJournalWithoutDestructiveRecovery(t *testing.T) {
	valid := `{"state":"pending","version":1,"server_present":true,"server_url":"http://127.0.0.1:45101","token_present":true,"token":"prior-token","intended_server_present":true,"intended_server_url":"http://127.0.0.1:45102","intended_token_present":true,"intended_token":"intended-token"}`
	cases := []struct {
		name string
		raw  string
	}{
		{name: "version only", raw: `{"version":1}`},
		{name: "missing state", raw: `{"version":1,"server_present":true,"server_url":"http://127.0.0.1:45101","token_present":true,"token":"prior-token"}`},
		{name: "missing version", raw: `{"state":"pending","server_present":true,"server_url":"http://127.0.0.1:45101","token_present":true,"token":"prior-token"}`},
		{name: "missing server presence", raw: `{"state":"pending","version":1,"server_url":"http://127.0.0.1:45101","token_present":true,"token":"prior-token"}`},
		{name: "missing server value", raw: `{"state":"pending","version":1,"server_present":true,"token_present":true,"token":"prior-token"}`},
		{name: "missing token presence", raw: `{"state":"pending","version":1,"server_present":true,"server_url":"http://127.0.0.1:45101","token":"prior-token"}`},
		{name: "missing token value", raw: strings.Replace(valid, `,"token":"prior-token"`, "", 1)},
		{name: "missing intended server presence", raw: strings.Replace(valid, `,"intended_server_present":true`, "", 1)},
		{name: "missing intended server value", raw: strings.Replace(valid, `,"intended_server_url":"http://127.0.0.1:45102"`, "", 1)},
		{name: "missing intended token presence", raw: strings.Replace(valid, `,"intended_token_present":true`, "", 1)},
		{name: "missing intended token value", raw: strings.Replace(valid, `,"intended_token":"intended-token"`, "", 1)},
		{name: "duplicate state", raw: strings.Replace(valid, `"state":"pending"`, `"state":"pending","state":"pending"`, 1)},
		{name: "duplicate version", raw: strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1)},
		{name: "duplicate server presence", raw: strings.Replace(valid, `"server_present":true`, `"server_present":true,"server_present":true`, 1)},
		{name: "duplicate server value", raw: strings.Replace(valid, `"server_url":"http://127.0.0.1:45101"`, `"server_url":"http://127.0.0.1:45101","server_url":"http://127.0.0.1:45101"`, 1)},
		{name: "duplicate token presence", raw: strings.Replace(valid, `"token_present":true`, `"token_present":true,"token_present":true`, 1)},
		{name: "duplicate token value", raw: strings.Replace(valid, `"token":"prior-token"`, `"token":"prior-token","token":"prior-token"`, 1)},
		{name: "duplicate intended server presence", raw: strings.Replace(valid, `"intended_server_present":true`, `"intended_server_present":true,"intended_server_present":true`, 1)},
		{name: "duplicate intended server value", raw: strings.Replace(valid, `"intended_server_url":"http://127.0.0.1:45102"`, `"intended_server_url":"http://127.0.0.1:45102","intended_server_url":"http://127.0.0.1:45102"`, 1)},
		{name: "duplicate intended token presence", raw: strings.Replace(valid, `"intended_token_present":true`, `"intended_token_present":true,"intended_token_present":true`, 1)},
		{name: "duplicate intended token value", raw: strings.Replace(valid, `"intended_token":"intended-token"`, `"intended_token":"intended-token","intended_token":"intended-token"`, 1)},
		{name: "unknown field", raw: strings.TrimSuffix(valid, "}") + `,"unknown":true}`},
		{name: "wrong state type", raw: strings.Replace(valid, `"state":"pending"`, `"state":1`, 1)},
		{name: "wrong version type", raw: strings.Replace(valid, `"version":1`, `"version":"1"`, 1)},
		{name: "wrong server presence type", raw: strings.Replace(valid, `"server_present":true`, `"server_present":"true"`, 1)},
		{name: "wrong server value type", raw: strings.Replace(valid, `"server_url":"http://127.0.0.1:45101"`, `"server_url":false`, 1)},
		{name: "wrong token presence type", raw: strings.Replace(valid, `"token_present":true`, `"token_present":1`, 1)},
		{name: "wrong token value type", raw: strings.Replace(valid, `"token":"prior-token"`, `"token":null`, 1)},
		{name: "wrong intended server presence type", raw: strings.Replace(valid, `"intended_server_present":true`, `"intended_server_present":1`, 1)},
		{name: "wrong intended server value type", raw: strings.Replace(valid, `"intended_server_url":"http://127.0.0.1:45102"`, `"intended_server_url":null`, 1)},
		{name: "wrong intended token presence type", raw: strings.Replace(valid, `"intended_token_present":true`, `"intended_token_present":"true"`, 1)},
		{name: "wrong intended token value type", raw: strings.Replace(valid, `"intended_token":"intended-token"`, `"intended_token":false`, 1)},
		{name: "trailing value", raw: valid + ` {}`},
		{name: "invalid state", raw: strings.Replace(valid, `"state":"pending"`, `"state":"unknown"`, 1)},
		{name: "absent server with value", raw: strings.Replace(valid, `"server_present":true`, `"server_present":false`, 1)},
		{name: "present server without value", raw: strings.Replace(valid, `"server_url":"http://127.0.0.1:45101"`, `"server_url":""`, 1)},
		{name: "absent token with value", raw: strings.Replace(valid, `"token_present":true`, `"token_present":false`, 1)},
		{name: "present token without value", raw: strings.Replace(valid, `"token":"prior-token"`, `"token":""`, 1)},
		{name: "absent intended server with value", raw: strings.Replace(valid, `"intended_server_present":true`, `"intended_server_present":false`, 1)},
		{name: "present intended server without value", raw: strings.Replace(valid, `"intended_server_url":"http://127.0.0.1:45102"`, `"intended_server_url":""`, 1)},
		{name: "absent intended token with value", raw: strings.Replace(valid, `"intended_token_present":true`, `"intended_token_present":false`, 1)},
		{name: "present intended token without value", raw: strings.Replace(valid, `"intended_token":"intended-token"`, `"intended_token":""`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			const currentServer = "http://127.0.0.1:45102"
			const currentToken = "current-token"
			if err := config.SaveServer(dir, currentServer); err != nil {
				t.Fatalf("SaveServer() error = %v", err)
			}
			if err := auth.NewFileStore(dir).Save(currentToken); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			if err := securefile.AtomicWrite(dir, loginTransactionFileName, []byte(tc.raw+"\n")); err != nil {
				t.Fatalf("AtomicWrite() error = %v", err)
			}
			if err := recoverLoginTransaction(dir); err == nil {
				t.Fatal("recoverLoginTransaction() accepted malformed journal")
			}
			assertStoredAuthState(t, dir, currentServer, currentToken)
			if _, err := os.Stat(filepath.Join(dir, loginTransactionFileName)); err != nil {
				t.Fatal("malformed journal was removed")
			}
		})
	}
}

func TestPendingJournalIsDurableBeforeLoginMutation(t *testing.T) {
	dir := t.TempDir()
	const oldServer = "http://127.0.0.1:45201"
	const oldToken = "old-token"
	if err := config.SaveServer(dir, oldServer); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	failure := persistExplicitLogin(dir, commandTestToken, "http://127.0.0.1:45202", func(stage string) error {
		if stage != loginStageJournalWritten {
			return nil
		}
		journal, err := readLoginJournal(dir)
		if err != nil || journal.State != loginTransactionPending {
			t.Fatalf("pending journal unavailable before mutation: %#v, %v", journal, err)
		}
		assertStoredAuthState(t, dir, oldServer, oldToken)
		return errSimulatedLoginCrash
	})
	if failure == nil {
		t.Fatal("persistExplicitLogin() succeeded, want simulated crash")
	}
}

func TestParentSyncFailurePreventsLoginMutation(t *testing.T) {
	dir := t.TempDir()
	const oldServer = "http://127.0.0.1:45301"
	const oldToken = "old-token"
	if err := config.SaveServer(dir, oldServer); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	ops := defaultLoginTransactionOperations()
	ops.writeJournal = func(string, loginTransactionJournal) error {
		return errors.New("injected parent sync failure")
	}
	failure := persistExplicitLoginWithOperations(dir, commandTestToken, "http://127.0.0.1:45302", nil, ops)
	if failure == nil {
		t.Fatal("persistExplicitLoginWithOperations() succeeded")
	}
	assertStoredAuthState(t, dir, oldServer, oldToken)
}

func TestCommittedJournalIsDurableBeforeRemovalAndRecoveryDoesNotRollback(t *testing.T) {
	dir := t.TempDir()
	const oldServer = "http://127.0.0.1:45401"
	const oldToken = "old-token"
	const newServer = "http://127.0.0.1:45402"
	if err := config.SaveServer(dir, oldServer); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	failure := persistExplicitLogin(dir, commandTestToken, newServer, func(stage string) error {
		if stage != loginStageCommitted {
			return nil
		}
		journal, err := readLoginJournal(dir)
		if err != nil || journal.State != loginTransactionCommitted {
			t.Fatalf("committed marker unavailable before removal: %#v, %v", journal, err)
		}
		assertStoredAuthState(t, dir, newServer, commandTestToken)
		return errSimulatedLoginCrash
	})
	if failure == nil {
		t.Fatal("persistExplicitLogin() succeeded, want simulated crash")
	}
	if err := recoverLoginTransaction(dir); err != nil {
		t.Fatalf("recoverLoginTransaction() error = %v", err)
	}
	assertStoredAuthState(t, dir, newServer, commandTestToken)
	assertCommittedCheckpointMatchesCurrent(t, dir)
}

func TestCommittedCheckpointPersistsAfterSuccessfulLogin(t *testing.T) {
	dir := t.TempDir()
	const oldServer = "http://127.0.0.1:45501"
	const oldToken = "old-token"
	const newServer = "http://127.0.0.1:45502"
	if err := config.SaveServer(dir, oldServer); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	ops := defaultLoginTransactionOperations()
	if failure := persistExplicitLoginWithOperations(dir, commandTestToken, newServer, nil, ops); failure != nil {
		t.Fatalf("persistExplicitLoginWithOperations() failure = %#v", failure)
	}
	journal, err := readLoginJournal(dir)
	if err != nil || journal.State != loginTransactionCommitted {
		t.Fatalf("journal = %#v, error = %v, want committed marker", journal, err)
	}
	assertStoredAuthState(t, dir, newServer, commandTestToken)
	if err := recoverLoginTransaction(dir); err != nil {
		t.Fatalf("recoverLoginTransaction() error = %v", err)
	}
	assertStoredAuthState(t, dir, newServer, commandTestToken)
}

func TestPostRenameSyncFailureAtEveryTransactionWriteRecoversDeterministically(t *testing.T) {
	const oldServer = "http://127.0.0.1:45601"
	const oldToken = "old-token"
	const newServer = "http://127.0.0.1:45602"
	tests := []struct {
		name    string
		stage   string
		wantNew bool
	}{
		{name: "pending journal", stage: "pending-journal", wantNew: false},
		{name: "token", stage: "token", wantNew: false},
		{name: "server", stage: "server", wantNew: true},
		{name: "committed marker", stage: "committed-journal", wantNew: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := config.SaveServer(dir, oldServer); err != nil {
				t.Fatalf("SaveServer() error = %v", err)
			}
			if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			ops := defaultLoginTransactionOperations()
			writeJournal := ops.writeJournal
			saveToken := ops.saveToken
			saveServer := ops.saveServer
			journalWrites := 0
			ops.writeJournal = func(configDir string, journal loginTransactionJournal) error {
				journalWrites++
				if err := writeJournal(configDir, journal); err != nil {
					return err
				}
				if (tc.stage == "pending-journal" && journalWrites == 1) || (tc.stage == "committed-journal" && journalWrites == 2) {
					return fmt.Errorf("journal write: %w", &securefile.ReplacementError{Err: errors.New("injected post-rename journal sync failure")})
				}
				return nil
			}
			ops.saveToken = func(configDir, token string) error {
				if err := saveToken(configDir, token); err != nil {
					return err
				}
				if tc.stage == "token" {
					return fmt.Errorf("token write: %w", &securefile.ReplacementError{Err: errors.New("injected post-rename token sync failure")})
				}
				return nil
			}
			ops.saveServer = func(configDir, serverURL string) error {
				if err := saveServer(configDir, serverURL); err != nil {
					return err
				}
				if tc.stage == "server" {
					return fmt.Errorf("server write: %w", &securefile.ReplacementError{Err: errors.New("injected post-rename server sync failure")})
				}
				return nil
			}

			failure := persistExplicitLoginWithOperations(dir, commandTestToken, newServer, nil, ops)
			if failure == nil {
				t.Fatal("persistExplicitLoginWithOperations() succeeded, want ambiguous durability failure")
			}
			if _, err := os.Stat(filepath.Join(dir, loginTransactionFileName)); err != nil {
				t.Fatal("ambiguous failure removed the only recovery record")
			}
			recoverThroughFreshAuthOperation(t, dir)
			if tc.wantNew {
				assertStoredAuthState(t, dir, newServer, commandTestToken)
			} else {
				assertStoredAuthState(t, dir, oldServer, oldToken)
			}
			assertCommittedCheckpointMatchesCurrent(t, dir)
		})
	}
}

func recoverThroughFreshAuthOperation(t *testing.T, dir string) {
	t.Helper()
	result := runAuthCommand(t, Runtime{ConfigDir: dir}, "--timeout", "100ms", "auth", "status")
	if result.code == 3 {
		t.Fatalf("fresh auth operation failed transaction recovery: %#v", result)
	}
}

func TestCommittedCheckpointBlocksCrashedPartialRollbackWithoutGuessing(t *testing.T) {
	const oldServer = "http://127.0.0.1:45701"
	const oldToken = "old-token"
	const newServer = "http://127.0.0.1:45702"
	tests := []struct {
		name            string
		interruptServer bool
		want            storedLoginPair
	}{
		{name: "token restored before rollback crash", want: storedLoginPair{ServerPresent: true, ServerURL: newServer, TokenPresent: true, Token: oldToken}},
		{name: "server restored before rollback crash", interruptServer: true, want: storedLoginPair{ServerPresent: true, ServerURL: oldServer, TokenPresent: true, Token: commandTestToken}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := config.SaveServer(dir, oldServer); err != nil {
				t.Fatalf("SaveServer() error = %v", err)
			}
			if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			ops := defaultLoginTransactionOperations()
			writeJournal := ops.writeJournal
			writes := 0
			ops.writeJournal = func(configDir string, journal loginTransactionJournal) error {
				writes++
				if err := writeJournal(configDir, journal); err != nil {
					return err
				}
				if writes == 2 {
					return fmt.Errorf("committed journal write: %w", &securefile.ReplacementError{Err: errors.New("injected committed-marker sync failure")})
				}
				return nil
			}
			if failure := persistExplicitLoginWithOperations(dir, commandTestToken, newServer, nil, ops); failure == nil {
				t.Fatal("persistExplicitLoginWithOperations() succeeded")
			}

			// Simulate rollback restoring one file and crashing before the other.
			if tc.interruptServer {
				if err := config.SaveServer(dir, oldServer); err != nil {
					t.Fatalf("partial server rollback error = %v", err)
				}
			} else if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
				t.Fatalf("partial token rollback error = %v", err)
			}
			journalBefore, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
			if err != nil {
				t.Fatalf("ReadFile checkpoint: %v", err)
			}
			result := runAuthCommand(t, Runtime{ConfigDir: dir}, "auth", "logout")
			if result.code != 3 {
				t.Fatalf("logout with mismatched checkpoint = %#v, want configuration failure", result)
			}
			assertStoredLoginPairEquals(t, dir, tc.want)
			journalAfter, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
			if err != nil || !bytes.Equal(journalBefore, journalAfter) {
				t.Fatal("mismatched checkpoint was changed during blocked recovery")
			}
		})
	}
}

func TestTransactionRecoveryRestoresPriorPairWhenCurrentStateIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	const oldServer = "http://127.0.0.1:45801"
	const oldToken = "old-token"
	if err := config.SaveServer(dir, oldServer); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	failure := persistExplicitLogin(dir, commandTestToken, "http://127.0.0.1:45802", func(stage string) error {
		if stage == loginStageJournalWritten {
			return errSimulatedLoginCrash
		}
		return nil
	})
	if failure == nil {
		t.Fatal("persistExplicitLogin() succeeded, want simulated crash")
	}
	if err := securefile.AtomicWrite(dir, "credentials.json", []byte("{malformed\n")); err != nil {
		t.Fatalf("AtomicWrite malformed credentials error = %v", err)
	}
	recoverThroughFreshAuthOperation(t, dir)
	assertStoredAuthState(t, dir, oldServer, oldToken)
	assertCommittedCheckpointMatchesCurrent(t, dir)
}

func TestRecoveryRetainsJournalUntilPriorAbsenceIsDurable(t *testing.T) {
	const priorServer = "http://127.0.0.1:45901"
	const priorToken = "prior-token"
	const intendedServer = "http://127.0.0.1:45902"
	tests := []struct {
		name          string
		prior         storedLoginPair
		failedRemoval string
	}{
		{
			name:          "prior server absent",
			prior:         storedLoginPair{TokenPresent: true, Token: priorToken},
			failedRemoval: "server",
		},
		{
			name:          "prior token absent",
			prior:         storedLoginPair{ServerPresent: true, ServerURL: priorServer},
			failedRemoval: "token",
		},
		{
			name:          "both absent after server removal",
			prior:         storedLoginPair{},
			failedRemoval: "server",
		},
		{
			name:          "both absent after token removal",
			prior:         storedLoginPair{},
			failedRemoval: "token",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.prior.ServerPresent {
				if err := config.SaveServer(dir, tc.prior.ServerURL); err != nil {
					t.Fatalf("SaveServer() error = %v", err)
				}
			}
			if tc.prior.TokenPresent {
				if err := auth.NewFileStore(dir).Save(tc.prior.Token); err != nil {
					t.Fatalf("Save() error = %v", err)
				}
			}
			failure := persistExplicitLogin(dir, commandTestToken, intendedServer, func(stage string) error {
				if stage == loginStageTokenSaved {
					return errSimulatedLoginCrash
				}
				return nil
			})
			if failure == nil {
				t.Fatal("persistExplicitLogin() succeeded, want simulated crash")
			}

			ops := defaultLoginTransactionOperations()
			injected := errors.New("injected post-remove parent sync failure")
			if tc.failedRemoval == "server" {
				ops.removeServer = func(configDir string) error {
					dir, err := config.ResolveDir(configDir)
					if err != nil {
						return err
					}
					if err := os.Remove(filepath.Join(dir, "config.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
						return err
					}
					return injected
				}
			} else {
				ops.removeToken = func(configDir string) error {
					dir, err := config.ResolveDir(configDir)
					if err != nil {
						return err
					}
					if err := os.Remove(filepath.Join(dir, "credentials.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
						return err
					}
					return injected
				}
			}
			if err := recoverLoginTransactionWithOperations(dir, ops); !errors.Is(err, injected) {
				t.Fatalf("recoverLoginTransactionWithOperations() error = %v, want injected failure", err)
			}
			journal, err := readLoginJournal(dir)
			if err != nil {
				t.Fatalf("readLoginJournal() after failed recovery error = %v", err)
			}

			// Model a crash resurrecting the deletion whose parent sync failed.
			if tc.failedRemoval == "server" {
				if err := config.SaveServer(dir, journal.IntendedServerURL); err != nil {
					t.Fatalf("resurrect intended server: %v", err)
				}
			} else if err := auth.NewFileStore(dir).Save(journal.IntendedToken); err != nil {
				t.Fatalf("resurrect intended token: %v", err)
			}

			_ = runAuthCommand(t, Runtime{ConfigDir: dir}, "--timeout", "100ms", "auth", "status")
			got, err := readStoredLoginPair(dir)
			if err != nil || got != tc.prior {
				t.Fatalf("stored pair after fresh recovery = %#v, %v, want %#v", got, err, tc.prior)
			}
			assertCommittedCheckpointMatchesCurrent(t, dir)
		})
	}
}

func TestCommittedRecoveryRefreshesCheckpointWithoutDeletingIt(t *testing.T) {
	dir := t.TempDir()
	const intendedServer = "http://127.0.0.1:45912"
	failure := persistExplicitLogin(dir, commandTestToken, intendedServer, func(stage string) error {
		if stage == loginStageCommitted {
			return errSimulatedLoginCrash
		}
		return nil
	})
	if failure == nil {
		t.Fatal("persistExplicitLogin() succeeded, want simulated crash")
	}
	assertCommittedCheckpointMatchesCurrent(t, dir)

	ops := defaultLoginTransactionOperations()
	writeJournal := ops.writeJournal
	var events []string
	ops.saveToken = func(string, string) error {
		events = append(events, "unexpected-token-write")
		return nil
	}
	ops.saveServer = func(string, string) error {
		events = append(events, "unexpected-server-write")
		return nil
	}
	ops.writeJournal = func(configDir string, journal loginTransactionJournal) error {
		events = append(events, "committed-durable")
		return writeJournal(configDir, journal)
	}
	if err := recoverLoginTransactionWithOperations(dir, ops); err != nil {
		t.Fatalf("recoverLoginTransactionWithOperations() error = %v", err)
	}
	if got := strings.Join(events, ","); got != "committed-durable" {
		t.Fatalf("recovery events = %q, want checkpoint refresh only", got)
	}
	assertStoredAuthState(t, dir, intendedServer, commandTestToken)
	assertCommittedCheckpointMatchesCurrent(t, dir)

	recoverThroughFreshAuthOperation(t, dir)
	assertStoredAuthState(t, dir, intendedServer, commandTestToken)
	assertCommittedCheckpointMatchesCurrent(t, dir)
}
