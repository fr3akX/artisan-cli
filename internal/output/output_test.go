package output

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWriteSuccessJSON(t *testing.T) {
	var got bytes.Buffer
	humanCalled := false

	err := WriteSuccess(&got, true, map[string]string{"value": "ok"}, func(io.Writer) error {
		humanCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("WriteSuccess() error = %v", err)
	}
	if humanCalled {
		t.Fatal("human renderer called in JSON mode")
	}
	if want := "{\"ok\":true,\"data\":{\"value\":\"ok\"}}\n"; got.String() != want {
		t.Fatalf("WriteSuccess() = %q, want %q", got.String(), want)
	}
}

func TestWriteSuccessHuman(t *testing.T) {
	var got bytes.Buffer
	wantErr := errors.New("render failed")

	err := WriteSuccess(&got, false, nil, func(w io.Writer) error {
		_, _ = io.WriteString(w, "human\n")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WriteSuccess() error = %v, want %v", err, wantErr)
	}
	if got.String() != "human\n" {
		t.Fatalf("WriteSuccess() = %q, want human output", got.String())
	}
}

func TestWriteFailureJSONOmitsNilHTTPStatus(t *testing.T) {
	var got bytes.Buffer
	failure := Error{ExitCode: 2, Code: "usage", Message: "Unknown command"}

	if err := WriteFailure(&got, true, failure); err != nil {
		t.Fatalf("WriteFailure() error = %v", err)
	}
	want := "{\"ok\":false,\"error\":{\"code\":\"usage\",\"message\":\"Unknown command\"}}\n"
	if got.String() != want {
		t.Fatalf("WriteFailure() = %q, want %q", got.String(), want)
	}
	if strings.Count(got.String(), "\n") != 1 {
		t.Fatalf("WriteFailure() emitted more than one newline-terminated object: %q", got.String())
	}
}

func TestWriteFailureHuman(t *testing.T) {
	var got bytes.Buffer
	if err := WriteFailure(&got, false, Error{Message: "Unknown command"}); err != nil {
		t.Fatalf("WriteFailure() error = %v", err)
	}
	if want := "Unknown command\n"; got.String() != want {
		t.Fatalf("WriteFailure() = %q, want %q", got.String(), want)
	}
}
