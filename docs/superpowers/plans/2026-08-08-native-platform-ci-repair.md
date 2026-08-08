# Native Platform CI Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore every Linux, macOS, Windows, release-contract, race, and private live-integration GitHub Actions job without weakening security checks.

**Architecture:** Keep the current package boundaries and correct each native boundary in place. Multipart snapshots gain explicit symlink-target identity; Darwin resolves held directory descriptors with `F_GETPATH`; Windows constructs protected ACLs from SDDL; platform-only test assumptions are normalized rather than broad tests being skipped. Workflow contracts enforce LF checkout, explicit private-repository authentication, and teardown that is safe after partial setup.

**Tech Stack:** Go 1.23, `golang.org/x/sys/unix`, `golang.org/x/sys/windows`, Git attributes, GitHub Actions YAML, Bash, GitHub CLI.

## Global Constraints

- Preserve canonical-path, DACL, digest, action-pinning, and atomic no-replace checks.
- Do not follow an untrusted replacement path in place of an already-held object.
- Do not add or update dependencies.
- Do not expose `ARTISAN_SERVER_REPOSITORY_TOKEN` in source, logs, command arguments, or tests.
- Keep release contents unchanged except for the existing Windows `.exe` payload convention.
- Use `/tmp/go1.23.2/bin/go` locally because `/usr/local/go` has a duplicate runtime symbol.
- Make one focused commit after each task and review `git diff --check` before committing.

---

### Task 1: Stable Text Checkout and Authenticated, Setup-Independent Integration Workflow

**Files:**
- Create: `.gitattributes`
- Modify: `integration/inventory_cli_test.go:560-740`
- Modify: `.github/workflows/integration.yml:39-45,192-211`

**Interfaces:**
- Consumes: pinned `actions/checkout` SHA and existing `integration/artisan-server.ref` contract.
- Produces: checkout input `token: ${{ secrets.ARTISAN_SERVER_REPOSITORY_TOKEN }}` and a teardown script callable when no server checkout or project name exists.

- [ ] **Step 1: Add failing workflow/text contract assertions**

Extend `TestIntegrationWorkflowPinsServerAndUsesBoundedExecution` (or its nearest workflow contract test) to require:

```go
for _, required := range []string{
    "token: ${{ secrets.ARTISAN_SERVER_REPOSITORY_TOKEN }}",
    `if [[ -n "${ARTISAN_SERVER_E2E_PROJECT_NAME:-}" && -d artisan-server ]]; then`,
} {
    if !strings.Contains(string(workflow), required) {
        t.Errorf("integration workflow missing %q", required)
    }
}
```

Add a contract test that reads `.gitattributes` and requires explicit LF rules for `*.go`, `*.md`, `*.yml`, `*.yaml`, `*.sh`, `*.py`, `*.ref`, `*.mod`, `*.sum`, and `*.ps1`.

- [ ] **Step 2: Run the contract tests and verify RED**

Run:

```bash
/tmp/go1.23.2/bin/go test ./integration -run 'TestIntegrationWorkflow|TestRepositoryText' -count=1 -v
```

Expected: FAIL because `.gitattributes`, the secret input, and guarded teardown are absent.

- [ ] **Step 3: Add LF normalization**

Create `.gitattributes` with:

```gitattributes
.gitattributes text eol=lf
*.go text eol=lf
*.md text eol=lf
*.yml text eol=lf
*.yaml text eol=lf
*.sh text eol=lf
*.ps1 text eol=lf
*.py text eol=lf
*.ref text eol=lf
*.mod text eol=lf
*.sum text eol=lf
```

Run `git add --renormalize .` and verify that only expected text normalization appears; do not accept unrelated content changes.

- [ ] **Step 4: Authenticate the private checkout and guard failure-only steps**

Add this input under `Check out pinned Artisan Server`:

```yaml
          token: ${{ secrets.ARTISAN_SERVER_REPOSITORY_TOKEN }}
```

Remove `working-directory: artisan-server` from log/teardown steps whose `if:` can run after checkout failure. Guard logs with `if [[ -n "${ARTISAN_SERVER_E2E_PROJECT_NAME:-}" && -d artisan-server ]]; then`, then `cd artisan-server` before invoking the wrapper. Make teardown use the same guard and exit successfully when setup never established the project or checkout.

- [ ] **Step 5: Verify workflow contracts pass**

Run:

```bash
/tmp/go1.23.2/bin/go test ./integration -run 'TestIntegrationWorkflow|TestRepositoryText' -count=1 -v
/tmp/go1.23.2/bin/go test ./internal/releasecontract -run 'Workflow|Pinned|Source' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add .gitattributes .github/workflows/integration.yml integration/inventory_cli_test.go
git commit -m "fix: stabilize authenticated integration workflow"
```

### Task 2: Symlink-Target-Aware Multipart Snapshots

**Files:**
- Modify: `internal/api/multipart_test.go:206-404`
- Modify: `internal/api/multipart.go:39-335`

**Interfaces:**
- Consumes: `os.Lstat`, `os.Readlink`, existing handle metadata and SHA-256 fingerprints.
- Produces: `func (image multipartImage) pathMatches() bool`, backed by stored `linkTarget` and `symbolicLink` snapshot fields.

- [ ] **Step 1: Write a failing direct symlink-retarget regression**

Add:

```go
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
```

- [ ] **Step 2: Verify RED**

Run:

```bash
/tmp/go1.23.2/bin/go test ./internal/api -run TestMultipartSnapshotRejectsSymlinkRetarget -count=1 -v
```

Expected: build FAIL because `multipartImage.pathMatches` is undefined.

- [ ] **Step 3: Store and compare the textual symlink target**

Add `symbolicLink bool` and `linkTarget string` to `multipartImage`. During capture, call `os.Readlink(path)` when `linkInfo.Mode()&os.ModeSymlink != 0`. Implement `pathMatches` to:

1. `Lstat` the path.
2. Require `os.SameFile(image.linkInfo, current)`.
3. Require the current symlink/non-symlink kind to match.
4. For symlinks, require `os.Readlink(image.path) == image.linkTarget`.

Use `pathMatches` in capture's post-fingerprint check, `openVerified`, `streamVerified`, and `verifyCurrent`, while retaining exact opened-file metadata and digest checks.

- [ ] **Step 4: Verify GREEN and stress the previous flaky case**

Run:

```bash
/tmp/go1.23.2/bin/go test ./internal/api -run 'TestMultipartSnapshotRejectsSymlinkRetarget|TestManifestMultipartDetectsActiveStreamSourceMutation' -count=100
/tmp/go1.23.2/bin/go test ./internal/api -count=1
```

Expected: PASS with no blocked producer or leaked descriptor.

- [ ] **Step 5: Commit**

```bash
git add internal/api/multipart.go internal/api/multipart_test.go
git commit -m "fix: track multipart symlink targets"
```

### Task 3: Native Darwin Held-Directory Paths

**Files:**
- Modify: `internal/releasebuilder/handle_path_linux.go`
- Modify: `internal/releasebuilder/handle_path_darwin.go`
- Create: `internal/releasebuilder/handle_path_darwin_test.go`
- Modify: `internal/releasebuilder/publish_unix.go:90-145`
- Modify: `internal/releasebuilder/builder.go:150-270`

**Interfaces:**
- Consumes: an already-open directory file descriptor.
- Produces: `func directoryHandlePath(fd int) (string, error)` and `func (s *heldStage) payloadPath() (string, error)`.

- [ ] **Step 1: Add the Darwin regression**

Create a Darwin-tagged test that opens a canonical temporary directory with `unix.Open`, calls `directoryHandlePath`, and requires the result to equal `filepath.EvalSymlinks(dir)` and support `os.Stat(filepath.Join(got, child))`.

- [ ] **Step 2: Verify the current Darwin implementation is invalid**

Run a Darwin compile locally:

```bash
GOOS=darwin GOARCH=amd64 /tmp/go1.23.2/bin/go test ./internal/releasebuilder -run TestDirectoryHandlePath -c -o /tmp/releasebuilder-darwin.test
```

Expected: compile may succeed, while the existing native macOS CI evidence remains RED with `/dev/fd/.../payload: not a directory`. Record that native execution is required for this regression.

- [ ] **Step 3: Implement `F_GETPATH` and propagate errors**

Linux returns `fmt.Sprintf("/proc/self/fd/%d", fd), nil`. Darwin allocates `unix.MAXPATHLEN`, calls:

```go
_, _, errno := unix.Syscall(
    unix.SYS_FCNTL,
    uintptr(fd),
    uintptr(unix.F_GETPATH),
    uintptr(unsafe.Pointer(&buffer[0])),
)
```

Reject nonzero `errno` and a missing NUL terminator; return the canonical string before NUL. In `createStaging`, resolve the stage handle and clean up if resolution fails. Change `payloadPath` to resolve the currently held payload handle on every call so its path follows native publication renames. Update every caller in `builder.go` to handle and wrap the error before `Chmod` or final held-payload validation.

- [ ] **Step 4: Verify native builds remain valid**

Run:

```bash
/tmp/go1.23.2/bin/go test ./internal/releasebuilder -count=1
GOOS=darwin GOARCH=amd64 /tmp/go1.23.2/bin/go test ./internal/releasebuilder -c -o /tmp/releasebuilder-darwin-amd64.test
GOOS=darwin GOARCH=arm64 /tmp/go1.23.2/bin/go test ./internal/releasebuilder -c -o /tmp/releasebuilder-darwin-arm64.test
```

Expected: PASS/compile success.

- [ ] **Step 5: Commit**

```bash
git add internal/releasebuilder/handle_path_*.go internal/releasebuilder/publish_unix.go internal/releasebuilder/builder.go
git commit -m "fix: resolve held directory paths on Darwin"
```

### Task 4: Canonical macOS Test Roots and Cross-Platform Fixture Semantics

**Files:**
- Modify: `internal/skill/content_test.go:100-285`
- Modify: `integration/inventory_cli_test.go:330-425`
- Modify: `internal/releasebuilder/builder_test.go:580-655,790-870`
- Modify: `internal/releasecontract/release_contract_test.go:190-260,910-980`

**Interfaces:**
- Consumes: `filepath.EvalSymlinks`, `filepath.VolumeName`, `runtime.GOOS`.
- Produces: test helpers that canonicalize temporary roots and compare checkout isolation only when paths share a volume.

- [ ] **Step 1: Preserve the native failures as the RED evidence**

Document the existing macOS failures (`/var` versus `/private/var`, unsafe root components) and Windows failures (different volumes, missing `.exe`) in the task notes. No production behavior is changed in this task.

- [ ] **Step 2: Canonicalize temporary API roots**

Add a test helper:

```go
func canonicalTempDir(t *testing.T) string {
    t.Helper()
    root := t.TempDir()
    canonical, err := filepath.EvalSymlinks(root)
    if err != nil {
        t.Fatal(err)
    }
    return canonical
}
```

Use it in cross-platform skill and integration tests that intentionally pass trusted roots. For the explicit working-directory shell fixture, compare output against its canonical path on macOS. Skip shell-script executable fixtures on Windows with a specific message; native Windows executable behavior remains covered by the CI smoke build.

- [ ] **Step 3: Correct release test isolation across Windows volumes**

Before calling `filepath.Rel(source, root)`, compare nonempty `filepath.VolumeName` values case-insensitively. Different volumes are inherently isolated and must not call `Rel`. On Darwin, canonicalize copied temporary roots after creation before passing them to the builder.

- [ ] **Step 4: Use the native executable archive name**

In tests that construct a tar payload for `runtime.GOOS`, choose:

```go
binary := "artisan"
if runtime.GOOS == "windows" {
    binary += ".exe"
}
```

Use `binary` consistently in `writeTarGzip`, expected archive entries, and payload reads.

- [ ] **Step 5: Verify Linux and cross-compilation**

Run:

```bash
/tmp/go1.23.2/bin/go test ./internal/skill ./integration ./internal/releasebuilder ./internal/releasecontract -count=1
GOOS=windows GOARCH=amd64 /tmp/go1.23.2/bin/go test -exec=/bin/true ./integration ./internal/releasebuilder ./internal/releasecontract
GOOS=darwin GOARCH=amd64 /tmp/go1.23.2/bin/go test -exec=/bin/true ./integration ./internal/skill ./internal/releasebuilder ./internal/releasecontract
```

The cross-target commands compile each package and use `/bin/true` instead of attempting to execute a foreign test binary. Expected: native Linux PASS and all target packages compile.

- [ ] **Step 6: Commit**

```bash
git add internal/skill/content_test.go integration/inventory_cli_test.go internal/releasebuilder/builder_test.go internal/releasecontract/release_contract_test.go
git commit -m "test: honor native filesystem semantics"
```

### Task 5: Strict Windows Private ACL Construction

**Files:**
- Create: `internal/securefile/securefile_windows_internal_test.go`
- Modify: `internal/securefile/securefile_windows.go:155-199`
- Modify: `internal/securefile/securefile_windows_test.go:1-110` only if assertion diagnostics need refinement

**Interfaces:**
- Consumes: current process user SID and Windows SDDL aliases `SY` and `BA`.
- Produces: `privateACL(directory bool) (*windows.ACL, error)` with exactly three full-access ACEs and exact directory inheritance flags.

- [ ] **Step 1: Add an internal ACL-construction regression**

In package `securefile`, call `privateACL(true)` and inspect every ACE with `windows.GetAce`. Require exactly three allowed ACEs and exact flags `OBJECT_INHERIT_ACE|CONTAINER_INHERIT_ACE`. Repeat with `privateACL(false)` and require zero flags. The existing external `TestPrivateWindowsACLsForDirectoryAndFile` continues to verify the applied protected DACL.

- [ ] **Step 2: Verify Windows RED on a native runner contract**

Cross-compile locally:

```bash
GOOS=windows GOARCH=amd64 /tmp/go1.23.2/bin/go test ./internal/securefile -c -o /tmp/securefile-windows.test.exe
```

Expected: compile success. Existing Windows CI is the runtime RED evidence: `directory ACE flags = 0x0, want 0x3`.

- [ ] **Step 3: Build the DACL from protected SDDL**

Replace `ACLFromEntries` construction with a descriptor string using the current user SID:

```go
flags := ""
if directory {
    flags = "OICI"
}
sddl := fmt.Sprintf(
    "D:P(A;%s;GA;;;%s)(A;%s;GA;;;SY)(A;%s;GA;;;BA)",
    flags, user.User.Sid.String(), flags, flags,
)
descriptor, err := windows.SecurityDescriptorFromString(sddl)
acl, _, err := descriptor.DACL()
```

Return descriptive errors for descriptor and DACL extraction, keep the descriptor alive through DACL use, and remove the now-unused `runtime` import. Retain `verifyPrivateHandle` unchanged so protected state, exact principals, exact flags, duplicate rejection, and full access remain mandatory.

- [ ] **Step 4: Verify Linux unaffected and Windows compiles**

Run:

```bash
/tmp/go1.23.2/bin/go test ./internal/securefile -count=1
GOOS=windows GOARCH=amd64 /tmp/go1.23.2/bin/go test ./internal/securefile -c -o /tmp/securefile-windows.test.exe
```

Expected: Linux PASS and Windows compile success; native CI must execute the regression.

- [ ] **Step 5: Commit**

```bash
git add internal/securefile/securefile_windows.go internal/securefile/securefile_windows_internal_test.go internal/securefile/securefile_windows_test.go
git commit -m "fix: preserve Windows private ACL inheritance"
```

### Task 6: Windows Open-Reader Atomic Replacement Contract

**Files:**
- Modify: `internal/skill/content_test.go:245-285`

**Interfaces:**
- Consumes: existing `Install(root, true)` replacement semantics.
- Produces: a test contract allowing Windows to deny replacement while a reader has the target open, while requiring the old target to remain complete and a retry after reader closure to succeed.

- [ ] **Step 1: Refine the failing native test without weakening atomicity**

After stopping the reader, if `runtime.GOOS == "windows" && installErr != nil`:

1. Read the target and require it still equals the complete old payload.
2. Retry `Install(root, true)` after the competing reader is closed.
3. Require retry success and final exact embedded `Content`.

On Unix, retain the existing requirement that the first replacement succeeds while the reader runs. On every platform, reject partial bytes and leftover temporary files.

- [ ] **Step 2: Verify package tests and Windows compilation**

Run:

```bash
/tmp/go1.23.2/bin/go test ./internal/skill -run TestForcedInstallAtomicallyReplacesVisibleContent -count=100
GOOS=windows GOARCH=amd64 /tmp/go1.23.2/bin/go test ./internal/skill -c -o /tmp/skill-windows.test.exe
```

Expected: Linux stress PASS and Windows compile success.

- [ ] **Step 3: Commit**

```bash
git add internal/skill/content_test.go
git commit -m "test: handle Windows reader replacement denial"
```

### Task 7: Full Verification, Repository Secret, and Native CI

**Files:**
- Modify only if a verification failure demonstrates a focused defect.
- Verify: all changed files and GitHub repository secret configuration.

**Interfaces:**
- Consumes: commits from Tasks 1-6 and authenticated `gh` CLI.
- Produces: green local/race/contract checks, configured secret metadata, pushed branch, and green native GitHub Actions evidence.

- [ ] **Step 1: Format and inspect**

Run:

```bash
gofmt -w $(git diff main --name-only -- '*.go')
git diff --check
git status --short
git diff --stat main...
```

Expected: no formatting/whitespace failures and only planned files changed.

- [ ] **Step 2: Run focused stress and full tests**

Run:

```bash
/tmp/go1.23.2/bin/go test ./internal/api -run 'Multipart.*(Mutation|Retarget)|SnapshotRejectsSymlink' -count=100
/tmp/go1.23.2/bin/go test ./internal/skill -run TestForcedInstallAtomicallyReplacesVisibleContent -count=100
/tmp/go1.23.2/bin/go test ./... -count=1
/tmp/go1.23.2/bin/go test -race ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run repository contracts and native compile matrix**

Run the repository's tracked contract scripts discovered under `scripts/`, then compile every package for Darwin amd64/arm64 and Windows amd64 with unique outputs. At minimum:

```bash
GOOS=darwin GOARCH=amd64 /tmp/go1.23.2/bin/go test -exec=/bin/true ./...
GOOS=darwin GOARCH=arm64 /tmp/go1.23.2/bin/go test -exec=/bin/true ./...
GOOS=windows GOARCH=amd64 /tmp/go1.23.2/bin/go test -exec=/bin/true ./...
```

Expected: all compile checks PASS.

- [ ] **Step 4: Configure the selected repository secret securely**

Confirm authentication and set the secret through stdin:

```bash
gh auth status
gh auth token | gh secret set ARTISAN_SERVER_REPOSITORY_TOKEN --repo fr3akX/artisan-cli
gh secret list --repo fr3akX/artisan-cli
```

Expected: the secret name appears with an updated timestamp; no token value appears.

- [ ] **Step 5: Request independent code review**

Use the `requesting-code-review` skill against `main...HEAD`. Resolve every confirmed high/medium finding with a focused regression and rerun affected checks.

- [ ] **Step 6: Push and verify native CI**

```bash
git push -u origin fix/ci-native-platforms
gh run list --branch fix/ci-native-platforms --limit 20
gh run watch <run-id> --exit-status
```

Watch every workflow triggered for the branch rather than only the first run. For a native-only failure, download the failed log, add or tighten a focused regression, fix minimally, rerun local checks, push, and watch the replacement run.

- [ ] **Step 7: Record final evidence**

Record exact passing commands, run URLs/IDs, commit range, changed files, and any residual native risk. Verify:

```bash
git status --short
git log --oneline main..HEAD
```

Expected: clean worktree and all task commits present.
