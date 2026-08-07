package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/textproto"
)

// NewManifestMultipartBody prepares a manifest-only multipart body whose bytes
// and content type remain identical every time the returned factory is opened.
// Task 8 extends this seam with replayable image file parts.
func NewManifestMultipartBody(manifest []byte) (func() (io.ReadCloser, string, error), error) {
	var random [18]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	boundary := "artisan-" + hex.EncodeToString(random[:])
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	if err := writer.SetBoundary(boundary); err != nil {
		return nil, err
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="manifest"`)
	header.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(manifest); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	contents := append([]byte(nil), encoded.Bytes()...)
	contentType := writer.FormDataContentType()
	return func() (io.ReadCloser, string, error) {
		return io.NopCloser(bytes.NewReader(contents)), contentType, nil
	}, nil
}
