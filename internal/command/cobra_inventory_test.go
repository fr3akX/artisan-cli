package command

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func commandPathSet(root *cobra.Command) map[string]bool {
	paths := make(map[string]bool)
	var walk func(*cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, child := range parent.Commands() {
			paths[child.CommandPath()] = true
			walk(child)
		}
	}
	walk(root)
	return paths
}

func TestCobraInventoryReadCommandPaths(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	got := commandPathSet(root)
	for _, want := range []string{
		"artisan inventory lot list",
		"artisan inventory lot show",
		"artisan inventory lot ledger",
		"artisan inventory lot reservations",
		"artisan inventory lot conflicts",
		"artisan inventory adjust",
		"artisan inventory reservation create",
		"artisan inventory reservation finalize",
		"artisan inventory reservation release",
		"artisan inventory conflict list",
		"artisan inventory conflict show",
		"artisan inventory conflict resolve",
	} {
		if !got[want] {
			t.Errorf("missing command %q", want)
		}
	}
}

func TestCobraInventoryParentsPreserveMissingAndUnknownErrors(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"inventory"}, want: "An inventory command is required\n"},
		{args: []string{"inventory", "unknown"}, want: "Unknown inventory command\n"},
		{args: []string{"inventory", "lot"}, want: "An inventory lot command is required\n"},
		{args: []string{"inventory", "lot", "unknown"}, want: "Unknown inventory lot command\n"},
		{args: []string{"inventory", "reservation"}, want: "An inventory reservation command is required\n"},
		{args: []string{"inventory", "reservation", "unknown"}, want: "Unknown inventory reservation command\n"},
		{args: []string{"inventory", "conflict"}, want: "An inventory conflict command is required\n"},
		{args: []string{"inventory", "conflict", "unknown"}, want: "Unknown inventory conflict command\n"},
	}
	for _, test := range tests {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, test.args...)
		if result.code != usageExitCode || result.stdout != "" || result.stderr != test.want {
			t.Errorf("Run(%q) = %#v, want stderr %q", test.args, result, test.want)
		}
	}
}

func TestCobraInventoryUnknownChildIgnoresTrailingRecognizedLeaf(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "text",
			args:       []string{"inventory", "lot", "bogus", "--bad", "show"},
			wantStderr: "Unknown inventory lot command\n",
		},
		{
			name:       "JSON",
			args:       []string{"--json", "inventory", "lot", "bogus", "--bad", "show"},
			wantStdout: "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Unknown inventory lot command\"}}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, test.args...)
			if result.code != usageExitCode || result.stdout != test.wantStdout || result.stderr != test.wantStderr {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestCobraInventoryParseErrorsAreRedactedAndHonorLaterJSON(t *testing.T) {
	const secret = "supplied-secret-value"
	result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "inventory", "adjust", commandLotID, "--grams", secret, "--json")
	if result.code != usageExitCode || result.stdout != "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Invalid inventory adjust option\"}}\n" || result.stderr != "" {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(result.stdout+result.stderr, secret) {
		t.Fatal("parser error exposed supplied value")
	}
}

func TestCobraInventoryStaticEnumCompletion(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{Getenv: func(string) string {
		t.Fatal("completion loaded configuration")
		return ""
	}}), nil)
	cmd, _, err := root.Find([]string{"inventory", "lot", "list"})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"state":        {"active", "archived"},
		"availability": {"positive", "zero", "negative"},
		"conflict":     {"open", "none"},
	} {
		completion, exists := cmd.GetFlagCompletionFunc(name)
		if !exists {
			t.Fatalf("completion for --%s is not registered", name)
		}
		got, directive := completion(cmd, nil, "")
		if !reflect.DeepEqual(got, want) || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("completion --%s = %q, %v; want %q, no-file", name, got, directive, want)
		}
	}
}

func TestCobraInventoryLegacySingleDashFlagsAreNormalized(t *testing.T) {
	got := normalizeLegacySingleDashArgs([]string{"inventory", "lot", "list", "-limit=100", "-state", "active", "-all", "--q", "-state"})
	want := []string{"inventory", "lot", "list", "--limit=100", "--state", "active", "--all", "--q", "-state"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized args = %q, want %q", got, want)
	}
}

func TestCobraInventoryRawPassthroughArgumentsAreNotNormalized(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "lot create dash-prefixed scalar",
			args: []string{"-json", "inventory", "lot", "create", "--name", "-state"},
			want: []string{"--json", "inventory", "lot", "create", "--name", "-state"},
		},
		{
			name: "image dash-prefixed path",
			args: []string{"-server=https://inventory.example", "inventory", "image", "add", commandLotID, "-state"},
			want: []string{"--server=https://inventory.example", "inventory", "image", "add", commandLotID, "-state"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeLegacySingleDashArgs(test.args)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("normalized args = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCobraInventoryReadHelpDeclaresLocalInterface(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "lot list",
			args: []string{"inventory", "lot", "list", "--help"},
			want: []string{"artisan inventory lot list", "--limit", "--cursor", "--all", "--q", "--state", "--availability", "--conflict", "--roast-uuid"},
		},
		{
			name: "adjust",
			args: []string{"inventory", "adjust", "--help"},
			want: []string{"artisan inventory adjust LOT_ID", "--grams", "--reason", "--reference", "--occurred-at", "--yes", "--idempotency-key"},
		},
		{
			name: "reservation create",
			args: []string{"inventory", "reservation", "create", "--help"},
			want: []string{"--client-reservation-uuid", "--client-instance-uuid", "--roast-uuid", "--lot-id", "--planned-grams", "--occurred-at", "--idempotency-key"},
		},
		{
			name: "conflict resolve",
			args: []string{"inventory", "conflict", "resolve", "--help"},
			want: []string{"artisan inventory conflict resolve CONFLICT_ID", "--note", "--yes", "--idempotency-key"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, test.args...)
			if result.code != 0 || result.stderr != "" {
				t.Fatalf("help result = %#v", result)
			}
			for _, want := range test.want {
				if !strings.Contains(result.stdout, want) {
					t.Errorf("help missing %q:\n%s", want, result.stdout)
				}
			}
		})
	}
}
