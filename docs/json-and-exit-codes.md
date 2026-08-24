# JSON, output, pagination, and exit codes

This contract applies with Artisan Server commit
`bc62ac3c0f5a54e34119ee2546e0f9dca5f85fea` or later. The compatible server
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

## Bean lot description JSON

`inventory lot show` full detail requires `description` as a nullable string.
For example, a full detail object can be:

```json
{
  "lot_id": "11111111111111111111111111111111",
  "name": "Launch Lot",
  "origin": null,
  "processing_method": null,
  "crop_year": null,
  "state": "active",
  "price_per_kg_eur_cents": null,
  "on_hand_grams": 0,
  "reserved_grams": 0,
  "available_grams": 0,
  "unresolved_conflict_count": 0,
  "cover_image": null,
  "updated_at": "2026-08-10T12:00:00.000000Z",
  "producer": null,
  "supplier": null,
  "external_reference": null,
  "received_date": null,
  "varietals": [],
  "sca_score": null,
  "processing_detail": null,
  "altitude_min_metres": null,
  "altitude_max_metres": null,
  "description": "Customer-facing story\nSecond paragraph",
  "notes": null,
  "images": [],
  "created_at": "2026-08-10T12:00:00.000000Z",
  "archived_at": null,
  "links": {
    "self": "/api/v1/inventory/read/bean-lots/11111111111111111111111111111111",
    "ledger": "/api/v1/inventory/read/bean-lots/11111111111111111111111111111111/ledger",
    "reservations": "/api/v1/inventory/read/bean-lots/11111111111111111111111111111111/reservations"
  }
}
```

Lot-list summary objects remain unchanged and do not contain `description`.
Strict create JSON includes the nullable `description` key inside `fields`; a
representative complete field object uses:

```json
{
  "fields": {
    "name": "Launch Lot",
    "origin": null,
    "producer": null,
    "supplier": null,
    "external_reference": null,
    "received_date": null,
    "crop_year": null,
    "price_per_kg_eur_cents": null,
    "varietals": [],
    "sca_score": null,
    "processing_method": null,
    "processing_detail": null,
    "altitude_min_metres": null,
    "altitude_max_metres": null,
    "description": null,
    "notes": null
  },
  "opening_grams": 0,
  "opening_reason": null,
  "opening_reference": null,
  "images": []
}
```

Strict sparse updates set the same key:

```json
{"description":"Updated customer-facing story"}
```

They clear the key with JSON null:

```json
{"description":null}
```

## Roast JSON fields

`roast list` data has `items` and nullable `next_cursor`. Each item contains
`roast_uuid`, `state`, nullable `roast_at`, `title`, `batch_prefix`,
`batch_number`, `batch_position`, `operator`, `machine`, `machine_setup`,
`temperature_unit`, `duration_seconds`, `green_weight_kg`, and
`roasted_weight_kg`, plus `revision_count`, `updated_at`, and `labels`. Each
label has `label_uuid`, `name`, `color`, and `archived`.

`roast show` returns those summary fields plus `current_metadata`, nullable
`current_revision`, and `links`. Links contain `self`, `chart`, and `revisions`.
A revision contains `revision_number`, `sha256`, `byte_size`, `parser_version`,
`parse_state`, nullable `parse_diagnostic_code` and
`parse_diagnostic_message`, `uploaded_at`, `metadata`, and
`reparse_recommended`. `roast revisions` returns those objects in `items` with
nullable `next_cursor`.

`roast comment list` data has `items` and nullable `next_cursor`. Every comment
contains `comment_uuid`, `roast_uuid`, `author_nickname`, nullable `body`,
`created_at`, nullable `edited_at` and `deleted_at`, `is_deleted`, `can_edit`,
and `can_delete`. Deleted comments are tombstones with null body; deleted comments are not recreated
by review replay.

`roast chart download` returns `path`, `roast_uuid`, `revision_number`,
`revision_sha256`, `parser_version`, `chart_schema_version`,
`compressed_bytes`, `compressed_sha256`, `file_bytes`, and `file_sha256`.
`compressed_sha256` identifies the bounded gzip transfer and `file_sha256`
identifies the validated JSON written to `path`.

`roast profile download` returns `path`, `roast_uuid`, `revision_number`,
`bytes`, and `sha256`. `roast review post` returns `comment`,
`revision_sha256`, `template_version`, and `idempotent_replay`; `comment` has the
same fields as a comment-list item. `idempotent_replay:false` means this request
won the slot, while true means the server returned its existing comment. A
never-posted stale identity fails with `roast_revision_changed` and exit 7.

`skill list` returns `items`, each containing `name`; `skill show` returns
`name` and exact `content`; `skill install` returns `name`, `path`, `installed`,
and `unchanged`.

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
