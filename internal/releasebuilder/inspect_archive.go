package releasebuilder

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func InspectArchive(path, version, goos, goarch string) error {
	return InspectArchivePayloads(path, version, goos, goarch, nil)
}

func InspectArchivePayloads(path, version, goos, goarch string, expectedDigests map[string][sha256.Size]byte) error {
	top := fmt.Sprintf("artisan-%s-%s-%s", version, goos, goarch)
	binary := "artisan"
	if goos == "windows" {
		binary = "artisan.exe"
	}
	want := archiveEntries(top, binary, map[string]payloadSnapshot{})
	if strings.HasSuffix(path, ".zip") {
		return inspectZIP(path, want, expectedDigests)
	}
	return inspectTarGzip(path, want, expectedDigests)
}

func inspectTarGzip(path string, want []archiveEntry, expected map[string][sha256.Size]byte) error {
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
		entry := want[index]
		if header.Name != entry.name {
			return fmt.Errorf("tar order/name %q, want %q", header.Name, entry.name)
		}
		wantType := byte(tar.TypeReg)
		if entry.directory {
			wantType = tar.TypeDir
		}
		if header.Typeflag != wantType || header.Mode != entry.mode || header.ModTime.UTC() != archiveTime || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			return fmt.Errorf("noncanonical tar metadata for %q", header.Name)
		}
		if header.Linkname != "" {
			return fmt.Errorf("archive link rejected: %q", header.Name)
		}
		if !entry.directory && expected != nil {
			digest, ok := expected[header.Name]
			if !ok {
				return fmt.Errorf("missing expected payload digest for %q", header.Name)
			}
			actual, err := digestReader(reader, header.Size)
			if err != nil {
				return err
			}
			if actual != digest {
				return fmt.Errorf("archive payload digest mismatch for %q", header.Name)
			}
		}
		index++
	}
	if index != len(want) {
		return fmt.Errorf("tar has %d entries, want %d", index, len(want))
	}
	return nil
}

func inspectZIP(path string, want []archiveEntry, expected map[string][sha256.Size]byte) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) != len(want) {
		return fmt.Errorf("zip has %d entries, want %d", len(reader.File), len(want))
	}
	for index, file := range reader.File {
		entry := want[index]
		if file.Name != entry.name {
			return fmt.Errorf("zip order/name %q, want %q", file.Name, entry.name)
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 || (!entry.directory && !mode.IsRegular()) {
			return fmt.Errorf("zip entry has unsafe type: %q", file.Name)
		}
		if entry.directory != mode.IsDir() || int64(mode.Perm()) != entry.mode {
			return fmt.Errorf("noncanonical zip mode for %q: %v", file.Name, mode)
		}
		if !file.Modified.UTC().Equal(archiveTime) {
			return fmt.Errorf("noncanonical zip timestamp for %q: %s", file.Name, file.Modified)
		}
		if entry.directory && !strings.HasSuffix(file.Name, "/") {
			return fmt.Errorf("directory missing slash: %q", file.Name)
		}
		if filepath.IsAbs(file.Name) || strings.Contains(file.Name, "../") {
			return fmt.Errorf("unsafe zip path: %q", file.Name)
		}
		if !entry.directory && expected != nil {
			digest, ok := expected[file.Name]
			if !ok {
				return fmt.Errorf("missing expected payload digest for %q", file.Name)
			}
			source, err := file.Open()
			if err != nil {
				return err
			}
			actual, readErr := digestReader(source, int64(file.UncompressedSize64))
			closeErr := source.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			if actual != digest {
				return fmt.Errorf("archive payload digest mismatch for %q", file.Name)
			}
		}
	}
	return nil
}

func digestReader(reader io.Reader, size int64) ([sha256.Size]byte, error) {
	if size < 0 || size > maximumBinarySize {
		return [sha256.Size]byte{}, errors.New("archive payload exceeds bound")
	}
	hash := sha256.New()
	written, err := io.CopyN(hash, reader, size)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if written != size {
		return [sha256.Size]byte{}, errors.New("short archive payload")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}
