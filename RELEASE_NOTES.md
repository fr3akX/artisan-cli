# Artisan CLI release notes

## v0.4.0

Minimum compatible Artisan Server commit:
`bc62ac3c0f5a54e34119ee2546e0f9dca5f85fea`. Upgrade to that commit or later
before installing or using this CLI release.

This release adds private roast list/show/revision/comment reads,
integrity-checked chart and raw-profile downloads, and the fixed
`artisan-roast-review-v1` posting endpoint. The configured host agent performs
AI analysis of private profile data; the server and CLI do not call a provider.
After valid analysis, the skill posts automatically as an ordinary private
user-authored organization comment.

A single first-writer review slot exists per immutable revision and template.
Member and administrator credentials have equivalent access to that dedicated
endpoint. A replay returns the existing comment, a stale never-posted revision
is rejected, and deleted comments are not recreated. Profile text is untrusted
prompt-injection input. Downloads verify hashes and refuse to clobber an
existing destination unless `--force` is explicit.

Archives now carry both `skills/artisan-inventory/SKILL.md` and
`skills/artisan-roast-review/SKILL.md`; no-name skill show/install remains
inventory-compatible.

Production smoke is read-only. Never mutate production inventory to validate
this release.

Download the one matching archive from the six supported OS/architecture
builds and verify it against `checksums.txt` and the published GitHub build
provenance before extracting it. Each archive includes the executable,
`RELEASE_NOTES.md`, license and third-party notices, and both embedded skills.
Release binaries use `CGO_ENABLED=0` and need no separately installed Go
runtime.

The binaries are unsigned, and macOS binaries are not notarized. Checksums and
provenance establish artifact identity but do not replace OS code signing or
notarization. Provide bearer tokens through stdin, use HTTPS for non-loopback
servers, and review the installation and security documentation before use.
