package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIErrorExitCodeMapAndPreservation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		status   int
		exitCode int
	}{
		{status: http.StatusUnauthorized, exitCode: 4},
		{status: http.StatusForbidden, exitCode: 5},
		{status: http.StatusNotFound, exitCode: 6},
		{status: http.StatusUnprocessableEntity, exitCode: 7},
		{status: http.StatusTooManyRequests, exitCode: 7},
		{status: http.StatusInternalServerError, exitCode: 9},
	} {
		test := test
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, `{"error":{"code":"server_code","message":"Safe server message","details":{"field":"value"}}}`)
			}))
			defer server.Close()

			client, err := NewClient(server.URL, "map-secret", time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/failure"}, nil)
			if failure == nil {
				t.Fatal("Do() unexpectedly succeeded")
			}
			if failure.ExitCode != test.exitCode || failure.Code != "server_code" || failure.Message != "Safe server message" {
				t.Errorf("failure = %+v, want exit=%d with preserved code/message", failure, test.exitCode)
			}
			if failure.HTTPStatus == nil || *failure.HTTPStatus != test.status {
				t.Errorf("HTTPStatus = %v, want %d", failure.HTTPStatus, test.status)
			}
		})
	}
}

func TestMalformedAPIErrorsHaveStableSanitizedFailure(t *testing.T) {
	t.Parallel()

	const secret = "raw-body-secret"
	for _, body := range []string{
		``,
		`not-json-` + secret,
		`{"error":null}`,
		`{"error":{"code":"","message":"message"}}`,
		`{"error":{"code":"code","message":""}}`,
		`{"error":{"code":"code","message":"message"}} trailing`,
	} {
		body := body
		t.Run(fmt.Sprintf("body_%d", len(body)), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()

			client, err := NewClient(server.URL, "token-"+secret, time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/failure"}, nil)
			if failure == nil || failure.ExitCode != 9 || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v, want invalid_server_response exit 9", failure)
			}
			if failure.Message != "The server returned an invalid response" {
				t.Errorf("Message = %q, want stable malformed response message", failure.Message)
			}
			if strings.Contains(failure.Message, secret) || strings.Contains(failure.Message, server.URL) {
				t.Errorf("failure message leaks response/server secret: %q", failure.Message)
			}
		})
	}
}
