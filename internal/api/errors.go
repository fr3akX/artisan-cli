package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

func decodeAPIError(status int, body []byte) *output.Error {
	var envelope errorEnvelope
	if err := decodeOneJSON(body, &envelope); err != nil || envelope.Error == nil || !validAPIError(envelope.Error) {
		return invalidServerResponse(status)
	}
	return &output.Error{
		ExitCode:   exitCodeForStatus(status),
		Code:       envelope.Error.Code,
		Message:    envelope.Error.Message,
		HTTPStatus: statusPointer(status),
	}
}

func validAPIError(serverError *apiErrorBody) bool {
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
	return true
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
