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
	if len(paths) == 0 || len(paths) > MaxInventoryImages {
		return mutationUsage("invalid_image_file", "Image files must contain between one and eight paths")
	}
	if _, err := NewManifestMultipartBody(nil, paths...); err != nil {
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
	body, err := NewManifestMultipartBody(manifest, paths...)
	if err != nil {
		return BeanLotDetail{}, multipartPreparationFailure(err)
	}
	var lot BeanLotDetail
	failure = c.Do(ctx, Request{
		Method: http.MethodPost, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/images",
		Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK,
	}, &lot)
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
	failure = c.Do(ctx, Request{
		Method: http.MethodPatch, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/images/" + imageID,
		Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK,
	}, &lot)
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
	failure = c.Do(ctx, Request{
		Method: http.MethodPut, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/images/order",
		Body: body, IdempotencyKey: key, ExpectedStatus: http.StatusOK,
	}, &lot)
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
	failure = c.Do(ctx, Request{
		Method: http.MethodDelete, Path: inventoryAdminRoot + "/bean-lots/" + lotID + "/images/" + imageID,
		Body: emptyRequestBody, IdempotencyKey: key, ExpectedStatus: http.StatusOK,
	}, &lot)
	return lot, failure
}

func emptyRequestBody() (io.ReadCloser, string, error) {
	return http.NoBody, "", nil
}

func multipartPreparationFailure(err error) *output.Error {
	var imageFailure *multipartFileError
	if errors.As(err, &imageFailure) {
		return mutationUsage("invalid_image_file", "Image files must be readable regular JPEG or PNG files")
	}
	return &output.Error{ExitCode: 1, Code: "request_body_error", Message: "Unable to prepare the request body"}
}

// DownloadInventoryImage streams one private WebP variant through a protected
// same-directory temporary file and installs it atomically.
func (c *Client) DownloadInventoryImage(ctx context.Context, rawLotID, rawImageID, variant, destination string, force bool) (result ImageDownload, failure *output.Error) {
	defer func() { failure = c.failureWithoutSecrets(failure) }()
	if ctx == nil {
		return result, localFailure("invalid_request", "Request context is required")
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
			return result, downloadLocalFailure("destination_exists", "Destination already exists; use --force to replace it")
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, downloadLocalFailure("download_write_failed", "Unable to inspect the download destination")
		}
	}

	endpoint, err := c.endpointURL(inventoryAdminRoot+"/bean-lots/"+lotID+"/images/"+imageID+"/"+variant, nil)
	if err != nil {
		return result, localFailure("invalid_request", "A valid API path is required")
	}
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return result, downloadLocalFailure("download_write_failed", "Unable to create a private download file")
	}
	temporaryPath := temporary.Name()
	temporaryClosed := false
	defer func() {
		if !temporaryClosed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := securefile.ProtectPrivateFile(temporary); err != nil {
		return result, downloadLocalFailure("download_write_failed", "Unable to protect the private download file")
	}

	var downloaded int64
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := resetDownloadTemp(temporary); err != nil {
			return result, downloadLocalFailure("download_write_failed", "Unable to prepare the private download file")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return result, localFailure("invalid_request", "The API request is invalid")
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("User-Agent", c.userAgent)
		response, err := c.httpClient.Do(request)
		if err != nil {
			if attempt < maxAttempts-1 && ctx.Err() == nil {
				if waitForRetry(ctx, attempt) == nil {
					continue
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
			if isTransientStatus(status) && attempt < maxAttempts-1 && readErr == nil && !oversized && ctx.Err() == nil {
				if waitForRetry(ctx, attempt) == nil {
					continue
				}
			}
			if readErr != nil || oversized || status < 400 || status >= 600 {
				return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
			}
			return result, decodeAPIError(status, body, c.token, c.serverURL.String())
		}
		if response.Header.Get("Content-Type") != "image/webp" || response.ContentLength > maxImageDownloadBytes {
			_ = response.Body.Close()
			return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
		}
		downloaded, err = io.Copy(temporary, io.LimitReader(response.Body, maxImageDownloadBytes+1))
		closeErr := response.Body.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			if attempt < maxAttempts-1 && ctx.Err() == nil {
				if waitForRetry(ctx, attempt) == nil {
					continue
				}
			}
			return result, networkFailure()
		}
		if downloaded == 0 || downloaded > maxImageDownloadBytes {
			return result, invalidServerResponseAvoiding(status, []string{c.token, c.serverURL.String()})
		}
		break
	}

	if err := temporary.Sync(); err != nil {
		return result, downloadLocalFailure("download_write_failed", "Unable to sync the private download file")
	}
	if err := temporary.Close(); err != nil {
		return result, downloadLocalFailure("download_write_failed", "Unable to close the private download file")
	}
	temporaryClosed = true
	if force {
		if err := atomicReplaceDownload(temporaryPath, destination); err != nil {
			return result, downloadLocalFailure("download_write_failed", "Unable to atomically replace the download destination")
		}
	} else {
		if err := atomicInstallDownloadNoReplace(temporaryPath, destination); err != nil {
			if errors.Is(err, os.ErrExist) {
				return result, downloadLocalFailure("destination_exists", "Destination already exists; use --force to replace it")
			}
			return result, downloadLocalFailure("download_write_failed", "Unable to atomically install the download")
		}
	}
	return ImageDownload{Path: destination, Variant: variant, Bytes: downloaded}, nil
}

func resetDownloadTemp(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}

func downloadLocalFailure(code, message string) *output.Error {
	return &output.Error{ExitCode: 1, Code: code, Message: message}
}
