# Native Platform CI Repair Design

**Date:** 2026-08-08

## Goal

Restore the merged `artisan-inventory-cli` feature to a green GitHub Actions state on Linux, macOS, and Windows without weakening filesystem, release-publication, workflow-pinning, or private-integration security guarantees.

## Current Failures

The merged branch exposes four classes of failure:

1. Multipart image streaming can miss a rapid symlink retarget because it compares only filesystem identity; an unlinked symlink inode can be reused immediately.
2. macOS tests and release staging assume Linux path behavior. Temporary paths under `/var` resolve canonically below `/private/var`, and macOS does not support traversing a directory through `/dev/fd/<fd>/child`.
3. Windows checkout and filesystem semantics differ from Unix. Git may convert repository text to CRLF; drive-relative path checks, executable fixtures, open-file replacement, and archive executable suffixes need platform-correct expectations. The private ACL must retain inheritable user, SYSTEM, and Administrators entries on directories.
4. The live integration workflow checks out a private sibling repository without an explicit credential, and teardown currently assumes setup completed.

## Chosen Approach

Apply narrow, platform-correct fixes at each established boundary rather than skipping security tests or introducing a new repository-wide filesystem abstraction.

### Multipart snapshot integrity

Capture a symlink's textual target with its initial `Lstat` snapshot. Re-read and compare that target whenever the source path is verified. Continue validating the opened target file by handle, size, modification time, and digest. This closes inode-reuse false negatives while preserving streaming and cancellation behavior.

### macOS held-directory paths

Keep Linux `/proc/self/fd/<fd>` behavior. Add a Darwin-specific helper that resolves an open directory handle with `fcntl(F_GETPATH)`. Operations that require a pathname use the path returned for the already-held handle; publication identity remains guarded by the existing held-handle checks and native no-replace rename. Tests canonicalize temporary roots before exercising APIs whose contract rejects symlinked path components.

### Windows ACL and filesystem semantics

Construct private DACLs from an explicit protected SDDL descriptor:

- Files: full access for the current user, SYSTEM, and Built-in Administrators, with no inheritance flags.
- Directories: the same principals and access, with object/container inheritance flags.

Add a Windows-only regression that inspects the generated ACL before application and the applied directory ACL after protection. Keep strict ACL verification.

Adjust tests only where Windows fundamentally differs: cross-volume relative-path checks, `.exe` payload names, shell-script fixtures, and denial of replacing an actively opened file. A denied replacement must leave the old canonical target intact; the test may retry after the competing reader closes.

### Repository text normalization

Add `.gitattributes` rules that force LF for source-controlled workflow YAML, shell scripts, Markdown contracts, and pinned reference files. Byte-level contract tests remain strict and platform-independent.

### Private integration authentication and cleanup

Reference `secrets.ARTISAN_SERVER_REPOSITORY_TOKEN` explicitly in the private `artisan-server` checkout. Configure that repository secret from the currently authenticated GitHub CLI token without printing its value. Initialize integration process state before setup and guard teardown operations so cleanup runs safely after partial setup or checkout failure.

## Testing Strategy

Use test-driven changes for production behavior:

1. Add a deterministic multipart symlink-target regression and observe failure before recording link targets.
2. Add Darwin unit coverage for held-directory path resolution where feasible through build-tagged code, then cross-compile and exercise it in macOS CI.
3. Add Windows ACL-construction and applied-ACL assertions, then cross-compile and exercise them in Windows CI.
4. Add workflow contract assertions for the named secret, LF-sensitive tracked files, and setup-independent cleanup before editing workflow configuration.
5. Update platform-specific fixtures only after reproducing their current invalid assumptions.

Local verification uses `/tmp/go1.23.2/bin/go` because the host's `/usr/local/go` tree has a pre-existing duplicate runtime symbol. Required checks are:

- `gofmt` on changed Go files.
- `go test ./...`.
- repeated multipart and security-sensitive focused tests.
- `go test -race ./...`.
- repository contract scripts.
- Linux tests plus Darwin and Windows cross-compilation.
- `git diff --check` and a clean status review.

Final verification requires pushing the branch and observing all GitHub Actions jobs, including live integration, pass. Failures that only occur on native runners are fixed iteratively with the same focused-regression discipline.

## Security and Error Handling

- Never follow an untrusted replacement path in place of an already-held object.
- Never weaken canonical path, DACL, digest, pinning, or atomic no-replace checks to satisfy a platform.
- Preserve ambiguous-publication errors: do not roll back or delete when publication visibility is uncertain.
- Do not expose the repository token in source, logs, command arguments, or test output.
- Teardown must tolerate absent process state while still cleaning every resource that was successfully created.

## Non-Goals

- Redesigning all filesystem access behind a new abstraction.
- Supporting live integration without repository credentials.
- Broadly skipping native security tests.
- Changing release contents, CLI user-visible behavior, dependency versions, or the private server repository.

## Acceptance Criteria

- All Linux, macOS, and Windows unit, integration-contract, race, and release jobs pass.
- The live private-server integration job authenticates through `ARTISAN_SERVER_REPOSITORY_TOKEN` and always runs safe cleanup.
- The multipart symlink-retarget regression is stable under repeated execution.
- Private Windows directories verify inheritable protected ACL entries for the three approved principals.
- Release archives and manifests remain byte- and contract-compatible except for platform-required `.exe` naming already defined by the release targets.
- No security assertion is removed or relaxed merely to make CI green.
