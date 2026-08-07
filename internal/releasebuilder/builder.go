package releasebuilder

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	modulePath = "github.com/fr3akX/artisan-cli"
	mainPath   = modulePath + "/cmd/artisan"
)

var (
	versionPattern = regexp.MustCompile(`^v[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	leafPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	archiveTime    = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
)

type Options struct {
	Root          string
	Version       string
	Commit        string
	Destination   string
	Go            string
	BeforePublish func() error
}

type target struct {
	goos, goarch string
}

var releaseTargets = []target{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
	{"windows", "arm64"},
}

func Build(options Options) (returnErr error) {
	if !versionPattern.MatchString(options.Version) {
		return errors.New("VERSION must be a safe v-prefixed tag value")
	}
	if !commitPattern.MatchString(options.Commit) {
		return errors.New("COMMIT must be exactly 40 hexadecimal characters")
	}
	if !leafPattern.MatchString(options.Destination) || strings.HasPrefix(options.Destination, ".") {
		return errors.New("DESTINATION must be one safe non-dot leaf name under dist")
	}
	root, err := canonicalDirectory(options.Root)
	if err != nil {
		return fmt.Errorf("canonical repository root: %w", err)
	}
	if options.Go == "" {
		options.Go = os.Getenv("GO")
		if options.Go == "" {
			options.Go = "go"
		}
	}
	if _, err := exec.LookPath(options.Go); err != nil {
		return fmt.Errorf("Go tool is unavailable: %w", err)
	}

	sources := []string{"LICENSE", "THIRD_PARTY_NOTICES.txt", "skills/artisan-inventory/SKILL.md"}
	for _, source := range sources {
		if err := requireRegularFile(filepath.Join(root, filepath.FromSlash(source))); err != nil {
			return fmt.Errorf("required source %s is unsafe: %w", source, err)
		}
	}

	dist := filepath.Join(root, "dist")
	distInfo, err := ensureRealDirectory(dist)
	if err != nil {
		return fmt.Errorf("unsafe dist directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(dist); err != nil || filepath.Clean(resolved) != dist {
		return errors.New("dist must be the canonical repository dist directory, not a link or reparse path")
	}
	final := filepath.Join(dist, options.Destination)
	if err := requireAbsent(final); err != nil {
		return fmt.Errorf("final destination must not pre-exist: %w", err)
	}

	published := false
	staging, err := os.MkdirTemp(dist, ".release-build-")
	if err != nil {
		return fmt.Errorf("create private staging directory: %w", err)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("protect staging directory: %w", err)
	}
	defer func() {
		current, boundaryErr := os.Lstat(dist)
		if boundaryErr != nil || isLinkOrReparse(current) || !current.IsDir() || !os.SameFile(distInfo, current) {
			return
		}
		if err := os.RemoveAll(staging); err != nil && returnErr == nil && !published {
			returnErr = fmt.Errorf("remove staging directory: %w", err)
		}
	}()

	publish := filepath.Join(staging, options.Destination)
	work := filepath.Join(staging, "work")
	if err := os.Mkdir(publish, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(publish, 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(work, 0o700); err != nil {
		return err
	}

	archives := make([]string, 0, len(releaseTargets))
	for _, releaseTarget := range releaseTargets {
		archiveName, err := buildTarget(root, work, publish, options, releaseTarget)
		if err != nil {
			return err
		}
		archives = append(archives, archiveName)
	}
	checksumPath := filepath.Join(publish, "checksums.txt")
	var checksumText strings.Builder
	for _, name := range archives {
		contents, err := os.ReadFile(filepath.Join(publish, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&checksumText, "%x  %s\n", sha256.Sum256(contents), name)
	}
	if len(archives) != 6 {
		return errors.New("checksum manifest must contain exactly six archives")
	}
	if err := os.WriteFile(checksumPath, []byte(checksumText.String()), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(checksumPath, 0o644); err != nil {
		return err
	}
	if err := verifyChecksums(publish, archives); err != nil {
		return err
	}
	if options.BeforePublish != nil {
		if err := options.BeforePublish(); err != nil {
			return fmt.Errorf("before publish: %w", err)
		}
	}

	currentDistInfo, err := os.Lstat(dist)
	if err != nil || isLinkOrReparse(currentDistInfo) || !currentDistInfo.IsDir() || !os.SameFile(distInfo, currentDistInfo) {
		return errors.New("dist boundary changed before publish")
	}
	resolvedDist, err := filepath.EvalSymlinks(dist)
	if err != nil || filepath.Clean(resolvedDist) != dist {
		return errors.New("dist boundary became unsafe before publish")
	}
	if err := requireAbsent(final); err != nil {
		return fmt.Errorf("final destination appeared before publish: %w", err)
	}
	if err := os.Rename(publish, final); err != nil {
		return fmt.Errorf("atomically publish release: %w", err)
	}
	published = true
	for _, name := range append(append([]string(nil), archives...), "checksums.txt") {
		fmt.Println(filepath.Join(final, name))
	}
	return nil
}

func buildTarget(root, work, publish string, options Options, releaseTarget target) (string, error) {
	top := fmt.Sprintf("artisan-%s-%s-%s", options.Version, releaseTarget.goos, releaseTarget.goarch)
	stage := filepath.Join(work, top)
	if err := os.MkdirAll(filepath.Join(stage, "skills", "artisan-inventory"), 0o755); err != nil {
		return "", err
	}
	for _, directory := range []string{stage, filepath.Join(stage, "skills"), filepath.Join(stage, "skills", "artisan-inventory")} {
		if err := os.Chmod(directory, 0o755); err != nil {
			return "", err
		}
	}
	for _, relative := range []string{"LICENSE", "THIRD_PARTY_NOTICES.txt", "skills/artisan-inventory/SKILL.md"} {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return "", err
		}
		destination := filepath.Join(stage, filepath.FromSlash(relative))
		if err := os.WriteFile(destination, contents, 0o644); err != nil {
			return "", err
		}
		if err := os.Chmod(destination, 0o644); err != nil {
			return "", err
		}
	}
	binary := "artisan"
	extension := ".tar.gz"
	if releaseTarget.goos == "windows" {
		binary = "artisan.exe"
		extension = ".zip"
	}
	binaryPath := filepath.Join(stage, binary)
	identity := "artisan-release:" + options.Version + ":" + options.Commit
	ldflags := fmt.Sprintf("-s -w -X %s/internal/release.Version=%s -X %s/internal/release.Commit=%s -X %s/internal/release.releaseIdentity=%s", modulePath, options.Version, modulePath, options.Commit, modulePath, identity)
	command := exec.Command(options.Go, "build", "-trimpath", "-buildvcs=false", "-ldflags="+ldflags, "-o", binaryPath, "./cmd/artisan")
	command.Dir = root
	command.Env = replaceEnv(os.Environ(), map[string]string{"CGO_ENABLED": "0", "GOOS": releaseTarget.goos, "GOARCH": releaseTarget.goarch})
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build %s/%s: %w\n%s", releaseTarget.goos, releaseTarget.goarch, err, output)
	}
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		return "", err
	}
	if err := InspectBinary(binaryPath, releaseTarget.goos, releaseTarget.goarch, options.Version, options.Commit); err != nil {
		return "", fmt.Errorf("inspect %s/%s: %w", releaseTarget.goos, releaseTarget.goarch, err)
	}
	if releaseTarget.goos == runtime.GOOS && releaseTarget.goarch == runtime.GOARCH {
		output, err := exec.Command(binaryPath, "--json", "version").CombinedOutput()
		if err != nil || !strings.Contains(string(output), `"version":"`+options.Version+`"`) || !strings.Contains(string(output), `"commit":"`+options.Commit+`"`) {
			return "", fmt.Errorf("native version smoke failed: %w: %s", err, output)
		}
	}
	archiveName := top + extension
	archivePath := filepath.Join(publish, archiveName)
	if releaseTarget.goos == "windows" {
		if err := writeZIP(archivePath, stage, top, binary); err != nil {
			return "", err
		}
	} else if err := writeTarGzip(archivePath, stage, top, binary); err != nil {
		return "", err
	}
	if err := InspectArchive(archivePath, options.Version, releaseTarget.goos, releaseTarget.goarch); err != nil {
		return "", fmt.Errorf("inspect archive %s: %w", archiveName, err)
	}
	return archiveName, nil
}

func InspectBinary(path, goos, goarch, version, commit string) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Go build info: %w", err)
	}
	if info.Path != mainPath || info.Main.Path != modulePath {
		return fmt.Errorf("unexpected module identity: path=%q module=%q", info.Path, info.Main.Path)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	for key, want := range map[string]string{"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0", "-trimpath": "true"} {
		if settings[key] != want {
			return fmt.Errorf("build setting %s=%q, want %q", key, settings[key], want)
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	identity := []byte("artisan-release:" + version + ":" + commit)
	if !bytes.Contains(contents, identity) {
		return errors.New("exact linked VERSION/COMMIT identity is missing")
	}
	return inspectNativeHeader(path, goos, goarch)
}

func inspectNativeHeader(path, goos, goarch string) error {
	switch goos {
	case "linux":
		file, err := elf.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		want := elf.EM_X86_64
		if goarch == "arm64" {
			want = elf.EM_AARCH64
		}
		if file.Machine != want {
			return fmt.Errorf("ELF machine %v, want %v", file.Machine, want)
		}
		if file.Section(".dynamic") != nil {
			return errors.New("Linux binary has a dynamic section")
		}
		for _, program := range file.Progs {
			if program.Type == elf.PT_INTERP {
				return errors.New("Linux binary has an interpreter")
			}
		}
	case "darwin":
		file, err := macho.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		want := macho.CpuAmd64
		if goarch == "arm64" {
			want = macho.CpuArm64
		}
		if file.Cpu != want {
			return fmt.Errorf("Mach-O CPU %v, want %v", file.Cpu, want)
		}
	case "windows":
		file, err := pe.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		want := uint16(pe.IMAGE_FILE_MACHINE_AMD64)
		if goarch == "arm64" {
			want = pe.IMAGE_FILE_MACHINE_ARM64
		}
		if file.Machine != want {
			return fmt.Errorf("PE machine %#x, want %#x", file.Machine, want)
		}
	default:
		return fmt.Errorf("unsupported GOOS %q", goos)
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || isLinkOrReparse(info) {
		return "", errors.New("not a real directory")
	}
	return filepath.Clean(resolved), nil
}

func ensureRealDirectory(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0o755); err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return nil, err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return nil, err
	}
	if isLinkOrReparse(info) || !info.IsDir() {
		return nil, errors.New("not a real directory")
	}
	return info, nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if isLinkOrReparse(info) || !info.Mode().IsRegular() {
		return errors.New("not a regular non-link file")
	}
	return nil
}

func requireAbsent(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return errors.New("path exists")
	}
	if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func verifyChecksums(directory string, names []string) error {
	contents, err := os.ReadFile(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(lines) != 6 || len(names) != 6 {
		return errors.New("checksum manifest does not have six entries")
	}
	for index, name := range names {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		want := fmt.Sprintf("%x  %s", sha256.Sum256(data), name)
		if lines[index] != want {
			return fmt.Errorf("checksum mismatch for %s", name)
		}
	}
	return nil
}

func replaceEnv(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		if _, replaced := replacements[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}

func archiveEntries(top, binary string) []archiveEntry {
	entries := []archiveEntry{
		{name: top + "/", mode: 0o755, directory: true},
		{name: top + "/LICENSE", mode: 0o644, source: "LICENSE"},
		{name: top + "/THIRD_PARTY_NOTICES.txt", mode: 0o644, source: "THIRD_PARTY_NOTICES.txt"},
		{name: top + "/" + binary, mode: 0o755, source: binary},
		{name: top + "/skills/", mode: 0o755, directory: true},
		{name: top + "/skills/artisan-inventory/", mode: 0o755, directory: true},
		{name: top + "/skills/artisan-inventory/SKILL.md", mode: 0o644, source: "skills/artisan-inventory/SKILL.md"},
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries
}

type archiveEntry struct {
	name, source string
	mode         int64
	directory    bool
}

func writeTarGzip(path, stage, top, binary string) (returnErr error) {
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
	for _, entry := range archiveEntries(top, binary) {
		header := &tar.Header{Name: entry.name, Mode: entry.mode, ModTime: archiveTime, Uid: 0, Gid: 0, Uname: "", Gname: "", Format: tar.FormatUSTAR}
		if entry.directory {
			header.Typeflag = tar.TypeDir
		} else {
			header.Typeflag = tar.TypeReg
			info, err := os.Stat(filepath.Join(stage, filepath.FromSlash(entry.source)))
			if err != nil {
				return err
			}
			header.Size = info.Size()
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !entry.directory {
			input, err := os.Open(filepath.Join(stage, filepath.FromSlash(entry.source)))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tarWriter, input)
			closeErr := input.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
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

func writeZIP(path, stage, top, binary string) (returnErr error) {
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
	for _, entry := range archiveEntries(top, binary) {
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
			input, err := os.Open(filepath.Join(stage, filepath.FromSlash(entry.source)))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(destination, input)
			closeErr := input.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}
