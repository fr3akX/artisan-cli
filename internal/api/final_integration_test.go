package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONResponsesRequireExactStatusAndTrustedMediaType(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType []string
		wantOK      bool
	}{
		{name: "canonical", status: http.StatusOK, contentType: []string{"application/json"}, wantOK: true},
		{name: "case insensitive with UTF-8", status: http.StatusOK, contentType: []string{"Application/JSON; Charset=UTF-8"}, wantOK: true},
		{name: "wrong success status", status: http.StatusCreated, contentType: []string{"application/json"}},
		{name: "missing", status: http.StatusOK},
		{name: "plain text", status: http.StatusOK, contentType: []string{"text/plain"}},
		{name: "unapproved parameter", status: http.StatusOK, contentType: []string{"application/json; profile=x"}},
		{name: "wrong charset", status: http.StatusOK, contentType: []string{"application/json; charset=iso-8859-1"}},
		{name: "duplicate values", status: http.StatusOK, contentType: []string{"application/json", "application/json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, value := range tt.contentType {
					w.Header().Add("Content-Type", value)
				}
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, `{"items":[],"next_cursor":null}`)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "media-secret", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, failure := client.ListBeanLots(context.Background(), LotListOptions{})
			if tt.wantOK {
				if failure != nil {
					t.Fatalf("failure = %+v", failure)
				}
				return
			}
			if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
				t.Fatalf("failure = %+v, want invalid_server_response exit 9", failure)
			}
		})
	}
}

func TestIdentityRequiresExactOKAndTrustedJSONMediaType(t *testing.T) {
	identityBody := `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"owner@example.com","nickname":"Owner"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`
	for _, test := range []struct {
		name        string
		status      int
		contentType string
		wantOK      bool
	}{
		{name: "exact contract", status: http.StatusOK, contentType: "application/json", wantOK: true},
		{name: "wrong successful status", status: http.StatusCreated, contentType: "application/json"},
		{name: "missing media type", status: http.StatusOK},
		{name: "wrong media type", status: http.StatusOK, contentType: "text/plain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.contentType != "" {
					w.Header().Set("Content-Type", test.contentType)
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, identityBody)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "identity-media-secret", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, failure := client.Identity(context.Background())
			if test.wantOK {
				if failure != nil {
					t.Fatalf("failure = %+v", failure)
				}
				return
			}
			if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
				t.Fatalf("failure = %+v, want invalid_server_response exit 9", failure)
			}
		})
	}
}

func TestEmptyNoContentResponseDoesNotRequireContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "no-content-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	failure := client.Do(context.Background(), Request{Method: http.MethodPost, Path: "/empty", IdempotencyKey: "empty-key", ExpectedStatus: http.StatusNoContent}, nil)
	if failure != nil {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestAPIErrorRequiresTrustedJSONMediaType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/json, text/plain"} {
		t.Run(contentType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if contentType != "" {
					w.Header().Set("Content-Type", contentType)
				}
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, `{"error":{"code":"bad","message":"bad"}}`)
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "error-media-secret", time.Second)
			failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/error", ExpectedStatus: http.StatusOK}, nil)
			if failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v", failure)
			}
		})
	}
}

func TestImageDownloadAPIErrorRequiresTrustedJSONMediaTypeAndCleansTemporary(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "image.webp")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"code":"bad","message":"bad"}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "download-media-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, failure := client.DownloadInventoryImage(context.Background(), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "display", destination, false)
	if failure == nil || failure.Code != "invalid_server_response" {
		t.Fatalf("failure = %+v", failure)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("download directory contains temporary residue: %v", entries)
	}
}

func TestSuccessfulJSONRejectsExactBearerReflectionBeforeDecode(t *testing.T) {
	for _, token := range []string{"reflected-bearer-secret", `reflected-"bearer\\secret`} {
		for _, route := range []struct {
			name, method string
			status       int
		}{
			{name: "list", method: http.MethodGet, status: http.StatusOK},
			{name: "lot", method: http.MethodGet, status: http.StatusOK},
			{name: "mutation", method: http.MethodPost, status: http.StatusCreated},
		} {
			t.Run(route.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(route.status)
					_, _ = fmt.Fprintf(w, `{"reflected":%q}`, token)
				}))
				defer server.Close()
				client, _ := NewClient(server.URL, token, time.Second)
				var destination any
				failure := client.Do(context.Background(), Request{Method: route.method, Path: "/reflection", IdempotencyKey: func() string {
					if route.method == http.MethodPost {
						return "reflection-key"
					}
					return ""
				}(), ExpectedStatus: route.status}, &destination)
				if failure == nil || failure.Code != "invalid_server_response" || strings.Contains(failure.Message, token) || strings.Contains(failure.Code, token) {
					t.Fatalf("failure = %+v, want secret-free invalid response", failure)
				}
			})
		}
	}
}

func TestCanceledMultipartValidationIsInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := ValidateImageUploadFilesContext(ctx, []string{"unopened.jpg"})
	if failure == nil || failure.Code != "interrupted" || failure.ExitCode != 130 {
		t.Fatalf("failure = %+v, want interrupted exit 130", failure)
	}
}

func TestCallerCancellationIsInterruptedButDeadlineIsNetworkError(t *testing.T) {
	client, _ := NewClient("http://127.0.0.1", "cancel-secret", time.Second)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	failure := client.Do(canceled, Request{Method: http.MethodGet, Path: "/cancel", ExpectedStatus: http.StatusOK}, nil)
	if failure == nil || failure.Code != "interrupted" || failure.ExitCode != 130 {
		t.Fatalf("canceled failure = %+v", failure)
	}

	deadline, stop := context.WithTimeout(context.Background(), time.Nanosecond)
	defer stop()
	<-deadline.Done()
	failure = client.Do(deadline, Request{Method: http.MethodGet, Path: "/deadline", ExpectedStatus: http.StatusOK}, nil)
	if failure == nil || failure.Code != "network_error" || failure.ExitCode != 8 {
		t.Fatalf("deadline failure = %+v", failure)
	}
}
