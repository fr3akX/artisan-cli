// Package api provides authenticated, bounded access to the Artisan HTTP API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/release"
)

const maxResponseBodyBytes = 1 << 20

// ResponseValidator validates trusted response metadata for a successful API operation.
type ResponseValidator func(status int, header http.Header) *output.Error

// Request describes one API operation. Body must return a newly opened body on
// every call so an idempotent mutation can be replayed safely.
type Request struct {
	Method                  string
	Path                    string
	Query                   url.Values
	Body                    func() (io.ReadCloser, string, error)
	IdempotencyKey          string
	ExpectedStatus          int
	ValidateResponse        ResponseValidator
	ForbiddenResponseValues []string
}

// Client is an authenticated Artisan API client.
type Client struct {
	serverURL   *url.URL
	token       string
	httpClient  *http.Client
	userAgent   string
	downloadOps downloadOperations
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
		userAgent:   "artisan-cli/" + release.Info().Version,
		downloadOps: defaultDownloadOperations(),
	}, nil
}

// Do executes an API request, applying bounded retries only to safe reads or
// replayable idempotent mutations. It returns nil on success.
func (c *Client) Do(ctx context.Context, request Request, destination any) *output.Error {
	return c.do(ctx, request, destination, false)
}

func (c *Client) do(ctx context.Context, request Request, destination any, missingEndpointOnUnstructuredNotFound bool) (failure *output.Error) {
	defer func() {
		failure = c.failureWithoutSecrets(failure)
	}()

	if ctx == nil {
		return localFailure("invalid_request", "Request context is required")
	}
	if ctx.Err() != nil {
		return contextOrNetworkFailure(ctx)
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
	forbiddenResponseValues := make([]string, 0, 2+len(request.ForbiddenResponseValues))
	forbiddenResponseValues = append(forbiddenResponseValues, c.token, c.serverURL.String())
	forbiddenResponseValues = append(forbiddenResponseValues, request.ForbiddenResponseValues...)
	forbiddenBodyValues := make([]string, 0, 1+len(request.ForbiddenResponseValues))
	forbiddenBodyValues = append(forbiddenBodyValues, c.token)
	forbiddenBodyValues = append(forbiddenBodyValues, request.ForbiddenResponseValues...)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return contextOrNetworkFailure(ctx)
		}
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
			if multipartSourceChanged(err) {
				return imageFileChangedFailure()
			}
			if canRetry && attempt < maxAttempts-1 && ctx.Err() == nil {
				if err := waitForRetry(ctx, attempt); err == nil {
					continue
				}
			}
			return contextOrNetworkFailure(ctx)
		}

		status := response.StatusCode
		if status >= 300 && status < 400 {
			_ = response.Body.Close()
			return redirectRefused(status)
		}

		responseBody, oversized, readErr := readBoundedResponse(response.Body)
		if readErr != nil {
			isExpectedClientError := status >= 400 && status < 500
			isNonTransientServerError := status >= 500 && status < 600 && !isTransientStatus(status)
			if isExpectedClientError || isNonTransientServerError {
				return invalidServerResponseAvoiding(status, forbiddenResponseValues)
			}
			if canRetry && attempt < maxAttempts-1 && ctx.Err() == nil {
				if err := waitForRetry(ctx, attempt); err == nil {
					continue
				}
			}
			if isTransientStatus(status) {
				return invalidServerResponseAvoiding(status, forbiddenResponseValues)
			}
			return contextOrNetworkFailure(ctx)
		}
		if oversized {
			return invalidServerResponseAvoiding(status, forbiddenResponseValues)
		}
		if ctx.Err() != nil {
			return contextOrNetworkFailure(ctx)
		}
		if responseBodyContainsAnyExactToken(responseBody, forbiddenBodyValues) || responseHeaderContainsAny(response.Header, forbiddenResponseValues) {
			return invalidServerResponseAvoiding(status, forbiddenResponseValues)
		}
		if status == http.StatusNotFound && missingEndpointOnUnstructuredNotFound && isUnstructuredRouteNotFound(responseBody, response.Header) {
			return serverUpgradeRequiredFailure()
		}
		if responseRequiresJSON(responseBody) && !trustedJSONContentType(response.Header) {
			return invalidServerResponseAvoiding(status, forbiddenResponseValues)
		}
		if isTransientStatus(status) && canRetry && attempt < maxAttempts-1 {
			if err := waitForRetry(ctx, attempt); err != nil {
				return contextOrNetworkFailure(ctx)
			}
			continue
		}
		if status >= 200 && status < 300 {
			if request.ExpectedStatus != 0 && status != request.ExpectedStatus {
				return invalidServerResponseAvoiding(status, forbiddenResponseValues)
			}
			if request.ValidateResponse != nil {
				if failure := request.ValidateResponse(status, response.Header.Clone()); failure != nil {
					return failure
				}
			}
			return decodeSuccess(request.Method, status, responseBody, destination)
		}
		if status >= 400 && status < 600 {
			return decodeAPIError(status, responseBody, forbiddenResponseValues...)
		}
		return invalidServerResponseAvoiding(status, forbiddenResponseValues)
	}
	return contextOrNetworkFailure(ctx)
}

func responseRequiresJSON(body []byte) bool {
	return len(bytes.TrimSpace(body)) != 0
}

func responseHeaderContainsAny(header http.Header, forbiddenValues []string) bool {
	for name, values := range header {
		if containsAny(name, forbiddenValues) {
			return true
		}
		for _, value := range values {
			if containsAny(value, forbiddenValues) {
				return true
			}
		}
	}
	return false
}

func responseBodyContainsAnyExactToken(body []byte, forbiddenValues []string) bool {
	for _, forbidden := range forbiddenValues {
		if containsExactTokenReflection(body, forbidden) {
			return true
		}
	}
	return false
}

func containsExactTokenReflection(body []byte, token string) bool {
	if token == "" {
		return false
	}
	if bytes.Contains(body, []byte(token)) {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	for {
		value, err := decoder.Token()
		if err != nil {
			return false
		}
		if text, ok := value.(string); ok && strings.Contains(text, token) {
			return true
		}
	}
}

func trustedJSONContentType(header http.Header) bool {
	values := header.Values("Content-Type")
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func contextOrNetworkFailure(ctx context.Context) *output.Error {
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return interruptionFailure()
	}
	return networkFailure()
}

func (c *Client) failureWithoutSecrets(failure *output.Error) *output.Error {
	if failure == nil {
		return nil
	}
	forbiddenValues := []string{c.token, c.serverURL.String()}
	if !containsAny(failure.Code, forbiddenValues) && !containsAny(failure.Message, forbiddenValues) {
		return failure
	}

	sanitized := *failure
	if containsAny(sanitized.Code, forbiddenValues) {
		sanitized.Code = firstSafeGeneric([]string{"request_failed", "remote_error", "failure", "x"}, forbiddenValues)
	}
	if containsAny(sanitized.Message, forbiddenValues) {
		sanitized.Message = firstSafeGeneric([]string{"Request failed", "Remote error", "Failure", "x"}, forbiddenValues)
	}
	return &sanitized
}

func (c *Client) endpointURL(path string, query url.Values) (string, error) {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "", errors.New("invalid path")
	}
	parsedPath, err := url.Parse(path)
	if err != nil || parsedPath.IsAbs() || parsedPath.Host != "" || parsedPath.User != nil || parsedPath.RawQuery != "" || parsedPath.ForceQuery || strings.Contains(path, "#") {
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
		if multipartSourceChanged(err) {
			return nil, "", imageFileChangedFailure()
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

func multipartSourceChanged(err error) bool {
	var fileFailure *multipartFileError
	return errors.As(err, &fileFailure) && fileFailure.changed
}

func imageFileChangedFailure() *output.Error {
	return localFailure("image_file_changed", "An image file changed after upload preparation")
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

func decodeSuccess(method string, status int, body []byte, destination any) *output.Error {
	if len(strings.TrimSpace(string(body))) == 0 {
		if method == http.MethodHead || status == http.StatusNoContent || status == http.StatusResetContent {
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
