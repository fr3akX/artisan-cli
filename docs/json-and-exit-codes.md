# JSON, output, pagination, and exit codes

This contract applies with Artisan Server commit
`4c0136fe98f6728f4bb94e416c5abe570e7f4831` or later. The compatible server
must be deployed before the CLI is released.

## Output streams and envelopes

Without `--json`, successful data and help go to stdout. Human-readable errors
and terminal approval/token prompts go to stderr. Tables escape control
characters rather than emitting them directly.

With global `--json` (before the command), both success and failure envelopes go
to stdout as exactly one JSON value followed by a newline; stderr remains for a
last-resort output-write diagnostic or interactive prompt. A success is:

```json
{"ok":true,"data":{"example":"command-specific fields"}}
```

The stable shape is `{"ok":true,"data":...}`. An error is:

```json
{"ok":false,"error":{"code":"not_found","message":"Not found","http_status":404}}
```

The stable shape is `{"ok":false,"error":...}`. `code` and `message` are always
present. `http_status` appears only when a remote HTTP status is available. The
process exit code is intentionally not duplicated in JSON. Command-specific
`data` is the validated API response (or the version, skill, authentication, or
download result); consumers must reject malformed or incomplete fields.

## Stable exit-code mapping

| Exit | Meaning |
|---:|---|
| 0 | Success |
| 1 | Local runtime/input/output preparation failure, including secure idempotency-key generation |
| 2 | Usage or local validation failure |
| 3 | Missing/unsafe configuration or local storage/install failure, including an installed skill that differs and needs explicit `--force` |
| 4 | HTTP 401 only |
| 5 | HTTP 403 |
| 6 | HTTP 404 |
| 7 | Other HTTP 4xx response, including HTTP 409 |
| 8 | Network/transport failure, including request deadlines/timeouts |
| 9 | Server 5xx, redirect, invalid/oversized server response, or pagination safety failure |
| 10 | Confirmation declined, unavailable, or required for noninteractive use |
| 130 | Caller interruption (SIGINT, and SIGTERM where supported) |

An interruption produces the same stable `interrupted` error in human and JSON
modes after deferred cleanup runs. A configured timeout or deadline remains a
`network_error` with exit 8 rather than being reported as an interruption.

Remote error `code` and `message` are preserved only when bounded, valid, and
free of known token/server secrets. Redirects are never followed. A 409 remains
a normal class-7 failure and can represent a business conflict or an ambiguous
idempotency outcome; read authoritative state before deciding whether to retry.

## Pagination and bounds

`--limit` is omitted at zero (server default) or must be from 1 through 100.
`--cursor` is an opaque server value of at most 4096 bytes. Without `--all`, a
list command returns one page and human output prints `Next cursor` when one is
present. Pass that exact cursor to the next call; do not decode or construct it.

`--all` performs bounded pagination: at most 1,000 pages and 10,000 total items.
A repeated cursor, page bound, item bound, malformed page, or cancellation is a
failure, never partial success. Automation should impose its own tighter page,
item, and time budget when appropriate.

## Idempotency, quantities, and ambiguity

Every mutation sends an `Idempotency-Key`. If `--idempotency-key` is omitted,
the CLI generates a cryptographically random one. Supplied keys must match
`[A-Za-z0-9][A-Za-z0-9._:-]{0,254}`. Use one key per logical mutation and reuse
that same key only to reconcile/retry the same unchanged operation. After a
timeout, transport error, HTTP 409, or otherwise ambiguous result, read the lot
and relevant ledger/reservation/conflict data before retrying.

All stock quantities are signed or unsigned **integer grams** as specified by
the individual command. Do not pass decimals, kilograms, unit suffixes, or
floating-point conversions. Reservation create requires positive planned
grams; supplied finalize actual grams must be at least 1. Manual adjustment
requires a nonzero signed integer.

A reservation operation can return a conflict while also returning authoritative
balance fields. Conflict resolution records review state only: neither the CLI
nor `inventory conflict resolve` automatically adjusts stock. Do not transform
a 409 or conflict into an adjustment. Ambiguous IDs or multiple matching
records must be resolved by an explicit authoritative ID rather than guessed.

## Financial JSON and totals invariants

Financial fields are machine values, never presentation strings.
`price_per_kg_eur_cents` is integer cents or `null`: zero is a priced value and
null means unpriced. `roast_cost_eur_cents` is a nonnegative safe integer or
null. Human-only `€` and `/kg` formatting never appears in JSON.

`inventory totals` returns the server object with `lot_count`,
`on_hand_grams`, `reserved_grams`, `available_grams`,
`on_hand_value_eur_cents`, `priced_lot_count`, and `unpriced_lot_count`.
Validated invariants require nonnegative safe counts,
`priced_lot_count + unpriced_lot_count == lot_count`, and
`available_grams == on_hand_grams - reserved_grams`. Valuation is null exactly
when `priced_lot_count` is zero; otherwise it is a signed safe integer. Partial
valuation is valid and consumers must report both coverage counts.

Totals are authoritative across all matching lots; they must not be summed from paginated list output.
Reservation costs are likewise server-authoritative;
clients must not compute either value locally.

A missing compatible inventory namespace is the stable
`server_upgrade_required` server upgrade error with exit 9. It is distinct from
an entity-specific 404, which remains exit 6.

See [commands](commands.md) and [security](security.md).
