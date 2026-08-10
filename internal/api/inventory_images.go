package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/securefile"
)

const (
	// MaxInventoryImages is the server's maximum image count per lot.
	MaxInventoryImages    = 8
	maxImageDownloadBytes = int64(10 << 20)
)

type downloadOperations struct {
	createTemp       func(string, string) (*os.File, error)
	protect          func(*os.File) error
	writer           func(*os.File) io.Writer
	syncFile         func(*os.File) error
	closeFile        func(*os.File) error
	installNoReplace func(string, string) (bool, error)
	replace          func(string, string) (bool, error)
	syncParent       func(string) error
}

func defaultDownloadOperations() downloadOperations {
	return downloadOperations{
		createTemp:       os.CreateTemp,
		protect:          securefile.ProtectPrivateFile,
		writer:           func(file *os.File) io.Writer { return file },
		syncFile:         func(file *os.File) error { return file.Sync() },
		closeFile:        func(file *os.File) error { return file.Close() },
		installNoReplace: atomicInstallDownloadNoReplace,
		replace:          atomicReplaceDownload,
		syncParent:       securefile.SyncParentDirectory,
	}
}

// InventoryImagePatch preserves field presence, including caption and alt-text
// clears represented by JSON null.
type InventoryImagePatch struct {
	fields map[string]any
}

// ImageOrderWrite is the complete ordered image identifier list.
type ImageOrderWrite struct {
	ImageIDs []string `json:"image_ids"`
}

// ImageDownload describes an image written to a local file.
type ImageDownload struct {
	Path    string `json:"path"`
	Variant string `json:"variant"`
	Bytes   int64  `json:"bytes"`
}

func (patch InventoryImagePatch) MarshalJSON() ([]byte, error) {
	return json.Marshal(patch.fields)
}

// NewInventoryImagePatch validates and normalizes a presence-sensitive image
// metadata patch.
func NewInventoryImagePatch(fields map[string]any) (InventoryImagePatch, *output.Error) {
	if len(fields) == 0 {
		return InventoryImagePatch{}, mutationUsage("invalid_image_patch", "Image update requires at least one metadata field")
	}
	canonical := make(map[string]any, len(fields))
	for name, value := range fields {
		switch name {
		case "caption", "alt_text":
			if value == nil {
				canonical[name] = nil
				continue
			}
			text, ok := value.(string)
			if !ok {
				return InventoryImagePatch{}, mutationUsage("invalid_image_patch", "Image metadata contains an invalid field value")
			}
			codePoints, bytesLimit := 500, 2000
			if name == "alt_text" {
				codePoints, bytesLimit = 300, 1200
			}
			normalized, valid := normalizeRequestText(text, codePoints, bytesLimit, false, false)
			if !valid {
				return InventoryImagePatch{}, mutationUsage("invalid_image_patch", "Image metadata contains an invalid field value")
			}
			if normalized == "" {
				canonical[name] = nil
			} else {
				canonical[name] = normalized
			}
		case "is_cover":
			cover, ok := value.(bool)
			if !ok {
				return InventoryImagePatch{}, mutationUsage("invalid_image_patch", "Image cover state must be true or false")
			}
			canonical[name] = cover
		default:
			return InventoryImagePatch{}, mutationUsage("invalid_image_patch", "Image update contains an unknown metadata field")
		}
	}
	return InventoryImagePatch{fields: canonical}, nil
}

func normalizeImageUploadManifest(images []ImageUploadManifest, requireNonempty bool) ([]ImageUploadManifest, *output.Error) {
	if images == nil || len(images) > MaxInventoryImages || (requireNonempty && len(images) == 0) {
		return nil, mutationUsage("invalid_images", "Image declarations must contain between one and eight images")
	}
	normalized := make([]ImageUploadManifest, len(images))
	copy(normalized, images)
	coverCount := 0
	for index := range normalized {
		image := &normalized[index]
		if image.UploadIndex != int64(index) {
			return nil, mutationUsage("invalid_images", "Image upload indexes must be contiguous, ordered, and zero-based")
		}
		caption, ok := normalizeOptionalRequestText(image.Caption, 500, 2000, false)
		if !ok {
			return nil, mutationUsage("invalid_images", "Image caption is invalid")
		}
		altText, ok := normalizeOptionalRequestText(image.AltText, 300, 1200, false)
		if !ok {
			return nil, mutationUsage("invalid_images", "Image alt text is invalid")
		}
		image.Caption = caption
		image.AltText = altText
		if image.IsCover {
			coverCount++
		}
	}
	if coverCount > 1 {
		return nil, mutationUsage("invalid_images", "Only one image may be declared as cover")
	}
	return normalized, nil
}

// ValidateImageUploadFiles checks locally knowable file requirements without
// reading image contents into memory.
func ValidateImageUploadFiles(paths []string) *output.Error {
	return ValidateImageUploadFilesContext(context.Background(), paths)
}

// ValidateImageUploadFilesContext is the cancellation-aware command validation
// path for upload sources.
func ValidateImageUploadFilesContext(ctx context.Context, paths []string) *output.Error {
	if len(paths) == 0 || len(paths) > MaxInventoryImages {
		return mutationUsage("invalid_image_file", "Image files must contain between one and eight paths")
	}
	if _, err := newManifestMultipartBody(ctx, nil, paths...); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return contextOrNetworkFailure(ctx)
		}
		return multipartPreparationFailure(err)
	}
	return nil
}

// AddInventoryImages attaches ordered JPEG/PNG files and metadata to a lot.
func (c *Client) AddInventoryImages(ctx context.Context, rawLotID string, metadata []ImageUploadManifest, paths []string, key string) (BeanLotDetail, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	metadata, failure = normalizeImageUploadManifest(metadata, true)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	if len(paths) != len(metadata) {
		return BeanLotDetail{}, mutationUsage("invalid_images", "Every image declaration must have exactly one file")
	}
	manifest, err := json.Marshal(struct {
		Images []ImageUploadManifest `json:"images"`
	}{Images: metadata})
	if err != nil || len(manifest) > maxBeanLotManifestBytes {
		return BeanLotDetail{}, mutationUsage("invalid_images", "Unable to encode image declarations")
	}
	body, err := newManifestMultipartBody(ctx, manifest, paths...)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return BeanLotDetail{}, contextOrNetworkFailure(ctx)
		}
		return BeanLotDetail{}, multipartPreparationFailure(err)
	}
	var lot BeanLotDetail
	failure = c.doInventoryAdminJSON(ctx, Request{
		Method: http.MethodPost, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/images",
		Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK,
	}, &lot, true)
	if failure == nil && lot.LotID != lotID {
		return BeanLotDetail{}, invalidServerResponse(http.StatusOK)
	}
	return lot, failure
}

// PatchInventoryImage edits caption, alt text, or cover state.
func (c *Client) PatchInventoryImage(ctx context.Context, rawLotID, rawImageID string, patch InventoryImagePatch, key string) (BeanLotDetail, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	imageID, failure := normalizeInventoryUUID(rawImageID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	if len(patch.fields) == 0 {
		return BeanLotDetail{}, mutationUsage("invalid_image_patch", "Image update requires at least one metadata field")
	}
	body, err := newJSONBody(patch)
	if err != nil {
		return BeanLotDetail{}, mutationUsage("invalid_image_patch", "Unable to encode image metadata")
	}
	var lot BeanLotDetail
	failure = c.doInventoryAdminJSON(ctx, Request{
		Method: http.MethodPatch, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/images/" + imageID,
		Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK,
	}, &lot, true)
	if failure == nil && (lot.LotID != lotID || !patchedImageMatches(lot.Images, imageID, patch)) {
		return BeanLotDetail{}, invalidServerResponse(http.StatusOK)
	}
	return lot, failure
}

// ReorderInventoryImages sends the complete ordered image identifier list.
func (c *Client) ReorderInventoryImages(ctx context.Context, rawLotID string, rawImageIDs []string, key string) (BeanLotDetail, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	if len(rawImageIDs) == 0 || len(rawImageIDs) > MaxInventoryImages {
		return BeanLotDetail{}, mutationUsage("invalid_image_order", "Image reorder requires the complete list of one to eight image IDs")
	}
	order := ImageOrderWrite{ImageIDs: make([]string, len(rawImageIDs))}
	seen := make(map[string]struct{}, len(rawImageIDs))
	for index, raw := range rawImageIDs {
		imageID, identifierFailure := normalizeInventoryUUID(raw)
		if identifierFailure != nil {
			return BeanLotDetail{}, identifierFailure
		}
		if _, exists := seen[imageID]; exists {
			return BeanLotDetail{}, mutationUsage("invalid_image_order", "Image reorder must not contain duplicate IDs")
		}
		seen[imageID] = struct{}{}
		order.ImageIDs[index] = imageID
	}
	body, err := newJSONBody(order)
	if err != nil {
		return BeanLotDetail{}, mutationUsage("invalid_image_order", "Unable to encode image order")
	}
	var lot BeanLotDetail
	failure = c.doInventoryAdminJSON(ctx, Request{
		Method: http.MethodPut, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/images/order",
		Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK,
	}, &lot, true)
	if failure == nil && (lot.LotID != lotID || !imageOrderMatches(lot.Images, order.ImageIDs)) {
		return BeanLotDetail{}, invalidServerResponse(http.StatusOK)
	}
	return lot, failure
}

// DeleteInventoryImage removes one image. An explicit empty body keeps the
// idempotent mutation replayable on transient failures.
func (c *Client) DeleteInventoryImage(ctx context.Context, rawLotID, rawImageID, key string) (BeanLotDetail, *output.Error) {
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	imageID, failure := normalizeInventoryUUID(rawImageID)
	if failure != nil {
		return BeanLotDetail{}, failure
	}
	var lot BeanLotDetail
	failure = c.doInventoryAdminJSON(ctx, Request{
		Method: http.MethodDelete, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/images/" + imageID,
		Body: emptyRequestBody, IdempotencyKey: key, ExpectedStatus: http.StatusOK,
	}, &lot, true)
	if failure == nil && (lot.LotID != lotID || inventoryImagePresent(lot.Images, imageID)) {
		return BeanLotDetail{}, invalidServerResponse(http.StatusOK)
	}
	return lot, failure
}

func patchedImageMatches(images []InventoryImage, imageID string, patch InventoryImagePatch) bool {
	for _, image := range images {
		if image.ImageID != imageID {
			continue
		}
		for name, expected := range patch.fields {
			switch name {
			case "caption":
				if !optionalStringMatchesValue(image.Caption, expected) {
					return false
				}
			case "alt_text":
				if !optionalStringMatchesValue(image.AltText, expected) {
					return false
				}
			case "is_cover":
				cover, ok := expected.(bool)
				if !ok || image.IsCover != cover {
					return false
				}
			}
		}
		return true
	}
	return false
}

func optionalStringMatchesValue(actual *string, expected any) bool {
	if expected == nil {
		return actual == nil
	}
	text, ok := expected.(string)
	return ok && actual != nil && *actual == text
}

func imageOrderMatches(images []InventoryImage, imageIDs []string) bool {
	if len(images) != len(imageIDs) {
		return false
	}
	for index := range images {
		if images[index].ImageID != imageIDs[index] || images[index].Position != int64(index) {
			return false
		}
	}
	return true
}

func inventoryImagePresent(images []InventoryImage, imageID string) bool {
	for _, image := range images {
		if image.ImageID == imageID {
			return true
		}
	}
	return false
}

func emptyRequestBody() (io.ReadCloser, string, error) {
	return http.NoBody, "", nil
}

func multipartPreparationFailure(err error) *output.Error {
	var imageFailure *multipartFileError
	if errors.As(err, &imageFailure) {
		return mutationUsage("invalid_image_file", "Image files must be readable nonempty regular JPEG or PNG files no larger than 10 MiB each")
	}
	return &output.Error{ExitCode: 1, Code: "request_body_error", Message: "Unable to prepare the request body"}
}

// DownloadInventoryImage streams one private WebP variant through a protected
// same-directory temporary file and installs it atomically and durably.
func (c *Client) DownloadInventoryImage(ctx context.Context, rawLotID, rawImageID, variant, destination string, force bool) (result ImageDownload, failure *output.Error) {
	defer func() { failure = c.failureWithoutSecrets(failure) }()
	if ctx == nil {
		return result, localFailure("invalid_request", "Request context is required")
	}
	if ctx.Err() != nil {
		return result, contextOrNetworkFailure(ctx)
	}
	lotID, failure := normalizeInventoryUUID(rawLotID)
	if failure != nil {
		return result, failure
	}
	imageID, failure := normalizeInventoryUUID(rawImageID)
	if failure != nil {
		return result, failure
	}
	if variant != "display" && variant != "thumbnail" {
		return result, mutationUsage("invalid_image_variant", "Image variant must be display or thumbnail")
	}
	if destination == "" || filepath.Base(destination) == "." || filepath.Base(destination) == string(filepath.Separator) {
		return result, mutationUsage("invalid_destination", "Image download requires a destination file path")
	}
	if !force {
		if _, err := os.Lstat(destination); err == nil {
			return result, destinationExistsFailure()
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, localStorageFailure("Unable to store the image download safely")
		}
	}

	endpoint, err := c.endpointURL(inventoryReadRoot+"/bean-lots/"+lotID+"/images/"+imageID+"/"+variant, nil)
	if err != nil {
		return result, localFailure("invalid_request", "A valid API path is required")
	}
	directory := filepath.Dir(destination)
	temporary, err := c.downloadOps.createTemp(directory, "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return result, localStorageFailure("Unable to store the image download safely")
	}
	temporaryPath := temporary.Name()
	temporaryClosed := false
	defer func() {
		if !temporaryClosed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := c.downloadOps.protect(temporary); err != nil {
		return result, localStorageFailure("Unable to store the image download safely")
	}

	var downloaded int64
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return result, contextOrNetworkFailure(ctx)
		}
		if err := resetDownloadTemp(temporary); err != nil {
			return result, localStorageFailure("Unable to store the image download safely")
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
				if ctx.Err() != nil {
					return result, contextOrNetworkFailure(ctx)
				}
			}
			return result, networkFailure()
		}
		status := response.StatusCode
		if status >= 300 && status < 400 {
			_ = response.Body.Close()
			return result, redirectRefused(status)
		}
		if status != http.StatusOK {
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
				if ctx.Err() != nil {
					return result, contextOrNetworkFailure(ctx)
				}
			}
			if readErr != nil || oversized || status < 400 || status >= 600 {
				return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
			}
			return result, classifyInventoryAPIFailure(decodeAPIError(status, body, c.token, c.serverURL.String()), true)
		}
		if response.Header.Get("Content-Type") != "image/webp" || response.ContentLength > maxImageDownloadBytes {
			_ = response.Body.Close()
			return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
		}
		var readErr, writeErr error
		downloaded, readErr, writeErr = copyDownloadResponse(c.downloadOps.writer(temporary), response.Body)
		closeErr := response.Body.Close()
		if writeErr != nil {
			return result, localStorageFailure("Unable to store the image download safely")
		}
		if readErr == nil {
			readErr = closeErr
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return result, contextOrNetworkFailure(ctx)
			}
			if attempt < maxAttempts-1 {
				if waitForRetry(ctx, attempt) == nil {
					continue
				}
				if ctx.Err() != nil {
					return result, contextOrNetworkFailure(ctx)
				}
			}
			return result, networkFailure()
		}
		if downloaded == 0 || downloaded > maxImageDownloadBytes {
			return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
		}
		break
	}

	if ctx.Err() != nil {
		return result, contextOrNetworkFailure(ctx)
	}
	if err := c.downloadOps.syncFile(temporary); err != nil {
		return result, localStorageFailure("Unable to store the image download safely")
	}
	if err := c.downloadOps.closeFile(temporary); err != nil {
		return result, localStorageFailure("Unable to store the image download safely")
	}
	temporaryClosed = true
	if ctx.Err() != nil {
		return result, contextOrNetworkFailure(ctx)
	}
	var installed bool
	if force {
		installed, err = c.downloadOps.replace(temporaryPath, destination)
	} else {
		installed, err = c.downloadOps.installNoReplace(temporaryPath, destination)
	}
	installedResult := ImageDownload{Path: destination, Variant: variant, Bytes: downloaded}
	if installed {
		if syncErr := c.downloadOps.syncParent(directory); syncErr != nil {
			return installedResult, localStorageFailure("The image download is installed, but storage durability is uncertain")
		}
		if err != nil {
			return installedResult, localStorageFailure("The image download is installed, but a local storage operation did not complete")
		}
		return installedResult, nil
	}
	if err != nil && errors.Is(err, os.ErrExist) {
		return result, destinationExistsFailure()
	}
	return result, localStorageFailure("Unable to store the image download safely")
}

func copyDownloadResponse(destination io.Writer, source io.Reader) (written int64, readErr, writeErr error) {
	buffer := make([]byte, 32*1024)
	remaining := maxImageDownloadBytes + 1
	for remaining > 0 {
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		count, err := source.Read(buffer[:chunk])
		if count > 0 {
			destinationCount, destinationErr := destination.Write(buffer[:count])
			written += int64(destinationCount)
			if destinationErr != nil {
				return written, nil, destinationErr
			}
			if destinationCount != count {
				return written, nil, io.ErrShortWrite
			}
			remaining -= int64(count)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return written, nil, nil
			}
			return written, err, nil
		}
		if count == 0 {
			return written, io.ErrNoProgress, nil
		}
	}
	return written, nil, nil
}

func resetDownloadTemp(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}

func localStorageFailure(message string) *output.Error {
	return &output.Error{ExitCode: 3, Code: "local_storage_error", Message: message}
}

func destinationExistsFailure() *output.Error {
	return localStorageFailure("Destination already exists; use --force to replace it")
}
