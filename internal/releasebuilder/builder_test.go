package releasebuilder

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	contractVersion = "v0.0.0-builder-test"
	contractCommit  = "0123456789abcdef0123456789abcdef01234567"
)

func TestBuildRejectsUnsafeDestinationBeforeBuild(t *testing.T) {
	root := fakeRoot(t)
	sentinel := filepath.Join(filepath.Dir(root), "escape")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, leaf := range []string{"", ".", "..", ".hidden", "../escape", "nested/release", `nested\\release`, "/tmp/release", "dist/release"} {
		t.Run(strings.ReplaceAll(leaf, "/", "_"), func(t *testing.T) {
			err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: leaf, Go: filepath.Join(root, "missing-go")})
			if err == nil {
				t.Fatalf("unsafe destination %q accepted", leaf)
			}
			assertFileContents(t, sentinel, "keep")
		})
	}
}

func TestBuildRejectsSymlinkedDistAndFinalWithoutTouchingExternalFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	t.Run("dist", func(t *testing.T) {
		root := fakeRoot(t)
		external := t.TempDir()
		sentinel := filepath.Join(external, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, "dist")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(root, "dist")); err != nil {
			t.Fatal(err)
		}
		if err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "release", Go: "missing"}); err == nil {
			t.Fatal("symlinked dist accepted")
		}
		assertFileContents(t, sentinel, "keep")
	})
	t.Run("final", func(t *testing.T) {
		root := fakeRoot(t)
		external := t.TempDir()
		sentinel := filepath.Join(external, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(root, "dist", "release")); err != nil {
			t.Fatal(err)
		}
		if err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "release", Go: "missing"}); err == nil {
			t.Fatal("symlinked final accepted")
		}
		assertFileContents(t, sentinel, "keep")
	})
}

func TestBuildRefusesPreexistingFinal(t *testing.T) {
	root := fakeRoot(t)
	final := filepath.Join(root, "dist", "release")
	if err := os.Mkdir(final, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(final, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "release", Go: "missing"}); err == nil {
		t.Fatal("preexisting final accepted")
	}
	assertFileContents(t, sentinel, "keep")
}

func TestBuildLateFailureLeavesFinalAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the release matrix")
	}
	root := repositoryRoot(t)
	leaf := "builder-late-failure-test"
	final := filepath.Join(root, "dist", leaf)
	_ = os.RemoveAll(final)
	sentinel := filepath.Join(root, "late-failure-sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(final); _ = os.Remove(sentinel) })
	injected := errors.New("injected late failure")
	err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: leaf, Go: goCommand(t), BeforePublish: func() error { return injected }})
	if !errors.Is(err, injected) {
		t.Fatalf("got %v, want injected failure", err)
	}
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		t.Fatalf("partial final visible after failure: %v", err)
	}
	assertFileContents(t, sentinel, "keep")
}

func TestBuildBeforePublishRacesFailWithoutOverwriteOrEscape(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("deterministic symlink swap fixture is Linux-specific")
	}
	t.Run("competitor", func(t *testing.T) {
		root := copyBuildRoot(t)
		leaf := "competitor"
		final := filepath.Join(root, "dist", leaf)
		err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: leaf, Go: goCommand(t), BeforePublish: func() error {
			if err := os.Mkdir(final, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(final, "sentinel"), []byte("keep"), 0o600)
		}})
		if err == nil {
			t.Fatal("competitor was overwritten")
		}
		assertFileContents(t, filepath.Join(final, "sentinel"), "keep")
	})
	t.Run("dist swap", func(t *testing.T) {
		root := copyBuildRoot(t)
		distPath := filepath.Join(root, "dist")
		oldPath := filepath.Join(root, "dist-original")
		external := t.TempDir()
		sentinel := filepath.Join(external, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "release", Go: goCommand(t), BeforePublish: func() error {
			if err := os.Rename(distPath, oldPath); err != nil {
				return err
			}
			return os.Symlink(external, distPath)
		}})
		if err == nil {
			t.Fatal("dist swap returned success")
		}
		assertFileContents(t, sentinel, "keep")
		if _, statErr := os.Lstat(filepath.Join(external, "release")); !os.IsNotExist(statErr) {
			t.Fatalf("release escaped held dist: %v", statErr)
		}
		if removeErr := os.Remove(distPath); removeErr != nil {
			t.Fatal(removeErr)
		}
		if renameErr := os.Rename(oldPath, distPath); renameErr != nil {
			t.Fatal(renameErr)
		}
	})
}

func TestBuildRejectsPayloadFinalAndStageIdentitySwaps(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("deterministic rename race fixture is Linux-specific")
	}
	findStage := func(t *testing.T, root string) string {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(root, "dist", ".release-build-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("stage matches=%v err=%v", matches, err)
		}
		return matches[0]
	}
	t.Run("payload immediately before native rename", func(t *testing.T) {
		root := copyBuildRoot(t)
		err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "release", Go: goCommand(t), BeforeNativeRename: func() error {
			stage := findStage(t, root)
			if err := os.Rename(filepath.Join(stage, "payload"), filepath.Join(stage, "payload-held")); err != nil {
				return err
			}
			if err := os.Mkdir(filepath.Join(stage, "payload"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(stage, "payload", "competitor"), []byte("keep"), 0o600)
		}})
		if err == nil {
			t.Fatal("payload identity swap reported success")
		}
		stage := findStage(t, root)
		assertFileContents(t, filepath.Join(stage, "payload", "competitor"), "keep")
		if _, statErr := os.Lstat(filepath.Join(root, "dist", "release")); !os.IsNotExist(statErr) {
			t.Fatalf("wrong payload published: %v", statErr)
		}
	})
	t.Run("final immediately after native rename", func(t *testing.T) {
		root := copyBuildRoot(t)
		err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "release", Go: goCommand(t), AfterNativeRename: func() error {
			dist := filepath.Join(root, "dist")
			if err := os.Rename(filepath.Join(dist, "release"), filepath.Join(dist, "verified-payload")); err != nil {
				return err
			}
			if err := os.Mkdir(filepath.Join(dist, "release"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dist, "release", "competitor"), []byte("keep"), 0o600)
		}})
		if err == nil {
			t.Fatal("final identity swap reported success")
		}
		assertFileContents(t, filepath.Join(root, "dist", "release", "competitor"), "keep")
		if _, statErr := os.Lstat(filepath.Join(root, "dist", "verified-payload", "checksums.txt")); statErr != nil {
			t.Fatalf("verified payload was deleted: %v", statErr)
		}
	})
	t.Run("requested dist after verified publish", func(t *testing.T) {
		root := copyBuildRoot(t)
		external := t.TempDir()
		sentinel := filepath.Join(external, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		original := filepath.Join(root, "dist-original")
		err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "release", Go: goCommand(t), AfterPublish: func() error {
			if err := os.Rename(filepath.Join(root, "dist"), original); err != nil {
				return err
			}
			return os.Symlink(external, filepath.Join(root, "dist"))
		}})
		if err == nil {
			t.Fatal("post-publish dist swap reported success")
		}
		assertFileContents(t, sentinel, "keep")
		if _, statErr := os.Lstat(filepath.Join(external, "release")); !os.IsNotExist(statErr) {
			t.Fatalf("publication escaped: %v", statErr)
		}
		if _, statErr := os.Lstat(filepath.Join(original, "release", "checksums.txt")); statErr != nil {
			t.Fatalf("verified payload deleted: %v", statErr)
		}
	})
	t.Run("stage name before cleanup", func(t *testing.T) {
		root := copyBuildRoot(t)
		var replacement string
		err := Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "release", Go: goCommand(t), BeforeCleanup: func(name string) error {
			dist := filepath.Join(root, "dist")
			if err := os.Rename(filepath.Join(dist, name), filepath.Join(dist, name+"-held")); err != nil {
				return err
			}
			replacement = filepath.Join(dist, name)
			if err := os.Mkdir(replacement, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(replacement, "competitor"), []byte("keep"), 0o600)
		}})
		if err == nil {
			t.Fatal("stage identity swap reported success")
		}
		assertFileContents(t, filepath.Join(replacement, "competitor"), "keep")
		if _, statErr := os.Lstat(filepath.Join(root, "dist", "release", "checksums.txt")); statErr != nil {
			t.Fatalf("published payload missing: %v", statErr)
		}
	})
}

func TestInspectBinaryRejectsMislabeledTarget(t *testing.T) {
	root := repositoryRoot(t)
	binary := filepath.Join(t.TempDir(), "artisan")
	ldflags := "-s -w -X github.com/fr3akX/artisan-cli/internal/release.Version=" + contractVersion + " -X github.com/fr3akX/artisan-cli/internal/release.Commit=" + contractCommit + " -X github.com/fr3akX/artisan-cli/internal/release.releaseIdentity=artisan-release:" + contractVersion + ":" + contractCommit
	command := exec.Command(goCommand(t), "build", "-trimpath", "-buildvcs=false", "-ldflags="+ldflags, "-o", binary, "./cmd/artisan")
	command.Dir = root
	command.Env = replaceEnv(os.Environ(), map[string]string{"CGO_ENABLED": "0", "GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH})
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	wrongArch := "arm64"
	if runtime.GOARCH == "arm64" {
		wrongArch = "amd64"
	}
	if err := InspectBinary(binary, runtime.GOOS, wrongArch, contractVersion, contractCommit); err == nil {
		t.Fatal("mislabeled binary accepted")
	}
	if err := InspectBinary(binary, runtime.GOOS, runtime.GOARCH, contractVersion+"-wrong", contractCommit); err == nil {
		t.Fatal("wrong exact VERSION metadata accepted")
	}
}

func TestHeldPublishNeverReplacesCompetitorAndSurvivesDistSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink race requires Unix privileges; Windows implementation is cross-compiled")
	}
	t.Run("competitor", func(t *testing.T) {
		root := fakeRoot(t)
		distPath := filepath.Join(root, "dist")
		dist, err := openHeldDist(distPath)
		if err != nil {
			t.Fatal(err)
		}
		defer dist.close()
		stage, err := dist.createStaging()
		if err != nil {
			t.Fatal(err)
		}
		defer dist.cleanup(stage)
		if err := os.Mkdir(filepath.Join(stage.path, "payload"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := stage.preparePayload(); err != nil {
			t.Fatal(err)
		}
		competitor := filepath.Join(distPath, "release")
		if err := os.Mkdir(competitor, 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(competitor, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := dist.publish(stage, "release", nil, nil); err == nil {
			t.Fatal("atomic no-replace publish overwrote competitor")
		}
		assertFileContents(t, sentinel, "keep")
	})
	t.Run("dist swap before publish", func(t *testing.T) {
		root := fakeRoot(t)
		distPath := filepath.Join(root, "dist")
		oldPath := filepath.Join(root, "dist-original")
		external := t.TempDir()
		sentinel := filepath.Join(external, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		dist, err := openHeldDist(distPath)
		if err != nil {
			t.Fatal(err)
		}
		defer dist.close()
		stage, err := dist.createStaging()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(stage.path, "payload"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := stage.preparePayload(); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(distPath, oldPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, distPath); err != nil {
			t.Fatal(err)
		}
		if dist.pathMatches() {
			t.Fatal("swapped dist matched held identity")
		}
		assertFileContents(t, sentinel, "keep")
		if _, err := os.Lstat(filepath.Join(external, "release")); !os.IsNotExist(err) {
			t.Fatalf("wrote outside held dist: %v", err)
		}
		if err := dist.cleanup(stage); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(distPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(oldPath, distPath); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("dist swap after publish rolls back", func(t *testing.T) {
		root := fakeRoot(t)
		distPath := filepath.Join(root, "dist")
		oldPath := filepath.Join(root, "dist-original")
		external := t.TempDir()
		sentinel := filepath.Join(external, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		dist, err := openHeldDist(distPath)
		if err != nil {
			t.Fatal(err)
		}
		defer dist.close()
		stage, err := dist.createStaging()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(stage.path, "payload"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage.path, "payload", "complete"), []byte("yes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := stage.preparePayload(); err != nil {
			t.Fatal(err)
		}
		if err := dist.publish(stage, "release", nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(distPath, oldPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, distPath); err != nil {
			t.Fatal(err)
		}
		if dist.pathMatches() {
			t.Fatal("post-publish swap matched")
		}
		assertFileContents(t, sentinel, "keep")
		if _, err := os.Lstat(filepath.Join(external, "release")); !os.IsNotExist(err) {
			t.Fatalf("post-publish path escaped: %v", err)
		}
		assertFileContents(t, filepath.Join(oldPath, "release", "complete"), "yes")
		if err := dist.cleanup(stage); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(distPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(oldPath, distPath); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBuildHooksCannotChangeSnapshottedSourcesOrBinaries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release matrix snapshot fixture is Linux-specific")
	}
	root := copyBuildRoot(t)
	originalLicense, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	var inspectedBinary []byte
	err = Build(Options{Root: root, Version: contractVersion, Commit: contractCommit, Destination: "work", Go: goCommand(t), AfterSourceSnapshot: func() error {
		return os.WriteFile(filepath.Join(root, "LICENSE"), []byte("changed after source snapshot"), 0o644)
	}, AfterBinarySnapshot: func(goos, goarch, path string) error {
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if goos == "linux" && goarch == "amd64" {
			inspectedBinary = append([]byte(nil), contents...)
		}
		return os.WriteFile(path, []byte("changed after binary snapshot"), 0o755)
	}})
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "dist", "work", "artisan-"+contractVersion+"-linux-amd64.tar.gz")
	top := "artisan-" + contractVersion + "-linux-amd64"
	gotLicense, gotBinary := readTarPayloads(t, archivePath, top+"/LICENSE", top+"/artisan")
	if !bytes.Equal(gotLicense, originalLicense) || !bytes.Equal(gotBinary, inspectedBinary) {
		t.Fatal("Build archive escaped immutable inspected snapshots")
	}
}

func TestSnapshotBytesRemainBoundToInspectedArchivePayload(t *testing.T) {
	root := repositoryRoot(t)
	temporary := t.TempDir()
	binaryPath := filepath.Join(temporary, "artisan")
	ldflags := "-s -w -X github.com/fr3akX/artisan-cli/internal/release.Version=" + contractVersion + " -X github.com/fr3akX/artisan-cli/internal/release.Commit=" + contractCommit + " -X github.com/fr3akX/artisan-cli/internal/release.releaseIdentity=artisan-release:" + contractVersion + ":" + contractCommit
	command := exec.Command(goCommand(t), "build", "-trimpath", "-buildvcs=false", "-ldflags="+ldflags, "-o", binaryPath, "./cmd/artisan")
	command.Dir = root
	command.Env = replaceEnv(os.Environ(), map[string]string{"CGO_ENABLED": "0", "GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH})
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build snapshot fixture: %v\n%s", err, output)
	}
	binaryBytes, err := readRegularSnapshot(binaryPath, maximumBinarySize)
	if err != nil {
		t.Fatal(err)
	}
	if err := InspectBinaryBytes(binaryBytes, runtime.GOOS, runtime.GOARCH, contractVersion, contractCommit); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(temporary, "LICENSE")
	if err := os.WriteFile(sourcePath, []byte("inspected source"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceBytes, err := readRegularSnapshot(sourcePath, maximumSourceSize)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("changed source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("changed binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	payloads := map[string]payloadSnapshot{"LICENSE": {bytes: sourceBytes, digest: sha256.Sum256(sourceBytes)}, "THIRD_PARTY_NOTICES.txt": {bytes: []byte("notice"), digest: sha256.Sum256([]byte("notice"))}, "skills/artisan-inventory/SKILL.md": {bytes: []byte("skill"), digest: sha256.Sum256([]byte("skill"))}, "artisan": {bytes: binaryBytes, digest: sha256.Sum256(binaryBytes)}}
	archivePath := filepath.Join(temporary, "snapshot.tar.gz")
	top := "artisan-" + contractVersion + "-" + runtime.GOOS + "-" + runtime.GOARCH
	if err := writeTarGzip(archivePath, top, "artisan", payloads); err != nil {
		t.Fatal(err)
	}
	expected := map[string][sha256.Size]byte{}
	for _, entry := range archiveEntries(top, "artisan", payloads) {
		if !entry.directory {
			expected[entry.name] = entry.payload.digest
		}
	}
	if err := InspectArchivePayloads(archivePath, contractVersion, runtime.GOOS, runtime.GOARCH, expected); err != nil {
		t.Fatal(err)
	}
	wrongExpected := make(map[string][sha256.Size]byte, len(expected))
	for name, digest := range expected {
		wrongExpected[name] = digest
	}
	wrongExpected[top+"/LICENSE"] = sha256.Sum256([]byte("changed payload"))
	if err := InspectArchivePayloads(archivePath, contractVersion, runtime.GOOS, runtime.GOARCH, wrongExpected); err == nil {
		t.Fatal("archive inspector accepted changed expected payload digest")
	}
	gotSource, gotBinary := readTarPayloads(t, archivePath, top+"/LICENSE", top+"/artisan")
	if !bytes.Equal(gotSource, sourceBytes) || !bytes.Equal(gotBinary, binaryBytes) {
		t.Fatal("archive did not contain inspected immutable snapshots")
	}
}

func TestNativeSmokeIsBoundedAndParsesOneExactEnvelope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixtures are Unix-specific")
	}
	valid := `{"ok":true,"data":{"version":"` + contractVersion + `","commit":"` + contractCommit + `"}}`
	cases := []struct {
		name, body string
		wantOK     bool
		timeout    time.Duration
	}{
		{"valid", "printf '%s' '" + valid + "'", true, time.Second},
		{"nested substring", "printf '%s' '{\"ok\":true,\"data\":{\"version\":\"wrong " + contractVersion + "\",\"commit\":\"wrong " + contractCommit + "\"}}'", false, time.Second},
		{"trailing JSON", "printf '%s' '" + valid + " {}'", false, time.Second},
		{"trailing prose", "printf '%s' '" + valid + " prose'", false, time.Second},
		{"overflow", "head -c 70000 /dev/zero | tr '\\0' x", false, time.Second},
		{"hang", "sleep 5", false, 50 * time.Millisecond},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			script := writeScript(t, test.body)
			err := runNativeSmoke(script, contractVersion, contractCommit, test.timeout)
			if (err == nil) != test.wantOK {
				t.Fatalf("runNativeSmoke error=%v wantOK=%v", err, test.wantOK)
			}
		})
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	body := "sleep 30 >/dev/null 2>&1 & child=$!; echo $child > '" + pidFile + "'; printf '%s\\n' '" + valid + "'"
	script := writeScript(t, body)
	if err := runNativeSmoke(script, contractVersion, contractCommit, time.Second); err != nil {
		t.Fatalf("parent-exit fixture: %v", err)
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if processExists(pid) {
		t.Fatalf("child %d remained after helper", pid)
	}
	marker := filepath.Join(t.TempDir(), "leaked-child")
	script = writeScript(t, "(sleep 0.2; echo leaked > '"+marker+"') & wait")
	if err := runNativeSmoke(script, contractVersion, contractCommit, 50*time.Millisecond); err == nil {
		t.Fatal("process-tree hang accepted")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("timed-out child leaked: %v", err)
	}
}

func TestLinuxNativeToolValidationRejectsMissingHangAndMisreport(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux tool contract")
	}
	goodFile := writeScript(t, "echo 'fixture: ELF 64-bit LSB executable, x86-64, statically linked'")
	goodLDD := writeScript(t, "echo 'not a dynamic executable' >&2; exit 1")
	if err := verifyNativeLinuxTools("fixture", goodFile, goodLDD, time.Second); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, file, ldd string
		timeout         time.Duration
	}{
		{"missing", "/definitely/missing/file", goodLDD, time.Second}, {"hanging", writeScript(t, "sleep 5"), goodLDD, 50 * time.Millisecond}, {"file misreport", writeScript(t, "echo 'ELF dynamic ARM'"), goodLDD, time.Second}, {"ldd succeeds", goodFile, writeScript(t, "echo 'not a dynamic executable'; exit 0"), time.Second}, {"ldd misreports", goodFile, writeScript(t, "echo 'libc.so'; exit 1"), time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyNativeLinuxTools("fixture", test.file, test.ldd, test.timeout); err == nil {
				t.Fatal("invalid native tool result accepted")
			}
		})
	}
}

func readTarPayloads(t *testing.T, path string, names ...string) ([]byte, []byte) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	found := map[string][]byte{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if header.Name == name {
				found[name], err = io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return found[names[0]], found[names[1]]
}
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func copyBuildRoot(t *testing.T) string {
	t.Helper()
	source := repositoryRoot(t)
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"cmd", "internal", "skills", "go.mod", "go.sum", "LICENSE", "THIRD_PARTY_NOTICES.txt"} {
		sourcePath := filepath.Join(source, relative)
		err := filepath.WalkDir(sourcePath, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			suffix, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			destination := filepath.Join(root, suffix)
			if entry.IsDir() {
				return os.MkdirAll(destination, 0o755)
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(destination, contents, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func fakeRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for path := range map[string]bool{"LICENSE": true, "THIRD_PARTY_NOTICES.txt": true, "skills/artisan-inventory/SKILL.md": true} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(path), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s changed: %q", path, got)
	}
}

func goCommand(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("GO"); configured != "" {
		return configured
	}
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go unavailable")
	}
	return path
}
