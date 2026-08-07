package releasebuilder

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func InspectArchive(path, version, goos, goarch string) error {
	top := fmt.Sprintf("artisan-%s-%s-%s", version, goos, goarch)
	binary := "artisan"
	if goos == "windows" {
		binary = "artisan.exe"
	}
	want := archiveEntries(top, binary)
	if strings.HasSuffix(path, ".zip") {
		return inspectZIP(path, want)
	}
	return inspectTarGzip(path, want)
}

func inspectTarGzip(path string, want []archiveEntry) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	compressed, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer compressed.Close()
	if !compressed.Header.ModTime.Equal(archiveTime) || compressed.Header.Name != "" || compressed.Header.Comment != "" || compressed.Header.OS != 255 {
		return errors.New("noncanonical gzip header")
	}
	reader := tar.NewReader(compressed)
	index := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if index >= len(want) {
			return fmt.Errorf("unexpected tar entry %q", header.Name)
		}
		expected := want[index]
		if header.Name != expected.name {
			return fmt.Errorf("tar order/name %q, want %q", header.Name, expected.name)
		}
		wantType := byte(tar.TypeReg)
		if expected.directory {
			wantType = tar.TypeDir
		}
		if header.Typeflag != wantType || header.Mode != expected.mode || header.ModTime.UTC() != archiveTime || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			return fmt.Errorf("noncanonical tar metadata for %q", header.Name)
		}
		if header.Linkname != "" {
			return fmt.Errorf("archive link rejected: %q", header.Name)
		}
		index++
	}
	if index != len(want) {
		return fmt.Errorf("tar has %d entries, want %d", index, len(want))
	}
	return nil
}

func inspectZIP(path string, want []archiveEntry) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) != len(want) {
		return fmt.Errorf("zip has %d entries, want %d", len(reader.File), len(want))
	}
	for index, file := range reader.File {
		expected := want[index]
		if file.Name != expected.name {
			return fmt.Errorf("zip order/name %q, want %q", file.Name, expected.name)
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 || (!expected.directory && !mode.IsRegular()) {
			return fmt.Errorf("zip entry has unsafe type: %q", file.Name)
		}
		if expected.directory != mode.IsDir() || int64(mode.Perm()) != expected.mode {
			return fmt.Errorf("noncanonical zip mode for %q: %v", file.Name, mode)
		}
		if !file.Modified.UTC().Equal(archiveTime) {
			return fmt.Errorf("noncanonical zip timestamp for %q: %s", file.Name, file.Modified)
		}
		if expected.directory && !strings.HasSuffix(file.Name, "/") {
			return fmt.Errorf("directory missing slash: %q", file.Name)
		}
		if filepath.IsAbs(file.Name) || strings.Contains(file.Name, "../") {
			return fmt.Errorf("unsafe zip path: %q", file.Name)
		}
	}
	return nil
}
