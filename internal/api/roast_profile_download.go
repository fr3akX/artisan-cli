package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const maxRoastProfileBytes = int64(16 << 20)

// RoastProfileDownload describes exact immutable profile bytes installed at a
// caller-selected local path.
type RoastProfileDownload struct {
	Path           string `json:"path"`
	RoastUUID      string `json:"roast_uuid"`
	RevisionNumber int64  `json:"revision_number"`
	Bytes          int64  `json:"bytes"`
	SHA256         string `json:"sha256"`
}

// DownloadRoastProfile downloads one immutable raw roast revision, verifies
// its server-declared identity, and makes it visible only after full validation.
func (c *Client) DownloadRoastProfile(ctx context.Context, rawRoastUUID string, revisionNumber int64, destination string, force bool) (result RoastProfileDownload, failure *output.Error) {
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
	if revisionNumber < 1 || revisionNumber > maxRoastRevisionNumber {
		return result, &output.Error{ExitCode: 2, Code: "invalid_revision_number", Message: "Roast revision number must be between 1 and 2147483647"}
	}

	target, err := newDownloadTarget(destination, force, c.downloadOps)
	if err != nil {
		return result, profileTargetFailure(err)
	}
	defer target.Abort()

	revision, failure := c.findRoastRevision(ctx, roastUUID, revisionNumber)
	if failure != nil {
		return result, failure
	}
	if revision.ByteSize > maxRoastProfileBytes {
		return result, invalidServerResponse(http.StatusOK)
	}
	endpoint, err := c.endpointURL(roastAPIRoot+"/"+roastUUID+"/revisions/"+strconv.FormatInt(revisionNumber, 10)+"/download", nil)
	if err != nil {
		return result, localFailure("invalid_request", "A valid API path is required")
	}

	var downloaded int64
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return result, contextOrNetworkFailure(ctx)
		}
		if err := target.Reset(); err != nil {
			return result, profileStorageFailure("Unable to store the roast profile safely")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return result, localFailure("invalid_request", "The API request is invalid")
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("User-Agent", c.userAgent)
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

		if responseHeaderContainsAny(response.Header, []string{c.token, c.serverURL.String()}) || !validRoastProfileHeaders(response, revision) {
			_ = response.Body.Close()
			return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
		}
		hasher := sha256.New()
		observed := &observedDownloadWriter{destination: target.Writer()}
		writer := io.MultiWriter(observed, hasher)
		downloaded, err = io.Copy(writer, io.LimitReader(response.Body, revision.ByteSize+1))
		closeErr := response.Body.Close()
		if observed.err != nil {
			return result, profileStorageFailure("Unable to store the roast profile safely")
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
		if downloaded != revision.ByteSize || hex.EncodeToString(hasher.Sum(nil)) != revision.SHA256 {
			return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
		}
		break
	}

	if ctx.Err() != nil {
		return result, contextOrNetworkFailure(ctx)
	}
	installedResult := RoastProfileDownload{
		Path: destination, RoastUUID: roastUUID, RevisionNumber: revisionNumber,
		Bytes: downloaded, SHA256: revision.SHA256,
	}
	installed, err := target.InstallContext(ctx, force)
	if installed.Publication == publicationNone && ctx.Err() != nil {
		return result, contextOrNetworkFailure(ctx)
	}
	if installed.Visible() {
		if !installed.Durable() {
			return installedResult, profileStorageFailure("The roast profile is installed, but storage durability is uncertain")
		}
		if err != nil {
			return installedResult, profileStorageFailure("The roast profile is installed, but a local storage operation did not complete")
		}
		return installedResult, nil
	}
	if installed.Publication != publicationNone {
		return installedResult, profileStorageFailure("The roast profile may have been published, but its requested path identity is uncertain")
	}
	if errors.Is(err, os.ErrExist) {
		return result, destinationExistsFailure()
	}
	return result, profileStorageFailure("Unable to store the roast profile safely")
}

func (c *Client) findRoastRevision(ctx context.Context, roastUUID string, revisionNumber int64) (RoastRevision, *output.Error) {
	cursor := ""
	seen := make(map[string]struct{}, MaxRoastAggregatePages)
	items := 0
	for pageNumber := 0; pageNumber < MaxRoastAggregatePages; pageNumber++ {
		if _, exists := seen[cursor]; exists {
			return RoastRevision{}, invalidServerResponse(http.StatusOK)
		}
		seen[cursor] = struct{}{}
		page, failure := c.RoastRevisions(ctx, roastUUID, PageOptions{Limit: maxRoastPageItems, Cursor: cursor})
		if failure != nil {
			return RoastRevision{}, failure
		}
		if len(page.Items) == 0 && (pageNumber > 0 || page.NextCursor != nil) {
			return RoastRevision{}, invalidServerResponse(http.StatusOK)
		}
		items += len(page.Items)
		if items > MaxRoastAggregateItems {
			return RoastRevision{}, &output.Error{ExitCode: 9, Code: "pagination_limit_exceeded", Message: "Roast pagination exceeded the 10000 item safety limit"}
		}
		for _, revision := range page.Items {
			if revision.RevisionNumber == revisionNumber {
				return revision, nil
			}
		}
		if page.NextCursor == nil {
			return RoastRevision{}, &output.Error{ExitCode: 6, Code: "not_found", Message: "Roast revision was not found"}
		}
		if items == MaxRoastAggregateItems {
			return RoastRevision{}, &output.Error{ExitCode: 9, Code: "pagination_limit_exceeded", Message: "Roast pagination exceeded the 10000 item safety limit"}
		}
		cursor = *page.NextCursor
	}
	return RoastRevision{}, &output.Error{ExitCode: 9, Code: "pagination_page_limit_exceeded", Message: "Roast pagination exceeded the 1000 page safety limit"}
}

func validRoastProfileHeaders(response *http.Response, revision RoastRevision) bool {
	contentType, ok := oneCanonicalHeader(response.Header, "Content-Type")
	if !ok || contentType != "application/x-artisan-profile" {
		return false
	}
	disposition, ok := oneCanonicalHeader(response.Header, "Content-Disposition")
	if !ok || !validProfileDisposition(disposition) {
		return false
	}
	length, ok := oneCanonicalHeader(response.Header, "Content-Length")
	if !ok || length != strconv.FormatInt(revision.ByteSize, 10) || response.ContentLength != revision.ByteSize {
		return false
	}
	etag, ok := oneCanonicalHeader(response.Header, "ETag")
	if !ok || etag != `"`+revision.SHA256+`"` {
		return false
	}
	contentSHA, ok := oneCanonicalHeader(response.Header, "X-Content-SHA256")
	if !ok || contentSHA != revision.SHA256 {
		return false
	}
	checksumSHA, ok := oneCanonicalHeader(response.Header, "X-Checksum-SHA256")
	if !ok || checksumSHA != revision.SHA256 {
		return false
	}
	revisionHeader, ok := oneCanonicalHeader(response.Header, "X-Revision-Number")
	return ok && revisionHeader == strconv.FormatInt(revision.RevisionNumber, 10)
}

func oneCanonicalHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] || strings.ContainsAny(values[0], "\r\n") {
		return "", false
	}
	return values[0], true
}

func validProfileDisposition(value string) bool {
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "attachment" || len(parameters) != 1 {
		return false
	}
	filename, exists := parameters["filename"]
	if !exists || filename == "" || len(filename) > 255 || !utf8.ValidString(filename) || strings.TrimSpace(filename) != filename || filepath.Base(filename) != filename || filename == "." || filename == ".." || strings.ContainsAny(filename, `/\\`) {
		return false
	}
	for _, character := range filename {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

type observedDownloadWriter struct {
	destination io.Writer
	err         error
}

func (writer *observedDownloadWriter) Write(buffer []byte) (int, error) {
	count, err := writer.destination.Write(buffer)
	if err == nil && count != len(buffer) {
		err = io.ErrShortWrite
	}
	if err != nil {
		writer.err = err
	}
	return count, err
}

func profileTargetFailure(err error) *output.Error {
	if errors.Is(err, errInvalidDownloadDestination) {
		return &output.Error{ExitCode: 2, Code: "invalid_destination", Message: "Roast profile download requires a destination file path"}
	}
	if errors.Is(err, os.ErrExist) {
		return destinationExistsFailure()
	}
	return profileStorageFailure("Unable to store the roast profile safely")
}

func profileStorageFailure(message string) *output.Error {
	return &output.Error{ExitCode: 3, Code: "local_storage_error", Message: message}
}
