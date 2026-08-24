package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const (
	maxAPIErrorCodeBytes    = 128
	maxAPIErrorMessageBytes = 4096
)

var (
	apiErrorCodePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	errMultipleJSONValues = errors.New("multiple JSON values")
)

type errorEnvelope struct {
	Error *apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

func isUnstructuredRouteNotFound(body []byte, header http.Header) bool {
	if len(body) == 0 {
		return len(header.Values("Content-Type")) == 0
	}
	trimmed := bytes.TrimSpace(body)
	if exactFrameworkNotFound(trimmed) {
		return trustedJSONContentType(header)
	}
	if exactGoRouteNotFound(body) || bytes.Equal(body, []byte("Not Found")) {
		return trustedRouteNotFoundContentType(header, "text/plain")
	}
	if exactProxyRouteNotFoundHTML(body) {
		return trustedRouteNotFoundContentType(header, "text/html")
	}
	return false
}

func exactGoRouteNotFound(body []byte) bool {
	return bytes.Equal(body, []byte("404 page not found")) ||
		bytes.Equal(body, []byte("404 page not found\n")) ||
		bytes.Equal(body, []byte("404 page not found\r\n"))
}

func exactProxyRouteNotFoundHTML(body []byte) bool {
	switch string(body) {
	case "<html><body>Not Found</body></html>":
		return true
	default:
		return false
	}
}

func trustedRouteNotFoundContentType(header http.Header, expected string) bool {
	values := header.Values("Content-Type")
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != expected {
		return false
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func exactFrameworkNotFound(body []byte) bool {
	if rejectDuplicateJSONKeys(body) != nil {
		return false
	}
	var object map[string]json.RawMessage
	if decodeOneJSON(body, &object) != nil || len(object) != 1 {
		return false
	}
	raw, exists := object["detail"]
	if !exists {
		return false
	}
	var detail string
	return decodeOneJSON(raw, &detail) == nil && detail == "Not Found"
}

func decodeAPIError(status int, body []byte, forbiddenValues ...string) *output.Error {
	var envelope errorEnvelope
	if err := decodeOneJSON(body, &envelope); err != nil || envelope.Error == nil || !validAPIError(envelope.Error, forbiddenValues) {
		return invalidServerResponseAvoiding(status, forbiddenValues)
	}
	return &output.Error{
		ExitCode:   exitCodeForStatus(status),
		Code:       envelope.Error.Code,
		Message:    envelope.Error.Message,
		HTTPStatus: statusPointer(status),
	}
}

func validAPIError(serverError *apiErrorBody, forbiddenValues []string) bool {
	if len(serverError.Code) == 0 || len(serverError.Code) > maxAPIErrorCodeBytes || !apiErrorCodePattern.MatchString(serverError.Code) {
		return false
	}
	if len(serverError.Message) == 0 || len(serverError.Message) > maxAPIErrorMessageBytes || !utf8.ValidString(serverError.Message) {
		return false
	}
	for _, character := range serverError.Message {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return !containsAny(serverError.Code, forbiddenValues) && !containsAny(serverError.Message, forbiddenValues)
}

func decodeOneJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errMultipleJSONValues
		}
		return err
	}
	return nil
}

func exitCodeForStatus(status int) int {
	switch status {
	case http.StatusUnauthorized:
		return 4
	case http.StatusForbidden:
		return 5
	case http.StatusNotFound:
		return 6
	default:
		if status >= 400 && status < 500 {
			return 7
		}
		return 9
	}
}

func invalidServerResponse(status int) *output.Error {
	failure := &output.Error{
		ExitCode: 9,
		Code:     "invalid_server_response",
		Message:  "The server returned an invalid response",
	}
	if status != 0 {
		failure.HTTPStatus = statusPointer(status)
	}
	return failure
}

func invalidServerResponseAvoiding(status int, forbiddenValues []string) *output.Error {
	failure := invalidServerResponse(status)
	failure.Code = firstSafeGeneric([]string{"invalid_server_response", "remote_response_rejected", "request_failed", "x"}, forbiddenValues)
	failure.Message = firstSafeGeneric([]string{
		"The server returned an invalid response",
		"Remote response rejected",
		"Request failed",
		"x",
	}, forbiddenValues)
	return failure
}

func firstSafeGeneric(candidates, forbiddenValues []string) string {
	for _, candidate := range candidates {
		if !containsAny(candidate, forbiddenValues) {
			return candidate
		}
	}
	return ""
}

func containsAny(value string, forbiddenValues []string) bool {
	for _, forbidden := range forbiddenValues {
		if forbidden != "" && strings.Contains(value, forbidden) {
			return true
		}
	}
	return false
}

func redirectRefused(status int) *output.Error {
	return &output.Error{
		ExitCode:   9,
		Code:       "redirect_refused",
		Message:    "The server returned a redirect, which was refused",
		HTTPStatus: statusPointer(status),
	}
}

func networkFailure() *output.Error {
	return &output.Error{
		ExitCode: 8,
		Code:     "network_error",
		Message:  "Unable to communicate with the server",
	}
}

func interruptionFailure() *output.Error {
	return &output.Error{
		ExitCode: 130,
		Code:     "interrupted",
		Message:  "Operation interrupted",
	}
}

func localFailure(code, message string) *output.Error {
	return &output.Error{ExitCode: 2, Code: code, Message: message}
}

func statusPointer(status int) *int {
	value := status
	return &value
}

func containsUnsafeHeaderValue(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}
