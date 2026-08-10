# Artisan CLI inventory release

Minimum compatible Artisan Server commit:
`436ffff581fd01e3b356a8fda188593cbf1cf60b`. Upgrade to that commit or later
before installing or using this CLI release.

This release adds exact EUR lot pricing and the server-authoritative
`inventory totals` read. Active members and administrators can perform every
safe financial read; only administrators can create, update, or clear prices.
Human output adds `PRICE/KG` and `ROAST COST`, while JSON preserves nullable
integer cents in `price_per_kg_eur_cents` and `roast_cost_eur_cents`. Totals
report priced and unpriced lot counts so partial valuation coverage remains
explicit; they are never reconstructed from paginated lot output.

Price flags accept canonical unsigned decimal EUR with zero, one, or two
fractional digits, for example `--price-per-kg-eur 12.34`.
Only the single whole part `0` may start with zero;
whole parts such as `00` and `01` are rejected. Zero is priced and null is
unpriced. Price changes retain idempotency and
authoritative reread requirements. A server without the compatible inventory
API returns `server_upgrade_required`.

Production smoke is read-only. Never mutate production inventory to validate
this release.

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
