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
	if !strings.Contains(result.stderr, commandLotID) || !strings.Contains(result.stderr, "-125 grams") {
		t.Fatalf("confirmation = %q", result.stderr)
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

func TestInventoryAdjustNonTTYAndDeclineIssueZeroRequests(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
