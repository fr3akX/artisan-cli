package command

import "testing"

func TestParsePricePerKgEUR(t *testing.T) {
	accepted := map[string]int64{
		"0": 0, "0.0": 0, "0.00": 0,
		"1": 100, "12.3": 1230, "12.34": 1234,
		"21474836.47": 2147483647,
	}
	for raw, want := range accepted {
		t.Run("accept "+raw, func(t *testing.T) {
			got, failure := parsePricePerKgEUR(raw)
			if failure != nil {
				t.Fatalf("parsePricePerKgEUR(%q) failure = %#v", raw, failure)
			}
			if got != want {
				t.Fatalf("parsePricePerKgEUR(%q) = %d, want %d", raw, got, want)
			}
		})
	}

	rejected := []string{
		"", " 1", "1 ", "+1", "-1", "00", "01", ".1", "1.", "1.234",
		"1,00", "1_00", "1e2", "NaN", "١", "21474836.48",
	}
	for _, raw := range rejected {
		t.Run("reject "+raw, func(t *testing.T) {
			_, failure := parsePricePerKgEUR(raw)
			if failure == nil || failure.ExitCode != 2 || failure.Code != "invalid_price_per_kg_eur" {
				t.Fatalf("parsePricePerKgEUR(%q) failure = %#v", raw, failure)
			}
		})
	}
}

func TestFormatEURCents(t *testing.T) {
	want := map[int64]string{0: "€0.00", 1: "€0.01", 1234: "€12.34"}
	for cents, expected := range want {
		if got := formatEURCents(cents); got != expected {
			t.Errorf("formatEURCents(%d) = %q, want %q", cents, got, expected)
		}
	}
	if got := formatSignedEURCents(-1234); got != "-€12.34" {
		t.Errorf("formatSignedEURCents(-1234) = %q", got)
	}
	if got := formatSignedEURCents(1234); got != "€12.34" {
		t.Errorf("formatSignedEURCents(1234) = %q", got)
	}
	if got := optionalEURCents(nil); got != "-" {
		t.Errorf("optionalEURCents(nil) = %q, want %q", got, "-")
	}
	cents := int64(1234)
	if got := optionalEURCents(&cents); got != "€12.34" {
		t.Errorf("optionalEURCents(&1234) = %q", got)
	}
}
