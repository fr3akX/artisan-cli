package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/securefile"
)

const (
	// ReviewTemplateVersion is the only supported fixed roast-review contract.
	ReviewTemplateVersion = "artisan-roast-review-v1"
	maxRoastReviewBytes   = 16_000
	maxRoastReviewRunes   = 4_000
)

var roastReviewRevisionMarker = regexp.MustCompile(`^Profile revision: ([1-9][0-9]*) \(([0-9a-f]{64})\)$`)

// RoastReviewRequest is the strict three-field review endpoint request.
type RoastReviewRequest struct {
	Body            string `json:"body"`
	RevisionSHA256  string `json:"revision_sha256"`
	TemplateVersion string `json:"template_version"`
}

// RoastReviewResult reports the ordinary comment selected by the server's
// first-writer review slot without exposing its canonical idempotency key.
type RoastReviewResult struct {
	Comment          CommentView `json:"comment"`
	RevisionSHA256   string      `json:"revision_sha256"`
	TemplateVersion  string      `json:"template_version"`
	IdempotentReplay bool        `json:"idempotent_replay"`
}

// CanonicalRoastReviewKey derives the one stable identity for a roast,
// immutable revision, and fixed template.
func CanonicalRoastReviewKey(rawRoastUUID, revisionSHA, template string) (string, *output.Error) {
	roastUUID, failure := NormalizeRoastUUID(rawRoastUUID)
	if failure != nil {
		return "", failure
	}
	if !canonicalSHA256.MatchString(revisionSHA) || template != ReviewTemplateVersion {
		return "", invalidReviewFailure()
	}
	sum := sha256.Sum256([]byte("artisan-roast-review\x00" + roastUUID + "\x00" + revisionSHA + "\x00" + template))
	return "review-" + hex.EncodeToString(sum[:]), nil
}

// ReadRoastReviewFile securely snapshots and normalizes one fixed review body.
func ReadRoastReviewFile(path, revisionSHA, template string) (RoastReviewRequest, *output.Error) {
	contents, err := securefile.ReadRegularSnapshot(path, 16<<10)
	if err != nil || bytes.ContainsRune(contents, '\r') {
		return RoastReviewRequest{}, invalidReviewFileFailure()
	}
	request := RoastReviewRequest{
		Body:            strings.TrimSpace(string(contents)),
		RevisionSHA256:  revisionSHA,
		TemplateVersion: template,
	}
	if _, failure := validateRoastReviewRequest(request); failure != nil {
		return RoastReviewRequest{}, invalidReviewFileFailure()
	}
	return request, nil
}

// PostRoastReview preflights the current parsed revision and posts one
// replay-safe, canonical review request.
func (c *Client) PostRoastReview(ctx context.Context, rawRoastUUID string, request RoastReviewRequest) (RoastReviewResult, *output.Error) {
	var result RoastReviewResult
	roastUUID, failure := NormalizeRoastUUID(rawRoastUUID)
	if failure != nil {
		return result, failure
	}
	revisionNumber, failure := validateRoastReviewRequest(request)
	if failure != nil {
		return result, failure
	}
	current, failure := c.Roast(ctx, roastUUID)
	if failure != nil {
		return result, failure
	}
	requestedIsCurrent := current.CurrentRevision != nil &&
		current.CurrentRevision.RevisionNumber == revisionNumber &&
		current.CurrentRevision.SHA256 == request.RevisionSHA256
	if requestedIsCurrent {
		if current.State != "parsed" || current.CurrentRevision.ParseState != "parsed" {
			return result, reviewRevisionChangedFailure()
		}
	} else {
		revision, lookupFailure := c.findRoastRevision(ctx, roastUUID, revisionNumber)
		if lookupFailure != nil {
			if lookupFailure.Code == "not_found" && lookupFailure.HTTPStatus == nil {
				return result, reviewRevisionChangedFailure()
			}
			return result, lookupFailure
		}
		if revision.SHA256 != request.RevisionSHA256 || revision.ParseState != "parsed" {
			return result, reviewRevisionChangedFailure()
		}
	}
	key, failure := CanonicalRoastReviewKey(roastUUID, request.RevisionSHA256, request.TemplateVersion)
	if failure != nil {
		return result, failure
	}
	body, err := newJSONBody(request)
	if err != nil {
		return result, invalidReviewFailure()
	}

	var replay bool
	var locationCommentUUID string
	validator := func(status int, header http.Header) *output.Error {
		if status != http.StatusCreated || !validRoastReviewSuccessHeaders(header, request.Body, key, c.token, c.serverURL.String()) {
			return invalidServerResponse(status)
		}
		if !singleHeaderEquals(header, "Cache-Control", "no-store") ||
			!singleHeaderEquals(header, "X-Roast-Revision-SHA256", request.RevisionSHA256) ||
			!singleHeaderEquals(header, "X-Review-Template-Version", request.TemplateVersion) {
			return invalidServerResponse(status)
		}
		replayValues := header.Values("X-Idempotent-Replay")
		if len(replayValues) != 1 || (replayValues[0] != "true" && replayValues[0] != "false") {
			return invalidServerResponse(status)
		}
		replay = replayValues[0] == "true"
		if !requestedIsCurrent && !replay {
			return invalidServerResponse(status)
		}
		locationValues := header.Values("Location")
		prefix := roastAPIRoot + "/" + roastUUID + "/comments/"
		if len(locationValues) != 1 || !strings.HasPrefix(locationValues[0], prefix) {
			return invalidServerResponse(status)
		}
		locationCommentUUID = strings.TrimPrefix(locationValues[0], prefix)
		if !validRoastUUID(locationCommentUUID) || locationValues[0] != prefix+locationCommentUUID {
			return invalidServerResponse(status)
		}
		return nil
	}

	var comment CommentView
	failure = c.do(ctx, Request{
		Method: http.MethodPost,
		Path:   roastAPIRoot + "/" + roastUUID + "/comments/ai-review",
		Body:   body, IdempotencyKey: key, ExpectedStatus: http.StatusCreated,
		ValidateResponse: validator, ForbiddenResponseValues: []string{key},
	}, &comment, true)
	if failure != nil {
		return result, sanitizeRoastReviewPostFailure(failure)
	}
	if roastReviewCommentReflectsForbidden(comment, request.Body, key, c.token, c.serverURL.String()) || comment.RoastUUID != roastUUID || comment.CommentUUID != locationCommentUUID || (!replay && (comment.IsDeleted || comment.Body == nil || *comment.Body != request.Body)) {
		return result, invalidServerResponse(http.StatusCreated)
	}
	return RoastReviewResult{
		Comment: comment, RevisionSHA256: request.RevisionSHA256,
		TemplateVersion: request.TemplateVersion, IdempotentReplay: replay,
	}, nil
}

func validateRoastReviewRequest(request RoastReviewRequest) (int64, *output.Error) {
	if request.TemplateVersion != ReviewTemplateVersion || !canonicalSHA256.MatchString(request.RevisionSHA256) ||
		request.Body == "" || len(request.Body) > maxRoastReviewBytes || !utf8.ValidString(request.Body) ||
		utf8.RuneCountInString(request.Body) > maxRoastReviewRunes || strings.TrimSpace(request.Body) != request.Body ||
		strings.ContainsRune(request.Body, '\r') {
		return 0, invalidReviewFailure()
	}
	for _, character := range request.Body {
		if character != '\n' && unicode.IsControl(character) {
			return 0, invalidReviewFailure()
		}
	}
	lines := strings.Split(request.Body, "\n")
	if len(lines) < 5 || lines[0] != "AI roast analysis" || lines[1] != "Template: "+ReviewTemplateVersion || lines[3] != "" {
		return 0, invalidReviewFailure()
	}
	marker := roastReviewRevisionMarker.FindStringSubmatch(lines[2])
	if len(marker) != 3 || marker[2] != request.RevisionSHA256 {
		return 0, invalidReviewFailure()
	}
	revisionNumber, err := strconv.ParseInt(marker[1], 10, 64)
	if err != nil || !between(revisionNumber, 1, maxRoastRevisionNumber) {
		return 0, invalidReviewFailure()
	}
	return revisionNumber, nil
}

func singleHeaderEquals(header http.Header, name, expected string) bool {
	values := header.Values(name)
	return len(values) == 1 && values[0] == expected && !strings.Contains(values[0], ",")
}

// Review responses allow only endpoint contract metadata, net/http framing
// metadata, and ubiquitous proxy/trace metadata. Application-specific custom
// headers require an explicit client update so reflected data cannot be split
// across newly invented names or values.
var roastReviewSuccessHeaderNames = map[string]struct{}{
	"cache-control":             {},
	"location":                  {},
	"x-idempotent-replay":       {},
	"x-roast-revision-sha256":   {},
	"x-review-template-version": {},
	"content-type":              {},
	"content-length":            {},
	"date":                      {},
	"server":                    {},
	"via":                       {},
	"x-request-id":              {},
	"traceparent":               {},
	"tracestate":                {},
}

// Values for these headers are checked against their exact endpoint contract
// below, so marker text that necessarily contains the same fixed values is not
// mistaken for response reflection.
var roastReviewFixedValueHeaderNames = map[string]struct{}{
	"cache-control":             {},
	"location":                  {},
	"x-idempotent-replay":       {},
	"x-roast-revision-sha256":   {},
	"x-review-template-version": {},
	"content-type":              {},
}

func validRoastReviewSuccessHeaders(header http.Header, body string, forbiddenValues ...string) bool {
	for name, values := range header {
		lowerName := strings.ToLower(name)
		if _, allowed := roastReviewSuccessHeaderNames[lowerName]; !allowed || len(values) == 0 {
			return false
		}
		if containsCaseInsensitive(name, forbiddenValues) || containsReviewBodyExcerpt(name, body) {
			return false
		}
		_, hasFixedContractValue := roastReviewFixedValueHeaderNames[lowerName]
		for _, value := range values {
			if containsAny(value, forbiddenValues) || (!hasFixedContractValue && containsReviewBodyExcerpt(value, body)) {
				return false
			}
		}
	}
	if !singleHeaderEquals(header, "Content-Type", "application/json") &&
		!singleHeaderEquals(header, "Content-Type", "application/json; charset=utf-8") {
		return false
	}
	if values := header.Values("Content-Length"); len(values) > 1 {
		return false
	} else if len(values) == 1 {
		if _, err := strconv.ParseUint(values[0], 10, 63); err != nil {
			return false
		}
	}
	if values := header.Values("Date"); len(values) > 1 {
		return false
	} else if len(values) == 1 {
		if _, err := http.ParseTime(values[0]); err != nil {
			return false
		}
	}
	return true
}

func containsCaseInsensitive(value string, forbiddenValues []string) bool {
	lowerValue := strings.ToLower(value)
	for _, forbidden := range forbiddenValues {
		if forbidden != "" && strings.Contains(lowerValue, strings.ToLower(forbidden)) {
			return true
		}
	}
	return false
}

func containsReviewBodyExcerpt(value, body string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= 8 && strings.Contains(value, line) {
			return true
		}
	}
	return false
}

func roastReviewCommentReflectsForbidden(comment CommentView, requestBody, key, token, serverURL string) bool {
	forbidden := []string{key, token, serverURL}
	bodyIndependentFields := []string{
		comment.CommentUUID, comment.RoastUUID, comment.AuthorNickname, comment.CreatedAt,
	}
	if comment.EditedAt != nil {
		bodyIndependentFields = append(bodyIndependentFields, *comment.EditedAt)
	}
	if comment.DeletedAt != nil {
		bodyIndependentFields = append(bodyIndependentFields, *comment.DeletedAt)
	}
	for _, value := range bodyIndependentFields {
		if containsAny(value, forbidden) || containsReviewBodyExcerpt(value, requestBody) {
			return true
		}
	}
	return comment.Body != nil && containsAny(*comment.Body, forbidden)
}

func invalidReviewFailure() *output.Error {
	return localFailure("invalid_review", "Roast review is invalid")
}

func invalidReviewFileFailure() *output.Error {
	return localFailure("invalid_review_file", "Roast review file is invalid")
}

func reviewRevisionChangedFailure() *output.Error {
	return &output.Error{ExitCode: 7, Code: "roast_revision_changed", Message: "The roast revision changed before the review could be posted"}
}

func sanitizeRoastReviewPostFailure(failure *output.Error) *output.Error {
	if failure == nil || failure.HTTPStatus == nil {
		return failure
	}
	status := *failure.HTTPStatus
	if failure.Code == "server_upgrade_required" && status == http.StatusNotFound {
		return serverUpgradeRequiredFailure()
	}
	if failure.Code == "invalid_server_response" {
		return invalidServerResponse(status)
	}
	if !coherentRoastReviewFailure(status, failure.Code) {
		return invalidServerResponse(status)
	}

	sanitized := *failure
	switch failure.Code {
	case "roast_revision_changed":
		sanitized.Message = "The roast revision changed before the review could be posted"
	case "review_idempotency_conflict":
		sanitized.Message = "The roast review identity conflicts with an existing request"
	case "not_found":
		sanitized.Message = "Roast not found"
	case "authentication_required":
		sanitized.Message = "Authentication required"
	case "permission_denied":
		sanitized.Message = "Permission denied"
	case "invalid_review":
		sanitized.Message = "Roast review is invalid"
	}
	return &sanitized
}

func coherentRoastReviewFailure(status int, code string) bool {
	switch status {
	case http.StatusUnauthorized:
		return code == "authentication_required"
	case http.StatusForbidden:
		return code == "permission_denied"
	case http.StatusNotFound:
		return code == "not_found"
	case http.StatusConflict:
		return code == "roast_revision_changed" || code == "review_idempotency_conflict"
	case http.StatusUnprocessableEntity:
		return code == "invalid_review"
	default:
		return false
	}
}
