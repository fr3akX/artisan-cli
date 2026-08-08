package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)

// NewIdempotencyKey returns a cryptographically random key accepted by the
// Artisan server's Idempotency-Key contract.
func NewIdempotencyKey() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("generate idempotency key: secure randomness unavailable")
	}
	return "k" + base64.RawURLEncoding.EncodeToString(random[:]), nil
}

// ValidateIdempotencyKey enforces the Artisan server's exact key syntax and
// length without echoing a rejected key into the returned error.
func ValidateIdempotencyKey(key string) error {
	if !idempotencyKeyPattern.MatchString(key) {
		return errors.New("idempotency key must match [A-Za-z0-9][A-Za-z0-9._:-]{0,254}")
	}
	return nil
}
