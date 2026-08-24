package command

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestCobraRoastCommandPathsAndExactFlagSurface(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	paths := commandPathSet(root)
	for _, want := range []string{
		"artisan roast list",
		"artisan roast show",
		"artisan roast revisions",
		"artisan roast chart download",
		"artisan roast profile download",
		"artisan roast comment list",
		"artisan roast review post",
	} {
		if !paths[want] {
			t.Errorf("missing command %q", want)
		}
	}

	wantFlags := map[string][]string{
		"roast list":             {"all", "cursor", "label-id", "limit", "machine", "roast-at-from", "roast-at-to", "search", "state"},
		"roast show":             {},
		"roast revisions":        {"all", "cursor", "limit"},
		"roast chart download":   {"force"},
		"roast profile download": {"force"},
		"roast comment list":     {"all", "cursor", "limit"},
		"roast review post":      {"body-file", "revision-sha256", "template-version"},
	}
	for path, want := range wantFlags {
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil {
			t.Fatalf("find %s: %v", path, err)
		}
		got := []string{}
		cmd.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
			if flag.Name != "help" {
				got = append(got, flag.Name)
			}
		})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s flags = %q, want %q", path, got, want)
		}
		if cmd.Flags().Lookup("yes") != nil {
			t.Errorf("%s unexpectedly exposes --yes", path)
		}
	}
}

func TestCobraRoastGeneratedHelpAndRootDescription(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	if root.Short != "Artisan Server command line client" {
		t.Fatalf("root short = %q", root.Short)
	}
	for _, test := range []struct {
		args []string
		want []string
	}{
		{[]string{"--help"}, []string{"Artisan Server command line client", "Read private roasts and post review comments"}},
		{[]string{"roast", "list", "--help"}, []string{"artisan roast list", "--roast-at-from", "--roast-at-to", "--label-id"}},
		{[]string{"roast", "show", "--help"}, []string{"artisan roast show ROAST_UUID"}},
		{[]string{"roast", "revisions", "--help"}, []string{"artisan roast revisions ROAST_UUID", "--limit", "--cursor", "--all"}},
		{[]string{"roast", "chart", "download", "--help"}, []string{"artisan roast chart download ROAST_UUID DESTINATION", "--force"}},
		{[]string{"roast", "profile", "download", "--help"}, []string{"artisan roast profile download ROAST_UUID REVISION_NUMBER DESTINATION", "--force"}},
		{[]string{"roast", "comment", "list", "--help"}, []string{"artisan roast comment list ROAST_UUID", "--limit", "--cursor", "--all"}},
		{[]string{"roast", "review", "post", "--help"}, []string{"artisan roast review post ROAST_UUID", "--revision-sha256", "--template-version", "--body-file"}},
	} {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, test.args...)
		if result.code != 0 || result.stderr != "" {
			t.Fatalf("help %q = %#v", test.args, result)
		}
		for _, want := range test.want {
			if !strings.Contains(result.stdout, want) {
				t.Errorf("help %q missing %q:\n%s", test.args, want, result.stdout)
			}
		}
		if strings.Contains(result.stdout, "--yes") {
			t.Errorf("help %q contains forbidden --yes", test.args)
		}
	}

	jsonHelp := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "roast", "review", "post", "--help", "--json")
	if jsonHelp.code != 0 || jsonHelp.stderr != "" || strings.Count(jsonHelp.stdout, "\n") != 1 || !strings.HasPrefix(jsonHelp.stdout, `{"ok":true,"data":{"usage":`) {
		t.Fatalf("JSON help = %#v", jsonHelp)
	}
}

func TestCobraRoastExactPositionalsAndParentErrors(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"roast"}, "A roast command is required"},
		{[]string{"roast", "unknown"}, "Unknown roast command"},
		{[]string{"roast", "chart"}, "A roast chart command is required"},
		{[]string{"roast", "chart", "unknown"}, "Unknown roast chart command"},
		{[]string{"roast", "profile"}, "A roast profile command is required"},
		{[]string{"roast", "comment"}, "A roast comment command is required"},
		{[]string{"roast", "review"}, "A roast review command is required"},
		{[]string{"roast", "list", "extra"}, "roast list does not accept arguments"},
		{[]string{"roast", "show"}, "roast show requires one ROAST_UUID"},
		{[]string{"roast", "show", commandRoastID, "extra"}, "roast show requires one ROAST_UUID"},
		{[]string{"roast", "revisions"}, "roast revisions requires one ROAST_UUID"},
		{[]string{"roast", "chart", "download", commandRoastID}, "roast chart download requires ROAST_UUID DESTINATION"},
		{[]string{"roast", "profile", "download", commandRoastID, "1"}, "roast profile download requires ROAST_UUID REVISION_NUMBER DESTINATION"},
		{[]string{"roast", "comment", "list"}, "roast comment list requires one ROAST_UUID"},
		{[]string{"roast", "review", "post"}, "roast review post requires one ROAST_UUID"},
	} {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, test.args...)
		if result.code != usageExitCode || !strings.Contains(result.stdout+result.stderr, test.want) {
			t.Errorf("Run(%q) = %#v, want %q", test.args, result, test.want)
		}
	}
}

func TestCobraRoastCompletionIsStaticAndPositionAware(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{Getenv: func(string) string {
		t.Fatal("completion loaded configuration")
		return ""
	}}), nil)
	list, _, _ := root.Find([]string{"roast", "list"})
	stateCompletion, exists := list.GetFlagCompletionFunc("state")
	if !exists {
		t.Fatal("state completion missing")
	}
	values, directive := stateCompletion(list, nil, "")
	if !reflect.DeepEqual(values, []string{"awaiting_profile", "parsed", "parse_failed"}) || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("state completion = %q, %v", values, directive)
	}

	review, _, _ := root.Find([]string{"roast", "review", "post"})
	templateCompletion, exists := review.GetFlagCompletionFunc("template-version")
	if !exists {
		t.Fatal("template completion missing")
	}
	values, directive = templateCompletion(review, nil, "")
	if !reflect.DeepEqual(values, []string{api.ReviewTemplateVersion}) || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("template completion = %q, %v", values, directive)
	}
	bodyCompletion, exists := review.GetFlagCompletionFunc("body-file")
	if !exists {
		t.Fatal("body file completion missing")
	}
	_, directive = bodyCompletion(review, nil, "")
	if directive&cobra.ShellCompDirectiveNoFileComp != 0 {
		t.Fatalf("body file completion disables files: %v", directive)
	}

	for _, test := range []struct {
		path   []string
		args   []string
		noFile bool
	}{
		{[]string{"roast", "show"}, nil, true},
		{[]string{"roast", "chart", "download"}, nil, true},
		{[]string{"roast", "chart", "download"}, []string{commandRoastID}, false},
		{[]string{"roast", "chart", "download"}, []string{commandRoastID, "chart.json"}, true},
		{[]string{"roast", "profile", "download"}, []string{commandRoastID}, true},
		{[]string{"roast", "profile", "download"}, []string{commandRoastID, "1"}, false},
		{[]string{"roast", "profile", "download"}, []string{commandRoastID, "1", "profile.alog"}, true},
	} {
		cmd, _, _ := root.Find(test.path)
		_, got := cmd.ValidArgsFunction(cmd, test.args, "")
		if got&cobra.ShellCompDirectiveNoFileComp != 0 != test.noFile {
			t.Errorf("%s args %q directive = %v, noFile want %v", strings.Join(test.path, " "), test.args, got, test.noFile)
		}
	}
}

func TestCobraRoastParseFailuresHonorLaterJSONAndRedactValues(t *testing.T) {
	const secret = "roast-secret-value"
	for _, args := range [][]string{
		{"roast", "list", "--limit", secret, "--json"},
		{"roast", "profile", "download", commandRoastID, "1", "out", "--bad", secret, "--json"},
		{"roast", "review", "post", commandRoastID, "--revision-sha256", secret, "--json"},
	} {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != usageExitCode || result.stderr != "" || !strings.HasPrefix(result.stdout, `{"ok":false,"error":{"code":"usage"`) || strings.Contains(result.stdout, secret) {
			t.Errorf("result for %q = %#v", args, result)
		}
	}
}

func TestCobraRoastLegacySingleDashAndDoubleDashRouting(t *testing.T) {
	got := normalizeLegacySingleDashArgs([]string{
		"-json", "roast", "list", "-limit=10", "-cursor", "opaque", "-all", "-search", "coffee",
		"-roast-at-from", commandTimestamp, "-roast-at-to", commandTimestamp, "-machine", "Loring", "-state", "parsed", "-label-id", commandLotID,
	})
	want := []string{
		"--json", "roast", "list", "--limit=10", "--cursor", "opaque", "--all", "--search", "coffee",
		"--roast-at-from", commandTimestamp, "--roast-at-to", commandTimestamp, "--machine", "Loring", "--state", "parsed", "--label-id", commandLotID,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized = %q, want %q", got, want)
	}
	for _, args := range [][]string{
		{"roast", "chart", "download", "-force", "--", commandRoastID, "-chart.json"},
		{"roast", "profile", "download", "-force=false", "--", commandRoastID, "1", "-profile.alog"},
		{"roast", "review", "post", commandRoastID, "-revision-sha256", strings.Repeat("d", 64), "-template-version", api.ReviewTemplateVersion, "-body-file=-review.txt"},
	} {
		normalized := normalizeLegacySingleDashArgs(args)
		doubleDash := false
		for index, item := range normalized {
			if item == "--" {
				doubleDash = true
				if index+1 < len(normalized) && !strings.HasPrefix(normalized[index+1], "-") && strings.HasPrefix(args[index+1], "-") {
					t.Fatalf("dash positional changed: %q -> %q", args, normalized)
				}
			}
		}
		if strings.Contains(strings.Join(args, " "), " -- ") && !doubleDash {
			t.Fatalf("normalization removed --: %q", normalized)
		}
	}
}

func TestCobraRoastReviewRequiresChangedNonemptyFlagsWithoutRuntimeAccess(t *testing.T) {
	for _, args := range [][]string{
		{"roast", "review", "post", commandRoastID},
		{"roast", "review", "post", commandRoastID, "--revision-sha256", "", "--template-version", api.ReviewTemplateVersion, "--body-file", "body"},
		{"roast", "review", "post", commandRoastID, "--revision-sha256", strings.Repeat("d", 64), "--template-version", "", "--body-file", "body"},
		{"roast", "review", "post", commandRoastID, "--revision-sha256", strings.Repeat("d", 64), "--template-version", api.ReviewTemplateVersion, "--body-file", ""},
		{"roast", "review", "post", commandRoastID, "--yes"},
	} {
		result := runAuthCommand(t, Runtime{ConfigDir: "\x00", Getenv: func(string) string {
			t.Fatal("invalid review flags loaded configuration")
			return ""
		}}, args...)
		if result.code != usageExitCode || result.stdout != "" || result.stderr == "" {
			t.Errorf("result for %q = %#v", args, result)
		}
	}
}
