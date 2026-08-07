package command

import (
	"bytes"
	"context"
	"strings"
	"testing"
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

func TestGlobalFlagsMustPrecedeCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "version", "--json")
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Fatal("stderr is empty, want usage diagnostic")
	}
}
