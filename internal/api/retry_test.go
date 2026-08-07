package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSafeReadsRetryOnlyTransientStatusesWithThreeAttemptLimit(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := requests.Add(1)
				if attempt < 3 {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"code":"temporary","message":"Temporary failure"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer server.Close()

			client, err := NewClient(server.URL, "retry-secret", time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			var destination struct {
				OK bool `json:"ok"`
			}
			if failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/read"}, &destination); failure != nil {
				t.Fatalf("Do() failure = %+v", failure)
			}
			if !destination.OK || requests.Load() != 3 {
				t.Fatalf("destination.OK = %v, requests = %d; want true, 3", destination.OK, requests.Load())
			}
		})
	}
}

func TestRetriesStopAfterThreeTotalAttempts(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"unavailable","message":"Still unavailable"}}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "retry-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/read"}, nil)
	if failure == nil || failure.ExitCode != 9 || failure.Code != "unavailable" {
		t.Fatalf("Do() failure = %+v, want final server failure", failure)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want exactly 3", requests.Load())
	}
}

func TestExpectedClientErrorsAreNotRetried(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"Conflict"}}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "retry-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/read"}, nil)
	if failure == nil || failure.ExitCode != 7 {
		t.Fatalf("Do() failure = %+v, want exit 7", failure)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestReplayableMutationRecreatesBodyAndPreservesKey(t *testing.T) {
	t.Parallel()

	const key = "stable-key:123"
	var requests atomic.Int32
	var mu sync.Mutex
	var keys, bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		bodies = append(bodies, string(body))
		mu.Unlock()
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"temporary","message":"Temporary"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"saved":true}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "mutation-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	var factories atomic.Int32
	request := Request{
		Method:         http.MethodPost,
		Path:           "/mutation",
		IdempotencyKey: key,
		Body: func() (io.ReadCloser, string, error) {
			factories.Add(1)
			return io.NopCloser(strings.NewReader(`{"operation":"same"}`)), "application/json", nil
		},
	}
	var destination any
	if failure := client.Do(context.Background(), request, &destination); failure != nil {
		t.Fatalf("Do() failure = %+v", failure)
	}
	if factories.Load() != 3 || requests.Load() != 3 {
		t.Fatalf("body factories = %d, requests = %d; want 3, 3", factories.Load(), requests.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for index := range keys {
		if keys[index] != key {
			t.Errorf("attempt %d key = %q, want %q", index+1, keys[index], key)
		}
		if bodies[index] != `{"operation":"same"}` {
			t.Errorf("attempt %d body = %q, want identical replay body", index+1, bodies[index])
		}
	}
}

func TestMutationWithoutReplayableBodyDoesNotRetry(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"temporary","message":"Temporary"}}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "mutation-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	failure := client.Do(context.Background(), Request{
		Method:         http.MethodDelete,
		Path:           "/mutation",
		IdempotencyKey: "valid-delete-key",
	}, nil)
	if failure == nil || failure.ExitCode != 9 {
		t.Fatalf("Do() failure = %+v, want server failure", failure)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestInvalidMutationKeyAndBodyFactoryFailureDoNotSend(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "mutation-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	failure := client.Do(context.Background(), Request{
		Method:         http.MethodPost,
		Path:           "/mutation",
		IdempotencyKey: "bad key secret",
		Body: func() (io.ReadCloser, string, error) {
			return io.NopCloser(strings.NewReader("body")), "text/plain", nil
		},
	}, nil)
	if failure == nil || failure.ExitCode != 2 || failure.Code != "invalid_idempotency_key" {
		t.Fatalf("invalid key failure = %+v", failure)
	}
	assertFailureOmits(t, failure.Message, "bad key secret")

	failure = client.Do(context.Background(), Request{
		Method:         http.MethodPost,
		Path:           "/mutation",
		IdempotencyKey: "valid-key",
		Body: func() (io.ReadCloser, string, error) {
			return nil, "", errors.New("body-secret-file")
		},
	}, nil)
	if failure == nil || failure.ExitCode != 2 || failure.Code != "request_body_error" {
		t.Fatalf("body factory failure = %+v", failure)
	}
	assertFailureOmits(t, failure.Message, "body-secret-file")
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
}

func TestSafeReadRetriesTransportFailuresWithoutLeakingThem(t *testing.T) {
	t.Parallel()

	client, err := NewClient("http://127.0.0.1", "transport-token-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	var attempts atomic.Int32
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("transport-server-secret")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})
	var destination any
	if failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/read"}, &destination); failure != nil {
		t.Fatalf("Do() failure = %+v", failure)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}

func TestSafeReadRetriesResponseConnectionFailures(t *testing.T) {
	t.Parallel()

	client, err := NewClient("http://127.0.0.1", "response-read-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	var attempts atomic.Int32
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		body := io.ReadCloser(io.NopCloser(strings.NewReader(`{"ok":true}`)))
		if attempt < 3 {
			body = failingReadCloser{}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})
	var destination any
	if failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/read"}, &destination); failure != nil {
		t.Fatalf("Do() failure = %+v", failure)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}

func TestRetryBackoffStopsWhenContextIsCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"temporary","message":"Temporary"}}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "cancel-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	failure := client.Do(ctx, Request{Method: http.MethodGet, Path: "/read"}, nil)
	if failure == nil || failure.ExitCode != 8 || failure.Code != "network_error" {
		t.Fatalf("Do() failure = %+v, want network_error exit 8", failure)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("response-connection-secret")
}

func (failingReadCloser) Close() error { return nil }
