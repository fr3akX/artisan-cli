package command

import (
	"bytes"
	"context"
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
)

const commandTestToken = "test-secret-token"

const commandIdentityJSON = `{
	"user":{"id":"user-id","email":"owner@example.com","nickname":"Owner"},
	"organization":{"id":"organization-id","name":"My Roastery","slug":"my-roastery"},
	"role":"admin"
}`

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
	want := `{"ok":true,"data":{"user":{"id":"user-id","email":"owner@example.com","nickname":"Owner"},"organization":{"id":"organization-id","name":"My Roastery","slug":"my-roastery"},"role":"admin"}}` + "\n"
	if result.stdout != want {
		t.Fatalf("stdout = %q, want identity envelope", result.stdout)
	}
	assertTokenRedacted(t, result)
}

func TestAuthLoginRequiresServerWhenNoneConfigured(t *testing.T) {
	configDir := t.TempDir()
	result := runAuthCommand(t, Runtime{
		In:        strings.NewReader(commandTestToken + "\n"),
		ConfigDir: configDir,
	}, "auth", "login", "--token-stdin")

	if result.code != usageExitCode || !strings.Contains(result.stderr, "--server") {
		t.Fatalf("result = %#v, want --server usage failure", result)
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
		_, _ = fmt.Fprintf(w, `{"user":{"id":"u","email":"e","nickname":%q},"organization":{"id":"o","name":"Org","slug":"org"},"role":"admin"}`, commandTestToken)
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
	wantJSON := `{"ok":true,"data":{"user":{"id":"user-id","email":"owner@example.com","nickname":"Owner"},"organization":{"id":"organization-id","name":"My Roastery","slug":"my-roastery"},"role":"admin"}}` + "\n"
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
				return storedServer.URL
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
