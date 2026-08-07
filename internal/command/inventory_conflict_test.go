package command

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInventoryConflictReadsNeverPromptAndUseAdminNamespace(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/conflicts") {
			_, _ = fmt.Fprint(w, `{"items":[`+commandConflictJSON()+`],"next_cursor":null}`)
			return
		}
		_, _ = fmt.Fprint(w, commandConflictJSON())
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.IsTerminal = func(int) bool { t.Fatal("conflict read checked terminal"); return false }
	listed := runAuthCommand(t, runtime, "inventory", "conflict", "list", "--lot", "11111111-1111-4111-8111-111111111111")
	shown := runAuthCommand(t, runtime, "inventory", "conflict", "show", "55555555-5555-4555-8555-555555555555")
	if listed.code != 0 || shown.code != 0 || listed.stderr != "" || shown.stderr != "" {
		t.Fatalf("listed=%#v shown=%#v", listed, shown)
	}
	want := []string{"/api/v1/inventory/admin/bean-lots/" + commandLotID + "/conflicts", "/api/v1/inventory/admin/conflicts/" + commandConflictID}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%#v want=%#v", paths, want)
	}
	wantList := "CONFLICT ID                       LOT ID                            SOURCE ENTRY ID                   ROAST UUID  RESERVATION ID  TRIGGER            AVAILABLE GRAMS  STATE  RESOLUTION NOTE  RESOLVED BY USER ID  RESOLVED AT  CREATED AT\n" +
		"55555555555545558555555555555555  11111111111141118111111111111111  33333333333343338333333333333333  -           -               manual_adjustment  -1               open   -                -                    -            2026-08-04T12:00:00.000000Z\n"
	wantShow := "Conflict ID: 55555555555545558555555555555555\n" +
		"Lot ID: 11111111111141118111111111111111\n" +
		"Source ledger entry ID: 33333333333343338333333333333333\n" +
		"Roast UUID: -\nReservation ID: -\nTrigger operation: manual_adjustment\nAvailable grams snapshot: -1\nState: open\nResolution note: -\nResolved by user ID: -\nResolved at: -\nCreated at: 2026-08-04T12:00:00.000000Z\n"
	if listed.stdout != wantList || shown.stdout != wantShow {
		t.Fatalf("listed=%q shown=%q", listed.stdout, shown.stdout)
	}
}

func TestInventoryConflictResolveConfirmsExactTargetAndNoteAndUsesOneKey(t *testing.T) {
	var path, body, key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		key = r.Header.Get("Idempotency-Key")
		contents, _ := io.ReadAll(r.Body)
		body = string(contents)
		_, _ = fmt.Fprint(w, commandResolvedConflictJSONWithNote("Café\nagain\tcolumn"))
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.In = strings.NewReader("yes\n")
	runtime.IsTerminal = func(int) bool { return true }
	result := runAuthCommand(t, runtime, "inventory", "conflict", "resolve", "55555555-5555-4555-8555-555555555555", "--note", " Cafe\u0301\r\nagain\tcolumn ", "--idempotency-key", "resolve-key")
	wantPrompt := "Resolve conflict " + commandConflictID + ` with note: Café\nagain\tcolumn? Type yes to continue: `
	if result.code != 0 || result.stderr != wantPrompt || strings.Count(result.stderr, "\n") != 0 {
		t.Fatalf("result=%#v want prompt=%q", result, wantPrompt)
	}
	if path != "/api/v1/inventory/admin/conflicts/"+commandConflictID+"/resolve" || body != `{"resolution_note":"Café\nagain\tcolumn"}` || key != "resolve-key" {
		t.Fatalf("path=%q body=%q key=%q", path, body, key)
	}
	wantOutput := "Conflict ID: " + commandConflictID + "\n" +
		"Lot ID: " + commandLotID + "\n" +
		"Source ledger entry ID: " + commandEntryID + "\n" +
		"Roast UUID: " + commandRoastID + "\n" +
		"Reservation ID: " + commandReservationID + "\n" +
		"Trigger operation: consumption\n" +
		"Available grams snapshot: -25\n" +
		"State: resolved\n" +
		`Resolution note: Café\nagain\tcolumn` + "\n" +
		"Resolved by user ID: " + commandUserID + "\n" +
		"Resolved at: " + commandTimestamp + "\n" +
		"Created at: " + commandTimestamp + "\n"
	if result.stdout != wantOutput {
		t.Fatalf("stdout=%q want=%q", result.stdout, wantOutput)
	}
}

func TestInventoryConflictResolveDeclineNonTTYAndInvalidConfirmationIssueZeroRequests(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, commandResolvedConflictJSON())
	}))
	defer server.Close()
	tests := []struct {
		name string
		tty  bool
		in   io.Reader
		args []string
	}{
		{name: "decline", tty: true, in: strings.NewReader("no\n")},
		{name: "non tty", in: strings.NewReader("")},
		{name: "incomplete yes", tty: true, in: strings.NewReader("yes")},
		{name: "reader error", tty: true, in: commandConflictErrorReader{err: errors.New("read failed")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := inventoryRuntime(t, server.URL)
			runtime.In = test.in
			runtime.IsTerminal = func(int) bool { return test.tty }
			result := runAuthCommand(t, runtime, "--json", "inventory", "conflict", "resolve", commandConflictID, "--note", "counted")
			if result.code != 10 || requests.Load() != 0 {
				t.Fatalf("result=%#v requests=%d", result, requests.Load())
			}
		})
	}
}

type commandConflictErrorReader struct{ err error }

func (reader commandConflictErrorReader) Read([]byte) (int, error) { return 0, reader.err }

func TestInventoryConflictResolveYesPreservesServerConflictWithoutAutoAdjustment(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"negative_inventory_available","message":"Inventory remains negative","details":null}}`)
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.IsTerminal = func(int) bool { return false }
	result := runAuthCommand(t, runtime, "--json", "inventory", "conflict", "resolve", commandConflictID, "--note", "counted", "--yes")
	if result.code != 7 || !strings.Contains(result.stdout, `"code":"negative_inventory_available"`) || !strings.Contains(result.stdout, `"message":"Inventory remains negative"`) {
		t.Fatalf("result=%#v", result)
	}
	if !reflect.DeepEqual(paths, []string{"/api/v1/inventory/admin/conflicts/" + commandConflictID + "/resolve"}) {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestInventoryConflictResolveRejectsMissingBlankOversizedNoteIDAndKeyLocally(t *testing.T) {
	for _, args := range [][]string{
		{"inventory", "conflict", "resolve", commandConflictID, "--yes"},
		{"inventory", "conflict", "resolve", commandConflictID, "--note", " ", "--yes"},
		{"inventory", "conflict", "resolve", commandConflictID, "--note", strings.Repeat("x", 2001), "--yes"},
		{"inventory", "conflict", "resolve", "bad", "--note", "counted", "--yes"},
		{"inventory", "conflict", "resolve", commandConflictID, "--note", "counted", "--idempotency-key", "", "--yes"},
	} {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != 2 {
			t.Errorf("args=%q result=%#v", args, result)
		}
	}
}

func TestInventoryConflictResolveJSONEnvelopeIsExact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, commandResolvedConflictJSON())
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "conflict", "resolve", commandConflictID, "--note", "counted", "--yes")
	want := `{"ok":true,"data":` + commandResolvedConflictJSON() + "}\n"
	if result.code != 0 || result.stderr != "" || result.stdout != want {
		t.Fatalf("result=%#v want=%q", result, want)
	}
}
