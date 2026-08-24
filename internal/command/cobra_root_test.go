package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCobraRootHelpListsDiscoverableCommands(t *testing.T) {
	result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "--help")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("root help = %#v", result)
	}
	for _, want := range []string{"Artisan Server command line client", "Authentication and saved credentials", "Manage green-coffee inventory", "Read private roasts and post review comments", "Install or inspect the embedded agent skill", "version"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("root help missing %q:\n%s", want, result.stdout)
		}
	}
}

func TestCobraVersionHelpIsGenerated(t *testing.T) {
	result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "version", "--help")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("version help = %#v", result)
	}
	for _, want := range []string{"Print version information", "artisan version", "Global Flags:"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("version help missing %q:\n%s", want, result.stdout)
		}
	}
}

func TestCobraParserStateDoesNotLeakAcrossRuns(t *testing.T) {
	machine := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "--json", "version")
	if machine.code != 0 || !strings.HasPrefix(machine.stdout, "{\"ok\":true,") || machine.stderr != "" {
		t.Fatalf("JSON version = %#v", machine)
	}

	human := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "version")
	if human.code != 0 || human.stdout != "artisan dev (unknown)\n" || human.stderr != "" {
		t.Fatalf("human version after JSON run = %#v", human)
	}
}

func TestCobraUnknownChildAttributionIgnoresTrailingRecognizedWords(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		message string
	}{
		{name: "auth", args: []string{"auth", "bogus", "--bad", "status"}, message: "Unknown auth command"},
		{name: "skill", args: []string{"skill", "bogus", "--bad", "show"}, message: "Unknown skill command"},
		{name: "completion", args: []string{"completion", "bogus", "--bad", "bash"}, message: "Unknown completion command"},
		{name: "inventory image", args: []string{"inventory", "image", "bogus"}, message: "Unknown inventory image command"},
		{name: "roast", args: []string{"roast", "bogus", "--bad", "list"}, message: "Unknown roast command"},
		{name: "roast chart", args: []string{"roast", "chart", "bogus", "--bad", "download"}, message: "Unknown roast chart command"},
	} {
		for _, jsonMode := range []bool{false, true} {
			name := "text"
			args := append([]string(nil), test.args...)
			wantStdout := ""
			wantStderr := test.message + "\n"
			if jsonMode {
				name = "JSON"
				args = append([]string{"--json"}, args...)
				wantStdout = "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":" + strconv.Quote(test.message) + "}}\n"
				wantStderr = ""
			}
			t.Run(test.name+"/"+name, func(t *testing.T) {
				result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
				if result.code != usageExitCode || result.stdout != wantStdout || result.stderr != wantStderr {
					t.Fatalf("result = %#v, want code %d, stdout %q, stderr %q", result, usageExitCode, wantStdout, wantStderr)
				}
			})
		}
	}
}

func TestCobraUnknownCommandMappingIncludesInventoryImage(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		name := "text"
		args := []string{"inventory", "image", "bogus"}
		wantStdout := ""
		wantStderr := "Unknown inventory image command\n"
		if jsonMode {
			name = "JSON"
			args = append([]string{"--json"}, args...)
			wantStdout = "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Unknown inventory image command\"}}\n"
			wantStderr = ""
		}
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := writeCobraFailure(Runtime{Out: &stdout, Err: &stderr}, &cobraState{}, args, errors.New("unknown command \"bogus\" for \"artisan inventory image\""))
			if code != usageExitCode || stdout.String() != wantStdout || stderr.String() != wantStderr {
				t.Fatalf("result = (%d, %q, %q), want (%d, %q, %q)", code, stdout.String(), stderr.String(), usageExitCode, wantStdout, wantStderr)
			}
		})
	}
}

func TestCompletionIsStaticAndDoesNotReadConfiguration(t *testing.T) {
	const tokenValue = "completion-token-value-must-not-appear"
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			serverValue := filepath.Join(t.TempDir(), "completion-server-value-must-not-appear")
			result := runAuthCommand(t, Runtime{
				In:        strings.NewReader(tokenValue),
				ConfigDir: serverValue,
				Getenv: func(string) string {
					panic("completion read environment")
				},
				IsTerminal: func(int) bool {
					panic("completion inspected terminal")
				},
				ReadPassword: func(int) ([]byte, error) {
					panic("completion read a credential")
				},
			}, "completion", shell)
			if result.code != 0 || result.stderr != "" || result.stdout == "" {
				t.Fatalf("%s completion = %#v", shell, result)
			}
			for _, want := range []string{"artisan", "__complete"} {
				if !strings.Contains(result.stdout, want) {
					t.Errorf("%s completion missing dynamic protocol marker %q", shell, want)
				}
			}
			for _, secret := range []string{tokenValue, serverValue} {
				if strings.Contains(result.stdout+result.stderr, secret) {
					t.Errorf("%s completion exposed %q", shell, secret)
				}
			}
		})
	}
}

func TestCompletionProtocolOffersNestedCommandsAndFlagsWithoutRuntimeAccess(t *testing.T) {
	runtime := Runtime{
		ConfigDir: filepath.Join(t.TempDir(), "missing"),
		Getenv: func(string) string {
			panic("completion protocol read environment")
		},
		IsTerminal: func(int) bool {
			panic("completion protocol inspected terminal")
		},
		ReadPassword: func(int) ([]byte, error) {
			panic("completion protocol read a credential")
		},
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "nested command", args: []string{"__complete", ""}, want: "inventory"},
		{name: "inherited server flag", args: []string{"__complete", "--s"}, want: "--server"},
		{name: "login token stdin flag", args: []string{"__complete", "auth", "login", "--token"}, want: "--token-stdin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runAuthCommand(t, runtime, test.args...)
			if result.code != 0 || !strings.Contains(result.stdout, test.want) {
				t.Fatalf("completion protocol %q = %#v; want %q", test.args, result, test.want)
			}
		})
	}
}

func TestCompletionRemainsRawWithJSONFlag(t *testing.T) {
	result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "completion", "bash", "--json")
	if result.code != 0 || result.stderr != "" || !strings.Contains(result.stdout, "artisan") {
		t.Fatalf("JSON completion = %#v", result)
	}
	if strings.HasPrefix(result.stdout, "{") || strings.Contains(result.stdout, `"ok":true`) {
		t.Fatalf("completion was wrapped in JSON: %.200q", result.stdout)
	}
}

type completionFailWriter struct{}

func (completionFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("completion write failed")
}

func TestCompletionGeneratorWriteFailureUsesOutputErrorPath(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"completion", "bash"}, Runtime{
		Out:       completionFailWriter{},
		Err:       &stderr,
		ConfigDir: filepath.Join(t.TempDir(), "missing"),
	})
	if code != 1 || !strings.Contains(stderr.String(), "failed to write output: completion write failed") {
		t.Fatalf("completion write failure = (%d, %q)", code, stderr.String())
	}
}

func TestCobraPublicCommandManifestAndGeneratedHelp(t *testing.T) {
	expected := []string{
		"artisan",
		"artisan auth",
		"artisan auth login",
		"artisan auth logout",
		"artisan auth status",
		"artisan completion",
		"artisan completion bash",
		"artisan completion fish",
		"artisan completion powershell",
		"artisan completion zsh",
		"artisan inventory",
		"artisan inventory adjust",
		"artisan inventory conflict",
		"artisan inventory conflict list",
		"artisan inventory conflict resolve",
		"artisan inventory conflict show",
		"artisan inventory image",
		"artisan inventory image add",
		"artisan inventory image delete",
		"artisan inventory image download",
		"artisan inventory image reorder",
		"artisan inventory image update",
		"artisan inventory lot",
		"artisan inventory lot archive",
		"artisan inventory lot conflicts",
		"artisan inventory lot create",
		"artisan inventory lot ledger",
		"artisan inventory lot list",
		"artisan inventory lot reservations",
		"artisan inventory lot restore",
		"artisan inventory lot show",
		"artisan inventory lot update",
		"artisan inventory reservation",
		"artisan inventory reservation create",
		"artisan inventory reservation finalize",
		"artisan inventory reservation release",
		"artisan inventory totals",
		"artisan roast",
		"artisan roast chart",
		"artisan roast chart download",
		"artisan roast comment",
		"artisan roast comment list",
		"artisan roast list",
		"artisan roast profile",
		"artisan roast profile download",
		"artisan roast review",
		"artisan roast review post",
		"artisan roast revisions",
		"artisan roast show",
		"artisan skill",
		"artisan skill install",
		"artisan skill show",
		"artisan version",
	}

	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	root.InitDefaultHelpCmd()
	got := []string{root.CommandPath()}
	var walk func(*cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, child := range parent.Commands() {
			if child.Hidden {
				continue
			}
			got = append(got, child.CommandPath())
			walk(child)
		}
	}
	walk(root)
	sort.Strings(got)
	sort.Strings(expected)
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("public command paths =\n%q\nwant\n%q", got, expected)
	}

	for _, path := range expected {
		t.Run(strings.ReplaceAll(path, " ", "_"), func(t *testing.T) {
			args := strings.Fields(strings.TrimPrefix(path, "artisan"))
			args = append(args, "--help")
			result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
			if result.code != 0 || result.stdout == "" || result.stderr != "" || !strings.Contains(result.stdout, "Usage:") {
				t.Fatalf("%s help = %#v", path, result)
			}
		})
	}
}

func TestRepresentativeGeneratedHelpUsesOneJSONEnvelope(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"auth", "login", "--help"},
		{"inventory", "lot", "list", "--help"},
		{"inventory", "totals", "--help"},
		{"roast", "list", "--help"},
		{"roast", "review", "post", "--help"},
		{"skill", "show", "--help"},
	} {
		args = append(args, "--json")
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
			if result.code != 0 || result.stderr != "" || strings.Count(result.stdout, "\n") != 1 {
				t.Fatalf("JSON help %q = %#v", args, result)
			}
			var envelope struct {
				OK   bool `json:"ok"`
				Data struct {
					Usage string `json:"usage"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
				t.Fatalf("decode JSON help %q: %v", args, err)
			}
			if !envelope.OK || !strings.Contains(envelope.Data.Usage, "Usage:") {
				t.Fatalf("JSON help %q envelope = %#v", args, envelope)
			}
		})
	}
}

func TestNormalizeLegacySingleDashArgsOnlyChangesKnownFormsBeforeDoubleDash(t *testing.T) {
	args := []string{
		"-json", "-server=https://inventory.example", "-timeout", "5m",
		"skill", "install", "-directory=/tmp/skills", "-force=false",
		"-unknown", "-json-extra", "--", "-json", "-force",
	}
	got := normalizeLegacySingleDashArgs(args)
	want := []string{
		"--json", "--server=https://inventory.example", "--timeout", "5m",
		"skill", "install", "--directory=/tmp/skills", "--force=false",
		"-unknown", "-json-extra", "--", "-json", "-force",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized args = %q, want %q", got, want)
	}
	if args[0] != "-json" || args[6] != "-directory=/tmp/skills" {
		t.Fatalf("normalization mutated input: %q", args)
	}
}

func TestDescriptionIsKnownValueConsumingLotFlag(t *testing.T) {
	for _, path := range []string{"inventory lot create", "inventory lot update"} {
		if !isKnownLegacySingleDashFlagForPath("description", path) {
			t.Errorf("description is not a known flag for %q", path)
		}
		if !cobraFlagConsumesValueForPath("description", path) {
			t.Errorf("description does not consume a value for %q", path)
		}
	}
	if !cobraFlagConsumesValue("description") {
		t.Fatal("description is absent from exact value-consuming flag enumeration")
	}
}

func TestCanonicalLegacyArgsPreservesRepeatedAndExplicitFalseFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "leaf"}
	cmd.Flags().StringArray("item", nil, "repeatable item")
	cmd.Flags().Bool("cover", true, "cover state")
	if err := cmd.Flags().Parse([]string{"--item=a", "value", "--item=b", "--cover=false"}); err != nil {
		t.Fatal(err)
	}
	got := canonicalLegacyArgs(cmd, cmd.Flags().Args())
	want := []string{"--cover=false", "--item=a", "--item=b", "value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical args = %q, want %q", got, want)
	}
}
