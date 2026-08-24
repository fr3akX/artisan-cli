//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/auth"
	"github.com/fr3akX/artisan-cli/internal/config"
)

const signalTestToken = "compiled-process-signal-secret"

func TestCompiledProcessSIGINTExits130WithStableOutputAndCleanup(t *testing.T) {
	binary := buildSignalTestBinary(t)
	for _, test := range []struct {
		name     string
		jsonMode bool
		upload   bool
	}{
		{name: "download human"},
		{name: "download JSON", jsonMode: true},
		{name: "upload JSON", jsonMode: true, upload: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			serverRelease := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer "+signalTestToken {
					t.Errorf("Authorization = %q", got)
				}
				close(requestStarted)
				<-serverRelease
			}))
			defer server.Close()
			defer close(serverRelease)

			configRoot := t.TempDir()
			configDir := filepath.Join(configRoot, "artisan")
			if runtime.GOOS == "darwin" {
				configDir = filepath.Join(configRoot, "Library", "Application Support", "artisan")
			}
			if err := config.SaveServer(configDir, server.URL); err != nil {
				t.Fatal(err)
			}
			if err := auth.NewFileStore(configDir).Save(signalTestToken); err != nil {
				t.Fatal(err)
			}

			workDir := t.TempDir()
			destination := filepath.Join(workDir, "download.webp")
			args := []string{"inventory", "image", "download", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", destination}
			if test.upload {
				source := filepath.Join(workDir, "source.jpg")
				if err := os.WriteFile(source, []byte("image bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				args = []string{"inventory", "image", "add", "--idempotency-key", "signal-upload-key", "11111111-1111-4111-8111-111111111111", source}
			}
			if test.jsonMode {
				args = append([]string{"--json"}, args...)
			}

			var stdout, stderr bytes.Buffer
			command := exec.Command(binary, args...)
			command.Stdout = &stdout
			command.Stderr = &stderr
			command.Env = signalTestEnvironment(configRoot)
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-requestStarted:
			case <-time.After(3 * time.Second):
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatal("compiled process did not reach blocking server")
			}
			// Reaching the request proves the download target was created. Linux
			// and Darwin intentionally keep that source anonymous/unlinked, so no
			// raceable temporary pathname is expected here.
			if err := command.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			waitErr := waitForSignalTestProcess(t, command)
			var exitError *exec.ExitError
			if !errors.As(waitErr, &exitError) || exitError.ExitCode() != 130 {
				t.Fatalf("Wait() error = %v, want exit 130", waitErr)
			}

			combined := stdout.String() + stderr.String()
			if strings.Contains(combined, signalTestToken) || strings.Contains(combined, server.URL) {
				t.Fatalf("interruption output leaked a secret: %q", combined)
			}
			if test.jsonMode {
				if stdout.String() != "{\"ok\":false,\"error\":{\"code\":\"interrupted\",\"message\":\"Operation interrupted\"}}\n" || stderr.Len() != 0 {
					t.Fatalf("JSON output = stdout %q stderr %q", stdout.String(), stderr.String())
				}
			} else if stdout.Len() != 0 || stderr.String() != "Operation interrupted\n" {
				t.Fatalf("human output = stdout %q stderr %q", stdout.String(), stderr.String())
			}
			entries, err := os.ReadDir(workDir)
			if err != nil {
				t.Fatal(err)
			}
			wantEntries := 0
			if test.upload {
				wantEntries = 1
			}
			if len(entries) != wantEntries {
				t.Fatalf("work directory after interruption = %v, want %d retained source files", entryNames(entries), wantEntries)
			}
		})
	}
}

func buildSignalTestBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "artisan")
	goCommand := filepath.Join(runtime.GOROOT(), "bin", "go")
	command := exec.Command(goCommand, "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build compiled signal test binary: %v\n%s", err, output)
	}
	return binary
}

func signalTestEnvironment(configRoot string) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "HOME=") || strings.HasPrefix(value, "XDG_CONFIG_HOME=") || strings.HasPrefix(value, "ARTISAN_SERVER_URL=") || strings.HasPrefix(value, "ARTISAN_SERVER_TOKEN=") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "HOME="+configRoot, "XDG_CONFIG_HOME="+configRoot)
}

func waitForSignalTestProcess(t *testing.T, command *exec.Cmd) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- command.Wait() }()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		_ = command.Process.Kill()
		<-result
		t.Fatal("compiled process did not exit after SIGINT")
		return context.DeadlineExceeded
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}
