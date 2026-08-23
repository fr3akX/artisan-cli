package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const maxRoastChartBytes = int64(64 << 20)

// RoastChartDownload describes a validated decompressed chart and both its
// compressed transfer and readable file identities.
type RoastChartDownload struct {
	Path               string `json:"path"`
	RoastUUID          string `json:"roast_uuid"`
	RevisionNumber     int64  `json:"revision_number"`
	RevisionSHA256     string `json:"revision_sha256"`
	ParserVersion      string `json:"parser_version"`
	ChartSchemaVersion int64  `json:"chart_schema_version"`
	CompressedBytes    int64  `json:"compressed_bytes"`
	CompressedSHA256   string `json:"compressed_sha256"`
	FileBytes          int64  `json:"file_bytes"`
	FileSHA256         string `json:"file_sha256"`
}

// DownloadRoastChart downloads the current parsed chart through a bounded
// compressed staging file and publishes exact JSON only while its revision is
// still current.
func (c *Client) DownloadRoastChart(ctx context.Context, rawRoastUUID, destination string, force bool) (result RoastChartDownload, failure *output.Error) {
	defer func() { failure = c.failureWithoutSecrets(failure) }()
	if ctx == nil {
		return result, localFailure("invalid_request", "Request context is required")
	}
	if ctx.Err() != nil {
		return result, contextOrNetworkFailure(ctx)
	}
	roastUUID, failure := NormalizeRoastUUID(rawRoastUUID)
	if failure != nil {
		return result, failure
	}

	target, err := newDownloadTarget(destination, force, c.downloadOps)
	if err != nil {
		return result, chartTargetFailure(err)
	}
	defer target.Abort()
	// A second held target provides a protected same-directory compressed
	// staging object. It is never published and is always aborted.
	compressedTarget, err := newDownloadTarget(destination, true, c.downloadOps)
	if err != nil {
		return result, chartStorageFailure("Unable to store the roast chart safely")
	}
	defer compressedTarget.Abort()

	before, failure := c.Roast(ctx, roastUUID)
	if failure != nil {
		return result, failure
	}
	if before.State != "parsed" || before.CurrentRevision == nil || before.CurrentRevision.ParseState != "parsed" {
		return result, chartUnavailableFailure()
	}
	revision := *before.CurrentRevision
	endpoint, err := c.endpointURL(roastAPIRoot+"/"+roastUUID+"/chart", nil)
	if err != nil {
		return result, localFailure("invalid_request", "A valid API path is required")
	}

	var compressedBytes int64
	var compressedSHA string
	var parserVersion string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return result, contextOrNetworkFailure(ctx)
		}
		if err := target.Reset(); err != nil {
			return result, chartStorageFailure("Unable to store the roast chart safely")
		}
		if err := compressedTarget.Reset(); err != nil {
			return result, chartStorageFailure("Unable to store the roast chart safely")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return result, localFailure("invalid_request", "The API request is invalid")
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("User-Agent", c.userAgent)
		// Setting this explicitly disables net/http's automatic decompression.
		request.Header.Set("Accept-Encoding", "gzip")
		response, err := c.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return result, contextOrNetworkFailure(ctx)
			}
			if attempt < maxAttempts-1 {
				if waitForRetry(ctx, attempt) == nil {
					continue
				}
				return result, contextOrNetworkFailure(ctx)
			}
			return result, networkFailure()
		}
		status := response.StatusCode
		if status >= 300 && status < 400 {
			_ = response.Body.Close()
			return result, redirectRefused(status)
		}
		if status != http.StatusOK {
			if responseHeaderContainsAny(response.Header, []string{c.token, c.serverURL.String()}) {
				_ = response.Body.Close()
				return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
			}
			body, oversized, readErr := readBoundedResponse(response.Body)
			if ctx.Err() != nil {
				return result, contextOrNetworkFailure(ctx)
			}
			if !oversized && responseRequiresJSON(body) && !trustedJSONContentType(response.Header) {
				return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
			}
			if isTransientStatus(status) && attempt < maxAttempts-1 && !oversized {
				if waitForRetry(ctx, attempt) == nil {
					continue
				}
				return result, contextOrNetworkFailure(ctx)
			}
			if readErr != nil || oversized || status < 400 || status >= 600 {
				return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
			}
			return result, classifyRoastAPIFailure(decodeAPIError(status, body, c.token, c.serverURL.String()), true)
		}

		expectedBytes, expectedSHA, responseParser, ok := validRoastChartHeaders(response, revision)
		if responseHeaderContainsAny(response.Header, []string{c.token, c.serverURL.String()}) || !ok {
			_ = response.Body.Close()
			return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
		}
		hasher := sha256.New()
		observed := &observedDownloadWriter{destination: compressedTarget.Writer()}
		compressedBytes, err = io.Copy(io.MultiWriter(observed, hasher), io.LimitReader(response.Body, maxRoastChartBytes+1))
		closeErr := response.Body.Close()
		if observed.err != nil {
			return result, chartStorageFailure("Unable to store the roast chart safely")
		}
		if err != nil || closeErr != nil {
			if ctx.Err() != nil {
				return result, contextOrNetworkFailure(ctx)
			}
			if attempt < maxAttempts-1 {
				if waitForRetry(ctx, attempt) == nil {
					continue
				}
				return result, contextOrNetworkFailure(ctx)
			}
			return result, networkFailure()
		}
		compressedSHA = hex.EncodeToString(hasher.Sum(nil))
		if compressedBytes != expectedBytes || compressedBytes > maxRoastChartBytes || compressedSHA != expectedSHA {
			return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
		}
		parserVersion = responseParser
		break
	}

	fileBytes, fileSHA, failure := decompressAndValidateChart(ctx, target, compressedTarget, compressedBytes, parserVersion)
	if failure != nil {
		return result, failure
	}
	// Release and remove compressed staging before the revision fence and final
	// publication. The final destination durability sync therefore also follows
	// any named compressed-stage cleanup in the same directory.
	if err := compressedTarget.abortOwned(); err != nil {
		return result, chartStorageFailure("Unable to clean up the compressed roast chart safely")
	}
	if ctx.Err() != nil {
		return result, contextOrNetworkFailure(ctx)
	}
	after, failure := c.Roast(ctx, roastUUID)
	if failure != nil {
		return result, failure
	}
	if after.State != "parsed" || after.CurrentRevision == nil ||
		after.CurrentRevision.ParseState != "parsed" ||
		after.CurrentRevision.RevisionNumber != revision.RevisionNumber ||
		after.CurrentRevision.SHA256 != revision.SHA256 {
		return result, roastRevisionChangedFailure()
	}

	installedResult := RoastChartDownload{
		Path: destination, RoastUUID: roastUUID,
		RevisionNumber: revision.RevisionNumber, RevisionSHA256: revision.SHA256,
		ParserVersion: parserVersion, ChartSchemaVersion: 1,
		CompressedBytes: compressedBytes, CompressedSHA256: compressedSHA,
		FileBytes: fileBytes, FileSHA256: fileSHA,
	}
	installed, err := target.InstallContext(ctx, force)
	if installed.Publication == publicationNone && ctx.Err() != nil {
		return result, contextOrNetworkFailure(ctx)
	}
	if installed.Visible() {
		if !installed.Durable() {
			return installedResult, chartStorageFailure("The roast chart is installed, but storage durability is uncertain")
		}
		if err != nil {
			return installedResult, chartStorageFailure("The roast chart is installed, but a local storage operation did not complete")
		}
		return installedResult, nil
	}
	if installed.Publication != publicationNone {
		return installedResult, chartStorageFailure("The roast chart may have been published, but its requested path identity is uncertain")
	}
	if errors.Is(err, os.ErrExist) {
		return result, destinationExistsFailure()
	}
	return result, chartStorageFailure("Unable to store the roast chart safely")
}

func validRoastChartHeaders(response *http.Response, revision RoastRevision) (int64, string, string, bool) {
	if response.Uncompressed || len(response.TransferEncoding) != 0 {
		return 0, "", "", false
	}
	contentType, ok := oneCanonicalHeader(response.Header, "Content-Type")
	if !ok || contentType != "application/json" {
		return 0, "", "", false
	}
	encoding, ok := oneCanonicalHeader(response.Header, "Content-Encoding")
	if !ok || encoding != "gzip" {
		return 0, "", "", false
	}
	lengthValue, ok := oneCanonicalHeader(response.Header, "Content-Length")
	if !ok || lengthValue == "0" || strings.HasPrefix(lengthValue, "+") || (len(lengthValue) > 1 && lengthValue[0] == '0') {
		return 0, "", "", false
	}
	length, err := strconv.ParseInt(lengthValue, 10, 64)
	if err != nil || length < 1 || length > maxRoastChartBytes || response.ContentLength != length {
		return 0, "", "", false
	}
	etag, ok := oneCanonicalHeader(response.Header, "ETag")
	if !ok || len(etag) != 66 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		return 0, "", "", false
	}
	checksum := etag[1 : len(etag)-1]
	if !canonicalSHA256.MatchString(checksum) {
		return 0, "", "", false
	}
	contentSHA, ok := oneCanonicalHeader(response.Header, "X-Content-SHA256")
	if !ok || contentSHA != checksum {
		return 0, "", "", false
	}
	checksumSHA, ok := oneCanonicalHeader(response.Header, "X-Checksum-SHA256")
	if !ok || checksumSHA != checksum {
		return 0, "", "", false
	}
	parserVersion, ok := oneCanonicalHeader(response.Header, "X-Parser-Version")
	if !ok || parserVersion != revision.ParserVersion || !validBoundedString(parserVersion, 1, 64, false) {
		return 0, "", "", false
	}
	schema, ok := oneCanonicalHeader(response.Header, "X-Chart-Schema-Version")
	if !ok || schema != "1" {
		return 0, "", "", false
	}
	return length, checksum, parserVersion, true
}

func decompressAndValidateChart(ctx context.Context, target, compressedTarget *downloadTarget, compressedBytes int64, parserVersion string) (int64, string, *output.Error) {
	if compressedBytes < 1 || compressedTarget.heldSourceFile() == nil {
		return 0, "", invalidServerResponse(http.StatusOK)
	}
	stageHasher := sha256.New()
	stage := io.TeeReader(io.NewSectionReader(compressedTarget.heldSourceFile(), 0, compressedBytes), stageHasher)
	source := bufio.NewReader(stage)
	reader, err := gzip.NewReader(source)
	if err != nil {
		return 0, "", invalidServerResponse(http.StatusOK)
	}
	reader.Multistream(false)
	hasher := sha256.New()
	observed := &observedDownloadWriter{destination: target.Writer()}
	fileBytes, copyErr := io.Copy(io.MultiWriter(observed, hasher), io.LimitReader(contextBoundReader{ctx: ctx, reader: reader}, maxRoastChartBytes+1))
	closeErr := reader.Close()
	if observed.err != nil {
		return 0, "", chartStorageFailure("Unable to store the roast chart safely")
	}
	if ctx.Err() != nil {
		return 0, "", contextOrNetworkFailure(ctx)
	}
	if copyErr != nil || closeErr != nil || fileBytes > maxRoastChartBytes {
		return 0, "", invalidServerResponse(http.StatusOK)
	}
	if _, err := source.ReadByte(); !errors.Is(err, io.EOF) {
		return 0, "", invalidServerResponse(http.StatusOK)
	}
	var stageDigest [sha256.Size]byte
	copy(stageDigest[:], stageHasher.Sum(nil))
	if compressedTarget.observer == nil || compressedTarget.observer.count != compressedBytes || stageDigest != compressedTarget.observer.digest() {
		return 0, "", invalidServerResponse(http.StatusOK)
	}
	contents, err := io.ReadAll(io.LimitReader(contextBoundReader{ctx: ctx, reader: io.NewSectionReader(target.heldSourceFile(), 0, fileBytes+1)}, maxRoastChartBytes+1))
	if ctx.Err() != nil {
		return 0, "", contextOrNetworkFailure(ctx)
	}
	if err != nil {
		return 0, "", chartStorageFailure("Unable to store the roast chart safely")
	}
	if int64(len(contents)) != fileBytes || !validateRoastChartJSON(contents, parserVersion) {
		return 0, "", invalidServerResponse(http.StatusOK)
	}
	return fileBytes, hex.EncodeToString(hasher.Sum(nil)), nil
}

type contextBoundReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextBoundReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func validateRoastChartJSON(data []byte, parserVersion string) bool {
	if len(data) == 0 || !utf8.Valid(data) || validateJSONStringSurrogateEscapes(data) != nil || rejectDuplicateJSONKeys(data) != nil {
		return false
	}
	root, ok := decodeChartObject(data)
	if !ok {
		return false
	}
	control, ok := requiredChartObject(root, "control")
	if !ok || !requiredChartArray(control, "markers") || !requiredChartArray(control, "steps") {
		return false
	}
	core, ok := requiredChartObject(root, "core")
	if !ok {
		return false
	}
	bt, ok := requiredChartNumberArray(core, "bt", true)
	if !ok {
		return false
	}
	btRoR, ok := requiredChartNumberArray(core, "bt_ror", true)
	if !ok {
		return false
	}
	et, ok := requiredChartNumberArray(core, "et", true)
	if !ok {
		return false
	}
	etRoR, ok := requiredChartNumberArray(core, "et_ror", true)
	if !ok {
		return false
	}
	times, ok := requiredChartNumberArray(core, "time_seconds", false)
	if !ok {
		return false
	}
	events, ok := requiredChartObject(root, "events")
	if !ok || !requiredChartArray(events, "milestones") {
		return false
	}
	special, ok := requiredChartRawArray(events, "special")
	if !ok {
		return false
	}
	extra, ok := requiredChartObject(root, "extra")
	if !ok {
		return false
	}
	series, ok := requiredChartRawArray(extra, "series")
	if !ok {
		return false
	}
	summary, ok := requiredChartObject(root, "summary")
	if !ok {
		return false
	}
	if raw, exists := summary["duration_seconds"]; !exists || !validChartNumber(raw, false) {
		return false
	}
	sampleCount, ok := requiredChartCount(summary, "sample_count")
	if !ok {
		return false
	}
	extraCount, ok := requiredChartCount(summary, "extra_series_count")
	if !ok {
		return false
	}
	specialCount, ok := requiredChartCount(summary, "special_event_count")
	if !ok {
		return false
	}
	if sampleCount != len(times) || len(bt) != sampleCount || len(btRoR) != sampleCount || len(et) != sampleCount || len(etRoR) != sampleCount || extraCount != len(series) || specialCount != len(special) {
		return false
	}
	var bodyParser string
	if raw, exists := root["parser_version"]; !exists || decodeOneJSON(raw, &bodyParser) != nil || bodyParser != parserVersion {
		return false
	}
	var schema json.Number
	if raw, exists := root["schema_version"]; !exists || !isJSONNumberToken(raw) || decodeChartScalar(raw, &schema) != nil || schema.String() != "1" {
		return false
	}
	unitRaw, exists := root["source_temperature_unit"]
	if !exists {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(unitRaw), []byte("null")) {
		return true
	}
	var unit string
	return decodeOneJSON(unitRaw, &unit) == nil && (unit == "C" || unit == "F")
}

func decodeChartObject(data []byte) (map[string]json.RawMessage, bool) {
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := decodeChartScalar(data, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func decodeChartScalar(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func requiredChartObject(parent map[string]json.RawMessage, name string) (map[string]json.RawMessage, bool) {
	raw, exists := parent[name]
	if !exists {
		return nil, false
	}
	return decodeChartObject(raw)
}

func requiredChartRawArray(parent map[string]json.RawMessage, name string) ([]json.RawMessage, bool) {
	raw, exists := parent[name]
	if !exists || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		return nil, false
	}
	var values []json.RawMessage
	if decodeChartScalar(raw, &values) != nil || values == nil {
		return nil, false
	}
	return values, true
}

func requiredChartArray(parent map[string]json.RawMessage, name string) bool {
	_, ok := requiredChartRawArray(parent, name)
	return ok
}

func requiredChartNumberArray(parent map[string]json.RawMessage, name string, nullable bool) ([]json.RawMessage, bool) {
	values, ok := requiredChartRawArray(parent, name)
	if !ok {
		return nil, false
	}
	for _, value := range values {
		if !validChartNumber(value, nullable) {
			return nil, false
		}
	}
	return values, true
}

func validChartNumber(raw json.RawMessage, nullable bool) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nullable
	}
	if !isJSONNumberToken(raw) {
		return false
	}
	var number json.Number
	if decodeChartScalar(raw, &number) != nil {
		return false
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	return err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func isJSONNumberToken(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) != 0 && (trimmed[0] == '-' || trimmed[0] >= '0' && trimmed[0] <= '9')
}

func requiredChartCount(parent map[string]json.RawMessage, name string) (int, bool) {
	raw, exists := parent[name]
	if !exists {
		return 0, false
	}
	if !isJSONNumberToken(raw) {
		return 0, false
	}
	var number json.Number
	if decodeChartScalar(raw, &number) != nil {
		return 0, false
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || value < 0 || value > int64(maxRoastChartBytes) {
		return 0, false
	}
	return int(value), true
}

func chartTargetFailure(err error) *output.Error {
	if errors.Is(err, errInvalidDownloadDestination) {
		return &output.Error{ExitCode: 2, Code: "invalid_destination", Message: "Roast chart download requires a destination file path"}
	}
	if errors.Is(err, os.ErrExist) {
		return destinationExistsFailure()
	}
	return chartStorageFailure("Unable to store the roast chart safely")
}

func chartStorageFailure(message string) *output.Error {
	return &output.Error{ExitCode: 3, Code: "local_storage_error", Message: message}
}

func chartUnavailableFailure() *output.Error {
	return &output.Error{ExitCode: 7, Code: "chart_unavailable", Message: "Roast chart is unavailable because the current revision is not parsed"}
}

func roastRevisionChangedFailure() *output.Error {
	return &output.Error{ExitCode: 7, Code: "roast_revision_changed", Message: "The roast revision changed before the chart download could be installed"}
}
