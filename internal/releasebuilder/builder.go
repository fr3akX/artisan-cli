package releasebuilder

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	modulePath        = "github.com/fr3akX/artisan-cli"
	mainPath          = modulePath + "/cmd/artisan"
	maximumSourceSize = 2 << 20
	maximumBinarySize = 64 << 20
	maximumOutputSize = 64 << 10
)

var (
	versionPattern = regexp.MustCompile(`^v[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	leafPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	archiveTime    = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
)

type Options struct {
	Root                string
	Version             string
	Commit              string
	Destination         string
	Go                  string
	FileTool            string
	LDDTool             string
	CommandTimeout      time.Duration
	AfterSourceSnapshot func() error
	AfterBinarySnapshot func(goos, goarch, path string) error
	BeforePublish       func() error
	AfterPublish        func() error
}

type target struct{ goos, goarch string }

var releaseTargets = []target{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}}

type payloadSnapshot struct {
	bytes  []byte
	digest [sha256.Size]byte
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
	if options.FileTool == "" {
		options.FileTool = "file"
	}
	if options.LDDTool == "" {
		options.LDDTool = "ldd"
	}
	if options.CommandTimeout <= 0 {
		options.CommandTimeout = 10 * time.Second
	}

	sources := make(map[string]payloadSnapshot, 3)
	for _, relative := range []string{"LICENSE", "THIRD_PARTY_NOTICES.txt", "skills/artisan-inventory/SKILL.md"} {
		contents, err := readRegularSnapshot(filepath.Join(root, filepath.FromSlash(relative)), maximumSourceSize)
		if err != nil {
			return fmt.Errorf("snapshot required source %s: %w", relative, err)
		}
		sources[relative] = payloadSnapshot{bytes: contents, digest: sha256.Sum256(contents)}
	}
	if options.AfterSourceSnapshot != nil {
		if err := options.AfterSourceSnapshot(); err != nil {
			return fmt.Errorf("after source snapshot: %w", err)
		}
	}

	distPath := filepath.Join(root, "dist")
	if _, err := ensureRealDirectory(distPath); err != nil {
		return fmt.Errorf("unsafe dist directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(distPath)
	if err != nil || filepath.Clean(resolved) != distPath {
		return errors.New("dist must be the canonical repository dist directory")
	}
	dist, err := openHeldDist(distPath)
	if err != nil {
		return fmt.Errorf("hold canonical dist directory: %w", err)
	}
	defer dist.close()
	if !dist.pathMatches() {
		return errors.New("requested dist path does not match held directory")
	}
	exists, err := dist.finalExists(options.Destination)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("final destination must not pre-exist")
	}

	stage, err := dist.createStaging()
	if err != nil {
		return fmt.Errorf("create private held staging directory: %w", err)
	}
	published := false
	defer func() {
		if err := dist.cleanup(stage); err != nil && returnErr == nil && !published {
			returnErr = fmt.Errorf("remove held staging directory: %w", err)
		}
	}()
	publishPath := filepath.Join(stage.path, "payload")
	workPath := filepath.Join(stage.path, "build-work")
	if err := os.Mkdir(publishPath, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(publishPath, 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(workPath, 0o700); err != nil {
		return err
	}

	archives := make([]string, 0, len(releaseTargets))
	for _, releaseTarget := range releaseTargets {
		name, err := buildTarget(root, workPath, publishPath, options, releaseTarget, sources)
		if err != nil {
			return err
		}
		archives = append(archives, name)
	}
	var manifest strings.Builder
	for _, name := range archives {
		contents, err := os.ReadFile(filepath.Join(publishPath, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(contents), name)
	}
	if len(archives) != 6 {
		return errors.New("checksum manifest must contain exactly six archives")
	}
	checksumPath := filepath.Join(publishPath, "checksums.txt")
	if err := os.WriteFile(checksumPath, []byte(manifest.String()), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(checksumPath, 0o644); err != nil {
		return err
	}
	if err := verifyChecksums(publishPath, archives); err != nil {
		return err
	}
	if err := stage.preparePayload(); err != nil {
		return fmt.Errorf("hold completed payload: %w", err)
	}
	if options.BeforePublish != nil {
		if err := options.BeforePublish(); err != nil {
			return fmt.Errorf("before publish: %w", err)
		}
	}
	if !dist.pathMatches() {
		return errors.New("requested dist identity changed before publish")
	}
	if err := dist.publish(stage, options.Destination); err != nil {
		if isAlreadyExists(err) {
			return fmt.Errorf("final destination appeared before atomic publish: %w", err)
		}
		return fmt.Errorf("atomic no-replace publish: %w", err)
	}
	published = true
	if options.AfterPublish != nil {
		_ = options.AfterPublish()
	}
	if !dist.pathMatches() {
		if rollbackErr := dist.rollback(stage, options.Destination); rollbackErr == nil {
			published = false
			return errors.New("requested dist identity changed after publish; publication rolled back")
		}
		return errors.New("requested dist identity changed after publish; publication status is confined to held original dist")
	}
	// Do not print lexical paths: the held directory is authoritative, while the
	// requested path can be renamed by another process after the final check.
	return nil
}

func buildTarget(root, work, publish string, options Options, releaseTarget target, sources map[string]payloadSnapshot) (string, error) {
	top := fmt.Sprintf("artisan-%s-%s-%s", options.Version, releaseTarget.goos, releaseTarget.goarch)
	binary := "artisan"
	extension := ".tar.gz"
	if releaseTarget.goos == "windows" {
		binary = "artisan.exe"
		extension = ".zip"
	}
	binaryPath := filepath.Join(work, top+"-"+binary)
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
	if releaseTarget.goos == runtime.GOOS && releaseTarget.goarch == runtime.GOARCH {
		if err := runNativeSmoke(binaryPath, options.Version, options.Commit, options.CommandTimeout); err != nil {
			return "", err
		}
	}
	if runtime.GOOS == "linux" && releaseTarget.goos == "linux" && releaseTarget.goarch == "amd64" {
		if err := verifyNativeLinuxTools(binaryPath, options.FileTool, options.LDDTool, options.CommandTimeout); err != nil {
			return "", err
		}
	}
	binaryBytes, err := readRegularSnapshot(binaryPath, maximumBinarySize)
	if err != nil {
		return "", fmt.Errorf("snapshot built binary %s/%s: %w", releaseTarget.goos, releaseTarget.goarch, err)
	}
	if err := InspectBinaryBytes(binaryBytes, releaseTarget.goos, releaseTarget.goarch, options.Version, options.Commit); err != nil {
		return "", fmt.Errorf("inspect %s/%s snapshot: %w", releaseTarget.goos, releaseTarget.goarch, err)
	}
	if options.AfterBinarySnapshot != nil {
		if err := options.AfterBinarySnapshot(releaseTarget.goos, releaseTarget.goarch, binaryPath); err != nil {
			return "", err
		}
	}
	payloads := make(map[string]payloadSnapshot, len(sources)+1)
	for name, snapshot := range sources {
		payloads[name] = snapshot
	}
	payloads[binary] = payloadSnapshot{bytes: binaryBytes, digest: sha256.Sum256(binaryBytes)}
	archiveName := top + extension
	archivePath := filepath.Join(publish, archiveName)
	if releaseTarget.goos == "windows" {
		err = writeZIP(archivePath, top, binary, payloads)
	} else {
		err = writeTarGzip(archivePath, top, binary, payloads)
	}
	if err != nil {
		return "", err
	}
	expected := make(map[string][sha256.Size]byte)
	for _, entry := range archiveEntries(top, binary, payloads) {
		if !entry.directory {
			expected[entry.name] = entry.payload.digest
		}
	}
	if err := InspectArchivePayloads(archivePath, options.Version, releaseTarget.goos, releaseTarget.goarch, expected); err != nil {
		return "", fmt.Errorf("inspect archive %s: %w", archiveName, err)
	}
	return archiveName, nil
}

func InspectBinary(path, goos, goarch, version, commit string) error {
	contents, err := readRegularSnapshot(path, maximumBinarySize)
	if err != nil {
		return err
	}
	return InspectBinaryBytes(contents, goos, goarch, version, commit)
}

func InspectBinaryBytes(contents []byte, goos, goarch, version, commit string) error {
	reader := bytes.NewReader(contents)
	info, err := buildinfo.Read(reader)
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
	if !bytes.Contains(contents, []byte("artisan-release:"+version+":"+commit)) {
		return errors.New("exact linked VERSION/COMMIT identity is missing")
	}
	return inspectNativeHeaderBytes(reader, goos, goarch)
}

func inspectNativeHeaderBytes(reader io.ReaderAt, goos, goarch string) error {
	switch goos {
	case "linux":
		file, err := elf.NewFile(reader)
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
		file, err := macho.NewFile(reader)
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
		file, err := pe.NewFile(reader)
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

func runNativeSmoke(binary, version, commit string, timeout time.Duration) error {
	output, exitErr, runErr := runBounded(timeout, maximumOutputSize, nil, binary, "--json", "version")
	if runErr != nil {
		return fmt.Errorf("native version smoke: %w", runErr)
	}
	if exitErr != nil {
		return fmt.Errorf("native version smoke exited unsuccessfully: %w: %s", exitErr, output)
	}
	var envelope struct {
		Ok   bool `json:"ok"`
		Data struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("native version JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("native version output must contain exactly one JSON value and EOF")
	}
	if !envelope.Ok || envelope.Data.Version != version || envelope.Data.Commit != commit {
		return errors.New("native version envelope identity mismatch")
	}
	return nil
}

func verifyNativeLinuxTools(binary, fileTool, lddTool string, timeout time.Duration) error {
	environment := replaceEnv(os.Environ(), map[string]string{"LC_ALL": "C", "LANG": "C"})
	fileOutput, fileExit, err := runBounded(timeout, maximumOutputSize, environment, fileTool, binary)
	if err != nil {
		return fmt.Errorf("file validation: %w", err)
	}
	if fileExit != nil {
		return fmt.Errorf("file validation exited unsuccessfully: %w: %s", fileExit, fileOutput)
	}
	fileText := string(fileOutput)
	if !strings.Contains(fileText, "ELF 64-bit LSB executable") || !strings.Contains(fileText, "x86-64") || (!strings.Contains(fileText, "statically linked") && !strings.Contains(fileText, "static-pie linked")) {
		return fmt.Errorf("file did not confirm exact static x86-64 ELF: %s", fileOutput)
	}
	lddOutput, lddExit, err := runBounded(timeout, maximumOutputSize, environment, lddTool, binary)
	if err != nil {
		return fmt.Errorf("ldd validation: %w", err)
	}
	if lddExit == nil {
		return fmt.Errorf("ldd unexpectedly succeeded: %s", lddOutput)
	}
	lddText := strings.ToLower(string(lddOutput))
	if !strings.Contains(lddText, "not a dynamic executable") && !strings.Contains(lddText, "not dynamically linked") && !strings.Contains(lddText, "statically linked") {
		return fmt.Errorf("ldd did not confirm no dynamic linkage: %s", lddOutput)
	}
	return nil
}

type cappedBuffer struct {
	mutex    sync.Mutex
	bytes    []byte
	maximum  int
	overflow bool
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	available := buffer.maximum - len(buffer.bytes)
	if available > 0 {
		count := len(data)
		if count > available {
			count = available
		}
		buffer.bytes = append(buffer.bytes, data[:count]...)
	}
	if len(data) > available {
		buffer.overflow = true
	}
	return len(data), nil
}
func runBounded(timeout time.Duration, maximum int, environment []string, name string, args ...string) ([]byte, error, error) {
	contextValue, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(contextValue, name, args...)
	command.WaitDelay = 2 * time.Second
	if environment != nil {
		command.Env = environment
	}
	output := &cappedBuffer{maximum: maximum}
	command.Stdout = output
	command.Stderr = output
	cleanup, startErr := startContainedProcess(command)
	if startErr != nil {
		return output.bytes, nil, startErr
	}
	defer cleanup()
	exitErr := command.Wait()
	if contextValue.Err() != nil {
		return output.bytes, nil, fmt.Errorf("command timed out and was killed: %w", contextValue.Err())
	}
	if output.overflow {
		return output.bytes, nil, errors.New("command output exceeded bound")
	}
	return output.bytes, exitErr, nil
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
func randomStagingName() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return ".release-build-" + hex.EncodeToString(data), nil
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
		if lines[index] != fmt.Sprintf("%x  %s", sha256.Sum256(data), name) {
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
