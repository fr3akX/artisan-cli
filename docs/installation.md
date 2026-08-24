# Installing Artisan CLI

Artisan CLI requires Artisan Server at commit
`bc62ac3c0f5a54e34119ee2546e0f9dca5f85fea` or later. Deploy the compatible
server before releasing or installing this CLI.

## Release downloads

Tagged release CI runs the builder in an isolated runner from a trusted,
quiescent checkout. Its path, archive, checksum, and identity validations are
point-in-time checks: they address unsafe inputs, stale paths, ordinary
concurrent builders, failures, and accidental drift, not a malicious process
under the same UID/SID mutating files between system calls or after completion.
See the bounded [release build threat model](security.md#release-build-threat-model-and-authenticity).

A tagged release publishes six archives. Select the row matching the operating
system and CPU reported by `uname -s`/`uname -m`, Windows **System type**, or
`go env GOOS GOARCH`:

| System | amd64/x86-64 | arm64/AArch64 |
|---|---|---|
| Linux | `artisan-VERSION-linux-amd64.tar.gz` | `artisan-VERSION-linux-arm64.tar.gz` |
| macOS | `artisan-VERSION-darwin-amd64.tar.gz` | `artisan-VERSION-darwin-arm64.tar.gz` |
| Windows | `artisan-VERSION-windows-amd64.zip` | `artisan-VERSION-windows-arm64.zip` |

Replace `VERSION` with the complete tag, such as `v1.0.0`. Download the archive
and `checksums.txt` from the same GitHub release.

Verify before extracting:

```sh
# Linux
sha256sum --check --ignore-missing checksums.txt

# macOS: compare this output with the matching checksums.txt line
shasum -a 256 artisan-VERSION-darwin-arm64.tar.gz
```

```powershell
# Windows: compare Hash with the matching checksums.txt line
Get-FileHash -Algorithm SHA256 .\artisan-VERSION-windows-amd64.zip
```

Each archive has one top-level directory named
`artisan-VERSION-OS-ARCH`. It contains only `artisan` (`artisan.exe` on
Windows), `LICENSE`, `RELEASE_NOTES.md`, `THIRD_PARTY_NOTICES.txt`, and
`skills/artisan-inventory/SKILL.md`, and
`skills/artisan-roast-review/SKILL.md`. The same reviewed `RELEASE_NOTES.md` is the
GitHub release body. Release automation also publishes GitHub build provenance
for the six archives and `checksums.txt`; the note is archive content, not an
additional downloadable asset.

The binaries are currently **unsigned** and macOS builds are **not notarized**.
Checksums and GitHub provenance help establish artifact identity but do not
substitute for Authenticode, Apple code signing, or notarization. OS warnings
may therefore require an explicit local trust decision.

## Put the executable on PATH

Extract the archive, then copy its executable to a directory already on `PATH`,
or add its directory to `PATH`:

```sh
# Linux (choose a user-writable PATH directory if preferred)
tar -xzf artisan-VERSION-linux-amd64.tar.gz
install -m 0755 artisan-VERSION-linux-amd64/artisan "$HOME/.local/bin/artisan"

# macOS
tar -xzf artisan-VERSION-darwin-arm64.tar.gz
install -m 0755 artisan-VERSION-darwin-arm64/artisan /usr/local/bin/artisan
```

```powershell
Expand-Archive .\artisan-VERSION-windows-amd64.zip
# Copy artisan.exe to a directory on the user or system PATH.
```

Confirm the selected build:

```sh
artisan --json version
```

## Shell completion

Create the target directory before generating a completion file. For Bash and
Fish:

```sh
mkdir -p "$HOME/.local/share/bash-completion/completions"
artisan completion bash > "$HOME/.local/share/bash-completion/completions/artisan"

mkdir -p "$HOME/.config/fish/completions"
artisan completion fish > "$HOME/.config/fish/completions/artisan.fish"
```

For Zsh, use a user-owned directory on `fpath`:

```zsh
mkdir -p "$HOME/.zfunc"
artisan completion zsh > "$HOME/.zfunc/_artisan"
```

Add `fpath=("$HOME/.zfunc" $fpath)` before `autoload -Uz compinit && compinit`
in `.zshrc`. Restart the shell, or source its configuration after installation.
Bash and Fish likewise discover their standard completion directories in a new
shell; source the generated Bash file directly if you need it immediately.

For PowerShell, create the profile directory and write a script that can be
dot-sourced without evaluating generated text from a pipeline:

```powershell
$ProfileDir = Split-Path -Parent $PROFILE
New-Item -ItemType Directory -Force -Path $ProfileDir | Out-Null
$CompletionPath = Join-Path $ProfileDir 'artisan-completion.ps1'
artisan completion powershell | Set-Content -Encoding utf8 -Path $CompletionPath
. $CompletionPath
```

Add the corresponding `. <path-to-artisan-completion.ps1>` line to the
PowerShell profile to load it in future sessions. Completion output is a raw
shell program even when the global `--json` flag is present.

## Build from source

Go 1.23.x is required. Build only from a trusted, quiescent source checkout;
do not run a release builder concurrently with untrusted same-account
processes. From that checkout:

```sh
CGO_ENABLED=0 go build -trimpath -o artisan ./cmd/artisan
./artisan --json version
```

Release builds use `CGO_ENABLED=0`; they are self-contained and need no
separately installed Go runtime. The Linux release is statically linked (the
release builder verifies it with `file` and `ldd`). Executables still use normal
OS services such as networking, DNS, certificate roots, filesystem access, and
(for terminal prompts) a terminal.

Continue with [commands and configuration](commands.md), [JSON and exit
codes](json-and-exit-codes.md), and the [security model](security.md).
