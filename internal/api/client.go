// Package api provides authenticated, bounded access to the Artisan HTTP API.
package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/release"
)

const maxResponseBodyBytes = 1 << 20

// Request describes one API operation. Body must return a newly opened body on
// every call so an idempotent mutation can be replayed safely.
type Request struct {
	Method         string
	Path           string
	Query          url.Values
	Body           func() (io.ReadCloser, string, error)
	IdempotencyKey string
}

// Client is an authenticated Artisan API client.
type Client struct {
	serverURL  *url.URL
	token      string
	httpClient *http.Client
	userAgent  string
}

// NewClient validates its origin and credentials and creates a client that
// refuses redirects. Validation errors never echo the origin or token.
func NewClient(serverURL, token string, timeout time.Duration) (*Client, error) {
	normalized, err := config.NormalizeServerURL(serverURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" || containsUnsafeHeaderValue(token) {
		return nil, errors.New("invalid_credentials: token must be a nonblank single line")
	}
	if timeout <= 0 {
		return nil, errors.New("invalid_timeout: timeout must be greater than zero")
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, errors.New("invalid_server_url: malformed URL")
	}

	return &Client{
		serverURL: parsed,
		token:     token,
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		userAgent: "artisan-cli/" + release.Info().Version,
	}, nil
}

// Do executes an API request, applying bounded retries only to safe reads or
// replayable idempotent mutations. It returns nil on success.
func (c *Client) Do(ctx context.Context, request Request, destination any) *output.Error {
	if ctx == nil {
		return localFailure("invalid_request", "Request context is required")
	}
	request.Method = strings.ToUpper(strings.TrimSpace(request.Method))
	if request.Method == "" || containsUnsafeHeaderValue(request.Method) {
		return localFailure("invalid_request", "A valid HTTP method is required")
	}
	endpoint, err := c.endpointURL(request.Path, request.Query)
	if err != nil {
		return localFailure("invalid_request", "A valid API path is required")
	}
	if !isSafeRead(request.Method) {
		if err := ValidateIdempotencyKey(request.IdempotencyKey); err != nil {
			return localFailure("invalid_idempotency_key", "Idempotency key is invalid")
		}
	} else if request.IdempotencyKey != "" {
		if err := ValidateIdempotencyKey(request.IdempotencyKey); err != nil {
			return localFailure("invalid_idempotency_key", "Idempotency key is invalid")
		}
	}

	canRetry := requestCanRetry(request)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		body, contentType, failure := openRequestBody(request)
		if failure != nil {
			return failure
		}
		httpRequest, err := http.NewRequestWithContext(ctx, request.Method, endpoint, body)
		if err != nil {
			if body != nil {
				_ = body.Close()
			}
			return localFailure("invalid_request", "The API request is invalid")
		}
		httpRequest.Header.Set("Authorization", "Bearer "+c.token)
		httpRequest.Header.Set("User-Agent", c.userAgent)
		if contentType != "" {
			httpRequest.Header.Set("Content-Type", contentType)
		}
		if request.IdempotencyKey != "" {
			httpRequest.Header.Set("Idempotency-Key", request.IdempotencyKey)
		}

		response, err := c.httpClient.Do(httpRequest)
		if err != nil {
			if canRetry && attempt < maxAttempts-1 && ctx.Err() == nil {
				if err := waitForRetry(ctx, attempt); err == nil {
					continue
				}
			}
			return networkFailure()
		}

		responseBody, oversized, readErr := readBoundedResponse(response.Body)
		if readErr != nil {
			if canRetry && attempt < maxAttempts-1 && ctx.Err() == nil {
				if err := waitForRetry(ctx, attempt); err == nil {
					continue
				}
			}
			return networkFailure()
		}
		if oversized {
			return invalidServerResponse(response.StatusCode)
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return redirectRefused(response.StatusCode)
		}
		if isTransientStatus(response.StatusCode) && canRetry && attempt < maxAttempts-1 {
			if err := waitForRetry(ctx, attempt); err != nil {
				return networkFailure()
			}
			continue
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return decodeSuccess(response.StatusCode, responseBody, destination)
		}
		if response.StatusCode >= 400 && response.StatusCode < 600 {
			return decodeAPIError(response.StatusCode, responseBody)
		}
		return invalidServerResponse(response.StatusCode)
	}
	return networkFailure()
}

func (c *Client) endpointURL(path string, query url.Values) (string, error) {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "", errors.New("invalid path")
	}
	parsedPath, err := url.Parse(path)
	if err != nil || parsedPath.IsAbs() || parsedPath.Host != "" || parsedPath.User != nil || parsedPath.RawQuery != "" || parsedPath.Fragment != "" {
		return "", errors.New("invalid path")
	}
	endpoint := *c.serverURL
	endpoint.Path = parsedPath.Path
	endpoint.RawPath = parsedPath.RawPath
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func openRequestBody(request Request) (io.ReadCloser, string, *output.Error) {
	if request.Body == nil {
		return nil, "", nil
	}
	body, contentType, err := request.Body()
	if err != nil {
		if body != nil {
			_ = body.Close()
		}
		return nil, "", localFailure("request_body_error", "Unable to prepare the request body")
	}
	if body == nil {
		return nil, "", localFailure("request_body_error", "Unable to prepare the request body")
	}
	if containsUnsafeHeaderValue(contentType) {
		_ = body.Close()
		return nil, "", localFailure("request_body_error", "Request content type is invalid")
	}
	return body, contentType, nil
}

func readBoundedResponse(body io.ReadCloser) ([]byte, bool, error) {
	defer body.Close()
	contents, err := io.ReadAll(io.LimitReader(body, maxResponseBodyBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(contents) > maxResponseBodyBytes {
		return nil, true, nil
	}
	return contents, false, nil
}

func decodeSuccess(status int, body []byte, destination any) *output.Error {
	if len(strings.TrimSpace(string(body))) == 0 {
		if status == http.StatusNoContent || status == http.StatusResetContent {
			return nil
		}
		return invalidServerResponse(status)
	}
	if destination == nil {
		var ignored any
		if err := decodeOneJSON(body, &ignored); err != nil {
			return invalidServerResponse(status)
		}
		return nil
	}
	if err := decodeOneJSON(body, destination); err != nil {
		return invalidServerResponse(status)
	}
	return nil
}
