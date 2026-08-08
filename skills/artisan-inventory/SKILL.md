---
name: artisan-inventory
description: Use when an agent is asked to inspect or change bean lots, inventory images, reservations, ledger entries, or inventory conflicts with the Artisan CLI.
---

# Artisan Inventory

## Safety Gate

1. Start every run by checking the installed CLI version:

```sh
artisan version
```

2. Obtain the exact trusted server URL from the human, bind it in `TRUSTED_SERVER`, and then verify the server-bound credential:

```sh
artisan --json --server "$TRUSTED_SERVER" auth status
```

Require the exact expected user, organization, and role plus that exact server URL before mutation. Match user, organization, and role from the auth-status JSON; server assurance comes from the explicit global `--server` binding on this check and every later command.

3. Organization assurance comes from the bound auth identity plus server-side tenant scoping, not inventory response fields. Stop on identity/organization/server/role mismatch, nonzero exit, `ok:false`, malformed or incomplete JSON, timeout or ambiguous result, missing/repeated cursor, pagination limit, permission failure, or server upgrade requirement.
4. Use JSON for automation; validate fields, IDs, states, and integer values. Never parse human tables.
5. The CLI accepts dashed or compact UUID input and normalizes it; inventory response IDs are compact lowercase UUIDs. Normalize supplied IDs to compact lowercase before matching. Compare normalized IDs, never their dashed/compact spelling.
6. The agent must never request, read, print, persist, or pass a token and must never run `artisan auth login`. The human authenticates outside the agent session.
7. Use integer grams: `2500` or `-2500`; never `+2500g`, decimals, kilograms, or suffixes.

## Mutation Gate

- Resolve immutable IDs and re-read current state. State the exact mutation and impact. Obtain fresh explicit human approval immediately before execution.
- Add `--yes` only for that freshly approved operation; prior approval, role, urgency, or plan approval does not count.
- Assign one idempotency key per logical mutation. Record it with the operation. On retry or reconciliation, reuse the same key; never create a new key after timeout or ambiguity.
- Treat mutation output as provisional. Perform an authoritative reread with `lot show` and relevant history.
- On ambiguity, read before retrying. Retry only when state proves no effect, with unchanged intent and key.

## Lots: Resolve, Read, Re-read

Use an explicit page/item/time budget. Follow only returned opaque cursors and reject repeats. `--all` is bounded to 1,000 pages and 10,000 items; a reached bound is failure.

```sh
artisan --json --server <EXPECTED_SERVER_URL> inventory lot list --state active --availability positive --limit 100
artisan --json --server <EXPECTED_SERVER_URL> inventory lot list --state active --availability positive --limit 100 --cursor <NEXT_CURSOR>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot list --state active --availability positive --limit 100 --all
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot ledger <LOT_ID> --limit 100
artisan --json --server <EXPECTED_SERVER_URL> inventory lot reservations <LOT_ID> --limit 100
artisan --json --server <EXPECTED_SERVER_URL> inventory lot conflicts <LOT_ID> --limit 100
```

Select from JSON under a human-supplied policy. Re-read `<LOT_ID>`; compare its normalized `lot_id`, then verify state, integer `on_hand_grams`, `reserved_grams`, `available_grams`, and conflicts.

```sh
artisan --json --server <EXPECTED_SERVER_URL> inventory lot create --name <NAME> --opening-grams <INTEGER_GRAMS> --opening-reason <REASON> --idempotency-key <KEY>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot update <LOT_ID> --name <NAME> --idempotency-key <KEY>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot archive <LOT_ID> --idempotency-key <KEY> --yes
artisan --json --server <EXPECTED_SERVER_URL> inventory lot restore <LOT_ID> --idempotency-key <KEY>
artisan --json --server <EXPECTED_SERVER_URL> inventory adjust <LOT_ID> --grams <SIGNED_INTEGER_GRAMS> --reason <REASON> --idempotency-key <KEY> --yes
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot ledger <LOT_ID> --limit 100
```

Before adjustment, show current `on_hand_grams`, `reserved_grams`, `available_grams`, signed delta (never target stock), reason, and expected post-adjustment gram fields; then apply the Mutation Gate.

## Images: Resolve, Mutate, Re-read

Use `lot show` before and after. Compare normalized lot/image IDs and verify `<IMAGE_ID>` belongs to `<LOT_ID>`; reorder with the complete image-ID order. Delete only after deletion-specific approval.

```sh
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory image add --caption 0=<CAPTION> --alt-text 0=<ALT_TEXT> --idempotency-key <KEY> <LOT_ID> <FILE>
artisan --json --server <EXPECTED_SERVER_URL> inventory image update --caption <CAPTION> --idempotency-key <KEY> <LOT_ID> <IMAGE_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory image reorder --idempotency-key <KEY> <LOT_ID> <IMAGE_ID_1> <IMAGE_ID_2>
artisan --json --server <EXPECTED_SERVER_URL> inventory image download --variant display <LOT_ID> <IMAGE_ID> <DESTINATION>
artisan --json --server <EXPECTED_SERVER_URL> inventory image delete --yes --idempotency-key <KEY> <LOT_ID> <IMAGE_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot ledger <LOT_ID> --limit 100
```

After deletion timeout, re-read before any retry with the same key.

## Reservations: Resolve, Mutate, Re-read

Re-read the active lot and its integer `on_hand_grams`, `reserved_grams`, and `available_grams`. Preserve client reservation UUID and one idempotency key per create/finalize/release mutation. Match rereads by normalized client reservation UUID; compare normalized lot, roast, client instance, and conflict IDs, then verify grams and state.

```sh
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory reservation create --client-reservation-uuid <CLIENT_RESERVATION_UUID> --client-instance-uuid <CLIENT_INSTANCE_UUID> --roast-uuid <ROAST_UUID> --lot-id <LOT_ID> --planned-grams <INTEGER_GRAMS> --occurred-at <UTC_TIMESTAMP> --idempotency-key <KEY>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot reservations <LOT_ID> --limit 100 --all
artisan --json --server <EXPECTED_SERVER_URL> inventory reservation finalize <CLIENT_RESERVATION_UUID> --actual-grams <INTEGER_GRAMS> --occurred-at <UTC_TIMESTAMP> --idempotency-key <KEY>
artisan --json --server <EXPECTED_SERVER_URL> inventory reservation release <CLIENT_RESERVATION_UUID> --occurred-at <UTC_TIMESTAMP> --idempotency-key <KEY>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot reservations <LOT_ID> --limit 100 --all
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot ledger <LOT_ID> --limit 100
```

Do not finalize or release a different or ambiguous reservation.

## Conflicts: Read, Resolve, Re-read

On HTTP 409 or a conflict field, stop mutation and preserve the original key and intent. Do not adjust stock, auto-resolve, or convert the 409 into another operation.

```sh
artisan --json --server <EXPECTED_SERVER_URL> inventory conflict list --lot <LOT_ID> --limit 100
artisan --json --server <EXPECTED_SERVER_URL> inventory conflict show <CONFLICT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot ledger <LOT_ID> --limit 100
artisan --json --server <EXPECTED_SERVER_URL> inventory lot reservations <LOT_ID> --limit 100
```

Resolve only the reviewed conflict with adequate role and fresh resolution-specific approval:

```sh
artisan --json --server <EXPECTED_SERVER_URL> inventory conflict show <CONFLICT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory conflict resolve <CONFLICT_ID> --note <RESOLUTION_NOTE> --idempotency-key <KEY> --yes
artisan --json --server <EXPECTED_SERVER_URL> inventory conflict show <CONFLICT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot conflicts <LOT_ID> --limit 100
artisan --json --server <EXPECTED_SERVER_URL> inventory lot ledger <LOT_ID> --limit 100
```

## Quick Reference

| Authority | Command |
|---|---|
| Identity | `artisan --json --server <EXPECTED_SERVER_URL> auth status` |
| Lot/image | `artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>` |
| Ledger | `artisan --json --server <EXPECTED_SERVER_URL> inventory lot ledger <LOT_ID> --limit 100` |
| Reservation | `artisan --json --server <EXPECTED_SERVER_URL> inventory lot reservations <LOT_ID> --limit 100` |
| Conflict | `artisan --json --server <EXPECTED_SERVER_URL> inventory conflict show <CONFLICT_ID>` |

## Common Mistakes

| Mistake | Correction |
|---|---|
| Handle a token or log in | Stop; the human authenticates. |
| Put `--json` after a command | Put global flags before `auth` or `inventory`. |
| Parse a table | Validate JSON. |
| Use units with grams | Pass a signed integer only. |
| Use `--yes` for speed | Get fresh operation approval. |
| Retry with a new key | Reconcile; reuse the same key. |
| Traverse cursors without bounds | Enforce budgets and reject repeats. |
| Trust mutation output | Re-read lot and history. |
| Adjust after 409 | Stop, read, and escalate. |
