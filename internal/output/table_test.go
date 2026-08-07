package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestInventoryTableNeverTruncatesIDsOrIntegerGrams(t *testing.T) {
	const id = "11111111111141118111111111111111"
	const grams = "-2147483647"
	var out bytes.Buffer
	if err := WriteTable(&out, []string{"LOT ID", "AVAILABLE GRAMS"}, [][]string{{id, grams}}); err != nil {
		t.Fatalf("WriteTable() error = %v", err)
	}
	if !strings.Contains(out.String(), id) || !strings.Contains(out.String(), grams) {
		t.Fatalf("table truncated exact values: %q", out.String())
	}
	if strings.Contains(out.String(), "…") || strings.Contains(out.String(), "...") {
		t.Fatalf("table contains truncation marker: %q", out.String())
	}
}

func TestInventoryDetailsAreStableAndUntruncated(t *testing.T) {
	var out bytes.Buffer
	fields := []DetailField{{Label: "Lot ID", Value: inventoryOutputID}, {Label: "On hand grams", Value: "2147483647"}}
	if err := WriteDetails(&out, fields); err != nil {
		t.Fatalf("WriteDetails() error = %v", err)
	}
	want := "Lot ID: " + inventoryOutputID + "\nOn hand grams: 2147483647\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestVisibleOutputEscapesStructuralAndControlCharacters(t *testing.T) {
	const hostile = "slash\\tab\tcr\rlf\nnull\x00esc\x1bdel\x7fc1\u0085 café"
	const escaped = `slash\\tab\tcr\rlf\nnull\x00esc\x1Bdel\x7Fc1\x85 café`
	if got := EscapeVisible(hostile); got != escaped {
		t.Fatalf("EscapeVisible() = %q, want %q", got, escaped)
	}

	var table bytes.Buffer
	if err := WriteTable(&table, []string{"NAME", "NOTE\nLABEL"}, [][]string{{hostile, hostile}}); err != nil {
		t.Fatalf("WriteTable() error = %v", err)
	}
	if strings.Count(table.String(), "\n") != 2 || strings.Count(table.String(), escaped) != 2 || strings.Contains(table.String(), "\u0085") {
		t.Fatalf("table structure/value was not preserved: %q", table.String())
	}

	var details bytes.Buffer
	if err := WriteDetails(&details, []DetailField{{Label: "Notes\nLabel", Value: hostile}}); err != nil {
		t.Fatalf("WriteDetails() error = %v", err)
	}
	if strings.Count(details.String(), "\n") != 1 || details.String() != `Notes\nLabel: `+escaped+"\n" {
		t.Fatalf("detail structure/value was not preserved: %q", details.String())
	}
}

const inventoryOutputID = "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"
