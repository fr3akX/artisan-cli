package command

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
		"artisan inventory totals",
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

func TestInventoryTotalsCobraHelpHasExactFilterSurface(t *testing.T) {
	result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "inventory", "totals", "--help")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("help result = %#v", result)
	}
	for _, want := range []string{"artisan inventory totals", "--q", "--state", "--availability", "--conflict", "--roast-uuid"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("help missing %q:\n%s", want, result.stdout)
		}
	}
	for _, forbidden := range []string{"--limit", "--cursor", "--all"} {
		if strings.Contains(result.stdout, forbidden) {
			t.Errorf("totals help contains pagination flag %q:\n%s", forbidden, result.stdout)
		}
	}
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	cmd, _, err := root.Find([]string{"inventory", "totals"})
	if err != nil {
		t.Fatal(err)
	}
	var localFlags []string
	cmd.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Name != "help" {
			localFlags = append(localFlags, flag.Name)
		}
	})
	wantFlags := []string{"availability", "conflict", "q", "roast-uuid", "state"}
	if !reflect.DeepEqual(localFlags, wantFlags) {
		t.Errorf("totals local flags = %q, want exactly %q", localFlags, wantFlags)
	}
}

func TestInventoryTotalsCobraCompletionMatchesLotList(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{Getenv: func(string) string {
		t.Fatal("completion loaded configuration")
		return ""
	}}), nil)
	list, _, err := root.Find([]string{"inventory", "lot", "list"})
	if err != nil {
		t.Fatal(err)
	}
	totals, _, err := root.Find([]string{"inventory", "totals"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state", "availability", "conflict"} {
		listCompletion, listExists := list.GetFlagCompletionFunc(name)
		totalsCompletion, totalsExists := totals.GetFlagCompletionFunc(name)
		if !listExists || !totalsExists {
			t.Fatalf("completion registration for --%s: list=%v totals=%v", name, listExists, totalsExists)
		}
		listValues, listDirective := listCompletion(list, nil, "")
		totalsValues, totalsDirective := totalsCompletion(totals, nil, "")
		if !reflect.DeepEqual(totalsValues, listValues) || totalsDirective != listDirective || totalsDirective != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("totals --%s completion = %q, %v; list = %q, %v", name, totalsValues, totalsDirective, listValues, listDirective)
		}
	}
}

func TestInventoryPriceAndDescriptionCobraHelpCompletionAndPreservation(t *testing.T) {
	for _, leaf := range []string{"create", "update"} {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "inventory", "lot", leaf, "--help")
		if result.code != 0 || result.stderr != "" || !strings.Contains(result.stdout, "--price-per-kg-eur") {
			t.Errorf("%s help = %#v", leaf, result)
		}
		const wantDescriptionEntry = "--description string Public description shown on linked public roast pages"
		descriptionEntryFound := false
		for _, line := range strings.Split(result.stdout, "\n") {
			if strings.Join(strings.Fields(line), " ") == wantDescriptionEntry {
				descriptionEntryFound = true
				break
			}
		}
		if !descriptionEntryFound {
			t.Errorf("%s help missing description flag entry %q:\n%s", leaf, wantDescriptionEntry, result.stdout)
		}
	}

	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	update, _, err := root.Find([]string{"inventory", "lot", "update"})
	if err != nil {
		t.Fatal(err)
	}
	completion, exists := update.GetFlagCompletionFunc("clear")
	if !exists {
		t.Fatal("completion for --clear is not registered")
	}
	values, directive := completion(update, nil, "")
	for _, alias := range []string{"price-per-kg-eur", "price_per_kg_eur", "description"} {
		if !containsString(values, alias) {
			t.Errorf("--clear completion missing %q: %q", alias, values)
		}
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("--clear completion directive = %v", directive)
	}

	create, _, err := root.Find([]string{"inventory", "lot", "create"})
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []*cobra.Command{create, update} {
		for _, flagName := range []string{"price-per-kg-eur", "description"} {
			flagCompletion, exists := cmd.GetFlagCompletionFunc(flagName)
			if !exists {
				t.Errorf("%s %s completion is not registered", cmd.CommandPath(), flagName)
				continue
			}
			values, directive := flagCompletion(cmd, nil, "")
			if len(values) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("%s %s completion = %q, %v", cmd.CommandPath(), flagName, values, directive)
			}
		}
	}
	if err := create.Flags().Parse([]string{"--price-per-kg-eur", "12.30"}); err != nil {
		t.Fatal(err)
	}
	if got := canonicalLegacyArgs(create, nil); !reflect.DeepEqual(got, []string{"--price-per-kg-eur=12.30"}) {
		t.Fatalf("canonical price args = %q", got)
	}
}

func TestInventoryDescriptionDashPrefixedValueSurvivesCanonicalRouting(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	update, _, err := root.Find([]string{"inventory", "lot", "update"})
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"inventory", "lot", "update", commandLotID, "--description", "-bright coffee", "--idempotency-key", "key"}
	if err := update.Flags().Parse(args[4:]); err != nil {
		t.Fatal(err)
	}
	got := canonicalLegacyArgs(update, nil)
	want := []string{"--description=-bright coffee", "--idempotency-key=key"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonicalLegacyArgs for %q = %q, want %q", args, got, want)
	}
}

func TestInventoryTotalsCobraPathParseFailureAndGlobalFlags(t *testing.T) {
	for _, args := range [][]string{
		{"inventory", "totals", "--bad"},
		{"--json", "inventory", "totals", "--bad"},
		{"inventory", "totals", "--bad", "--json"},
	} {
		wantPath := "inventory totals"
		if got := knownInventoryCommandPath(args); got != wantPath {
			t.Errorf("knownInventoryCommandPath(%q) = %q, want %q", args, got, wantPath)
		}
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != usageExitCode || !strings.Contains(result.stdout+result.stderr, "Invalid inventory totals option") {
			t.Errorf("parse result for %q = %#v", args, result)
		}
	}
	if got := inventoryCobraParseFailureMessage("inventory totals"); got != "Invalid inventory totals option" {
		t.Fatalf("parse failure message = %q", got)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"lot_count":0,"on_hand_grams":0,"reserved_grams":0,"available_grams":0,"on_hand_value_eur_cents":null,"priced_lot_count":0,"unpriced_lot_count":0}`))
	}))
	defer server.Close()
	for _, args := range [][]string{
		{"--json", "--server", server.URL, "--timeout", "2s", "inventory", "totals"},
		{"inventory", "totals", "--json", "--server", server.URL, "--timeout", "2s"},
	} {
		result := runAuthCommand(t, inventoryRuntime(t, server.URL), args...)
		if result.code != 0 || result.stderr != "" || !strings.HasPrefix(result.stdout, `{"ok":true,"data":`) {
			t.Errorf("global flags for %q = %#v", args, result)
		}
	}
}

func TestInventoryTotalsCobraMatchesLegacyRequestAndOutput(t *testing.T) {
	var queries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"lot_count":2,"on_hand_grams":1500,"reserved_grams":250,"available_grams":1250,"on_hand_value_eur_cents":1234,"priced_lot_count":1,"unpriced_lot_count":1}`))
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	leafArgs := []string{"--q", "12.30", "--state", "active", "--availability", "positive", "--conflict", "none", "--roast-uuid", commandRoastID}
	legacy := runLegacyInventoryCommand(t, runtime, true, append([]string{"totals"}, leafArgs...)...)
	cobraResult := runAuthCommand(t, runtime, append([]string{"--json", "inventory", "totals"}, leafArgs...)...)
	if legacy != cobraResult {
		t.Fatalf("legacy = %#v, Cobra = %#v", legacy, cobraResult)
	}
	if len(queries) != 2 || !reflect.DeepEqual(queries[0], queries[1]) {
		t.Fatalf("queries = %#v", queries)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestCobraInventoryLegacySingleDashFlagsAreNormalized(t *testing.T) {
	got := normalizeLegacySingleDashArgs([]string{"inventory", "lot", "list", "-limit=100", "-state", "active", "-all", "--q", "-state"})
	want := []string{"inventory", "lot", "list", "--limit=100", "--state", "active", "--all", "--q", "-state"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized args = %q, want %q", got, want)
	}
}

func TestCobraInventoryWriteNormalizationPreservesDashPrefixedValuesAndFiles(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "lot create dash-prefixed scalar",
			args: []string{"-json", "inventory", "lot", "create", "-name", "-state", "-varietal", "SL28"},
			want: []string{"--json", "inventory", "lot", "create", "--name", "-state", "--varietal", "SL28"},
		},
		{
			name: "image dash-prefixed path and later legacy flag",
			args: []string{"-server=https://inventory.example", "inventory", "image", "add", commandLotID, "-state", "-caption", "0=Front"},
			want: []string{"--server=https://inventory.example", "inventory", "image", "add", "--caption", "0=Front", "--", commandLotID, "-state"},
		},
		{
			name: "explicit false boolean",
			args: []string{"inventory", "image", "update", commandLotID, commandImageID, "-cover=false"},
			want: []string{"inventory", "image", "update", commandLotID, commandImageID, "--cover=false"},
		},
		{
			name: "dash upload before metadata",
			args: []string{"inventory", "image", "add", commandLotID, "-state.jpg", "--caption", "0=Front"},
			want: []string{"inventory", "image", "add", "--caption", "0=Front", "--", commandLotID, "-state.jpg"},
		},
		{
			name: "recognized dash destination before later option",
			args: []string{"inventory", "image", "download", commandLotID, commandImageID, "-force", "--variant", "thumbnail"},
			want: []string{"inventory", "image", "download", "--variant", "thumbnail", "--", commandLotID, commandImageID, "-force"},
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
