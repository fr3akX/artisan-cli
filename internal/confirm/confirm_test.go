package confirm

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestAskAcceptsOnlyCompleteAffirmativeLine(t *testing.T) {
	exact := "yes" + strings.Repeat(" ", maxConfirmationResponseBytes-3)
	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "LF", input: "yes\n", want: true},
		{name: "case and whitespace", input: " \tYeS \n", want: true},
		{name: "CRLF", input: "YES\r\n", want: true},
		{name: "exact content LF", input: exact + "\n", want: true},
		{name: "exact content CRLF", input: exact + "\r\n", want: true},
		{name: "short word", input: "y\n"},
		{name: "other word", input: "true\n"},
		{name: "empty", input: "\n"},
		{name: "yes at EOF", input: "yes"},
		{name: "bare CR at EOF", input: "yes\r"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			got, err := Ask(strings.NewReader(test.input), &output, true, false, "Archive lot abc?")
			if err != nil {
				t.Fatalf("Ask(%q) error = %v", test.input, err)
			}
			if got != test.want {
				t.Errorf("Ask(%q) = %v, want %v", test.input, got, test.want)
			}
			if output.String() != "Archive lot abc? Type yes to continue: " {
				t.Errorf("prompt = %q", output.String())
			}
		})
	}
}

func TestAskMeasuresContentExcludingOnlyCompleteLineTerminator(t *testing.T) {
	exactWithBareCR := "yes" + strings.Repeat(" ", maxConfirmationResponseBytes-4) + "\r"
	overWithBareCR := "yes" + strings.Repeat(" ", maxConfirmationResponseBytes-3) + "\r"
	for _, test := range []struct {
		name      string
		input     string
		want      bool
		wantError bool
	}{
		{name: "4096 content LF", input: "yes" + strings.Repeat(" ", maxConfirmationResponseBytes-3) + "\n", want: true},
		{name: "4096 content CRLF", input: "yes" + strings.Repeat(" ", maxConfirmationResponseBytes-3) + "\r\n", want: true},
		{name: "4096 content bare CR", input: exactWithBareCR},
		{name: "4097 content LF", input: "yes" + strings.Repeat(" ", maxConfirmationResponseBytes-2) + "\n", wantError: true},
		{name: "4097 content CRLF", input: "yes" + strings.Repeat(" ", maxConfirmationResponseBytes-2) + "\r\n", wantError: true},
		{name: "4097 content bare CR", input: overWithBareCR, wantError: true},
		{name: "overlong prefix LF", input: "yes" + strings.Repeat(" ", maxConfirmationResponseBytes) + "trailing\n", wantError: true},
		{name: "overlong prefix EOF", input: "yes" + strings.Repeat(" ", maxConfirmationResponseBytes) + "trailing", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			approved, err := Ask(strings.NewReader(test.input), &bytes.Buffer{}, true, false, "Confirm?")
			if approved != test.want || (err != nil) != test.wantError {
				t.Fatalf("Ask approved=%v err=%v; want approved=%v error=%v", approved, err, test.want, test.wantError)
			}
		})
	}
}

func TestAskPropagatesReaderErrorsAndBoundsReads(t *testing.T) {
	sentinel := errors.New("reader failed")
	approved, err := Ask(io.MultiReader(strings.NewReader("ye"), errorReader{err: sentinel}), &bytes.Buffer{}, true, false, "Confirm?")
	if approved || !errors.Is(err, sentinel) {
		t.Fatalf("reader error approved=%v err=%v", approved, err)
	}

	input := strings.NewReader("yes" + strings.Repeat(" ", 1<<20))
	approved, err = Ask(input, &bytes.Buffer{}, true, false, "Confirm?")
	if approved || err == nil {
		t.Fatalf("large input approved=%v err=%v", approved, err)
	}
	if consumed := input.Size() - int64(input.Len()); consumed > maxConfirmationResponseBytes+3 {
		t.Fatalf("consumed %d bytes", consumed)
	}
}

func TestAskYesBypassesPromptAndNonTerminalRequiresIt(t *testing.T) {
	for _, test := range []struct{ terminal, yes bool }{{false, false}, {false, true}, {true, true}} {
		var output bytes.Buffer
		got, err := Ask(strings.NewReader("yes\n"), &output, test.terminal, test.yes, "ignored")
		if err != nil || got != test.yes {
			t.Fatalf("Ask(terminal=%v, yes=%v) = %v, %v", test.terminal, test.yes, got, err)
		}
		if output.Len() != 0 {
			t.Fatalf("nonterminal prompt = %q", output.String())
		}
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
