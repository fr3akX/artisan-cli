package api

import (
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

func TestManifestMultipartBodyIsReplayableWithZeroImages(t *testing.T) {
	manifest := []byte(`{"fields":{"name":"Lot"},"opening_grams":0,"images":[]}`)
	bodyFactory, err := NewManifestMultipartBody(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var firstBody, firstType string
	for attempt := 0; attempt < 2; attempt++ {
		body, contentType, err := bodyFactory()
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		mediaType, parameters, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content type = %q, %v", contentType, err)
		}
		reader := multipart.NewReader(strings.NewReader(string(contents)), parameters["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(part)
		if part.FormName() != "manifest" || part.FileName() != "" || part.Header.Get("Content-Type") != "application/json" || string(got) != string(manifest) {
			t.Fatalf("manifest part = name %q filename %q type %q body %q", part.FormName(), part.FileName(), part.Header.Get("Content-Type"), got)
		}
		if _, err := reader.NextPart(); err != io.EOF {
			t.Fatalf("unexpected second part: %v", err)
		}
		if attempt == 0 {
			firstBody, firstType = string(contents), contentType
		} else if string(contents) != firstBody || contentType != firstType {
			t.Fatal("multipart body or content type changed on replay")
		}
	}
}
