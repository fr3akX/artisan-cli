package command

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCobraRootHelpListsDiscoverableCommands(t *testing.T) {
	result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "--help")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("root help = %#v", result)
	}
	for _, want := range []string{"Authentication and saved credentials", "Manage green-coffee inventory", "Install or inspect the embedded agent skill", "version"} {
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
