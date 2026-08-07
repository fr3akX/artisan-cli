package releasebuilder

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
