package api

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestManifestMultipartStreamsManifestThenOrderedImagesAndReplaysExactBytes(t *testing.T) {
	dir := t.TempDir()
	jpeg := filepath.Join(dir, "first image.JPG")
	png := filepath.Join(dir, "second.png")
	if err := os.WriteFile(jpeg, []byte("jpeg-wire-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(png, []byte("png-wire-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"images":[{"upload_index":0},{"upload_index":1}]}`)
	factory, err := NewManifestMultipartBody(manifest, jpeg, png)
	if err != nil {
		t.Fatal(err)
	}

	var firstBytes []byte
	var firstType string
	for attempt := 0; attempt < 2; attempt++ {
		body, contentType, err := factory()
		if err != nil {
			t.Fatal(err)
		}
		wire, err := io.ReadAll(body)
		if closeErr := body.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			firstBytes = append([]byte(nil), wire...)
			firstType = contentType
		} else if !bytes.Equal(wire, firstBytes) || contentType != firstType {
			t.Fatal("multipart bytes or boundary changed between attempts")
		}

		_, parameters, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(bytes.NewReader(wire), parameters["boundary"])
		want := []struct {
			name, filename, contentType, contents string
		}{
			{name: "manifest", contentType: "application/json", contents: string(manifest)},
			{name: "images", filename: "first image.JPG", contentType: "image/jpeg", contents: "jpeg-wire-bytes"},
			{name: "images", filename: "second.png", contentType: "image/png", contents: "png-wire-bytes"},
		}
		for index, expected := range want {
			part, err := reader.NextPart()
			if err != nil {
				t.Fatalf("part %d: %v", index, err)
			}
			contents, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			if part.FormName() != expected.name || part.FileName() != expected.filename || part.Header.Get("Content-Type") != expected.contentType || string(contents) != expected.contents {
				t.Fatalf("part %d = name %q filename %q type %q body %q", index, part.FormName(), part.FileName(), part.Header.Get("Content-Type"), contents)
			}
		}
		if _, err := reader.NextPart(); err != io.EOF {
			t.Fatalf("unexpected extra part: %v", err)
		}
	}
}

func TestManifestMultipartRejectsChangedOrReplacedFilesBeforeReplay(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(t *testing.T, path string)
	}{
		{
			name: "modified in place",
			change: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("changed-size"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "same size with changed mtime",
			change: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("other"), 0o600); err != nil {
					t.Fatal(err)
				}
				changed := info.ModTime().Add(2 * time.Second)
				if err := os.Chtimes(path, changed, changed); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "replaced with same size and mtime",
			change: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				replacement := path + ".replacement"
				if err := os.WriteFile(replacement, []byte("other"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.jpg")
			if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
				t.Fatal(err)
			}
			factory, err := NewManifestMultipartBody([]byte(`{"images":[{"upload_index":0}]}`), path)
			if err != nil {
				t.Fatal(err)
			}
			body, _, err := factory()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, body); err != nil {
				t.Fatal(err)
			}
			if err := body.Close(); err != nil {
				t.Fatal(err)
			}
			test.change(t, path)
			if body, _, err := factory(); err == nil {
				_ = body.Close()
				t.Fatal("changed file was accepted for replay")
			}
		})
	}
}

func TestManifestMultipartRejectsReplacedSymlinkBeforeReplay(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.jpg")
	second := filepath.Join(dir, "second.jpg")
	link := filepath.Join(dir, "upload.jpg")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("same!"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(first, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	factory, err := NewManifestMultipartBody([]byte(`{"images":[{"upload_index":0}]}`), link)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	if body, _, err := factory(); err == nil {
		_ = body.Close()
		t.Fatal("replaced symlink was accepted")
	}
}

func TestManifestMultipartPartialCloseStopsProducerAndReleasesResources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.jpg")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	factory, err := NewManifestMultipartBody([]byte(`{"images":[{"upload_index":0}]}`), path)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	if _, err := body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- body.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing partial multipart body leaked a blocked producer")
	}
	if err := os.Rename(path, path+".closed"); err != nil {
		t.Fatalf("upload file remained in use after body close: %v", err)
	}
}

func TestManifestMultipartValidatesCountNamesAndRegularFilesBeforeOpeningBody(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.jpeg")
	if err := os.WriteFile(valid, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, 9)
	for index := range tooMany {
		tooMany[index] = valid
	}
	for _, paths := range [][]string{
		tooMany,
		{filepath.Join(dir, "missing.jpg")},
		{filepath.Join(dir, "wrong.webp")},
		{dir},
	} {
		if _, err := NewManifestMultipartBody([]byte(`{}`), paths...); err == nil {
			t.Errorf("accepted paths %q", paths)
		}
	}
}

func TestManifestMultipartLargeSparseFileDoesNotBufferWholeImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.png")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	const sparseSize = int64(128 << 20)
	if err := file.Truncate(sparseSize); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	factory, err := NewManifestMultipartBody([]byte(`{"images":[{"upload_index":0}]}`), path)
	if err != nil {
		t.Fatal(err)
	}
	body, contentType, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	_, parameters, _ := mime.ParseMediaType(contentType)
	reader := multipart.NewReader(body, parameters["boundary"])
	manifest, err := reader.NextPart()
	if err != nil {
		body.Close()
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, manifest); err != nil {
		body.Close()
		t.Fatal(err)
	}
	if _, err := reader.NextPart(); err != nil {
		body.Close()
		t.Fatal(err)
	}
	started := time.Now()
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("closing a partially consumed streaming body blocked")
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("opening 128 MiB sparse upload allocated %d bytes; body appears buffered", allocated)
	}
}
