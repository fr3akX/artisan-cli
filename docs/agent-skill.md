# Embedded agent skill

Artisan CLI embeds the canonical
[`artisan-inventory` skill](../skills/artisan-inventory/SKILL.md) at build time.
The skill and this CLI require Artisan Server commit
`4c0136fe98f6728f4bb94e416c5abe570e7f4831` or later; deploy that server before
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
it fails with `skill_exists` and exit 4. Review the difference, then intentionally
replace only that skill with:

```sh
artisan skill install --directory ROOT --force
```

The installer rejects a root containing a `..` component, missing/non-directory
roots, symlink/reparse traversal, unsafe targets, and races where the opened
location changes. Replacement is atomic; a durability-uncertain error requires
manual inspection before retrying. `--force` does not weaken those path checks
and does not modify files outside `ROOT/artisan-inventory/SKILL.md`.

## Agent security boundary

The skill instructs an agent to use global `--json` and an exact human-supplied
`--server`, verify identity/organization/role, normalize and re-read IDs, bound
pagination, use integer grams, preserve one idempotency key, and obtain fresh
explicit human approval immediately before mutation.

Critically, an agent **must not handle tokens**: it must never request, read,
print, persist, or pass one. An agent **must not log in** and must never run
`artisan auth login`. A human authenticates outside the agent session. The
agent begins with `artisan --json --server EXPECTED_URL auth status` and stops
on mismatch, incomplete JSON, ambiguity, bounds, permissions, timeout, or an
upgrade requirement.

Installing a skill is not a sandbox or authorization grant. The host agent,
its tool permissions, the selected server, local account, and human approval
process remain security boundaries. Review the embedded content with `skill
show` before enabling it and follow the [security model](security.md).

See [commands](commands.md) and [JSON/exit behavior](json-and-exit-codes.md).
