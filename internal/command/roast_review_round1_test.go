package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestRoastDownloadDestinationPreflightPrecedesConfigurationAndNetwork(t *testing.T) {
	root := t.TempDir()
	nondirectoryParent := filepath.Join(root, "parent-file")
	if err := os.WriteFile(nondirectoryParent, []byte("parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	directoryDestination := filepath.Join(root, "directory")
	if err := os.Mkdir(directoryDestination, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, command := range []struct {
		name string
		args func(string) []string
	}{
		{name: "chart", args: func(destination string) []string {
			return []string{"--json", "roast", "chart", "download", commandRoastID, destination}
		}},
		{name: "profile", args: func(destination string) []string {
			return []string{"--json", "roast", "profile", "download", commandRoastID, "1", destination}
		}},
	} {
		for _, test := range []struct {
			name        string
			destination string
			code        string
			exit        int
		}{
			{name: "empty", destination: "", code: "invalid_destination", exit: usageExitCode},
			{name: "nul", destination: filepath.Join(root, "bad\x00name"), code: "invalid_destination", exit: usageExitCode},
			{name: "missing parent", destination: filepath.Join(root, "missing", "file"), code: "local_storage_error", exit: 3},
			{name: "nondirectory parent", destination: filepath.Join(nondirectoryParent, "file"), code: "local_storage_error", exit: 3},
			{name: "directory destination", destination: directoryDestination, code: "invalid_destination", exit: usageExitCode},
			{name: "existing destination", destination: existing, code: "local_storage_error", exit: 3},
		} {
			t.Run(command.name+"/"+test.name, func(t *testing.T) {
				runtime := Runtime{ConfigDir: "\x00", Getenv: func(string) string {
					t.Fatal("destination failure loaded authentication configuration")
					return ""
				}}
				result := runAuthCommand(t, runtime, command.args(test.destination)...)
				if result.code != test.exit || result.stderr != "" || !strings.Contains(result.stdout, `"code":"`+test.code+`"`) || strings.Contains(result.stdout, "configuration_error") {
					t.Fatalf("result = %#v", result)
				}
			})
		}
	}
	contents, err := os.ReadFile(existing)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("existing destination changed: %q, %v", contents, err)
	}
}

func TestRoastDownloadEnvironmentalPreflightErrorsAreLocalStorageFailures(t *testing.T) {
	failure := roastDownloadPreflightFailure(&os.PathError{Op: "getwd", Path: ".", Err: os.ErrNotExist}, "invalid", "storage")
	if failure.ExitCode != 3 || failure.Code != "local_storage_error" || failure.Message != "storage" {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestRoastDownloadValidDestinationReachesAuthentication(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"authentication_required","message":"Authentication required"}}`)
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"--json", "roast", "chart", "download", commandRoastID, filepath.Join(t.TempDir(), "chart.json")},
		{"--json", "roast", "profile", "download", commandRoastID, "1", filepath.Join(t.TempDir(), "profile.alog")},
	} {
		before := requests.Load()
		result := runAuthCommand(t, inventoryRuntime(t, server.URL), args...)
		if result.code != 4 || !strings.Contains(result.stdout, `"code":"authentication_required"`) || requests.Load() != before+1 {
			t.Fatalf("result for %q = %#v requests=%d", args, result, requests.Load()-before)
		}
	}
}

func TestRoastCommandErrorMappingsInHumanAndTypedJSON(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body       string
		code       string
		exit       int
		human      string
		plainRoute bool
	}{
		{name: "authentication required", status: http.StatusUnauthorized, body: `{"error":{"code":"authentication_required","message":"Authentication required"}}`, code: "authentication_required", exit: 4, human: "Authentication required"},
		{name: "permission denied", status: http.StatusForbidden, body: `{"error":{"code":"permission_denied","message":"Permission denied"}}`, code: "permission_denied", exit: 5, human: "Permission denied"},
		{name: "server upgrade", status: http.StatusNotFound, code: "server_upgrade_required", exit: 9, human: "upgrade Artisan Server", plainRoute: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.plainRoute {
					http.NotFound(w, nil)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			for _, jsonMode := range []bool{false, true} {
				args := []string{"roast", "list"}
				if jsonMode {
					args = append([]string{"--json"}, args...)
				}
				result := runAuthCommand(t, inventoryRuntime(t, server.URL), args...)
				if result.code != test.exit {
					t.Fatalf("json=%v result=%#v", jsonMode, result)
				}
				if jsonMode {
					var envelope struct {
						OK    bool         `json:"ok"`
						Error output.Error `json:"error"`
					}
					if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil || envelope.OK || envelope.Error.Code != test.code || result.stderr != "" || strings.Contains(result.stdout, `"data"`) {
						t.Fatalf("typed JSON result=%#v envelope=%#v err=%v", result, envelope, err)
					}
				} else if result.stdout != "" || !strings.Contains(strings.ToLower(result.stderr), strings.ToLower(test.human)) {
					t.Fatalf("human result=%#v", result)
				}
			}
		})
	}

	t.Run("network failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		serverURL := server.URL
		server.Close()
		for _, jsonMode := range []bool{false, true} {
			args := []string{"roast", "list"}
			if jsonMode {
				args = append([]string{"--json"}, args...)
			}
			result := runAuthCommand(t, inventoryRuntime(t, serverURL), args...)
			if result.code != 8 || !strings.Contains(result.stdout+result.stderr, map[bool]string{false: "Unable to communicate with the server", true: `"code":"network_error"`}[jsonMode]) {
				t.Fatalf("json=%v result=%#v", jsonMode, result)
			}
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		for _, jsonMode := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var stdout, stderr bytes.Buffer
			args := []string{"roast", "list"}
			if jsonMode {
				args = append([]string{"--json"}, args...)
			}
			code := Run(ctx, args, Runtime{Out: &stdout, Err: &stderr, ConfigDir: t.TempDir(), Getenv: func(name string) string {
				switch name {
				case "ARTISAN_SERVER_URL":
					return "http://127.0.0.1:1"
				case "ARTISAN_SERVER_TOKEN":
					return commandTestToken
				default:
					return ""
				}
			}})
			if code != 130 || !strings.Contains(stdout.String()+stderr.String(), map[bool]string{false: "Operation interrupted", true: `"code":"interrupted"`}[jsonMode]) {
				t.Fatalf("json=%v code=%d stdout=%q stderr=%q", jsonMode, code, stdout.String(), stderr.String())
			}
			if jsonMode && stderr.Len() != 0 || !jsonMode && stdout.Len() != 0 {
				t.Fatalf("json=%v stdout=%q stderr=%q", jsonMode, stdout.String(), stderr.String())
			}
		}
	})
}

func TestRoastDownloadVisibleDurabilityUncertaintyIsExactInHumanAndJSON(t *testing.T) {
	for _, download := range []string{"chart", "profile"} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(download+"/json="+strconv.FormatBool(jsonMode), func(t *testing.T) {
				t.Parallel()
				destination := filepath.Join(t.TempDir(), download)
				runtime := inventoryRuntime(t, "http://127.0.0.1:1")
				hooks := roastDownloadHooks{}
				if download == "chart" {
					hooks.chart = func(_ context.Context, _ *api.Client, _ string, path string, _ bool) (api.RoastChartDownload, *output.Error) {
						if err := os.WriteFile(path, []byte("exact chart"), 0o600); err != nil {
							t.Fatal(err)
						}
						return api.RoastChartDownload{Path: path, RoastUUID: commandRoastID, FileBytes: 11}, &output.Error{ExitCode: 3, Code: "local_storage_error", Message: "The roast chart is installed, but storage durability is uncertain"}
					}
				} else {
					hooks.profile = func(_ context.Context, _ *api.Client, _ string, _ int64, path string, _ bool) (api.RoastProfileDownload, *output.Error) {
						if err := os.WriteFile(path, []byte("exact profile"), 0o600); err != nil {
							t.Fatal(err)
						}
						return api.RoastProfileDownload{Path: path, RoastUUID: commandRoastID, RevisionNumber: 1, Bytes: 13}, &output.Error{ExitCode: 3, Code: "local_storage_error", Message: "The roast profile is installed, but storage durability is uncertain"}
					}
				}
				args := []string{"roast", download, "download", commandRoastID}
				if download == "profile" {
					args = append(args, "1")
				}
				args = append(args, destination)
				if jsonMode {
					args = append([]string{"--json"}, args...)
				}
				result := runRoastCommandWithContext(t, withRoastDownloadHooks(context.Background(), hooks), runtime, args...)
				contents, err := os.ReadFile(destination)
				if result.code != 3 || err != nil || !strings.HasPrefix(string(contents), "exact ") || !strings.Contains(result.stdout+result.stderr, "storage durability is uncertain") {
					t.Fatalf("result=%#v contents=%q err=%v", result, contents, err)
				}
				if jsonMode {
					var envelope struct {
						OK    bool         `json:"ok"`
						Error output.Error `json:"error"`
					}
					if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil || envelope.OK || envelope.Error.Code != "local_storage_error" || result.stderr != "" || strings.Contains(result.stdout, `"data"`) {
						t.Fatalf("typed JSON result=%#v envelope=%#v err=%v", result, envelope, err)
					}
				} else if result.stdout != "" || result.stderr == "" {
					t.Fatalf("human streams = %#v", result)
				}
			})
		}
	}
}

func runRoastCommandWithContext(t *testing.T, ctx context.Context, runtime Runtime, args ...string) commandResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	runtime.Out = &stdout
	runtime.Err = &stderr
	code := Run(ctx, args, runtime)
	return commandResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestCobraRoastExecutesEveryLegacySingleDashFlagAndDashPath(t *testing.T) {
	profile := []byte("legacy profile")
	profileSHA := commandSHA(profile)
	chart := []byte(`{"control":{"markers":[],"steps":[]},"core":{"bt":[100.0],"bt_ror":[null],"et":[120.0],"et_ror":[null],"time_seconds":[0.0]},"events":{"milestones":[],"special":[]},"extra":{"series":[]},"parser_version":"artisan-4-v1","schema_version":1,"source_temperature_unit":"C","summary":{"duration_seconds":0.0,"extra_series_count":0,"sample_count":1,"special_event_count":0}}`)
	compressed := commandGzip(t, chart)
	body := commandReviewBody()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/roasts":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
		case "/api/v1/roasts/" + commandRoastID:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, commandRoastDetailJSON(int64(len(profile))))
		case "/api/v1/roasts/" + commandRoastID + "/revisions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":null}`, commandRoastRevisionJSON(1, profileSHA, int64(len(profile))))
		case "/api/v1/roasts/" + commandRoastID + "/revisions/1/download":
			setCommandProfileHeaders(w.Header(), profile, profileSHA)
			_, _ = w.Write(profile)
		case "/api/v1/roasts/" + commandRoastID + "/chart":
			setCommandChartHeaders(w.Header(), compressed)
			_, _ = w.Write(compressed)
		case "/api/v1/roasts/" + commandRoastID + "/comments":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
		case "/api/v1/roasts/" + commandRoastID + "/comments/ai-review":
			setCommandReviewHeaders(w.Header(), false)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, commandCommentJSON(&body, false))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	working := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	if err := os.WriteFile("-review.txt", []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	commands := [][]string{
		{"-json", "-server", server.URL, "-timeout=2s", "roast", "list", "-limit=10", "-cursor=", "-all=false", "-search", "coffee", "-roast-at-from", commandTimestamp, "-roast-at-to", commandTimestamp, "-machine", "Loring", "-state", "parsed", "-label-id", commandLabelID},
		{"-json", "roast", "revisions", commandRoastID, "-limit", "10", "-cursor=", "-all=false"},
		{"-json", "roast", "comment", "list", commandRoastID, "-limit=10", "-cursor=", "-all=false"},
		{"-json", "roast", "chart", "download", "-force", "--", commandRoastID, "-chart.json"},
		{"-json", "roast", "profile", "download", "-force=false", "--", commandRoastID, "1", "-profile.alog"},
		{"-json", "roast", "review", "post", "-revision-sha256", commandRoastSHA, "-template-version", api.ReviewTemplateVersion, "-body-file=-review.txt", "--", commandRoastID},
	}
	for _, args := range commands {
		runtime := inventoryRuntime(t, server.URL)
		result := runAuthCommand(t, runtime, args...)
		if result.code != 0 || result.stderr != "" || !strings.HasPrefix(result.stdout, `{"ok":true,"data":`) {
			t.Fatalf("Run(%q) = %#v", args, result)
		}
	}
	for path, want := range map[string][]byte{"-chart.json": chart, "-profile.alog": profile} {
		contents, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(contents, want) {
			t.Fatalf("%s = %q, %v", path, contents, err)
		}
	}
}

func TestRoastCommandSurfaceAndOutputsExcludeProviderConfigurationCorpus(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	roast, _, err := root.Find([]string{"roast"})
	if err != nil || roast == root {
		t.Fatalf("find roast command: command=%v err=%v", roast, err)
	}
	var surface strings.Builder
	walkCommandSurface(roast, &surface)
	outputs := []string{
		surface.String(),
		runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "--json", "roast", "show", "bad").stdout,
		runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "roast", "review", "post", commandRoastID).stderr,
	}
	for _, value := range outputs {
		if match := prohibitedRoastSurface(value); match != "" {
			t.Fatalf("roast command corpus contains forbidden %q: %s", match, value)
		}
	}
}

func TestProhibitedRoastSurfaceAllowsReviewTokenSecurityProse(t *testing.T) {
	for _, value := range []string{
		"Review tokens are never accepted or stored",
		"Bearer token security requirements",
		"The revision token remains private",
	} {
		if match := prohibitedRoastSurface(value); match != "" {
			t.Fatalf("legitimate token prose %q matched %q", value, match)
		}
	}
}

func TestWalkCommandSurfaceDetectsEveryRepresentativeProhibitedSurface(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*cobra.Command)
	}{
		{name: "root local flag", inject: func(root *cobra.Command) { root.Flags().String("provider", "", "Select a backend") }},
		{name: "child persistent flag", inject: func(root *cobra.Command) {
			child := &cobra.Command{Use: "safe"}
			child.PersistentFlags().String("api-key", "", "Credential")
			root.AddCommand(child)
		}},
		{name: "use", inject: func(root *cobra.Command) { root.AddCommand(&cobra.Command{Use: "model VALUE"}) }},
		{name: "short", inject: func(root *cobra.Command) { root.AddCommand(&cobra.Command{Use: "safe", Short: "Configure prompt"}) }},
		{name: "long", inject: func(root *cobra.Command) { root.AddCommand(&cobra.Command{Use: "safe", Long: "Select an AI provider"}) }},
		{name: "example", inject: func(root *cobra.Command) {
			root.AddCommand(&cobra.Command{Use: "safe", Example: "artisan roast safe --token-budget 10"})
		}},
		{name: "aliases", inject: func(root *cobra.Command) { root.AddCommand(&cobra.Command{Use: "safe", Aliases: []string{"cost"}}) }},
		{name: "valid args", inject: func(root *cobra.Command) {
			root.AddCommand(&cobra.Command{Use: "safe", ValidArgs: []string{"model_name"}})
		}},
		{name: "completion", inject: func(root *cobra.Command) {
			root.AddCommand(&cobra.Command{Use: "safe", ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
				return []string{"api_key"}, cobra.ShellCompDirectiveNoFileComp
			}})
		}},
		{name: "flag completion", inject: func(root *cobra.Command) {
			child := &cobra.Command{Use: "safe"}
			child.Flags().String("engine", "", "Backend")
			if err := child.RegisterFlagCompletionFunc("engine", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
				return []string{"cost"}, cobra.ShellCompDirectiveNoFileComp
			}); err != nil {
				panic(err)
			}
			root.AddCommand(child)
		}},
		{name: "flag usage", inject: func(root *cobra.Command) {
			child := &cobra.Command{Use: "safe"}
			child.Flags().String("engine", "", "Choose a model")
			root.AddCommand(child)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := &cobra.Command{Use: "roast", SilenceUsage: true, SilenceErrors: true}
			test.inject(root)
			var surface strings.Builder
			walkCommandSurface(root, &surface)
			if match := prohibitedRoastSurface(surface.String()); match == "" {
				t.Fatalf("prohibited injection was not discovered; surface=%q", surface.String())
			}
		})
	}
}

var prohibitedRoastText = regexp.MustCompile(`(?i)(^|[^a-z0-9])(?:provider|api[ _-]key|model(?:[ _-]name)?|prompt|token[ _-]budget|cost)($|[^a-z0-9])`)

func prohibitedRoastSurface(surface string) string {
	for _, line := range strings.Split(surface, "\n") {
		normalized := strings.ReplaceAll(strings.TrimLeft(strings.ToLower(strings.TrimSpace(line)), "-"), "_", "-")
		switch normalized {
		case "provider", "api-key", "model", "model-name", "prompt", "token-budget", "cost":
			return line
		}
		if match := prohibitedRoastText.FindString(line); match != "" {
			return strings.TrimSpace(match)
		}
	}
	return ""
}

func walkCommandSurface(command *cobra.Command, destination *strings.Builder) {
	writeCommandSurfaceValue(destination, command.CommandPath())
	writeCommandSurfaceValue(destination, command.Use)
	writeCommandSurfaceValue(destination, command.Short)
	writeCommandSurfaceValue(destination, command.Long)
	writeCommandSurfaceValue(destination, command.Example)
	for _, alias := range command.Aliases {
		writeCommandSurfaceValue(destination, alias)
	}
	for _, validArg := range command.ValidArgs {
		writeCommandSurfaceValue(destination, validArg)
	}
	if command.ValidArgsFunction != nil {
		completions, _ := command.ValidArgsFunction(command, nil, "")
		for _, completion := range completions {
			writeCommandSurfaceValue(destination, completion)
		}
	}
	walkCommandFlags(command, command.LocalNonPersistentFlags(), destination)
	walkCommandFlags(command, command.PersistentFlags(), destination)
	walkCommandFlags(command, command.InheritedFlags(), destination)
	for _, child := range command.Commands() {
		walkCommandSurface(child, destination)
	}
}

func walkCommandFlags(command *cobra.Command, flags *pflag.FlagSet, destination *strings.Builder) {
	flags.VisitAll(func(flag *pflag.Flag) {
		writeCommandSurfaceValue(destination, flag.Name)
		writeCommandSurfaceValue(destination, flag.Usage)
		if completion, exists := command.GetFlagCompletionFunc(flag.Name); exists {
			values, _ := completion(command, nil, "")
			for _, value := range values {
				writeCommandSurfaceValue(destination, value)
			}
		}
	})
}

func writeCommandSurfaceValue(destination *strings.Builder, value string) {
	destination.WriteString(value)
	destination.WriteByte('\n')
}
