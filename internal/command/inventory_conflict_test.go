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
}

func TestInventoryConflictResolveConfirmsExactTargetAndNoteAndUsesOneKey(t *testing.T) {
	var path, body, key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		key = r.Header.Get("Idempotency-Key")
		contents, _ := io.ReadAll(r.Body)
		body = string(contents)
		_, _ = fmt.Fprint(w, commandResolvedConflictJSON())
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.In = strings.NewReader("yes\n")
	runtime.IsTerminal = func(int) bool { return true }
	result := runAuthCommand(t, runtime, "inventory", "conflict", "resolve", "55555555-5555-4555-8555-555555555555", "--note", " physical\r\ncount ", "--idempotency-key", "resolve-key")
	if result.code != 0 || !strings.Contains(result.stderr, commandConflictID) || !strings.Contains(result.stderr, "physical\ncount") {
		t.Fatalf("result=%#v", result)
	}
	if path != "/api/v1/inventory/admin/conflicts/"+commandConflictID+"/resolve" || body != `{"resolution_note":"physical\ncount"}` || key != "resolve-key" {
		t.Fatalf("path=%q body=%q key=%q", path, body, key)
	}
	for _, exact := range []string{commandConflictID, "resolved", "counted"} {
		if !strings.Contains(result.stdout, exact) {
			t.Fatalf("stdout %q missing %q", result.stdout, exact)
		}
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
