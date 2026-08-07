package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/release"
)

func TestClientSendsExactAuthenticationAndUserAgent(t *testing.T) {
	t.Parallel()

	const token = "super-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer "+token; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("User-Agent"), "artisan-cli/"+release.Info().Version; got != want {
			t.Errorf("User-Agent = %q, want %q", got, want)
		}
		if got, want := r.URL.RequestURI(), "/resource?name=coffee+lot"; got != want {
			t.Errorf("request URI = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"value":"ok"}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, token, time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	var destination struct {
		Value string `json:"value"`
	}
	failure := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/resource",
		Query:  map[string][]string{"name": {"coffee lot"}},
	}, &destination)
	if failure != nil {
		t.Fatalf("Do() failure = %+v", failure)
	}
	if destination.Value != "ok" {
		t.Fatalf("decoded value = %q, want ok", destination.Value)
	}
}

func TestClientRefusesEveryRedirectWithoutCredentialForwarding(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		status := status
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var targetRequests atomic.Int32
			var targetAuthorization atomic.Value
			targetAuthorization.Store("")
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetRequests.Add(1)
				targetAuthorization.Store(r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer target.Close()

			var sourceRequests atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceRequests.Add(1)
				http.Redirect(w, r, target.URL+"/stolen", status)
			}))
			defer source.Close()

			client, err := NewClient(source.URL, "redirect-secret", time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/start"}, nil)
			if failure == nil || failure.Code != "redirect_refused" || failure.ExitCode != 9 {
				t.Fatalf("Do() failure = %+v, want redirect_refused exit 9", failure)
			}
			if sourceRequests.Load() != 1 {
				t.Errorf("source requests = %d, want 1", sourceRequests.Load())
			}
			if targetRequests.Load() != 0 {
				t.Errorf("redirect target requests = %d, want 0", targetRequests.Load())
			}
			if got := targetAuthorization.Load().(string); got != "" {
				t.Errorf("redirect target received Authorization %q", got)
			}
		})
	}
}

func TestClientTimeoutAndErrorsDoNotLeakSecrets(t *testing.T) {
	t.Parallel()

	const token = "timeout-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, token, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	failure := client.Do(context.Background(), Request{
		Method:         http.MethodPost,
		Path:           "/slow",
		IdempotencyKey: "timeout-request-key",
	}, nil)
	if failure == nil || failure.ExitCode != 8 || failure.Code != "network_error" {
		t.Fatalf("Do() failure = %+v, want network_error exit 8", failure)
	}
	assertFailureOmits(t, failure.Message, token, server.URL, strings.TrimPrefix(server.URL, "http://"))
}

func TestNewClientValidationDoesNotLeakInput(t *testing.T) {
	t.Parallel()

	const secretURL = "https://user:password-secret@example.invalid"
	_, err := NewClient(secretURL, "token-secret", time.Second)
	if err == nil {
		t.Fatal("NewClient() unexpectedly succeeded")
	}
	assertFailureOmits(t, err.Error(), secretURL, "password-secret", "token-secret")
}

func TestClientBoundsSuccessAndErrorBodies(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "success", status: http.StatusOK},
		{name: "error", status: http.StatusBadRequest},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			body := &countingReadCloser{reader: strings.NewReader(strings.Repeat("x", maxResponseBodyBytes+4096))}
			client, err := NewClient("http://127.0.0.1", "bounded-secret", time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.status,
					Header:     make(http.Header),
					Body:       body,
				}, nil
			})

			var destination any
			failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/bounded"}, &destination)
			if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
				t.Fatalf("Do() failure = %+v, want invalid_server_response exit 9", failure)
			}
			if body.bytesRead > int64(maxResponseBodyBytes+1) {
				t.Fatalf("response bytes read = %d, exceeds bound %d", body.bytesRead, maxResponseBodyBytes+1)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestClientRejectsMalformedSuccessResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"value":true} trailing`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	var destination any
	failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/malformed"}, &destination)
	if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
		t.Fatalf("Do() failure = %+v, want invalid_server_response exit 9", failure)
	}
}

func assertFailureOmits(t *testing.T, message string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(message, secret) {
			t.Errorf("failure message %q leaks secret %q", message, secret)
		}
	}
}

type countingReadCloser struct {
	reader    io.Reader
	bytesRead int64
	closed    bool
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func (r *countingReadCloser) Close() error {
	r.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
