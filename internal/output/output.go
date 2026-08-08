// Package output renders human output and stable JSON envelopes.
package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Error describes a command failure. ExitCode controls the process status and
// is intentionally excluded from the JSON representation.
type Error struct {
	ExitCode   int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus *int   `json:"http_status,omitempty"`
}

type successEnvelope struct {
	OK   bool `json:"ok"`
	Data any  `json:"data"`
}

type errorEnvelope struct {
	OK    bool  `json:"ok"`
	Error Error `json:"error"`
}

// WriteSuccess writes data in a JSON envelope or delegates to the human
// renderer. JSON output is terminated by exactly one newline.
func WriteSuccess(w io.Writer, jsonMode bool, data any, human func(io.Writer) error) error {
	if jsonMode {
		return json.NewEncoder(w).Encode(successEnvelope{OK: true, Data: data})
	}
	return human(w)
}

// WriteFailure writes a failure in a JSON envelope or as a human-readable line.
func WriteFailure(w io.Writer, jsonMode bool, failure Error) error {
	if jsonMode {
		return json.NewEncoder(w).Encode(errorEnvelope{OK: false, Error: failure})
	}
	_, err := fmt.Fprintln(w, failure.Message)
	return err
}
