package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxMultipartImages = MaxInventoryImages
	// MaxInventoryImageBytes is the server-enforced per-upload image limit.
	MaxInventoryImageBytes int64 = 10 * 1024 * 1024
)

var fingerprintMultipartFileHook = fingerprintMultipartFileCancelable

type multipartFileError struct {
	changed bool
}

func (failure *multipartFileError) Error() string {
	if failure.changed {
		return "multipart image file changed"
	}
	return "invalid multipart image file"
}

type multipartImage struct {
	path         string
	filename     string
	contentType  string
	linkInfo     os.FileInfo
	symbolicLink bool
	linkTarget   string
	fileInfo     os.FileInfo
	fingerprint  [sha256.Size]byte
}

// NewManifestMultipartBody prepares a replayable streaming multipart body.
// The manifest is always the first part and each image is reopened and checked
// against its captured filesystem identity, size, modification time, and SHA-256
// fingerprint before and during every attempt. Image contents are never buffered.
func NewManifestMultipartBody(manifest []byte, imagePaths ...string) (func() (io.ReadCloser, string, error), error) {
	return newManifestMultipartBody(context.Background(), manifest, imagePaths...)
}

func newManifestMultipartBody(ctx context.Context, manifest []byte, imagePaths ...string) (func() (io.ReadCloser, string, error), error) {
	if ctx == nil {
		return nil, errors.New("multipart context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(imagePaths) > maxMultipartImages {
		return nil, &multipartFileError{}
	}
	images := make([]multipartImage, len(imagePaths))
	for index, path := range imagePaths {
		captured, err := captureMultipartImage(path, ctx.Done())
		if err != nil {
			return nil, err
		}
		images[index] = captured
	}

	var random [18]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	boundary := "artisan-" + hex.EncodeToString(random[:])
	contentTypeWriter := multipart.NewWriter(io.Discard)
	if err := contentTypeWriter.SetBoundary(boundary); err != nil {
		return nil, err
	}
	contentType := contentTypeWriter.FormDataContentType()
	manifestCopy := append([]byte(nil), manifest...)

	return func() (io.ReadCloser, string, error) {
		opened := make([]*os.File, 0, len(images))
		for _, image := range images {
			file, err := image.openVerified()
			if err != nil {
				closeMultipartFiles(opened)
				return nil, "", err
			}
			opened = append(opened, file)
		}

		reader, writer := io.Pipe()
		done := make(chan error, 1)
		cancel := make(chan struct{})
		body := &streamingMultipartBody{reader: reader, done: done, cancel: cancel}
		go writeMultipartBody(writer, done, cancel, boundary, manifestCopy, images, opened)
		return body, contentType, nil
	}, nil
}

func captureMultipartImage(path string, cancel <-chan struct{}) (multipartImage, error) {
	filename := filepath.Base(path)
	if path == "" || filename == "." || filename == string(filepath.Separator) || !validMultipartFilename(filename) {
		return multipartImage{}, &multipartFileError{}
	}
	contentType := imageContentType(filename)
	if contentType == "" {
		return multipartImage{}, &multipartFileError{}
	}
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return multipartImage{}, &multipartFileError{}
	}
	symbolicLink := linkInfo.Mode()&os.ModeSymlink != 0
	var linkTarget string
	if symbolicLink {
		linkTarget, err = os.Readlink(path)
		if err != nil {
			return multipartImage{}, &multipartFileError{}
		}
	}
	image := multipartImage{
		path: path, filename: filename, contentType: contentType,
		linkInfo: linkInfo, symbolicLink: symbolicLink, linkTarget: linkTarget,
	}
	file, err := os.Open(path)
	if err != nil {
		return multipartImage{}, &multipartFileError{}
	}
	fileInfo, statErr := file.Stat()
	if statErr != nil || !fileInfo.Mode().IsRegular() || fileInfo.Size() < 1 || fileInfo.Size() > MaxInventoryImageBytes {
		_ = file.Close()
		return multipartImage{}, &multipartFileError{}
	}
	fingerprint, fingerprintErr := fingerprintMultipartFileHook(file, fileInfo.Size(), cancel)
	postFileInfo, postStatErr := file.Stat()
	closeErr := file.Close()
	if fingerprintErr != nil {
		if errors.Is(fingerprintErr, io.ErrClosedPipe) {
			return multipartImage{}, fingerprintErr
		}
		return multipartImage{}, &multipartFileError{changed: true}
	}
	if postStatErr != nil || closeErr != nil ||
		!sameMultipartSnapshot(fileInfo, postFileInfo) || !image.pathMatches() {
		return multipartImage{}, &multipartFileError{changed: true}
	}
	image.fileInfo = fileInfo
	image.fingerprint = fingerprint
	return image, nil
}

func validMultipartFilename(filename string) bool {
	if filename == "" || filename == "." || filename == ".." || !utf8.ValidString(filename) || strings.ContainsAny(filename, "\x00\r\n") {
		return false
	}
	for _, character := range filename {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func imageContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	default:
		return ""
	}
}

func (image multipartImage) pathMatches() bool {
	current, err := os.Lstat(image.path)
	if err != nil || !os.SameFile(image.linkInfo, current) {
		return false
	}
	currentSymbolicLink := current.Mode()&os.ModeSymlink != 0
	if image.symbolicLink != currentSymbolicLink {
		return false
	}
	if !currentSymbolicLink {
		return true
	}
	currentTarget, err := os.Readlink(image.path)
	return err == nil && currentTarget == image.linkTarget
}

func (image multipartImage) openVerified() (*os.File, error) {
	if !image.pathMatches() {
		return nil, &multipartFileError{changed: true}
	}
	file, err := os.Open(image.path)
	if err != nil {
		return nil, &multipartFileError{changed: true}
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !sameMultipartSnapshot(image.fileInfo, info) {
		_ = file.Close()
		return nil, &multipartFileError{changed: true}
	}
	return file, nil
}

func sameMultipartSnapshot(expected, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) &&
		expected.Size() == actual.Size() && expected.ModTime().Equal(actual.ModTime())
}

func fingerprintMultipartFile(file *os.File, size int64) ([sha256.Size]byte, error) {
	return fingerprintMultipartFileCancelable(file, size, nil)
}

func fingerprintMultipartFileCancelable(file *os.File, size int64, cancel <-chan struct{}) ([sha256.Size]byte, error) {
	hasher := sha256.New()
	remaining := size
	buffer := make([]byte, 32*1024)
	for remaining > 0 {
		if cancel != nil {
			select {
			case <-cancel:
				return [sha256.Size]byte{}, io.ErrClosedPipe
			default:
			}
		}
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		count, err := file.Read(buffer[:chunk])
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
			remaining -= int64(count)
		}
		if err != nil || count == 0 {
			return [sha256.Size]byte{}, &multipartFileError{changed: true}
		}
	}
	var extra [1]byte
	count, err := file.Read(extra[:])
	if count != 0 || !errors.Is(err, io.EOF) {
		return [sha256.Size]byte{}, &multipartFileError{changed: true}
	}
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hasher.Sum(nil))
	return fingerprint, nil
}

func writeMultipartBody(pipe *io.PipeWriter, done chan<- error, cancel <-chan struct{}, boundary string, manifest []byte, images []multipartImage, files []*os.File) {
	var result error
	defer func() {
		closeMultipartFiles(files)
		if result == nil {
			result = pipe.Close()
		} else {
			_ = pipe.CloseWithError(result)
		}
		done <- result
	}()

	writer := multipart.NewWriter(pipe)
	if err := writer.SetBoundary(boundary); err != nil {
		result = err
		return
	}
	manifestHeader := make(textproto.MIMEHeader)
	manifestHeader.Set("Content-Disposition", `form-data; name="manifest"`)
	manifestHeader.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(manifestHeader)
	if err != nil {
		result = err
		return
	}
	if _, err := part.Write(manifest); err != nil {
		result = err
		return
	}
	for index, image := range images {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="images"; filename="%s"`, escapeMultipartQuotes(image.filename)))
		header.Set("Content-Type", image.contentType)
		part, err = writer.CreatePart(header)
		if err != nil {
			result = err
			return
		}
		if err := image.streamVerified(part, files[index]); err != nil {
			result = err
			return
		}
	}
	// Recheck every source after every part has streamed. This prevents a change
	// to an earlier image while a later image is in flight from being accepted.
	for index, image := range images {
		if err := image.verifyCurrent(files[index], cancel); err != nil {
			result = err
			return
		}
	}
	result = writer.Close()
}

func (image multipartImage) verifyCurrent(file *os.File, cancel <-chan struct{}) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return &multipartFileError{changed: true}
	}
	fingerprint, err := fingerprintMultipartFileCancelable(file, image.fileInfo.Size(), cancel)
	if err != nil {
		return err
	}
	postFileInfo, statErr := file.Stat()
	if statErr != nil || !sameMultipartSnapshot(image.fileInfo, postFileInfo) ||
		!image.pathMatches() || fingerprint != image.fingerprint {
		return &multipartFileError{changed: true}
	}
	return nil
}

func (image multipartImage) streamVerified(destination io.Writer, file *os.File) error {
	hasher := sha256.New()
	remaining := image.fileInfo.Size()
	buffer := make([]byte, 32*1024)
	for remaining > 0 {
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		count, readErr := file.Read(buffer[:chunk])
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
			written, writeErr := destination.Write(buffer[:count])
			if writeErr != nil {
				return writeErr
			}
			if written != count {
				return io.ErrShortWrite
			}
			remaining -= int64(count)
		}
		if readErr != nil {
			return &multipartFileError{changed: true}
		}
		if count == 0 {
			return &multipartFileError{changed: true}
		}
	}
	var extra [1]byte
	count, readErr := file.Read(extra[:])
	if count != 0 || !errors.Is(readErr, io.EOF) {
		return &multipartFileError{changed: true}
	}
	postFileInfo, statErr := file.Stat()
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hasher.Sum(nil))
	if statErr != nil || !sameMultipartSnapshot(image.fileInfo, postFileInfo) ||
		!image.pathMatches() || fingerprint != image.fingerprint {
		return &multipartFileError{changed: true}
	}
	return nil
}

func escapeMultipartQuotes(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", `"`, `\"`)
	return replacer.Replace(value)
}

func closeMultipartFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

type streamingMultipartBody struct {
	reader   *io.PipeReader
	done     <-chan error
	cancel   chan struct{}
	once     sync.Once
	closeErr error
}

func (body *streamingMultipartBody) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *streamingMultipartBody) Close() error {
	body.once.Do(func() {
		close(body.cancel)
		_ = body.reader.Close()
		body.closeErr = <-body.done
		if errors.Is(body.closeErr, io.ErrClosedPipe) {
			body.closeErr = nil
		}
	})
	return body.closeErr
}
