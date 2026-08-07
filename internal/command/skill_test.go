package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSkillShowHumanAndJSON(t *testing.T) {
	human := runAuthCommand(t, Runtime{}, "skill", "show")
	if human.code != 0 || human.stderr != "" || !strings.HasPrefix(human.stdout, "---\nname: artisan-inventory\n") {
		t.Fatalf("human result = %#v", human)
	}

	machine := runAuthCommand(t, Runtime{}, "--json", "skill", "show")
	if machine.code != 0 || machine.stderr != "" || strings.Count(machine.stdout, "\n") != 1 {
		t.Fatalf("JSON result = %#v", machine)
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(machine.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Data.Name != "artisan-inventory" || envelope.Data.Content != human.stdout {
		t.Fatalf("JSON envelope = %#v", envelope)
	}
}

func TestSkillHelpAndUsage(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"skill", "--help"}, want: "artisan skill show|install"},
		{args: []string{"skill", "show", "--help"}, want: "artisan skill show"},
		{args: []string{"skill", "install", "--help"}, want: "--directory ROOT"},
	} {
		result := runAuthCommand(t, Runtime{}, test.args...)
		if result.code != 0 || result.stderr != "" || !strings.Contains(result.stdout, test.want) {
			t.Errorf("Run(%q) = %#v, want help containing %q", test.args, result, test.want)
		}
	}

	result := runAuthCommand(t, Runtime{}, "--json", "skill", "install", "--help")
	if result.code != 0 || result.stderr != "" || !strings.HasPrefix(result.stdout, `{"ok":true,"data":{"usage":`) || strings.Count(result.stdout, "\n") != 1 {
		t.Fatalf("JSON help = %#v", result)
	}

	for _, args := range [][]string{
		{"skill"},
		{"skill", "unknown"},
		{"skill", "show", "extra"},
		{"skill", "install"},
		{"skill", "install", "--directory", t.TempDir(), "extra"},
	} {
		result := runAuthCommand(t, Runtime{}, args...)
		if result.code != usageExitCode || result.stdout != "" || result.stderr == "" {
			t.Errorf("Run(%q) = %#v, want human usage failure", args, result)
		}
	}
}

func TestSkillInstallHumanJSONAndNoNetworkConfiguration(t *testing.T) {
	root := t.TempDir()
	result := runAuthCommand(t, Runtime{
		Getenv: func(string) string { return "not-a-network-configuration" },
	}, "skill", "install", "--directory", root)
	path := filepath.Join(root, "artisan-inventory", "SKILL.md")
	if result.code != 0 || result.stderr != "" || !strings.Contains(result.stdout, path) {
		t.Fatalf("human install = %#v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	result = runAuthCommand(t, Runtime{}, "--json", "skill", "install", "--directory", root)
	if result.code != 0 || result.stderr != "" || strings.Count(result.stdout, "\n") != 1 {
		t.Fatalf("JSON idempotent install = %#v", result)
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Path      string `json:"path"`
			Installed bool   `json:"installed"`
			Unchanged bool   `json:"unchanged"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Data.Path != path || envelope.Data.Installed || !envelope.Data.Unchanged {
		t.Fatalf("JSON envelope = %#v", envelope)
	}
}

func TestSkillInstallRefusalForceAndStableJSONError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "artisan-inventory")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("local content\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runAuthCommand(t, Runtime{}, "--json", "skill", "install", "--directory", root)
	want := "{\"ok\":false,\"error\":{\"code\":\"skill_exists\",\"message\":\"Installed skill differs; use --force to replace it\"}}\n"
	if result.code != 4 || result.stdout != want || result.stderr != "" {
		t.Fatalf("refusal = %#v, want stdout %q", result, want)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "local content\n" {
		t.Fatal("refusal changed local skill")
	}

	result = runAuthCommand(t, Runtime{}, "skill", "install", "--directory", root, "--force")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("force = %#v", result)
	}
	got, _ = os.ReadFile(path)
	if !strings.HasPrefix(string(got), "---\nname: artisan-inventory\n") {
		t.Fatal("force did not install embedded content")
	}
}

func TestSkillInstallRejectsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	traversal := root + string(filepath.Separator) + "child" + string(filepath.Separator) + ".."
	result := runAuthCommand(t, Runtime{}, "--json", "skill", "install", "--directory", traversal)
	if result.code != usageExitCode || !strings.Contains(result.stdout, `"code":"invalid_skill_directory"`) || result.stderr != "" {
		t.Fatalf("traversal result = %#v", result)
	}

	if runtime.GOOS == "windows" {
		return
	}
	outside := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(outside, linkedRoot); err != nil {
		t.Fatal(err)
	}
	result = runAuthCommand(t, Runtime{}, "skill", "install", "--directory", linkedRoot)
	if result.code == 0 {
		t.Fatalf("symlink root accepted: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "artisan-inventory")); !os.IsNotExist(err) {
		t.Fatalf("install escaped through root symlink: %v", err)
	}
}
