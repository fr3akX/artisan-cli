package command

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
)

const commandRoastSHA = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
const commandCommentID = "bbbbbbbbbbbb4bbb8bbbbbbbbbbbbbbb"
const commandLabelID = "cccccccccccc4ccc8ccccccccccccccc"

func commandRoastRevisionJSON(number int64, sha string, byteSize int64) string {
	return fmt.Sprintf(`{"revision_number":%d,"sha256":%q,"byte_size":%d,"parser_version":"artisan-4-v1","parse_state":"parsed","parse_diagnostic_code":null,"parse_diagnostic_message":null,"uploaded_at":%q,"metadata":{"note":"line\\tab"},"reparse_recommended":false}`,
		number, sha, byteSize, commandTimestamp)
}

func commandRoastListItemJSON() string {
	return `{"roast_uuid":"` + commandRoastID + `","state":"parsed","roast_at":"` + commandTimestamp + `","title":"Review \\ roast","batch_prefix":"B","batch_number":7,"batch_position":2,"operator":"Operator","machine":"Loring","machine_setup":"S70","temperature_unit":"C","duration_seconds":720.5,"green_weight_kg":1.25,"roasted_weight_kg":1.05,"revision_count":1,"updated_at":"` + commandTimestamp + `","labels":[{"label_uuid":"` + commandLabelID + `","name":"Review","color":"violet","archived":false}]}`
}

func commandRoastDetailJSON(byteSize int64) string {
	return strings.TrimSuffix(commandRoastListItemJSON(), "}") + `,"current_metadata":{"private":"value\\nnext"},"current_revision":` + commandRoastRevisionJSON(1, commandRoastSHA, byteSize) + `,"links":{"self":"/api/v1/roasts/` + commandRoastID + `","chart":"/api/v1/roasts/` + commandRoastID + `/chart","revisions":"/api/v1/roasts/` + commandRoastID + `/revisions"}}`
}

func commandCommentJSON(body *string, deleted bool) string {
	bodyJSON := "null"
	deletedAt := "null"
	if body != nil {
		encoded, _ := json.Marshal(*body)
		bodyJSON = string(encoded)
	}
	if deleted {
		deletedAt = strconv.Quote(commandTimestamp)
	}
	return `{"comment_uuid":"` + commandCommentID + `","roast_uuid":"` + commandRoastID + `","author_nickname":"Member","body":` + bodyJSON + `,"created_at":"` + commandTimestamp + `","edited_at":null,"deleted_at":` + deletedAt + `,"is_deleted":` + strconv.FormatBool(deleted) + `,"can_edit":false,"can_delete":false}`
}

func commandReviewBody() string {
	return "AI roast analysis\nTemplate: " + api.ReviewTemplateVersion + "\nProfile revision: 1 (" + commandRoastSHA + ")\n\nOverall assessment\nMeasured evidence."
}

func TestRoastReadCommandsHumanAndJSONForMemberAndAdmin(t *testing.T) {
	hostileComment := "First line\nsecond\t\\line"
	for _, role := range []string{"member", "admin"} {
		t.Run(role, func(t *testing.T) {
			token := role + "-roast-token"
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Errorf("authorization = %q", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/roasts":
					_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"next\\ncursor"}`, commandRoastListItemJSON())
				case "/api/v1/roasts/" + commandRoastID:
					_, _ = io.WriteString(w, commandRoastDetailJSON(1234))
				case "/api/v1/roasts/" + commandRoastID + "/revisions":
					_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":null}`, commandRoastRevisionJSON(1, commandRoastSHA, 1234))
				case "/api/v1/roasts/" + commandRoastID + "/comments":
					_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":null}`, commandCommentJSON(&hostileComment, false))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			runtime := inventoryRuntime(t, server.URL)
			runtime.Getenv = func(name string) string {
				switch name {
				case "ARTISAN_SERVER_URL":
					return server.URL
				case "ARTISAN_SERVER_TOKEN":
					return token
				default:
					return ""
				}
			}

			commands := []struct {
				args      []string
				humanWant []string
				jsonField string
			}{
				{[]string{"roast", "list", "--limit", "10", "--search", "Review", "--state", "parsed"}, []string{"ROAST UUID", commandRoastID, `Review \\ roast`, `Next cursor: next\\ncursor`}, "items"},
				{[]string{"roast", "show", commandRoastID}, []string{"Roast UUID: " + commandRoastID, "Title: Review \\\\ roast", `Current metadata: {"private":"value\\\\nnext"}`, "LABEL UUID"}, "current_revision"},
				{[]string{"roast", "revisions", commandRoastID, "--all"}, []string{"REVISION", commandRoastSHA, `{"note":"line\\\\tab"}`}, "items"},
				{[]string{"roast", "comment", "list", commandRoastID}, []string{"COMMENT UUID", commandCommentID, `First line\nsecond\t\\line`}, "items"},
			}
			for _, command := range commands {
				human := runAuthCommand(t, runtime, command.args...)
				if human.code != 0 || human.stderr != "" {
					t.Fatalf("human %q = %#v", command.args, human)
				}
				for _, want := range command.humanWant {
					if !strings.Contains(human.stdout, want) {
						t.Errorf("human %q missing %q:\n%s", command.args, want, human.stdout)
					}
				}
				jsonArgs := append([]string{"--json"}, command.args...)
				machine := runAuthCommand(t, runtime, jsonArgs...)
				if machine.code != 0 || machine.stderr != "" || strings.Count(machine.stdout, "\n") != 1 {
					t.Fatalf("JSON %q = %#v", command.args, machine)
				}
				var envelope map[string]any
				if err := json.Unmarshal([]byte(machine.stdout), &envelope); err != nil || envelope["ok"] != true {
					t.Fatalf("JSON envelope %q = %#v, %v", command.args, envelope, err)
				}
				data := envelope["data"].(map[string]any)
				if _, exists := data[command.jsonField]; !exists {
					t.Errorf("JSON data for %q missing %q: %#v", command.args, command.jsonField, data)
				}
			}
			if requests.Load() != 8 {
				t.Fatalf("requests = %d, want one per invocation", requests.Load())
			}
		})
	}
}

func TestRoastListMapsEveryFilterAndAllTraversal(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "next" {
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"next"}`, commandRoastListItemJSON())
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "roast", "list",
		"--limit", "1", "--all", "--search", "coffee", "--roast-at-from", "2026-08-04T12:00:00+02:00",
		"--roast-at-to", "2026-08-04T13:00:00+02:00", "--machine", "Loring", "--state", "parsed", "--label-id", "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	if result.code != 9 { // a followed empty continuation is a hostile no-progress page
		t.Fatalf("result = %#v", result)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "roast_at_from=2026-08-04T10%3A00%3A00Z") || !strings.Contains(queries[0], "label_id="+commandLabelID) || !strings.Contains(queries[1], "cursor=next") {
		t.Fatalf("queries = %q", queries)
	}
}

func TestRoastProfileDownloadHumanJSONNoClobberAndHostilePathEscaping(t *testing.T) {
	profile := []byte("exact profile bytes\x00\xff")
	sum := sha256.Sum256(profile)
	sha := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/roasts/" + commandRoastID + "/revisions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":null}`, commandRoastRevisionJSON(1, sha, int64(len(profile))))
		case "/api/v1/roasts/" + commandRoastID + "/revisions/1/download":
			setCommandProfileHeaders(w.Header(), profile, sha)
			_, _ = w.Write(profile)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "profile\nname.alog")
	human := runAuthCommand(t, inventoryRuntime(t, server.URL), "roast", "profile", "download", commandRoastID, "1", destination)
	if human.code != 0 || human.stderr != "" || !strings.Contains(human.stdout, fmt.Sprintf("Downloaded %d bytes to %s", len(profile), strings.ReplaceAll(destination, "\n", `\n`))) || !strings.Contains(human.stdout, sha) {
		t.Fatalf("human = %#v", human)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(contents, profile) {
		t.Fatalf("profile = %x, %v", contents, err)
	}

	existing := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "roast", "profile", "download", commandRoastID, "1", destination)
	if existing.code == 0 || !strings.Contains(existing.stdout, `"code":"local_storage_error"`) {
		t.Fatalf("existing = %#v", existing)
	}

	machineDestination := filepath.Join(t.TempDir(), "machine.alog")
	machine := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "roast", "profile", "download", commandRoastID, "1", machineDestination)
	if machine.code != 0 || strings.Contains(machine.stdout, string(profile)) || !strings.Contains(machine.stdout, `"bytes":`) || !strings.Contains(machine.stdout, `"sha256":`) {
		t.Fatalf("machine = %#v", machine)
	}
}

func TestRoastChartDownloadHumanAndJSONDoNotEmbedFileBytes(t *testing.T) {
	chart := []byte(`{"control":{"markers":[],"steps":[]},"core":{"bt":[100.0],"bt_ror":[null],"et":[120.0],"et_ror":[null],"time_seconds":[0.0]},"events":{"milestones":[],"special":[]},"extra":{"series":[]},"parser_version":"artisan-4-v1","schema_version":1,"source_temperature_unit":"C","summary":{"duration_seconds":0.0,"extra_series_count":0,"sample_count":1,"special_event_count":0}}`)
	compressed := commandGzip(t, chart)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/roasts/" + commandRoastID:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, commandRoastDetailJSON(1234))
		case "/api/v1/roasts/" + commandRoastID + "/chart":
			setCommandChartHeaders(w.Header(), compressed)
			_, _ = w.Write(compressed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	humanPath := filepath.Join(t.TempDir(), "chart.json")
	human := runAuthCommand(t, inventoryRuntime(t, server.URL), "roast", "chart", "download", commandRoastID, humanPath)
	if human.code != 0 || human.stderr != "" || !strings.Contains(human.stdout, fmt.Sprintf("Downloaded %d bytes to %s", len(chart), humanPath)) || !strings.Contains(human.stdout, "Compressed SHA-256") {
		t.Fatalf("human = %#v", human)
	}
	machinePath := filepath.Join(t.TempDir(), "chart.json")
	machine := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "roast", "chart", "download", commandRoastID, machinePath)
	if machine.code != 0 || machine.stderr != "" || strings.Contains(machine.stdout, string(chart)) || !strings.Contains(machine.stdout, `"compressed_bytes":`) || !strings.Contains(machine.stdout, `"file_sha256":`) {
		t.Fatalf("machine = %#v", machine)
	}
}

func TestRoastChartStaleRevisionPreservesForcedDestination(t *testing.T) {
	chart := []byte(`{"control":{"markers":[],"steps":[]},"core":{"bt":[100.0],"bt_ror":[null],"et":[120.0],"et_ror":[null],"time_seconds":[0.0]},"events":{"milestones":[],"special":[]},"extra":{"series":[]},"parser_version":"artisan-4-v1","schema_version":1,"source_temperature_unit":"C","summary":{"duration_seconds":0.0,"extra_series_count":0,"sample_count":1,"special_event_count":0}}`)
	compressed := commandGzip(t, chart)
	var detailReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/roasts/" + commandRoastID:
			w.Header().Set("Content-Type", "application/json")
			detail := commandRoastDetailJSON(1234)
			if detailReads.Add(1) > 1 {
				detail = strings.Replace(detail, commandRoastSHA, strings.Repeat("e", 64), 1)
			}
			_, _ = io.WriteString(w, detail)
		case "/api/v1/roasts/" + commandRoastID + "/chart":
			setCommandChartHeaders(w.Header(), compressed)
			_, _ = w.Write(compressed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "chart.json")
	const prior = "prior chart"
	if err := os.WriteFile(destination, []byte(prior), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "roast", "chart", "download", commandRoastID, destination, "--force")
	contents, err := os.ReadFile(destination)
	if result.code != 7 || !strings.Contains(result.stdout, `"code":"roast_revision_changed"`) || err != nil || string(contents) != prior {
		t.Fatalf("result=%#v contents=%q err=%v", result, contents, err)
	}
}

func TestRoastReviewPostParsesFileBeforeConfigurationPostsWithoutPromptAndRendersReplay(t *testing.T) {
	body := commandReviewBody()
	bodyPath := filepath.Join(t.TempDir(), "review.txt")
	if err := os.WriteFile(bodyPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, replay := range []bool{false, true} {
		t.Run(strconv.FormatBool(replay), func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, commandRoastDetailJSON(1234))
					return
				}
				posts.Add(1)
				if r.URL.Path != "/api/v1/roasts/"+commandRoastID+"/comments/ai-review" {
					t.Errorf("post path = %s", r.URL.Path)
				}
				var request api.RoastReviewRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Body != body || request.RevisionSHA256 != commandRoastSHA || request.TemplateVersion != api.ReviewTemplateVersion {
					t.Errorf("request = %#v, %v", request, err)
				}
				setCommandReviewHeaders(w.Header(), replay)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, commandCommentJSON(&body, false))
			}))
			defer server.Close()
			runtime := inventoryRuntime(t, server.URL)
			runtime.In = errorReader{}
			runtime.IsTerminal = func(int) bool { t.Fatal("review prompted"); return false }
			runtime.ReadPassword = func(int) ([]byte, error) { t.Fatal("review read password"); return nil, nil }
			args := []string{"roast", "review", "post", commandRoastID, "--revision-sha256", commandRoastSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", bodyPath}
			result := runAuthCommand(t, runtime, args...)
			wantResult := "Created"
			if replay {
				wantResult = "Existing review"
			}
			if result.code != 0 || result.stderr != "" || posts.Load() != 1 || !strings.Contains(result.stdout, "Comment UUID: "+commandCommentID) || !strings.Contains(result.stdout, "Author: Member") || !strings.Contains(result.stdout, "Result: "+wantResult) || !strings.Contains(result.stdout, `Body: AI roast analysis\nTemplate:`) {
				t.Fatalf("result = %#v posts=%d", result, posts.Load())
			}
			if replay {
				machine := runAuthCommand(t, runtime, append([]string{"--json"}, args...)...)
				if machine.code != 0 || machine.stderr != "" || posts.Load() != 2 || !strings.Contains(machine.stdout, `"comment":{"comment_uuid":"`+commandCommentID+`"`) || !strings.Contains(machine.stdout, `"idempotent_replay":true`) {
					t.Fatalf("machine = %#v posts=%d", machine, posts.Load())
				}
			}
		})
	}
}

func TestRoastLocalValidationPrecedesConfigurationAndNetwork(t *testing.T) {
	validBody := filepath.Join(t.TempDir(), "review.txt")
	if err := os.WriteFile(validBody, []byte(commandReviewBody()), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		code string
	}{
		{[]string{"--json", "roast", "show", "BAD"}, "invalid_roast_uuid"},
		{[]string{"--json", "roast", "list", "--limit", "101"}, "invalid_roast_filter"},
		{[]string{"--json", "roast", "profile", "download", commandRoastID, "zero", "out"}, "invalid_revision_number"},
		{[]string{"--json", "roast", "profile", "download", commandRoastID, "0", "out"}, "invalid_revision_number"},
		{[]string{"--json", "roast", "review", "post", commandRoastID, "--revision-sha256", strings.Repeat("A", 64), "--template-version", api.ReviewTemplateVersion, "--body-file", validBody}, "invalid_review_file"},
		{[]string{"--json", "roast", "review", "post", commandRoastID, "--revision-sha256", commandRoastSHA, "--template-version", api.ReviewTemplateVersion, "--body-file", filepath.Join(t.TempDir(), "missing")}, "invalid_review_file"},
	} {
		runtime := Runtime{ConfigDir: "\x00", Getenv: func(string) string {
			t.Fatal("local validation loaded configuration")
			return ""
		}}
		result := runAuthCommand(t, runtime, test.args...)
		if result.code != usageExitCode || !strings.Contains(result.stdout, `"code":"`+test.code+`"`) {
			t.Errorf("result for %q = %#v", test.args, result)
		}
	}
}

func TestRoastErrorsCancellationSecretsAndBrokenStreams(t *testing.T) {
	t.Run("server upgrade", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()
		result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "roast", "list")
		if result.code != 9 || !strings.Contains(result.stdout, `"code":"server_upgrade_required"`) {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("secret reflection", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"error":{"code":"authentication_required","message":%q}}`, "bad "+commandTestToken)
		}))
		defer server.Close()
		result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "roast", "list")
		if result.code == 0 || strings.Contains(result.stdout+result.stderr, commandTestToken) {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		code := Run(ctx, []string{"--json", "roast", "list"}, Runtime{Out: &stdout, Err: &stderr, ConfigDir: t.TempDir(), Getenv: func(name string) string {
			if name == "ARTISAN_SERVER_URL" {
				return "http://127.0.0.1:1"
			}
			if name == "ARTISAN_SERVER_TOKEN" {
				return commandTestToken
			}
			return ""
		}})
		if code != 130 || !strings.Contains(stdout.String(), `"code":"interrupted"`) || stderr.Len() != 0 {
			t.Fatalf("result = %d %q %q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("broken success streams", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
		}))
		defer server.Close()
		for _, args := range [][]string{{"roast", "list"}, {"--json", "roast", "list"}} {
			var stderr bytes.Buffer
			runtime := inventoryRuntime(t, server.URL)
			runtime.Out = commandFailWriter{}
			runtime.Err = &stderr
			code := Run(context.Background(), args, runtime)
			if code != 1 || !strings.Contains(stderr.String(), "failed to write output") {
				t.Fatalf("result for %q = %d %q", args, code, stderr.String())
			}
		}
	})

	t.Run("broken error stream", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"permission_denied","message":"Forbidden"}}`)
		}))
		defer server.Close()
		runtime := inventoryRuntime(t, server.URL)
		runtime.Out = io.Discard
		runtime.Err = commandFailWriter{}
		if code := Run(context.Background(), []string{"roast", "list"}, runtime); code != 1 {
			t.Fatalf("code = %d", code)
		}
	})
}

func TestAuthenticatedClientAndAPISuccessRefactorPreserveInventoryBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	before := runLegacyInventoryCommand(t, runtime, true, "lot", "list")
	after := runAuthCommand(t, runtime, "--json", "inventory", "lot", "list")
	if !reflect.DeepEqual(before, after) || after.stdout != `{"ok":true,"data":{"items":[],"next_cursor":null}}`+"\n" {
		t.Fatalf("legacy=%#v cobra=%#v", before, after)
	}
}

type commandFailWriter struct{}

func (commandFailWriter) Write([]byte) (int, error) { return 0, errors.New("broken command output") }

func commandGzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	var result bytes.Buffer
	writer := gzip.NewWriter(&result)
	writer.Header.ModTime = time.Unix(0, 0)
	writer.Header.OS = 255
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func commandSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func setCommandChartHeaders(header http.Header, compressed []byte) {
	sha := commandSHA(compressed)
	header.Set("Content-Type", "application/json")
	header.Set("Content-Encoding", "gzip")
	header.Set("Content-Length", strconv.Itoa(len(compressed)))
	header.Set("ETag", `"`+sha+`"`)
	header.Set("X-Content-SHA256", sha)
	header.Set("X-Checksum-SHA256", sha)
	header.Set("X-Parser-Version", "artisan-4-v1")
	header.Set("X-Chart-Schema-Version", "1")
}

func setCommandProfileHeaders(header http.Header, profile []byte, sha string) {
	header.Set("Content-Type", "application/x-artisan-profile")
	header.Set("Content-Disposition", `attachment; filename="profile.alog"`)
	header.Set("Content-Length", strconv.Itoa(len(profile)))
	header.Set("ETag", `"`+sha+`"`)
	header.Set("X-Content-SHA256", sha)
	header.Set("X-Checksum-SHA256", sha)
	header.Set("X-Revision-Number", "1")
}

func setCommandReviewHeaders(header http.Header, replay bool) {
	header.Set("Cache-Control", "no-store")
	header.Set("Location", "/api/v1/roasts/"+commandRoastID+"/comments/"+commandCommentID)
	header.Set("X-Idempotent-Replay", strconv.FormatBool(replay))
	header.Set("X-Roast-Revision-SHA256", commandRoastSHA)
	header.Set("X-Review-Template-Version", api.ReviewTemplateVersion)
}
