package releasebuilder

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"compress/gzip"
	"io"
	"os"
	"sort"
)

type archiveEntry struct {
	name      string
	mode      int64
	directory bool
	payload   payloadSnapshot
}

func archiveEntries(top, binary string, payloads map[string]payloadSnapshot) []archiveEntry {
	entries := []archiveEntry{
		{name: top + "/", mode: 0o755, directory: true},
		{name: top + "/LICENSE", mode: 0o644, payload: payloads["LICENSE"]},
		{name: top + "/RELEASE_NOTES.md", mode: 0o644, payload: payloads["RELEASE_NOTES.md"]},
		{name: top + "/THIRD_PARTY_NOTICES.txt", mode: 0o644, payload: payloads["THIRD_PARTY_NOTICES.txt"]},
		{name: top + "/" + binary, mode: 0o755, payload: payloads[binary]},
		{name: top + "/skills/", mode: 0o755, directory: true},
		{name: top + "/skills/artisan-inventory/", mode: 0o755, directory: true},
		{name: top + "/skills/artisan-inventory/SKILL.md", mode: 0o644, payload: payloads["skills/artisan-inventory/SKILL.md"]},
		{name: top + "/skills/artisan-roast-review/", mode: 0o755, directory: true},
		{name: top + "/skills/artisan-roast-review/SKILL.md", mode: 0o644, payload: payloads["skills/artisan-roast-review/SKILL.md"]},
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries
}

func writeTarGzip(path, top, binary string, payloads map[string]payloadSnapshot) (returnErr error) {
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if err := output.Close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return err
	}
	gzipWriter.Header.ModTime = archiveTime
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range archiveEntries(top, binary, payloads) {
		header := &tar.Header{Name: entry.name, Mode: entry.mode, ModTime: archiveTime, Uid: 0, Gid: 0, Uname: "", Gname: "", Format: tar.FormatUSTAR}
		if entry.directory {
			header.Typeflag = tar.TypeDir
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.payload.bytes))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !entry.directory {
			if _, err := tarWriter.Write(entry.payload.bytes); err != nil {
				return err
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}

func writeZIP(path, top, binary string, payloads map[string]payloadSnapshot) (returnErr error) {
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if err := output.Close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	writer := zip.NewWriter(output)
	writer.RegisterCompressor(zip.Deflate, func(destination io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(destination, flate.BestCompression)
	})
	for _, entry := range archiveEntries(top, binary, payloads) {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetModTime(archiveTime)
		mode := os.FileMode(entry.mode)
		if entry.directory {
			mode |= os.ModeDir
			header.Method = zip.Store
		}
		header.SetMode(mode)
		destination, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if !entry.directory {
			if _, err := destination.Write(entry.payload.bytes); err != nil {
				return err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}
