package api

import (
	"bytes"
	"errors"
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

func TestMultipartSnapshotRejectsSymlinkRetarget(t *testing.T) {
	dir := t.TempDir()
	first := writeMultipartActiveSource(t, dir, "first.jpg", 1024)
	second := writeMultipartActiveSource(t, dir, "second.jpg", 1024)
	link := filepath.Join(dir, "link.jpg")
	if err := os.Symlink(first, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	image, err := captureMultipartImage(link, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	if image.pathMatches() {
		t.Fatal("multipart snapshot accepted a retargeted symbolic link")
	}
}

func TestManifestMultipartDetectsActiveStreamSourceMutation(t *testing.T) {
	const imageSize = 1 << 20
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, dir string) (string, func())
	}{
		{
			name: "append",
			prepare: func(t *testing.T, dir string) (string, func()) {
				path := writeMultipartActiveSource(t, dir, "append.jpg", imageSize)
				return path, func() {
					file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := file.Write([]byte("appended")); err != nil {
						file.Close()
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "truncate",
			prepare: func(t *testing.T, dir string) (string, func()) {
				path := writeMultipartActiveSource(t, dir, "truncate.jpg", imageSize)
				return path, func() {
					if err := os.Truncate(path, imageSize/2); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "same size content with restored mtime",
			prepare: func(t *testing.T, dir string) (string, func()) {
				path := writeMultipartActiveSource(t, dir, "content.jpg", imageSize)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				return path, func() {
					file, err := os.OpenFile(path, os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := file.WriteAt(bytes.Repeat([]byte("z"), 4096), 3*imageSize/4); err != nil {
						file.Close()
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "mtime only",
			prepare: func(t *testing.T, dir string) (string, func()) {
				path := writeMultipartActiveSource(t, dir, "mtime.jpg", imageSize)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				return path, func() {
					changed := info.ModTime().Add(3 * time.Second)
					if err := os.Chtimes(path, changed, changed); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "symlink retarget",
			prepare: func(t *testing.T, dir string) (string, func()) {
				first := writeMultipartActiveSource(t, dir, "first.jpg", imageSize)
				second := writeMultipartActiveSource(t, dir, "second.jpg", imageSize)
				link := filepath.Join(dir, "link.jpg")
				if err := os.Symlink(first, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				return link, func() {
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(second, link); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path, mutate := test.prepare(t, dir)
			factory, err := NewManifestMultipartBody([]byte(`{"images":[{"upload_index":0}]}`), path)
			if err != nil {
				t.Fatal(err)
			}
			body, contentType, err := factory()
			if err != nil {
				t.Fatal(err)
			}
			_, parameters, err := mime.ParseMediaType(contentType)
			if err != nil {
				t.Fatal(err)
			}
			reader := multipart.NewReader(body, parameters["boundary"])
			manifest, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, manifest); err != nil {
				t.Fatal(err)
			}
			image, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(image, make([]byte, imageSize/4)); err != nil {
				t.Fatal(err)
			}
			mutate()
			_, readErr := io.Copy(io.Discard, image)
			closeDone := make(chan error, 1)
			go func() { closeDone <- body.Close() }()
			var closeErr error
			select {
			case closeErr = <-closeDone:
			case <-time.After(2 * time.Second):
				t.Fatal("active mutation leaked a blocked multipart producer")
			}
			if !isChangedMultipartError(readErr) && !isChangedMultipartError(closeErr) {
				t.Fatalf("read error = %v, close error = %v", readErr, closeErr)
			}
			assertNoOpenMultipartDescriptor(t, dir)
		})
	}
}

func TestManifestMultipartActiveStreamPathReplacementUsesNativeSemantics(t *testing.T) {
	const imageSize = 1 << 20
	dir := t.TempDir()
	path := writeMultipartActiveSource(t, dir, "replace.jpg", imageSize)
	factory, err := NewManifestMultipartBody([]byte(`{"images":[{"upload_index":0}]}`), path)
	if err != nil {
		t.Fatal(err)
	}
	body, contentType, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = body.Close() })
	_, parameters, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(body, parameters["boundary"])
	manifest, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, manifest); err != nil {
		t.Fatal(err)
	}
	image, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(image, make([]byte, imageSize/4)); err != nil {
		t.Fatal(err)
	}

	replacement := filepath.Join(dir, "replacement.jpg")
	if err := os.WriteFile(replacement, bytes.Repeat([]byte("z"), imageSize), 0o600); err != nil {
		t.Fatal(err)
	}
	renameErr := os.Rename(replacement, path)
	remaining, readErr := io.ReadAll(image)
	closeErr := body.Close()
	if runtime.GOOS == "windows" {
		if !errors.Is(renameErr, os.ErrPermission) {
			t.Fatalf("Windows open-reader replacement error = %v, want permission/sharing denial", renameErr)
		}
		if readErr != nil || closeErr != nil || len(remaining) != 3*imageSize/4 || !bytes.Equal(remaining, bytes.Repeat([]byte("a"), len(remaining))) {
			t.Fatalf("denied replacement changed original stream: bytes=%d read=%v close=%v", len(remaining), readErr, closeErr)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatalf("replacement after close: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := bytes.Repeat([]byte("z"), imageSize)
		if !bytes.Equal(got, want) {
			t.Fatal("replacement target content differs from replacement source")
		}
		if _, err := os.Stat(replacement); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement source still exists after rename: %v", err)
		}
	} else {
		if renameErr != nil {
			t.Fatal(renameErr)
		}
		if !isChangedMultipartError(readErr) && !isChangedMultipartError(closeErr) {
			t.Fatalf("read error = %v, close error = %v", readErr, closeErr)
		}
	}
	assertNoOpenMultipartDescriptor(t, dir)
}

func writeMultipartActiveSource(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), size), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func isChangedMultipartError(err error) bool {
	var failure *multipartFileError
	return errors.As(err, &failure) && failure.changed
}

func assertNoOpenMultipartDescriptor(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot inspect descriptors: %v", err)
	}
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			continue
		}
		target = strings.TrimSuffix(target, " (deleted)")
		if strings.HasPrefix(filepath.Clean(target), prefix) {
			t.Fatalf("multipart source descriptor remained open: %s", entry.Name())
		}
	}
}

func TestManifestMultipartRechecksEarlierImagesBeforeFinalBoundary(t *testing.T) {
	dir := t.TempDir()
	first := writeMultipartActiveSource(t, dir, "first.jpg", 64<<10)
	second := writeMultipartActiveSource(t, dir, "second.jpg", 1<<20)
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := NewManifestMultipartBody([]byte(`{"images":[{"upload_index":0},{"upload_index":1}]}`), first, second)
	if err != nil {
		t.Fatal(err)
	}
	body, contentType, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	_, parameters, _ := mime.ParseMediaType(contentType)
	reader := multipart.NewReader(body, parameters["boundary"])
	manifest, _ := reader.NextPart()
	_, _ = io.Copy(io.Discard, manifest)
	firstPart, _ := reader.NextPart()
	if _, err := io.Copy(io.Discard, firstPart); err != nil {
		t.Fatal(err)
	}
	secondPart, _ := reader.NextPart()
	if _, err := io.ReadFull(secondPart, make([]byte, 1<<18)); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(first, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(bytes.Repeat([]byte("z"), 4096), 0); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(first, firstInfo.ModTime(), firstInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, secondPart)
	closeErr := body.Close()
	if !isChangedMultipartError(readErr) && !isChangedMultipartError(closeErr) {
		t.Fatalf("earlier image mutation was accepted: read=%v close=%v", readErr, closeErr)
	}
	assertNoOpenMultipartDescriptor(t, dir)
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

func TestManifestMultipartEnforcesPerImageSizeBeforeHashing(t *testing.T) {
	original := fingerprintMultipartFileHook
	defer func() { fingerprintMultipartFileHook = original }()
	var hashes int
	fingerprintMultipartFileHook = func(file *os.File, size int64, cancel <-chan struct{}) ([32]byte, error) {
		hashes++
		return fingerprintMultipartFileCancelable(file, size, cancel)
	}

	for _, test := range []struct {
		name       string
		size       int64
		wantError  bool
		wantHashes int
	}{
		{name: "empty", size: 0, wantError: true, wantHashes: 0},
		{name: "exact limit", size: 10 * 1024 * 1024, wantHashes: 1},
		{name: "over limit", size: 10*1024*1024 + 1, wantError: true, wantHashes: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			hashes = 0
			path := filepath.Join(t.TempDir(), "image.png")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(test.size); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			factory, err := NewManifestMultipartBody([]byte(`{"images":[{"upload_index":0}]}`), path)
			if (err != nil) != test.wantError || hashes != test.wantHashes {
				t.Fatalf("factory=%v err=%v hashes=%d", factory != nil, err, hashes)
			}
			if err == nil {
				for attempt := 0; attempt < 2; attempt++ {
					body, _, openErr := factory()
					if openErr != nil {
						t.Fatal(openErr)
					}
					if _, copyErr := io.Copy(io.Discard, body); copyErr != nil {
						_ = body.Close()
						t.Fatal(copyErr)
					}
					if closeErr := body.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
				}
			}
		})
	}
}
