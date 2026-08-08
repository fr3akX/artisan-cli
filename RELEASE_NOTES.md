# Artisan CLI inventory release

Minimum compatible Artisan Server commit:
`4c0136fe98f6728f4bb94e416c5abe570e7f4831`. Upgrade to that commit or later
before installing or using this CLI release.

Download the one matching archive from the six supported OS/architecture
builds and verify it against `checksums.txt` and the published GitHub build
provenance before extracting it. Each archive includes the executable,
`RELEASE_NOTES.md`, license and third-party notices, and the inventory skill.
Release binaries use `CGO_ENABLED=0` and need no separately installed Go
runtime.

The binaries are unsigned, and macOS binaries are not notarized. Checksums and
provenance establish artifact identity but do not replace OS code signing or
notarization. Provide bearer tokens through stdin, use HTTPS for non-loopback
servers, and review the installation and security documentation before use.
