package api

import (
	"crypto/rand"
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

const maxMultipartImages = MaxInventoryImages

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
	path        string
	filename    string
	contentType string
	linkInfo    os.FileInfo
	fileInfo    os.FileInfo
}

// NewManifestMultipartBody prepares a replayable streaming multipart body.
// The manifest is always the first part and each image is reopened and checked
// against its captured filesystem identity, size, and modification time before
// every attempt. Image contents are never buffered in memory.
func NewManifestMultipartBody(manifest []byte, imagePaths ...string) (func() (io.ReadCloser, string, error), error) {
	if len(imagePaths) > maxMultipartImages {
		return nil, &multipartFileError{}
	}
	images := make([]multipartImage, len(imagePaths))
	for index, path := range imagePaths {
		captured, err := captureMultipartImage(path)
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
		body := &streamingMultipartBody{reader: reader, done: done}
		go writeMultipartBody(writer, done, boundary, manifestCopy, images, opened)
		return body, contentType, nil
	}, nil
}

func captureMultipartImage(path string) (multipartImage, error) {
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
	file, err := os.Open(path)
	if err != nil {
		return multipartImage{}, &multipartFileError{}
	}
	fileInfo, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !fileInfo.Mode().IsRegular() {
		return multipartImage{}, &multipartFileError{}
	}
	return multipartImage{
		path: path, filename: filename, contentType: contentType,
		linkInfo: linkInfo, fileInfo: fileInfo,
	}, nil
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

func (image multipartImage) openVerified() (*os.File, error) {
	linkInfo, err := os.Lstat(image.path)
	if err != nil || !os.SameFile(image.linkInfo, linkInfo) {
		return nil, &multipartFileError{changed: true}
	}
	file, err := os.Open(image.path)
	if err != nil {
		return nil, &multipartFileError{changed: true}
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(image.fileInfo, info) || info.Size() != image.fileInfo.Size() || !info.ModTime().Equal(image.fileInfo.ModTime()) {
		_ = file.Close()
		return nil, &multipartFileError{changed: true}
	}
	return file, nil
}

func writeMultipartBody(pipe *io.PipeWriter, done chan<- error, boundary string, manifest []byte, images []multipartImage, files []*os.File) {
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
		if _, err := io.Copy(part, files[index]); err != nil {
			result = err
			return
		}
	}
	result = writer.Close()
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
	once     sync.Once
	closeErr error
}

func (body *streamingMultipartBody) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *streamingMultipartBody) Close() error {
	body.once.Do(func() {
		_ = body.reader.Close()
		body.closeErr = <-body.done
		if errors.Is(body.closeErr, io.ErrClosedPipe) {
			body.closeErr = nil
		}
	})
	return body.closeErr
}
