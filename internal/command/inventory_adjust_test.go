package command

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInventoryAdjustConfirmationIncludesExactChangeAndSendsCanonicalBody(t *testing.T) {
	var body, key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		body, key = string(contents), r.Header.Get("Idempotency-Key")
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.In = strings.NewReader("yes\n")
	runtime.IsTerminal = func(fd int) bool { return fd == 0 }
	result := runAuthCommand(t, runtime, "inventory", "adjust", commandLotID, "--grams", "-125", "--reason", " physical\r\ncount ", "--reference", "  ", "--occurred-at", "2026-08-07T12:34:56.000000Z", "--idempotency-key", "advanced-key")
	if result.code != 0 {
		t.Fatalf("result = %#v", result)
	}
	wantPrompt := `Adjust lot ` + commandLotID + ` by -125 grams; reason: physical\ncount; reference: <omitted>; occurred at: 2026-08-07T12:34:56.000000Z? Type yes to continue: `
	if result.stderr != wantPrompt {
		t.Fatalf("confirmation = %q, want %q", result.stderr, wantPrompt)
	}
	if body != `{"quantity_grams":-125,"reason":"physical\ncount","reference":null,"occurred_at":"2026-08-07T12:34:56.000000Z"}` || key != "advanced-key" {
		t.Fatalf("body=%q key=%q", body, key)
	}
	for _, balance := range []string{"5000", "1250", "3750"} {
		if !strings.Contains(result.stdout, balance) {
			t.Fatalf("output %q missing %q", result.stdout, balance)
		}
	}
}

func TestInventoryAdjustAcceptsFlagsAfterLotID(t *testing.T) {
	var body, key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		body, key = string(contents), r.Header.Get("Idempotency-Key")
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()

	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "adjust", commandLotID, "--grams", "-25", "--reason", "count", "--yes")
	if result.code != 0 || result.stderr != "" || key == "" {
		t.Fatalf("result=%#v key=%q", result, key)
	}
	if !strings.Contains(body, `"quantity_grams":-25`) || !strings.Contains(body, `"reason":"count"`) || !strings.Contains(body, `"occurred_at":"`) {
		t.Fatalf("body=%q", body)
	}
	if result.stdout != `{"ok":true,"data":`+commandLotDetailFullJSON()+"}\n" {
		t.Fatalf("stdout=%q", result.stdout)
	}
}

func TestInventoryAdjustJSONYesSkipsPromptAndSendsNormalizedAdjustment(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		body = string(contents)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.IsTerminal = func(int) bool { return true }
	result := runAuthCommand(t, runtime, "--json", "inventory", "adjust", "11111111-1111-4111-8111-111111111111", "--grams", "25", "--reason", " count\tline ", "--reference", " sheet-1 ", "--occurred-at", commandTimestamp, "--yes")
	if result.code != 0 || result.stderr != "" || body != `{"quantity_grams":25,"reason":"count\tline","reference":"sheet-1","occurred_at":"`+commandTimestamp+`"}` {
		t.Fatalf("result=%#v body=%q", result, body)
	}
}

func TestInventoryAdjustDeclineDoesNotLoadConfigurationOrConstructClient(t *testing.T) {
	var getenvCalls atomic.Int64
	runtime := Runtime{
		ConfigDir:  t.TempDir(),
		In:         strings.NewReader("no\n"),
		IsTerminal: func(int) bool { return true },
		Getenv: func(string) string {
			getenvCalls.Add(1)
			return ""
		},
	}
	result := runAuthCommand(t, runtime, "--json", "inventory", "adjust", "11111111-1111-4111-8111-111111111111", "--grams", "1", "--reason", " count\tline ", "--occurred-at", commandTimestamp)
	if result.code != 10 || getenvCalls.Load() != 0 {
		t.Fatalf("result=%#v getenvCalls=%d", result, getenvCalls.Load())
	}
}

func TestInventoryAdjustNonTTYAndDeclineIssueZeroRequests(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	for _, test := range []struct {
		name string
		tty  bool
		in   string
		json bool
		code string
	}{
		{name: "non tty human", code: "confirmation_required"},
		{name: "non tty JSON", code: "confirmation_required", json: true},
		{name: "declined human", tty: true, in: "no\n", code: "confirmation_declined"},
		{name: "declined JSON", tty: true, in: "no\n", code: "confirmation_declined", json: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := inventoryRuntime(t, server.URL)
			runtime.In = strings.NewReader(test.in)
			runtime.IsTerminal = func(int) bool { return test.tty }
			args := []string{"inventory", "adjust", commandLotID, "--grams", "1", "--reason", "count", "--occurred-at", "2026-08-07T12:34:56.000000Z"}
			if test.json {
				args = append([]string{"--json"}, args...)
			}
			result := runAuthCommand(t, runtime, args...)
			rendered := result.stderr
			if test.json {
				rendered = result.stdout
			}
			if result.code != 10 || (test.json && !strings.Contains(rendered, test.code)) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestInventoryAdjustIncompleteAndInvalidConfirmationsIssueZeroRequests(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		_, _ = fmt.Fprint(w, commandLotDetailFullJSON())
	}))
	defer server.Close()
	tests := []struct {
		name   string
		reader func() io.Reader
	}{
		{name: "yes at EOF", reader: func() io.Reader { return strings.NewReader("yes") }},
		{name: "bare CR at EOF", reader: func() io.Reader { return strings.NewReader("yes\r") }},
		{name: "4096 content bare CR", reader: func() io.Reader { return strings.NewReader("yes" + strings.Repeat(" ", 4092) + "\r") }},
		{name: "4097 content LF", reader: func() io.Reader { return strings.NewReader("yes" + strings.Repeat(" ", 4094) + "\n") }},
		{name: "4097 content CRLF", reader: func() io.Reader { return strings.NewReader("yes" + strings.Repeat(" ", 4094) + "\r\n") }},
		{name: "4097 content bare CR", reader: func() io.Reader { return strings.NewReader("yes" + strings.Repeat(" ", 4093) + "\r") }},
		{name: "overlong prefix", reader: func() io.Reader { return strings.NewReader("yes" + strings.Repeat(" ", 4096) + "trailing") }},
		{name: "reader error", reader: func() io.Reader { return commandErrorReader{err: errors.New("read failed")} }},
	}
	for _, test := range tests {
		for _, jsonMode := range []bool{false, true} {
			format := "human"
			if jsonMode {
				format = "JSON"
			}
			t.Run(test.name+" "+format, func(t *testing.T) {
				runtime := inventoryRuntime(t, server.URL)
				runtime.IsTerminal = func(int) bool { return true }
				runtime.In = test.reader()
				args := []string{"inventory", "adjust", commandLotID, "--grams", "1", "--reason", "count", "--yes=false"}
				if jsonMode {
					args = append([]string{"--json"}, args...)
				}
				result := runAuthCommand(t, runtime, args...)
				if result.code != 10 || requests.Load() != 0 {
					t.Fatalf("result=%#v requests=%d", result, requests.Load())
				}
			})
		}
	}
}

type commandErrorReader struct{ err error }

func (reader commandErrorReader) Read([]byte) (int, error) { return 0, reader.err }

func TestInventoryAdjustRejectsPresenceSensitiveEmptyFlagsLocally(t *testing.T) {
	for _, args := range [][]string{
		{"inventory", "adjust", commandLotID, "--grams", "1", "--reason", "count", "--occurred-at", "", "--yes"},
		{"inventory", "adjust", commandLotID, "--grams", "1", "--reason", "count", "--idempotency-key", "", "--yes"},
	} {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != 2 {
			t.Errorf("args %q result = %#v", args, result)
		}
	}
}

func TestInventoryAdjustRejectsFloatingZeroBlankTimestampAndInvalidKeyLocally(t *testing.T) {
	for _, args := range [][]string{
		{"inventory", "adjust", commandLotID, "--grams", "1.5", "--reason", "count", "--yes"},
		{"inventory", "adjust", commandLotID, "--grams", "0", "--reason", "count", "--yes"},
		{"inventory", "adjust", commandLotID, "--grams", "1", "--reason", " ", "--yes"},
		{"inventory", "adjust", commandLotID, "--grams", "1", "--reason", "count", "--occurred-at", "2026-08-07T12:34:56Z", "--yes"},
		{"inventory", "adjust", commandLotID, "--grams", "1", "--reason", "count", "--occurred-at", "0000-01-01T00:00:00.000000Z", "--yes"},
		{"inventory", "adjust", commandLotID, "--grams", "1", "--reason", "count", "--idempotency-key", "bad key", "--yes"},
	} {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != 2 {
			t.Errorf("args %v result = %#v", args, result)
		}
	}
}
