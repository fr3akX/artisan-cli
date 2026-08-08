package api

import (
	"regexp"
	"strings"
	"testing"
)

func TestNewIdempotencyKeyUsesServerContract(t *testing.T) {
	t.Parallel()

	contract := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)
	seen := make(map[string]struct{}, 64)
	for range 64 {
		key, err := NewIdempotencyKey()
		if err != nil {
			t.Fatalf("NewIdempotencyKey() error = %v", err)
		}
		if !contract.MatchString(key) {
			t.Fatalf("generated key %q does not match server contract", key)
		}
		if err := ValidateIdempotencyKey(key); err != nil {
			t.Fatalf("ValidateIdempotencyKey(generated key) error = %v", err)
		}
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate generated key %q", key)
		}
		seen[key] = struct{}{}
	}
}

func TestValidateIdempotencyKeyExactContract(t *testing.T) {
	t.Parallel()

	valid := []string{
		"a",
		"Z_9:request-id.1",
		"A" + strings.Repeat("-", 254),
	}
	for _, key := range valid {
		if err := ValidateIdempotencyKey(key); err != nil {
			t.Errorf("ValidateIdempotencyKey(%q) error = %v", key, err)
		}
	}

	invalid := []string{
		"",
		"_starts-with-punctuation",
		"contains space",
		"contains/slash",
		"contains\nnewline",
		"é",
		"A" + strings.Repeat("x", 255),
	}
	for _, key := range invalid {
		if err := ValidateIdempotencyKey(key); err == nil {
			t.Errorf("ValidateIdempotencyKey(%q) unexpectedly succeeded", key)
		} else if strings.Contains(err.Error(), key) && key != "" {
			t.Errorf("validation error leaks rejected key %q: %v", key, err)
		}
	}
}
