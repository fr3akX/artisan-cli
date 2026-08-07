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

const inventoryOutputID = "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"
