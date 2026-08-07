---
name: artisan-inventory
description: Use when an agent is asked to inspect or change bean lots, inventory images, reservations, ledger entries, or inventory conflicts with the Artisan CLI.
---

# Artisan Inventory

## Safety Gate

1. Start every run:

```sh
artisan --json auth status
```

2. Require the human-supplied expected user, organization, server, and role before mutation. Match user, organization, and role. Bind the expected server on this check and every later command:

```sh
artisan --json --server <EXPECTED_SERVER_URL> auth status
```

3. Stop on identity/organization/server/role mismatch, nonzero exit, `ok:false`, malformed or incomplete JSON, timeout or ambiguous result, missing/repeated cursor, pagination limit, permission failure, or server upgrade requirement.
4. Use JSON for automation; validate fields, IDs, states, and integer values. Never parse human tables.
5. The agent must never request, read, print, persist, or pass a token and must never run `artisan auth login`. The human authenticates outside the agent session.
6. Use integer grams: `2500` or `-2500`; never `+2500g`, decimals, kilograms, or suffixes.

## Mutation Gate

- Resolve immutable IDs and re-read current state. State the exact mutation and impact. Obtain fresh explicit human approval immediately before execution.
- Add `--yes` only for that freshly approved operation; prior approval, role, urgency, or plan approval does not count.
- Assign one idempotency key per logical mutation. Record it with the operation. On retry or reconciliation, reuse the same key; never create a new key after timeout or ambiguity.
- Treat mutation output as provisional. Perform an authoritative reread with `lot show` and relevant history.
- On ambiguity, read before retrying. Retry only when state proves no effect, with unchanged intent and key.

## Lots: Resolve, Read, Re-read

Use an explicit page/item/time budget. Follow only returned opaque cursors and reject repeats. `--all` is bounded to 1,000 pages and 10,000 items; a reached bound is failure.

```sh
artisan --json inventory lot list --state active --availability positive --limit 100
artisan --json inventory lot list --state active --availability positive --limit 100 --cursor <NEXT_CURSOR>
artisan --json inventory lot list --state active --availability positive --limit 100 --all
artisan --json inventory lot show <LOT_ID>
artisan --json inventory lot ledger <LOT_ID> --limit 100
artisan --json inventory lot reservations <LOT_ID> --limit 100
artisan --json inventory lot conflicts <LOT_ID> --limit 100
```

Select from JSON under a human-supplied policy. Re-read `<LOT_ID>`; verify organization, state, available grams, and conflicts.

```sh
artisan --json inventory lot create --name <NAME> --opening-grams <INTEGER_GRAMS> --opening-reason <REASON> --idempotency-key <KEY>
artisan --json inventory lot update <LOT_ID> --name <NAME> --idempotency-key <KEY>
artisan --json inventory lot archive <LOT_ID> --idempotency-key <KEY> --yes
artisan --json inventory lot restore <LOT_ID> --idempotency-key <KEY>
artisan --json inventory adjust <LOT_ID> --grams <SIGNED_INTEGER_GRAMS> --reason <REASON> --idempotency-key <KEY> --yes
artisan --json inventory lot show <LOT_ID>
artisan --json inventory lot ledger <LOT_ID> --limit 100
```

Before adjustment, show current balances, signed delta, reason, and expected balance; then apply the Mutation Gate.

## Images: Resolve, Mutate, Re-read

Use `lot show` before and after. Verify `<IMAGE_ID>` belongs to `<LOT_ID>`; reorder with the complete image-ID order. Delete only after deletion-specific approval.

```sh
artisan --json inventory lot show <LOT_ID>
artisan --json inventory image add --caption 0=<CAPTION> --alt-text 0=<ALT_TEXT> --idempotency-key <KEY> <LOT_ID> <FILE>
artisan --json inventory image update --caption <CAPTION> --idempotency-key <KEY> <LOT_ID> <IMAGE_ID>
artisan --json inventory image reorder --idempotency-key <KEY> <LOT_ID> <IMAGE_ID_1> <IMAGE_ID_2>
artisan --json inventory image download --variant display <LOT_ID> <IMAGE_ID> <DESTINATION>
artisan --json inventory image delete --yes --idempotency-key <KEY> <LOT_ID> <IMAGE_ID>
artisan --json inventory lot show <LOT_ID>
artisan --json inventory lot ledger <LOT_ID> --limit 100
```

After deletion timeout, re-read before any retry with the same key.

## Reservations: Resolve, Mutate, Re-read

Re-read the active lot and available grams. Preserve client reservation UUID and one idempotency key per create/finalize/release mutation. Match rereads by client reservation UUID; verify lot, roast, client instance, grams, state, and conflict ID.

```sh
artisan --json inventory lot show <LOT_ID>
artisan --json inventory reservation create --client-reservation-uuid <CLIENT_RESERVATION_UUID> --client-instance-uuid <CLIENT_INSTANCE_UUID> --roast-uuid <ROAST_UUID> --lot-id <LOT_ID> --planned-grams <INTEGER_GRAMS> --occurred-at <UTC_TIMESTAMP> --idempotency-key <KEY>
artisan --json inventory lot reservations <LOT_ID> --limit 100 --all
artisan --json inventory reservation finalize <CLIENT_RESERVATION_UUID> --actual-grams <INTEGER_GRAMS> --occurred-at <UTC_TIMESTAMP> --idempotency-key <KEY>
artisan --json inventory reservation release <CLIENT_RESERVATION_UUID> --occurred-at <UTC_TIMESTAMP> --idempotency-key <KEY>
artisan --json inventory lot reservations <LOT_ID> --limit 100 --all
artisan --json inventory lot show <LOT_ID>
artisan --json inventory lot ledger <LOT_ID> --limit 100
```

Do not finalize or release a different or ambiguous reservation.

## Conflicts: Read, Resolve, Re-read

On HTTP 409 or a conflict field, stop mutation and preserve the original key and intent. Do not adjust stock, auto-resolve, or convert the 409 into another operation.

```sh
artisan --json inventory conflict list --lot <LOT_ID> --limit 100
artisan --json inventory conflict show <CONFLICT_ID>
artisan --json inventory lot show <LOT_ID>
artisan --json inventory lot ledger <LOT_ID> --limit 100
artisan --json inventory lot reservations <LOT_ID> --limit 100
```

Resolve only the reviewed conflict with adequate role and fresh resolution-specific approval:

```sh
artisan --json inventory conflict show <CONFLICT_ID>
artisan --json inventory conflict resolve <CONFLICT_ID> --note <RESOLUTION_NOTE> --idempotency-key <KEY> --yes
artisan --json inventory conflict show <CONFLICT_ID>
artisan --json inventory lot show <LOT_ID>
artisan --json inventory lot conflicts <LOT_ID> --limit 100
artisan --json inventory lot ledger <LOT_ID> --limit 100
```

## Quick Reference

| Authority | Command |
|---|---|
| Identity | `artisan --json auth status` |
| Lot/image | `artisan --json inventory lot show <LOT_ID>` |
| Ledger | `artisan --json inventory lot ledger <LOT_ID> --limit 100` |
| Reservation | `artisan --json inventory lot reservations <LOT_ID> --limit 100` |
| Conflict | `artisan --json inventory conflict show <CONFLICT_ID>` |

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
