# Embedded agent skill

Artisan CLI embeds the canonical
[`artisan-inventory` skill](../skills/artisan-inventory/SKILL.md) at build time.
The skill and this CLI require Artisan Server commit
`436ffff581fd01e3b356a8fda188593cbf1cf60b` or later; deploy that server before
the CLI release.

## Inspect or install

Print the exact embedded bytes:

```sh
artisan skill show
artisan --json skill show
```

Human output is the `SKILL.md` content. JSON returns `name` and `content`.
Install below an explicitly selected existing agent skill root:

```sh
artisan skill install --directory ROOT
artisan --json skill install --directory ROOT
```

The result path is `ROOT/artisan-inventory/SKILL.md`. The CLI deliberately does
not detect or assume an agent product or home. Depending on a user's separately
verified agent configuration, possible roots might include
`$HOME/.claude/skills`, `$HOME/.config/opencode/skills`, or another dedicated
skills directory. These are examples only, not defaults or claims that those
agents use that path. Create/select the root yourself and pass its exact path.

Installation is idempotent when the existing canonical file has identical
bytes: it reports `Already installed` / JSON `unchanged:true`. If bytes differ,
it fails with `skill_exists` and exit 3, the local storage/install failure class.
Review the difference, then intentionally replace only that skill with:

```sh
artisan skill install --directory ROOT --force
```

The installer rejects a root containing a `..` component, missing/non-directory
roots, symlink/reparse traversal, unsafe targets, and races where the opened
location changes. Replacement is atomic; a durability-uncertain error requires
manual inspection before retrying. `--force` does not weaken those path checks
and does not modify files outside `ROOT/artisan-inventory/SKILL.md`.

## Pricing, totals, and role boundary

Members may perform every safe read but no admin mutation. This includes lot
prices, reservation costs, linked history/images, and filtered totals. A price
mutation requires a verified admin identity, fresh operation-specific approval,
one idempotency key for the logical change, and an authoritative lot reread.
The agent must stop rather than attempting an administrator operation as a
member.

The initial read/re-read gate includes:

```sh
artisan --json --server <EXPECTED_SERVER_URL> inventory totals --state active --availability positive
artisan --json --server <EXPECTED_SERVER_URL> inventory lot show <LOT_ID>
```

JSON prices use `price_per_kg_eur_cents` as integer cents or null. Human flags
use exact decimal syntax such as `--price-per-kg-eur 12.34`. Partial valuation
requires reporting `priced_lot_count` and `unpriced_lot_count`. Agents must not compute totals or costs locally.
They must never sum paginated list output as an authoritative aggregate.

Production smoke is read-only. The skill does not authorize an agent to invent
or perform production mutations for testing.

## Agent security boundary

The skill instructs an agent to use global `--json` and an exact human-supplied
`--server`, verify identity/organization/role, normalize and re-read IDs, bound
pagination, use integer grams, preserve one idempotency key, and obtain fresh
explicit human approval immediately before mutation.

A human login with explicit `--server` stores that server as the default for
later human commands. Agents deliberately do **not** rely on that default: every
automated status, read, and mutation command still binds
`--server "$TRUSTED_SERVER"` to prevent confused-server operation.

Critically, an agent **must not handle tokens**: it must never request, read,
print, persist, or pass one. An agent **must not log in** and must never run
`artisan auth login`. A human authenticates outside the agent session. The
agent begins, in order, with `artisan version` and then
`artisan --json --server "$TRUSTED_SERVER" auth status`; it stops on mismatch,
incomplete JSON, ambiguity, bounds, permissions, timeout, or an upgrade
requirement.

Installing a skill is not a sandbox or authorization grant. The host agent,
its tool permissions, the selected server, local account, and human approval
process remain security boundaries. Review the embedded content with `skill
show` before enabling it and follow the [security model](security.md).

See [commands](commands.md) and [JSON/exit behavior](json-and-exit-codes.md).
