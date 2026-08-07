# Installing Artisan CLI

Artisan CLI requires Artisan Server at commit
`4c0136fe98f6728f4bb94e416c5abe570e7f4831` or later. Deploy the compatible
server before releasing or installing this CLI.

## Release downloads

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
Windows), `LICENSE`, `THIRD_PARTY_NOTICES.txt`, and
`skills/artisan-inventory/SKILL.md`. Release automation also publishes GitHub
build provenance for these assets.

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

## Build from source

Go 1.23.x is required. From a trusted source checkout:

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
