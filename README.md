# Artisan CLI

`artisan` is the native command-line client for Artisan Roast Server. The first
release focuses on green-coffee inventory management and ships as a static
single executable for Linux, macOS, and Windows on amd64 and arm64.

Artisan CLI requires Artisan Server commit
`4c0136fe98f6728f4bb94e416c5abe570e7f4831` or later. Deploy the compatible
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

## Inventory pricing and totals

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

## Agent skill

Inspect or explicitly install the embedded `artisan-inventory` skill:

```sh
artisan skill show
artisan skill install --directory /an/existing/agent/skill/root
```

The CLI does not assume an agent product or installation root. See the [agent
skill boundaries](docs/agent-skill.md) before enabling it.

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
