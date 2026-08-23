package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/release"
)

func TestClientResponseValidatorReceivesClonedHeadersBeforeSuccessDecode(t *testing.T) {
	const token = "response-validator-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("X-Contract", "one")
		w.Header().Add("X-Contract", "two")
		_, _ = io.WriteString(w, `{"value":"ok"}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, token, time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	called := false
	var response struct {
		Value string `json:"value"`
	}
	failure := client.Do(context.Background(), Request{
		Method: http.MethodGet, Path: "/resource", ExpectedStatus: http.StatusOK,
		ValidateResponse: func(status int, header http.Header) *output.Error {
			called = true
			if status != http.StatusOK || !reflect.DeepEqual(header.Values("X-Contract"), []string{"one", "two"}) {
				t.Fatalf("validator status/header = %d/%#v", status, header)
			}
			for name, values := range header {
				if strings.Contains(name, token) || strings.Contains(name, server.URL) {
					t.Fatalf("validator received secret-bearing header name %q", name)
				}
				for _, value := range values {
					if strings.Contains(value, token) || strings.Contains(value, server.URL) {
						t.Fatalf("validator received secret-bearing header value")
					}
				}
			}
			header.Set("X-Contract", "mutated-clone")
			return nil
		},
	}, &response)
	if failure != nil || !called || response.Value != "ok" {
		t.Fatalf("Do() response=%#v failure=%#v called=%v", response, failure, called)
	}
}

func TestClientResponseValidatorRejectsBeforeDecodeAndNeverReceivesReflectedSecrets(t *testing.T) {
	const token = "response-header-reflection-token"
	for _, reflected := range []string{token, "SERVER_URL"} {
		t.Run(reflected, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				value := reflected
				if value == "SERVER_URL" {
					value = server.URL
				}
				w.Header().Set("X-Untrusted", value)
				_, _ = io.WriteString(w, `{"value":"ok"}`)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, token, time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			called := false
			var response struct {
				Value string `json:"value"`
			}
			failure := client.Do(context.Background(), Request{
				Method: http.MethodGet, Path: "/resource", ExpectedStatus: http.StatusOK,
				ValidateResponse: func(int, http.Header) *output.Error { called = true; return nil },
			}, &response)
			if failure == nil || failure.Code != "invalid_server_response" || called || response.Value != "" {
				t.Fatalf("Do() response=%#v failure=%#v called=%v", response, failure, called)
			}
		})
	}
}

func TestClientResponseValidatorFailureIsReturnedWithoutDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"value":"ok"}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "validator-token", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	want := &output.Error{ExitCode: 9, Code: "contract_mismatch", Message: "Contract mismatch"}
	var response struct {
		Value string `json:"value"`
	}
	failure := client.Do(context.Background(), Request{
		Method: http.MethodGet, Path: "/resource", ExpectedStatus: http.StatusOK,
		ValidateResponse: func(int, http.Header) *output.Error { return want },
	}, &response)
	if failure != want || response.Value != "" {
		t.Fatalf("Do() response=%#v failure=%#v", response, failure)
	}
}

func TestClientSendsExactAuthenticationAndUserAgent(t *testing.T) {
	t.Parallel()

	const token = "super-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
				w.Header().Set("Content-Type", "application/json")
				targetRequests.Add(1)
				targetAuthorization.Store(r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer target.Close()

			var sourceRequests atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
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
		w.Header().Set("Content-Type", "application/json")
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
		w.Header().Set("Content-Type", "application/json")
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

func TestKnownNonRetryStatusesClassifiedBeforeFailingBodyRead(t *testing.T) {
	t.Parallel()

	statuses := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
	}
	for status := 300; status < 400; status++ {
		statuses = append(statuses, status)
	}
	for _, status := range statuses {
		status := status
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			client, err := NewClient("http://127.0.0.1", "status-body-secret", time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			var attempts atomic.Int32
			body := &trackedFailingBody{}
			client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts.Add(1)
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
			})

			failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/status"}, nil)
			if failure == nil || failure.ExitCode != 9 {
				t.Fatalf("failure = %+v, want exit 9", failure)
			}
			if status >= 300 && status < 400 {
				if failure.Code != "redirect_refused" {
					t.Fatalf("failure = %+v, want redirect_refused", failure)
				}
				if body.reads.Load() != 0 {
					t.Errorf("redirect response body reads = %d, want 0", body.reads.Load())
				}
			} else if failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %+v, want invalid_server_response", failure)
			}
			if failure.HTTPStatus == nil || *failure.HTTPStatus != status {
				t.Errorf("HTTPStatus = %v, want %d", failure.HTTPStatus, status)
			}
			if attempts.Load() != 1 {
				t.Errorf("attempts = %d, want 1", attempts.Load())
			}
			if !body.closed.Load() {
				t.Error("response body was not closed")
			}
			assertOutputErrorOmits(t, failure, "status-body-secret", "failing-body-secret")
		})
	}
}

func TestClientExpectedStatusRejectsOtherSuccessfulStatusesWithoutChangingDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var destination any
	failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/resource", ExpectedStatus: http.StatusOK}, &destination)
	if failure == nil || failure.Code != "invalid_server_response" || failure.HTTPStatus == nil || *failure.HTTPStatus != http.StatusCreated {
		t.Fatalf("failure = %#v", failure)
	}
	if failure = client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/resource"}, &destination); failure != nil {
		t.Fatalf("default read status handling changed: %#v", failure)
	}
}

func TestHeadAllowsEmptySuccessfulResponse(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		attempt := requests.Add(1)
		if attempt == 1 && r.Method != http.MethodHead {
			t.Errorf("first method = %q, want HEAD", r.Method)
		}
		if attempt == 2 && r.Method != http.MethodGet {
			t.Errorf("second method = %q, want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "head-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if failure := client.Do(context.Background(), Request{Method: http.MethodHead, Path: "/resource"}, nil); failure != nil {
		t.Fatalf("HEAD failure = %+v, want success", failure)
	}
	failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/resource"}, nil)
	if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
		t.Fatalf("empty GET failure = %+v, want invalid_server_response exit 9", failure)
	}
}

func TestRequestPathStrictlySeparatesPathAndQuery(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests.Add(1)
		if got, want := r.URL.EscapedPath(), "/resource/%23fragment-data"; got != want {
			t.Errorf("EscapedPath() = %q, want %q", got, want)
		}
		if got, want := r.URL.RawQuery, "filter=active"; got != want {
			t.Errorf("RawQuery = %q, want %q", got, want)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "path-secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	for _, path := range []string{"/resource?", "/resource?hidden=true", "/resource#", "/resource#hidden"} {
		failure := client.Do(context.Background(), Request{Method: http.MethodGet, Path: path}, nil)
		if failure == nil || failure.Code != "invalid_request" || failure.ExitCode != 2 {
			t.Errorf("Path %q failure = %+v, want invalid_request exit 2", path, failure)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("requests after invalid paths = %d, want 0", requests.Load())
	}
	if failure := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/resource/%23fragment-data",
		Query:  map[string][]string{"filter": {"active"}},
	}, nil); failure != nil {
		t.Fatalf("escaped fragment path failure = %+v", failure)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
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

type trackedFailingBody struct {
	reads  atomic.Int32
	closed atomic.Bool
}

func (b *trackedFailingBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, fmt.Errorf("failing-body-secret")
}

func (b *trackedFailingBody) Close() error {
	b.closed.Store(true)
	return nil
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
