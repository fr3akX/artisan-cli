# Artisan CLI Inventory Pricing and Totals Design

**Date:** 2026-08-10  
**Status:** Approved design  
**Primary repository:** [`fr3akX/artisan-cli`](https://github.com/fr3akX/artisan-cli)  
**Server repository:** [`fr3akX/artisan-server`](https://github.com/fr3akX/artisan-server)

## Summary

Update Artisan CLI to expose the inventory pricing and filtered-total capabilities already deployed in Artisan Server. Human commands accept exact decimal EUR prices, machine contracts continue to use integer cents, and server-calculated totals cover every lot matching the active filters rather than one cursor page.

The server gains an additive read-only bearer namespace so active administrators and members have the same organization-private financial visibility as authenticated browser users. Existing admin mutation routes remain administrator-only, and the compatibility-sensitive Artisan desktop inventory projection remains unchanged.

The completed work is released as Artisan CLI `v0.3.0` after the compatible server commit is merged, validated, backed up, and deployed.

## Goals

1. Let administrators create, change, and clear a lot's optional EUR price from the CLI.
2. Let every active organization member view lot prices, filtered inventory totals, and derived reservation costs through bearer authentication.
3. Keep all monetary parsing and rendering exact, without binary floating-point arithmetic.
4. Preserve server authority for totals, valuation, authorization, and roast-cost calculations.
5. Preserve existing desktop, browser, public, and admin-write compatibility boundaries.
6. Maintain stable human output, JSON envelopes, exit codes, generated help, completion, and embedded Agent Skill behavior.
7. Publish verified static binaries for the six existing platform targets as `v0.3.0`.

## Non-goals

This release does not add:

- currencies other than EUR;
- exchange rates, taxes, shipping, discounts, or accounting records;
- local total or cost calculation;
- price history or reservation price snapshots;
- public financial fields;
- roast-list management commands;
- changes to Artisan desktop `/acoffees` or reduced inventory payloads;
- production inventory mutations as release smoke tests.

## User-facing commands

### Filtered totals

Add:

```text
artisan [GLOBAL FLAGS] inventory totals
    [--q TEXT]
    [--state active|archived]
    [--availability positive|zero|negative]
    [--conflict open|none]
    [--roast-uuid UUID]
```

The command accepts exactly the non-pagination filters supported by `inventory lot list`. It does not accept `--limit`, `--cursor`, or `--all`. The server performs one aggregate query across every matching organization lot.

Human output is a labeled detail block containing:

- matching lots;
- on-hand grams;
- reserved grams;
- available grams;
- on-hand EUR value;
- priced lots; and
- unpriced lots.

When no matching lot is priced, on-hand value is rendered as `-`. A partial valuation is still printed and the priced/unpriced counts make its coverage explicit. JSON output returns the exact server object inside the established success envelope:

```json
{
  "ok": true,
  "data": {
    "lot_count": 12,
    "on_hand_grams": 154000,
    "reserved_grams": 6000,
    "available_grams": 148000,
    "on_hand_value_eur_cents": 183420,
    "priced_lot_count": 10,
    "unpriced_lot_count": 2
  }
}
```

### Price input

Add this string flag to lot create and update:

```text
--price-per-kg-eur DECIMAL
```

Accepted values are canonical unsigned decimal EUR with zero, one, or two fractional digits, such as `0`, `12.3`, or `12.34`. The parser rejects signs, whitespace, separators, exponent notation, empty values, more than two fractional digits, and values above `21474836.47`. It converts directly to integer cents by splitting decimal digits; it never parses a float.

For flag-based create, omission sends a null price. For update, omission leaves the price unchanged. Clear an existing price with:

```text
artisan inventory lot update LOT_ID --clear price-per-kg-eur
```

The underscore alias `price_per_kg_eur` is accepted for consistency with existing clear aliases. `--price-per-kg-eur` and a matching `--clear` in one command are a local usage error.

Strict `--from-json` input continues to use the API field:

```json
{"price_per_kg_eur_cents": 1234}
```

JSON null clears the price in a patch. Booleans, floats, numeric strings, negatives, and values above `2147483647` are rejected locally.

### Financial reads

Administrator and member lot lists use the new bearer read route and support the full existing filter set. Human list output adds a `PRICE/KG` column. Lot detail adds `Price per kg`. Values render as exact EUR, for example `€12.34/kg`; null renders as `-`.

Reservation-history responses include `roast_cost_eur_cents`. Human tables add `ROAST COST`: reserved and finalized values render as exact EUR, while released or unpriced reservations render as `-`. JSON preserves the nullable integer-cent field without adding presentation strings.

All safe inventory reads use the member-capable namespace. Ledger and conflict mutation remain administrator-only.

## Server API

### Read-only bearer namespace

Add these routes:

```text
GET /api/v1/inventory/read/bean-lots
GET /api/v1/inventory/read/bean-lots/totals
GET /api/v1/inventory/read/bean-lots/{lot_id}
GET /api/v1/inventory/read/bean-lots/{lot_id}/reservations
GET /api/v1/inventory/read/bean-lots/{lot_id}/ledger
GET /api/v1/inventory/read/bean-lots/{lot_id}/conflicts
GET /api/v1/inventory/read/conflicts/{conflict_id}
GET /api/v1/inventory/read/bean-lots/{lot_id}/images/{image_id}/thumbnail
GET /api/v1/inventory/read/bean-lots/{lot_id}/images/{image_id}/display
```

The static `/totals` route is registered before the dynamic `/{lot_id}` route.

Every route requires an active bearer credential and current organization membership, but not administrator role. Credential expiry, revocation, inactive users, and missing membership retain the existing `401 authentication_required` behavior. Organization isolation and indistinguishable cross-organization not-found behavior remain unchanged.

The routes do not accept browser cookies or CSRF tokens. Responses are private and non-cacheable. They reuse the established browser schemas and service functions rather than duplicating inventory calculations or query semantics.

### Route behavior

The list route accepts `limit`, `cursor`, `q`, `state`, `availability`, `conflict`, and `roast_uuid`. It returns the complete lot summary including nullable `price_per_kg_eur_cents`.

The totals route accepts `q`, `state`, `availability`, `conflict`, and `roast_uuid`, but no pagination parameters. It calls the shared filter builder and aggregate service used by the browser totals route.

The detail route returns the complete lot projection including nullable price. Its `self`, `ledger`, and `reservations` links use the read namespace, and every image URL identifies the matching read-namespace stream.

The ledger, reservations, lot-conflicts, conflict-detail, and image-stream routes reuse the established projections and services. Reservation responses include nullable `roast_cost_eur_cents`, derived from the lot's current price and the reservation's current state and quantity basis. Image streams retain their private cache headers, size limits, content type, and tenant-scoped not-found behavior.

### Existing boundaries

These contracts remain unchanged:

- `GET /api/v1/inventory/bean-lots` and Artisan desktop projections;
- reservation mutation routes available to existing bearer roles;
- `/api/v1/inventory/admin` mutations and their administrator requirement;
- browser-cookie inventory routes;
- public roast and feedback routes.

The server does not add a migration for this CLI release. It relies on the deployed `0010_inventory_price_constraint` schema.

## CLI API models and validation

Add `PricePerKgEURCents *int64` to full bean-lot summary/detail models and `RoastCostEURCents *int64` to reservation models. Add an `InventoryTotals` response model with exact JSON names.

Read-namespace decoders require the new fields to be present, permit null only where specified, and continue tolerating unrelated unknown object fields. Validation enforces:

- unit price: null or `0..2147483647`;
- roast cost: null or a nonnegative JavaScript-safe integer;
- totals counts: nonnegative safe integers;
- totals quantities: signed safe integers with `available = on_hand - reserved`;
- priced plus unpriced lot count equals total lot count;
- null valuation exactly when priced lot count is zero;
- non-null signed safe-integer valuation when at least one lot is priced.

Malformed values fail as `invalid_server_response` with exit code 9.

`BeanLotFields`, strict create JSON, sparse patch validation, and flag-to-request assembly gain `price_per_kg_eur_cents`. Existing idempotency, retry, body-replay, and mutation confirmation behavior does not change.

## CLI request flow

Every safe inventory read calls the read namespace for both administrators and members: lot list/detail, ledger, reservations, lot conflicts, conflict detail, image download, and filtered totals. This removes the list command's role-dependent desktop/admin route selection, avoids an identity preflight request, and ensures links returned in complete lot projections remain usable by members.

A `404` that indicates the read namespace is absent maps to the established `server_upgrade_required` error. Entity-specific not-found responses remain ordinary `bean_lot_not_found` failures. CLI `v0.3.0` documentation names the resulting merged server commit as its minimum compatible version.

Create, update, archive, restore, adjustment, conflict, and image mutations continue to call `/api/v1/inventory/admin`. Reservation mutations continue to use their existing bearer routes.

## Human formatting

A focused money formatter accepts integer cents and emits a deterministic EUR string without locale-dependent separators. Unit prices append `/kg`; totals and roast costs do not. This avoids output changing with the machine locale and keeps scripts directed toward JSON mode.

The formatter handles zero exactly and never receives a float. Null values are rendered by the existing optional-value convention as `-`.

## Help, documentation, and Agent Skill

Update Cobra help and completion metadata for:

- the new `inventory totals` leaf;
- totals filter values;
- `--price-per-kg-eur` on create/update; and
- the new clear tokens.

Update `docs/commands.md`, `README.md`, release notes, JSON contract examples where relevant, and the embedded `artisan-inventory` skill. The skill instructs agents to:

- use decimal EUR only for human flags;
- use integer cents in JSON;
- never calculate authoritative totals locally;
- report partial valuation coverage explicitly;
- treat price mutation as an administrator operation; and
- re-read the lot after a price change without inventing production test mutations.

Regenerate the embedded skill artifact through the repository's established generator and verify source/embedded identity.

## Testing

### Server tests

Add focused tests for:

- administrator and member access to every read route;
- revoked, expired, inactive, and membership-missing bearer rejection;
- organization isolation and not-found behavior;
- list/detail price projection;
- exact list/totals filter parity;
- empty, fully priced, partially priced, zero-priced, and negative-balance totals;
- reservation costs for reserved, finalized, released, and unpriced states;
- malformed filters and route ordering;
- private/no-store response behavior;
- unchanged desktop/public payloads; and
- continued administrator-only mutation routes.

### CLI tests

Use unit and `httptest.Server` coverage for:

- accepted decimal forms and every rejected syntax/boundary;
- strict create and patch JSON handling;
- create/update/clear request bodies;
- list, detail, totals, and reservation response validation;
- totals query mapping without pagination;
- member and administrator behavior for every safe read, including ledger, conflicts, and image download;
- server-upgrade and entity-not-found classification;
- exact human money rendering;
- stable JSON envelopes;
- Cobra parsing, help, completion, and legacy parse compatibility;
- documentation and embedded-skill contracts; and
- absence of bearer values from output and diagnostics.

### Cross-repository integration

Advance `integration/artisan-server.ref` to the merged compatible server commit. The guarded disposable Compose suite compiles the CLI and exercises:

- member and administrator financial reads plus linked ledger, conflict, and image reads;
- identical filtered totals for both roles;
- administrator price create/update/clear behavior inside the disposable stack;
- member price mutation rejection;
- reservation-cost projection; and
- unchanged reduced desktop behavior.

No integration test targets a non-loopback server or production data.

## Rollout and release

1. Implement server and CLI changes in isolated repository worktrees using test-driven development.
2. Run focused checks, complete repository suites, static analysis, release-contract checks, and the disposable cross-repository integration suite.
3. Merge and push the server first. Require the exact main commit's GitHub Actions run to pass.
4. Follow the established coordinated production-backup procedure, preserve the current API image with a rollback tag, build and deploy only the API service, and leave PostgreSQL, MinIO, web, volumes, and network untouched.
5. Verify migration remains `0010_inventory_price_constraint`, service health, exact image identity, unauthenticated auth boundaries, and bounded logs. Perform only read-only authenticated production smoke checks when an existing configured credential is safely available; never mutate production inventory for validation.
6. Merge and push the CLI only after the server is deployed and healthy. Require CLI CI and pinned-server integration to pass.
7. Tag `v0.3.0`, publish six static platform archives plus checksums and provenance, download the assets, and verify every checksum and embedded version/commit.
8. Preserve rollback artifacts and the coordinated backup, then remove only owned branches and worktrees. Leave unrelated untracked files and worktrees untouched.

## Acceptance criteria

The work is complete when:

1. An active member or administrator can list and inspect lot prices, request server-calculated filtered totals, and see reservation costs with a bearer credential.
2. An administrator can create, update, and clear prices using exact decimal EUR flags or integer-cent JSON.
3. A member cannot mutate prices or use any administrator-only operation.
4. Existing desktop and public contracts remain unchanged.
5. Human and JSON output are exact, deterministic, documented, and fully tested.
6. The compatible server is merged, green, backed up, deployed, and healthy before CLI publication.
7. CLI main and release CI pass against the pinned server commit.
8. The `v0.3.0` six-platform release and checksum asset are published and independently verified.
