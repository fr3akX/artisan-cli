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
	if current.State != "parsed" || current.CurrentRevision == nil || current.CurrentRevision.ParseState != "parsed" || current.CurrentRevision.SHA256 != request.RevisionSHA256 || current.CurrentRevision.RevisionNumber != revisionNumber {
		return result, reviewRevisionChangedFailure()
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
		if status != http.StatusCreated || responseHeaderReflectsReviewBody(header, request.Body) {
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
	failure = c.Do(ctx, Request{
		Method: http.MethodPost,
		Path:   roastAPIRoot + "/" + roastUUID + "/comments/ai-review",
		Body:   body, IdempotencyKey: key, ExpectedStatus: http.StatusCreated,
		ValidateResponse: validator,
	}, &comment)
	if failure != nil {
		if containsAny(failure.Code, []string{request.Body}) || containsAny(failure.Message, []string{request.Body}) {
			return result, invalidServerResponseAvoiding(httpStatusOrZero(failure), []string{c.token, c.serverURL.String(), request.Body})
		}
		return result, classifyRoastAPIFailure(failure, true)
	}
	if comment.RoastUUID != roastUUID || comment.CommentUUID != locationCommentUUID || (!replay && (comment.IsDeleted || comment.Body == nil || *comment.Body != request.Body)) {
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

func responseHeaderReflectsReviewBody(header http.Header, body string) bool {
	allowed := map[string]struct{}{
		"Cache-Control": {}, "Content-Type": {}, "Content-Length": {}, "Date": {},
		"Location": {}, "X-Idempotent-Replay": {}, "X-Roast-Revision-Sha256": {},
		"X-Review-Template-Version": {},
	}
	for name, values := range header {
		if _, ok := allowed[http.CanonicalHeaderKey(name)]; ok {
			continue
		}
		for _, value := range values {
			if len(value) >= 8 && strings.Contains(body, value) {
				return true
			}
		}
	}
	return false
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

func httpStatusOrZero(failure *output.Error) int {
	if failure == nil || failure.HTTPStatus == nil {
		return 0
	}
	return *failure.HTTPStatus
}
