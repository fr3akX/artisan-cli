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

	fileBytes, fileSHA, failure := decompressAndValidateChart(ctx, target, compressedTarget, compressedBytes, parserVersion, []string{c.token, c.serverURL.String()})
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

	installedResult := RoastChartDownload{
		Path: target.destination, RoastUUID: roastUUID,
		RevisionNumber: revision.RevisionNumber, RevisionSHA256: revision.SHA256,
		ParserVersion: parserVersion, ChartSchemaVersion: 1,
		CompressedBytes: compressedBytes, CompressedSHA256: compressedSHA,
		FileBytes: fileBytes, FileSHA256: fileSHA,
	}
	installed, err := target.InstallContextBeforeNative(ctx, force, func() error {
		current, currentFailure := c.Roast(ctx, roastUUID)
		if currentFailure != nil {
			return &chartFenceCallbackError{failure: currentFailure}
		}
		if !sameRoastChartRevision(current, revision, parserVersion) {
			return &chartFenceCallbackError{failure: roastRevisionChangedFailure()}
		}
		return nil
	})
	var fenceErr *chartFenceCallbackError
	if errors.As(err, &fenceErr) {
		if installed.CleanupUncertain {
			return result, chartStorageFailure("Unable to clean up the roast chart safely")
		}
		return result, fenceErr.failure
	}
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

func decompressAndValidateChart(ctx context.Context, target, compressedTarget *downloadTarget, compressedBytes int64, parserVersion string, forbidden []string) (int64, string, *output.Error) {
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
	if err := validateRoastChartFile(ctx, target.heldSourceFile(), fileBytes, parserVersion, forbidden); err != nil {
		if ctx.Err() != nil {
			return 0, "", contextOrNetworkFailure(ctx)
		}
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

const (
	// These ceilings bound adversarial active object-key state while leaving
	// ample room for ordinary future schema fields inside the 64 MiB document.
	maxRoastChartJSONDepth      = 64
	maxRoastChartObjectKeys     = 100_000
	maxRoastChartObjectKeyBytes = 4 << 20
	// Schema-v1 scalar tokens are small values, identifiers, and labels. One MiB
	// leaves ample room for future data while preventing document-sized token
	// allocations in encoding/json.
	maxRoastChartTokenBytes = 1 << 20
)

var errInvalidRoastChartJSON = errors.New("invalid roast chart JSON")

type roastChartShape struct {
	control, core, events, extra, parser, schema, unit, summary bool
	bt, btRoR, et, etRoR, times, special, series                int
	sampleCount, extraCount, specialCount                       int
}

func validateRoastChartFile(ctx context.Context, file *os.File, fileBytes int64, parserVersion string, forbidden []string) error {
	if file == nil || fileBytes < 1 || fileBytes > maxRoastChartBytes {
		return errInvalidRoastChartJSON
	}
	reflected, err := preScanRoastChartTokens(contextBoundReader{ctx: ctx, reader: io.NewSectionReader(file, 0, fileBytes)}, forbidden)
	if err != nil {
		return err
	}
	if reflected {
		return errInvalidRoastChartJSON
	}
	// The lexical pass capped every contiguous non-string scalar and every raw
	// and decoded string before encoding/json can materialize a token. Decode
	// the unchanged held bytes so malformed separators or split syntax remain
	// the decoder's responsibility.
	decoder := json.NewDecoder(contextBoundReader{ctx: ctx, reader: io.NewSectionReader(file, 0, fileBytes)})
	decoder.UseNumber()
	if err := validateRoastChartTokens(decoder, parserVersion); err != nil {
		return err
	}
	return nil
}

func preScanRoastChartTokens(reader io.Reader, forbidden []string) (bool, error) {
	buffered := bufio.NewReaderSize(reader, 32<<10)
	patterns := make([][]byte, 0, len(forbidden))
	for _, value := range forbidden {
		if value != "" {
			patterns = append(patterns, []byte(value))
		}
	}
	decoded := make([]byte, 0, 32<<10)
	scalarBytes := 0
	for {
		value, err := buffered.ReadByte()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if value == '"' {
			scalarBytes = 0
			var reflected bool
			decoded, reflected, err = scanRoastChartJSONString(buffered, decoded[:0], patterns)
			if err != nil || reflected {
				return reflected, err
			}
			continue
		}
		if isRoastChartScalarSeparator(value) {
			scalarBytes = 0
			continue
		}
		scalarBytes++
		if scalarBytes > maxRoastChartTokenBytes {
			return false, errInvalidRoastChartJSON
		}
		if value >= 0x80 {
			if err := buffered.UnreadByte(); err != nil {
				return false, err
			}
			runeValue, size, err := buffered.ReadRune()
			if err != nil || runeValue == '\uFFFD' && size == 1 {
				return false, errInvalidRoastChartJSON
			}
			scalarBytes += size - 1
			if scalarBytes > maxRoastChartTokenBytes {
				return false, errInvalidRoastChartJSON
			}
		}
	}
}

func isRoastChartScalarSeparator(value byte) bool {
	switch value {
	case ' ', '\t', '\r', '\n', '{', '}', '[', ']', ',', ':':
		return true
	default:
		return false
	}
}

func scanRoastChartJSONString(reader *bufio.Reader, decoded []byte, forbidden [][]byte) ([]byte, bool, error) {
	rawBytes := 0
	appendDecoded := func(value ...byte) bool {
		decoded = append(decoded, value...)
		return len(decoded) <= maxRoastChartTokenBytes
	}
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return decoded, false, errInvalidRoastChartJSON
		}
		if value == '"' {
			for _, pattern := range forbidden {
				if bytes.Contains(decoded, pattern) {
					return decoded, true, nil
				}
			}
			return decoded, false, nil
		}
		rawBytes++
		if rawBytes > maxRoastChartTokenBytes {
			return decoded, false, errInvalidRoastChartJSON
		}
		if value >= 0x80 {
			if err := reader.UnreadByte(); err != nil {
				return decoded, false, err
			}
			runeValue, size, err := reader.ReadRune()
			if err != nil || runeValue == '\uFFFD' && size == 1 {
				return decoded, false, errInvalidRoastChartJSON
			}
			rawBytes += size - 1
			if rawBytes > maxRoastChartTokenBytes {
				return decoded, false, errInvalidRoastChartJSON
			}
			decoded = utf8.AppendRune(decoded, runeValue)
			if len(decoded) > maxRoastChartTokenBytes {
				return decoded, false, errInvalidRoastChartJSON
			}
			continue
		}
		if value != '\\' {
			if value < 0x20 || !appendDecoded(value) {
				return decoded, false, errInvalidRoastChartJSON
			}
			continue
		}

		escape, err := reader.ReadByte()
		rawBytes++
		if err != nil || rawBytes > maxRoastChartTokenBytes {
			return decoded, false, errInvalidRoastChartJSON
		}
		switch escape {
		case '"', '\\', '/':
			if !appendDecoded(escape) {
				return decoded, false, errInvalidRoastChartJSON
			}
		case 'b':
			if !appendDecoded('\b') {
				return decoded, false, errInvalidRoastChartJSON
			}
		case 'f':
			if !appendDecoded('\f') {
				return decoded, false, errInvalidRoastChartJSON
			}
		case 'n':
			if !appendDecoded('\n') {
				return decoded, false, errInvalidRoastChartJSON
			}
		case 'r':
			if !appendDecoded('\r') {
				return decoded, false, errInvalidRoastChartJSON
			}
		case 't':
			if !appendDecoded('\t') {
				return decoded, false, errInvalidRoastChartJSON
			}
		case 'u':
			code, err := readRoastChartHexEscape(reader)
			rawBytes += 4
			if err != nil || rawBytes > maxRoastChartTokenBytes || code >= 0xDC00 && code <= 0xDFFF {
				return decoded, false, errInvalidRoastChartJSON
			}
			runeValue := rune(code)
			if code >= 0xD800 && code <= 0xDBFF {
				backslash, firstErr := reader.ReadByte()
				u, secondErr := reader.ReadByte()
				low, thirdErr := readRoastChartHexEscape(reader)
				rawBytes += 6
				if firstErr != nil || secondErr != nil || thirdErr != nil || rawBytes > maxRoastChartTokenBytes || backslash != '\\' || u != 'u' || low < 0xDC00 || low > 0xDFFF {
					return decoded, false, errInvalidRoastChartJSON
				}
				runeValue = 0x10000 + (rune(code)-0xD800)<<10 + rune(low) - 0xDC00
			}
			decoded = utf8.AppendRune(decoded, runeValue)
			if len(decoded) > maxRoastChartTokenBytes {
				return decoded, false, errInvalidRoastChartJSON
			}
		default:
			return decoded, false, errInvalidRoastChartJSON
		}
	}
}

func readRoastChartHexEscape(reader *bufio.Reader) (uint16, error) {
	var value uint16
	for index := 0; index < 4; index++ {
		digit, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, errInvalidRoastChartJSON
		}
	}
	return value, nil
}

func validateRoastChartTokens(decoder *json.Decoder, parserVersion string) error {
	root, err := decoder.Token()
	if err != nil || root != json.Delim('{') {
		return errInvalidRoastChartJSON
	}
	shape := roastChartShape{}
	err = consumeRoastChartObject(decoder, 1, func(name string) error {
		switch name {
		case "control":
			shape.control = true
			return validateRoastChartControl(decoder, 2)
		case "core":
			shape.core = true
			return validateRoastChartCore(decoder, 2, &shape)
		case "events":
			shape.events = true
			return validateRoastChartEvents(decoder, 2, &shape)
		case "extra":
			shape.extra = true
			return validateRoastChartExtra(decoder, 2, &shape)
		case "summary":
			shape.summary = true
			return validateRoastChartSummary(decoder, 2, &shape)
		case "parser_version":
			shape.parser = true
			value, err := decoder.Token()
			if err != nil || value != parserVersion {
				return errInvalidRoastChartJSON
			}
			return nil
		case "schema_version":
			shape.schema = true
			value, err := decoder.Token()
			number, ok := value.(json.Number)
			if err != nil || !ok || number.String() != "1" {
				return errInvalidRoastChartJSON
			}
			return nil
		case "source_temperature_unit":
			shape.unit = true
			value, err := decoder.Token()
			if err != nil || value != nil && value != "C" && value != "F" {
				return errInvalidRoastChartJSON
			}
			return nil
		default:
			return skipRoastChartValue(decoder, 2)
		}
	})
	if err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errInvalidRoastChartJSON
	}
	if !shape.control || !shape.core || !shape.events || !shape.extra || !shape.summary || !shape.parser || !shape.schema || !shape.unit ||
		shape.sampleCount != shape.times || shape.bt != shape.sampleCount || shape.btRoR != shape.sampleCount ||
		shape.et != shape.sampleCount || shape.etRoR != shape.sampleCount || shape.extraCount != shape.series || shape.specialCount != shape.special {
		return errInvalidRoastChartJSON
	}
	return nil
}

func consumeRoastChartObject(decoder *json.Decoder, depth int, consume func(string) error) error {
	if depth > maxRoastChartJSONDepth {
		return errInvalidRoastChartJSON
	}
	seen := make(map[string]struct{})
	keyBytes := 0
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return errInvalidRoastChartJSON
		}
		keyBytes += len(name)
		if len(seen) >= maxRoastChartObjectKeys || keyBytes > maxRoastChartObjectKeyBytes {
			return errInvalidRoastChartJSON
		}
		if _, duplicate := seen[name]; duplicate {
			return errInvalidRoastChartJSON
		}
		seen[name] = struct{}{}
		if err := consume(name); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errInvalidRoastChartJSON
	}
	return nil
}

func requireRoastChartObject(decoder *json.Decoder, depth int, consume func(string) error) error {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errInvalidRoastChartJSON
	}
	return consumeRoastChartObject(decoder, depth, consume)
}

func validateRoastChartControl(decoder *json.Decoder, depth int) error {
	markers, steps := false, false
	err := requireRoastChartObject(decoder, depth, func(name string) error {
		switch name {
		case "markers":
			markers = true
			_, err := roastChartObjectArray(decoder, depth+1)
			return err
		case "steps":
			steps = true
			_, err := roastChartObjectArray(decoder, depth+1)
			return err
		default:
			return skipRoastChartValue(decoder, depth+1)
		}
	})
	if err != nil || !markers || !steps {
		return errInvalidRoastChartJSON
	}
	return nil
}

func validateRoastChartCore(decoder *json.Decoder, depth int, shape *roastChartShape) error {
	bt, btRoR, et, etRoR, times := false, false, false, false, false
	err := requireRoastChartObject(decoder, depth, func(name string) error {
		var destination *int
		nullable := true
		switch name {
		case "bt":
			bt, destination = true, &shape.bt
		case "bt_ror":
			btRoR, destination = true, &shape.btRoR
		case "et":
			et, destination = true, &shape.et
		case "et_ror":
			etRoR, destination = true, &shape.etRoR
		case "time_seconds":
			times, destination, nullable = true, &shape.times, false
		default:
			return skipRoastChartValue(decoder, depth+1)
		}
		count, err := roastChartNumberArray(decoder, nullable)
		if err == nil {
			*destination = count
		}
		return err
	})
	if err != nil || !bt || !btRoR || !et || !etRoR || !times {
		return errInvalidRoastChartJSON
	}
	return nil
}

func validateRoastChartEvents(decoder *json.Decoder, depth int, shape *roastChartShape) error {
	milestones, special := false, false
	err := requireRoastChartObject(decoder, depth, func(name string) error {
		switch name {
		case "milestones":
			milestones = true
			_, err := roastChartObjectArray(decoder, depth+1)
			return err
		case "special":
			special = true
			count, err := roastChartObjectArray(decoder, depth+1)
			shape.special = count
			return err
		default:
			return skipRoastChartValue(decoder, depth+1)
		}
	})
	if err != nil || !milestones || !special {
		return errInvalidRoastChartJSON
	}
	return nil
}

func validateRoastChartExtra(decoder *json.Decoder, depth int, shape *roastChartShape) error {
	series := false
	err := requireRoastChartObject(decoder, depth, func(name string) error {
		if name != "series" {
			return skipRoastChartValue(decoder, depth+1)
		}
		series = true
		count, err := roastChartObjectArray(decoder, depth+1)
		shape.series = count
		return err
	})
	if err != nil || !series {
		return errInvalidRoastChartJSON
	}
	return nil
}

func validateRoastChartSummary(decoder *json.Decoder, depth int, shape *roastChartShape) error {
	duration, samples, extras, specials := false, false, false, false
	err := requireRoastChartObject(decoder, depth, func(name string) error {
		switch name {
		case "duration_seconds":
			duration = true
			value, err := decoder.Token()
			if err != nil || !validRoastChartNumber(value, true, true) {
				return errInvalidRoastChartJSON
			}
			return nil
		case "sample_count":
			samples = true
			count, err := roastChartCount(decoder)
			shape.sampleCount = count
			return err
		case "extra_series_count":
			extras = true
			count, err := roastChartCount(decoder)
			shape.extraCount = count
			return err
		case "special_event_count":
			specials = true
			count, err := roastChartCount(decoder)
			shape.specialCount = count
			return err
		default:
			return skipRoastChartValue(decoder, depth+1)
		}
	})
	if err != nil || !duration || !samples || !extras || !specials {
		return errInvalidRoastChartJSON
	}
	return nil
}

func roastChartNumberArray(decoder *json.Decoder, nullable bool) (int, error) {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('[') {
		return 0, errInvalidRoastChartJSON
	}
	count := 0
	for decoder.More() {
		value, err := decoder.Token()
		if err != nil || !validRoastChartNumber(value, nullable, false) {
			return 0, errInvalidRoastChartJSON
		}
		count++
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return 0, errInvalidRoastChartJSON
	}
	return count, nil
}

func roastChartObjectArray(decoder *json.Decoder, depth int) (int, error) {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('[') || depth > maxRoastChartJSONDepth {
		return 0, errInvalidRoastChartJSON
	}
	count := 0
	for decoder.More() {
		value, err := decoder.Token()
		if err != nil || value != json.Delim('{') {
			return 0, errInvalidRoastChartJSON
		}
		if err := consumeRoastChartObject(decoder, depth+1, func(string) error { return skipRoastChartValue(decoder, depth+2) }); err != nil {
			return 0, err
		}
		count++
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return 0, errInvalidRoastChartJSON
	}
	return count, nil
}

func validRoastChartNumber(value json.Token, nullable, nonnegative bool) bool {
	if value == nil {
		return nullable
	}
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, err := strconv.ParseFloat(number.String(), 64)
	return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && (!nonnegative || parsed >= 0)
}

func roastChartCount(decoder *json.Decoder) (int, error) {
	value, err := decoder.Token()
	number, ok := value.(json.Number)
	if err != nil || !ok {
		return 0, errInvalidRoastChartJSON
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || parsed < 0 || parsed > maxRoastChartBytes {
		return 0, errInvalidRoastChartJSON
	}
	return int(parsed), nil
}

func skipRoastChartValue(decoder *json.Decoder, depth int) error {
	value, err := decoder.Token()
	if err != nil {
		return errInvalidRoastChartJSON
	}
	delimiter, ok := value.(json.Delim)
	if !ok {
		return nil
	}
	if depth > maxRoastChartJSONDepth {
		return errInvalidRoastChartJSON
	}
	switch delimiter {
	case '{':
		return consumeRoastChartObject(decoder, depth, func(string) error { return skipRoastChartValue(decoder, depth+1) })
	case '[':
		for decoder.More() {
			if err := skipRoastChartValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errInvalidRoastChartJSON
		}
		return nil
	default:
		return errInvalidRoastChartJSON
	}
}

func sameRoastChartRevision(current RoastDetail, before RoastRevision, parserVersion string) bool {
	return current.State == "parsed" && current.CurrentRevision != nil &&
		current.CurrentRevision.ParseState == "parsed" &&
		current.CurrentRevision.RevisionNumber == before.RevisionNumber &&
		current.CurrentRevision.SHA256 == before.SHA256 &&
		current.CurrentRevision.ByteSize == before.ByteSize &&
		current.CurrentRevision.UploadedAt == before.UploadedAt &&
		current.CurrentRevision.ParserVersion == before.ParserVersion &&
		current.CurrentRevision.ParserVersion == parserVersion
}

type chartFenceCallbackError struct{ failure *output.Error }

func (err *chartFenceCallbackError) Error() string { return "roast chart publication fence rejected" }

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
