package confirm

import (
	"bytes"
	"strings"
	"testing"
)

func TestAskAcceptsOnlyExplicitYesOnTerminal(t *testing.T) {
	for _, test := range []struct {
		input string
		want  bool
	}{
		{input: "yes\n", want: true},
		{input: "YES\n", want: true},
		{input: "y\n", want: false},
		{input: "true\n", want: false},
		{input: "\n", want: false},
	} {
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
	}
}

func TestAskRejectsOverflowBeforeTrimmingOrApproving(t *testing.T) {
	exact := "yes" + strings.Repeat(" ", maxConfirmationResponseBytes-3)
	for _, ending := range []string{"\n", "\r\n"} {
		approved, err := Ask(strings.NewReader(exact+ending), &bytes.Buffer{}, true, false, "Confirm?")
		if err != nil || !approved {
			t.Fatalf("exact bound with %q = %v, %v", ending, approved, err)
		}
	}
	for _, input := range []string{
		exact + "x\n",
		"yes" + strings.Repeat(" ", maxConfirmationResponseBytes) + "trailing bytes",
	} {
		approved, err := Ask(strings.NewReader(input), &bytes.Buffer{}, true, false, "Confirm?")
		if approved || err == nil {
			t.Fatalf("overflow approved=%v err=%v", approved, err)
		}
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
