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
	ReviewTemplateVersion              = "artisan-roast-review-v1"
	maxRoastReviewBytes                = 16_000
	maxRoastReviewRunes                = 4_000
	maxRoastReviewReflectionFields     = 13
	maxRoastReviewReflectionFieldBytes = 1_024
	maxRoastReviewReflectionSegments   = 8
	maxRoastReviewReconstructionStates = 1 << 20
	maxRoastReviewTraceparentBytes     = 512
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

// PostRoastReview preflights the current revision identity and posts one
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
	if current.CurrentRevision == nil {
		return result, reviewRevisionChangedFailure()
	}
	requestedIsCurrent := current.CurrentRevision.RevisionNumber == revisionNumber &&
		current.CurrentRevision.SHA256 == request.RevisionSHA256
	oldSlot := !requestedIsCurrent
	if !requestedIsCurrent {
		if revisionNumber >= current.CurrentRevision.RevisionNumber {
			return result, reviewRevisionChangedFailure()
		}
		revision, lookupFailure := c.findRoastRevision(ctx, roastUUID, revisionNumber)
		if lookupFailure != nil {
			if lookupFailure.Code == "not_found" && lookupFailure.HTTPStatus == nil {
				return result, reviewRevisionChangedFailure()
			}
			return result, lookupFailure
		}
		if revision.SHA256 != request.RevisionSHA256 {
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
	var responseMetadata []string
	validator := func(status int, header http.Header) *output.Error {
		if status != http.StatusCreated {
			return invalidServerResponse(status)
		}
		var valid bool
		replay, locationCommentUUID, responseMetadata, valid = validateRoastReviewSuccessHeaders(
			header, roastUUID, request.RevisionSHA256, request.TemplateVersion, request.Body,
			key, c.token, c.serverURL.String(),
		)
		if !valid || oldSlot && !replay {
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
	if roastReviewCommentReflectsForbidden(comment, responseMetadata, request.Body, key, c.token, c.serverURL.String()) || comment.RoastUUID != roastUUID || comment.CommentUUID != locationCommentUUID || (!replay && (comment.IsDeleted || comment.Body == nil || *comment.Body != request.Body)) {
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

// Review responses allow only exact endpoint contract metadata, net/http
// framing metadata, and a bounded set of ubiquitous proxy/trace metadata.
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

var roastReviewOptionalHeaderNames = []string{
	"Content-Length",
	"Date",
	"Server",
	"Via",
	"X-Request-ID",
	"Traceparent",
	"Tracestate",
}

var roastReviewRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:/+=-]+$`)

func validateRoastReviewSuccessHeaders(header http.Header, roastUUID, revisionSHA, template, body string, forbiddenValues ...string) (bool, string, []string, bool) {
	if !singleHeaderEquals(header, "Cache-Control", "no-store") ||
		!singleHeaderEquals(header, "X-Roast-Revision-SHA256", revisionSHA) ||
		!singleHeaderEquals(header, "X-Review-Template-Version", template) ||
		!singleHeaderEquals(header, "Content-Type", "application/json") &&
			!singleHeaderEquals(header, "Content-Type", "application/json; charset=utf-8") {
		return false, "", nil, false
	}

	replayValues := header.Values("X-Idempotent-Replay")
	if len(replayValues) != 1 || replayValues[0] != "true" && replayValues[0] != "false" {
		return false, "", nil, false
	}
	replay := replayValues[0] == "true"

	locationValues := header.Values("Location")
	prefix := roastAPIRoot + "/" + roastUUID + "/comments/"
	if len(locationValues) != 1 || !strings.HasPrefix(locationValues[0], prefix) {
		return false, "", nil, false
	}
	locationCommentUUID := strings.TrimPrefix(locationValues[0], prefix)
	if !validRoastUUID(locationCommentUUID) || locationValues[0] != prefix+locationCommentUUID {
		return false, "", nil, false
	}

	for name, values := range header {
		if _, allowed := roastReviewSuccessHeaderNames[strings.ToLower(name)]; !allowed || len(values) == 0 || containsCaseInsensitive(name, forbiddenValues) {
			return false, "", nil, false
		}
	}
	for _, name := range []string{
		"Cache-Control", "Location", "X-Idempotent-Replay", "X-Roast-Revision-SHA256",
		"X-Review-Template-Version", "Content-Type",
	} {
		if containsAny(header.Values(name)[0], forbiddenValues) {
			return false, "", nil, false
		}
	}

	metadata := make([]string, 0, len(roastReviewOptionalHeaderNames))
	for _, name := range roastReviewOptionalHeaderNames {
		values := header.Values(name)
		if len(values) > 1 {
			return false, "", nil, false
		}
		if len(values) == 0 {
			continue
		}
		value := values[0]
		if containsAny(value, forbiddenValues) || !validRoastReviewOptionalHeader(name, value) {
			return false, "", nil, false
		}
		metadata = append(metadata, value)
	}
	if roastReviewFieldsReflectSensitiveData(metadata, body, forbiddenValues...) {
		return false, "", nil, false
	}
	return replay, locationCommentUUID, metadata, true
}

func validRoastReviewOptionalHeader(name, value string) bool {
	switch name {
	case "Content-Length":
		parsed, err := strconv.ParseUint(value, 10, 63)
		return err == nil && strconv.FormatUint(parsed, 10) == value && parsed <= maxResponseBodyBytes
	case "Date":
		_, err := http.ParseTime(value)
		return len(value) <= 64 && err == nil
	case "Server":
		return validVisibleRoastReviewMetadata(value, 256)
	case "Via":
		if !validVisibleRoastReviewMetadata(value, 1024) {
			return false
		}
		for _, entry := range strings.Split(value, ",") {
			if len(strings.Fields(entry)) < 2 {
				return false
			}
		}
		return true
	case "X-Request-ID":
		return len(value) >= 1 && len(value) <= 256 && roastReviewRequestIDPattern.MatchString(value)
	case "Traceparent":
		return validTraceparent(value)
	case "Tracestate":
		return validTracestate(value)
	default:
		return false
	}
}

func validVisibleRoastReviewMetadata(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func validTraceparent(value string) bool {
	if len(value) < 55 || len(value) > maxRoastReviewTraceparentBytes ||
		value[2] != '-' || value[35] != '-' || value[52] != '-' || value[:2] == "ff" {
		return false
	}
	for index := 0; index < 55; index++ {
		if index == 2 || index == 35 || index == 52 {
			continue
		}
		if !isLowerHexByte(value[index]) {
			return false
		}
	}
	if value[3:35] == strings.Repeat("0", 32) || value[36:52] == strings.Repeat("0", 16) {
		return false
	}
	if value[:2] == "00" {
		return len(value) == 55
	}
	if len(value) == 55 {
		return true
	}
	if value[55] != '-' {
		return false
	}
	for index := 55; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func isLowerHexByte(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
}

func validTracestate(value string) bool {
	if len(value) == 0 || len(value) > 512 {
		return false
	}
	members := strings.Split(value, ",")
	if len(members) > 32 {
		return false
	}
	keys := make(map[string]struct{}, len(members))
	for index, rawMember := range members {
		member := rawMember
		if index == 0 {
			if len(member) > 0 && isW3COWS(member[0]) {
				return false
			}
		} else {
			member = trimLeftW3COWS(member)
		}
		if index == len(members)-1 {
			if len(member) > 0 && isW3COWS(member[len(member)-1]) {
				return false
			}
		} else {
			member = trimRightW3COWS(member)
		}
		if strings.Count(member, "=") != 1 {
			return false
		}
		parts := strings.SplitN(member, "=", 2)
		key := trimRightW3COWS(parts[0])
		memberValue := trimLeftW3COWS(parts[1])
		if !validTracestateKey(key) || !validTracestateValue(memberValue) {
			return false
		}
		if _, duplicate := keys[key]; duplicate {
			return false
		}
		keys[key] = struct{}{}
	}
	return true
}

func isW3COWS(value byte) bool {
	return value == ' ' || value == '\t'
}

func trimLeftW3COWS(value string) string {
	for len(value) > 0 && isW3COWS(value[0]) {
		value = value[1:]
	}
	return value
}

func trimRightW3COWS(value string) string {
	for len(value) > 0 && isW3COWS(value[len(value)-1]) {
		value = value[:len(value)-1]
	}
	return value
}

func validTracestateKey(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	parts := strings.Split(value, "@")
	switch len(parts) {
	case 1:
		return len(parts[0]) <= 256 && validTracestateKeyPart(parts[0], false, 256)
	case 2:
		return validTracestateKeyPart(parts[0], true, 241) &&
			validTracestateKeyPart(parts[1], false, 14)
	default:
		return false
	}
}

func validTracestateKeyPart(value string, digitFirst bool, maximum int) bool {
	if len(value) == 0 || len(value) > maximum ||
		(value[0] < 'a' || value[0] > 'z') && (!digitFirst || value[0] < '0' || value[0] > '9') {
		return false
	}
	for _, character := range value[1:] {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune("_*-/", character) {
			continue
		}
		return false
	}
	return true
}

func validTracestateValue(value string) bool {
	if len(value) == 0 || len(value) > 256 || value[0] == ' ' || value[len(value)-1] == ' ' {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e || character == ',' || character == '=' {
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

func roastReviewFieldsReflectSensitiveData(fields []string, body string, forbiddenValues ...string) bool {
	const minimumBodyWindow = 8
	if len(fields) > maxRoastReviewReflectionFields {
		return true
	}
	for _, field := range fields {
		if len(field) > maxRoastReviewReflectionFieldBytes || containsAny(field, forbiddenValues) {
			return true
		}
	}
	for _, forbidden := range forbiddenValues {
		if forbidden != "" && roastReviewTargetReconstructed(forbidden, fields, len(fields)) {
			return true
		}
	}
	if len(body) < minimumBodyWindow {
		return false
	}

	bodyPrefixes := make([]map[string]struct{}, minimumBodyWindow+1)
	for length := 1; length <= minimumBodyWindow; length++ {
		bodyPrefixes[length] = make(map[string]struct{}, len(body)-minimumBodyWindow+1)
	}
	for start := 0; start+minimumBodyWindow <= len(body); start++ {
		window := body[start : start+minimumBodyWindow]
		for length := 1; length <= minimumBodyWindow; length++ {
			bodyPrefixes[length][window[:length]] = struct{}{}
		}
	}
	for _, field := range fields {
		for start := 0; start+minimumBodyWindow <= len(field); start++ {
			if _, exists := bodyPrefixes[minimumBodyWindow][field[start:start+minimumBodyWindow]]; exists {
				return true
			}
		}
	}

	type reconstructionState struct {
		mask  uint16
		value string
	}
	visited := make(map[reconstructionState]struct{})
	states := 0
	var reconstructs func(mask uint16, value string, segments int) bool
	reconstructs = func(mask uint16, value string, segments int) bool {
		if len(value) == minimumBodyWindow {
			return true
		}
		if segments == maxRoastReviewReflectionSegments {
			return false
		}
		state := reconstructionState{mask: mask, value: value}
		if _, exists := visited[state]; exists {
			return false
		}
		visited[state] = struct{}{}
		states++
		if states > maxRoastReviewReconstructionStates {
			return true
		}
		for index, field := range fields {
			bit := uint16(1) << index
			if mask&bit != 0 || len(field) == 0 {
				continue
			}
			if value == "" {
				first := len(field) - minimumBodyWindow
				if first < 0 {
					first = 0
				}
				for start := first; start < len(field); start++ {
					candidate := field[start:]
					if _, exists := bodyPrefixes[len(candidate)][candidate]; exists && reconstructs(mask|bit, candidate, segments+1) {
						return true
					}
				}
				continue
			}
			remaining := minimumBodyWindow - len(value)
			piece := field
			if len(piece) > remaining {
				piece = piece[:remaining]
			}
			candidate := value + piece
			if _, exists := bodyPrefixes[len(candidate)][candidate]; exists && reconstructs(mask|bit, candidate, segments+1) {
				return true
			}
		}
		return false
	}
	return reconstructs(0, "", 0)
}

func roastReviewTargetReconstructed(target string, fields []string, maximumSegments int) bool {
	reconstructed, _, _ := roastReviewTargetReconstructionWithinBudget(
		target, fields, maximumSegments, maxRoastReviewReconstructionStates,
	)
	return reconstructed
}

// roastReviewTargetReconstructionWithinBudget returns a conservative match on
// budget exhaustion so response validation fails closed instead of continuing
// combinatorial reconstruction.
func roastReviewTargetReconstructionWithinBudget(target string, fields []string, maximumSegments, maximumStates int) (reconstructed bool, states int, exhausted bool) {
	type reconstructionState struct {
		mask   uint16
		offset int
	}
	visited := make(map[reconstructionState]struct{})
	var reconstructs func(mask uint16, offset, segments int) bool
	reconstructs = func(mask uint16, offset, segments int) bool {
		if offset == len(target) {
			return segments > 1
		}
		if segments == maximumSegments {
			return false
		}
		state := reconstructionState{mask: mask, offset: offset}
		if _, exists := visited[state]; exists {
			return false
		}
		visited[state] = struct{}{}
		states++
		if states > maximumStates {
			exhausted = true
			return true
		}
		for index, field := range fields {
			bit := uint16(1) << index
			if mask&bit != 0 || field == "" {
				continue
			}
			if offset == 0 {
				first := len(field) - len(target)
				if first < 0 {
					first = 0
				}
				for start := first; start < len(field); start++ {
					piece := field[start:]
					if strings.HasPrefix(target, piece) && reconstructs(mask|bit, len(piece), segments+1) {
						return true
					}
				}
				continue
			}
			remaining := len(target) - offset
			piece := field
			if len(piece) > remaining {
				piece = piece[:remaining]
			}
			if strings.HasPrefix(target[offset:], piece) && reconstructs(mask|bit, offset+len(piece), segments+1) {
				return true
			}
		}
		return false
	}
	reconstructed = reconstructs(0, 0, 0)
	return reconstructed, states, exhausted
}

func roastReviewCommentReflectsForbidden(comment CommentView, responseMetadata []string, requestBody, key, token, serverURL string) bool {
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
	combinedMetadata := append(append([]string(nil), responseMetadata...), bodyIndependentFields...)
	if roastReviewFieldsReflectSensitiveData(combinedMetadata, requestBody, forbidden...) {
		return true
	}
	if comment.Body == nil {
		return false
	}
	if containsAny(*comment.Body, forbidden) {
		return true
	}
	forbiddenReconstructionFields := append(append([]string(nil), combinedMetadata...), *comment.Body)
	if len(forbiddenReconstructionFields) > maxRoastReviewReflectionFields+1 {
		return true
	}
	for _, target := range forbidden {
		if target != "" && roastReviewTargetReconstructed(target, forbiddenReconstructionFields, len(forbiddenReconstructionFields)) {
			return true
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
	case "chart_unavailable":
		sanitized.Message = chartUnavailableFailure().Message
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
		return code == "roast_revision_changed" || code == "review_idempotency_conflict" || code == "chart_unavailable"
	case http.StatusUnprocessableEntity:
		return code == "invalid_review"
	default:
		return false
	}
}
