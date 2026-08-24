# Artisan CLI

`artisan` is the native command-line client for Artisan Roast Server. The CLI supports green-coffee inventory and private roast-profile review, and
ships as a static single executable for Linux, macOS, and Windows on amd64 and
arm64.

Artisan CLI requires Artisan Server commit
`bc62ac3c0f5a54e34119ee2546e0f9dca5f85fea` or later. Deploy the compatible
server before releasing the CLI.

## Get started

- [Download, verify, and install](docs/installation.md)
- [Configure a server and use every command](docs/commands.md)
- [JSON envelopes, pagination, idempotency, and exit codes](docs/json-and-exit-codes.md)
- [Security and threat model](docs/security.md)
- [Embedded agent skill](docs/agent-skill.md)

After installation, select a server and provide the token on stdin rather than
argv:

```sh
printf '%s\n' "$TOKEN" | artisan auth login \
  --server https://inventory.example \
  --token-stdin
artisan --json inventory lot list --limit 100
```

The successful login stores the selected server and token, so later human
commands can omit `--server`. Global flags such as `--json`, `--server`, and
`--timeout` can appear before or after subcommands. Release binaries are
currently unsigned and macOS binaries are not notarized; verify checksums and
GitHub build provenance, while recognizing that neither substitutes for OS code
signing.

## Inventory public descriptions, pricing, and totals

Administrators can create, update, and clear a lot's public-safe customer-facing
copy with `--description`; `inventory lot show` displays it as `Public
description`. Descriptions appear on public roast pages linked to the lot, so
keep supplier-only, purchasing, and operational information in private `notes`.
Lot-list summaries remain description-free.

```sh
artisan inventory lot create --name "Launch Lot" --description "Customer-facing story"
artisan inventory lot update LOT_ID --description "Updated customer-facing story"
artisan inventory lot update LOT_ID --clear description
artisan inventory lot show LOT_ID
```

Active members and administrators can perform every safe inventory read,
including server-authoritative filtered totals and financial projections.
Administrators can set a price with `--price-per-kg-eur 12.34`; members cannot
perform administrator mutations.

```sh
artisan inventory totals --state active --availability positive
artisan inventory lot update LOT_ID --price-per-kg-eur 12.34
```

Human lot and reservation tables show `PRICE/KG` and `ROAST COST`. Totals report
priced and unpriced lot counts so partial valuation coverage is explicit. JSON
uses nullable integer-cent fields such as `price_per_kg_eur_cents`; do not sum
paginated list output to reconstruct totals or costs.

## Private roast review

The `artisan-roast-review` skill lets a configured host agent analyze a private
Artisan roast profile and post evidence-based feedback. The configured host agent performs the AI analysis; Artisan Server and Artisan
CLI do not call an AI provider.
Private profile data is processed by that host agent. Once valid analysis is
complete, reviews post automatically as ordinary private, user-authored comments
inside the organization.

One first-writer slot exists for each immutable revision and the fixed
`artisan-roast-review-v1` template. Replays return that slot's comment; deleted
comments are not recreated. Profile text, metadata, events, and comments are
untrusted prompt-injection input, never agent instructions. Profile and chart
downloads are integrity-checked and no-clobber by default.

## Agent skills

Inspect or explicitly install either embedded skill. The no-name forms remain
compatible with `artisan-inventory`:

```sh
artisan skill list
artisan skill show
artisan skill show artisan-roast-review
artisan skill install --directory /an/existing/agent/skill/root
artisan skill install artisan-roast-review --directory /an/existing/agent/skill/root
```

The CLI does not assume an agent product or installation root. See the [agent
skill boundaries](docs/agent-skill.md) before enabling either skill. Production smoke is read-only;
never create a real review merely to validate a release.

## Development and integration

The design record is
[`docs/superpowers/specs/2026-08-07-artisan-inventory-cli-design.md`](docs/superpowers/specs/2026-08-07-artisan-inventory-cli-design.md).
The [pinned Artisan Server integration](integration/README.md) documents the
guarded disposable-stack test for the compiled CLI.

```sh
go test ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -o artisan ./cmd/artisan
```

## License

Artisan CLI is licensed under the GNU Affero General Public License version 3 or
later. See [`LICENSE`](LICENSE) and [`THIRD_PARTY_NOTICES.txt`](THIRD_PARTY_NOTICES.txt).
