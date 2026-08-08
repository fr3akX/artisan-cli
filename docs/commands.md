# Commands and configuration

Artisan CLI requires Artisan Server commit
`4c0136fe98f6728f4bb94e416c5abe570e7f4831` or later. Deploy that server before
the CLI release.

## Global syntax

```text
artisan [--json] [--server URL] [--timeout DURATION] COMMAND ...
```

In examples, the complete global option group is `--json --server URL --timeout DURATION`.
Global flags must precede `auth`, `inventory`, `skill`, or `version`; command
flags must appear in the positions shown below. `--timeout` uses a Go duration
such as `30s` or `2m`, must be positive, and cannot exceed `5m`. Its default is
`30s`; the exact `5m` boundary is accepted.

`--server` overrides the effective server for that invocation. Otherwise
`ARTISAN_SERVER_URL` overrides stored configuration. `ARTISAN_SERVER_TOKEN`
overrides the stored token independently; environment values are never
persisted. HTTPS is required except for `http://localhost`, `127.0.0.0/8`, or
IPv6 loopback origins. Server URLs cannot contain credentials, paths, queries,
or fragments.

Stored files are `config.json` and `credentials.json` below the OS user config
directory:

| OS | Default directory |
|---|---|
| Linux | `$XDG_CONFIG_HOME/artisan`, or `$HOME/.config/artisan` when `XDG_CONFIG_HOME` is unset |
| macOS | `$HOME/Library/Application Support/artisan` |
| Windows | `%AppData%\artisan` |

## Authentication

```text
artisan [GLOBAL FLAGS] auth login [--token-stdin]
artisan [GLOBAL FLAGS] auth status
artisan [GLOBAL FLAGS] auth logout
```

Select a server with a prior stored value, `ARTISAN_SERVER_URL`, or global
`--server URL`. For scripts, keep the bearer token out of argv:

```sh
printf '%s\n' "$TOKEN" | artisan --server https://inventory.example auth login --token-stdin
```

Without `--token-stdin`, login prompts only on a terminal with hidden input.
Login validates the token by reading the authenticated identity before storing
it. An explicit `--server` is stored with the token. `auth status` reads the
live identity. `auth logout` removes the stored token but retains the stored
server. Environment overrides remain outside these operations. Authentication
recovery, login publication, logout, and stored server/token snapshots are serialized
across CLI processes; a command uses one consistent origin/credential snapshot.

## Lots and inventory history

```text
artisan [GLOBAL FLAGS] inventory lot list [--limit N] [--cursor CURSOR] [--all]
    [--q TEXT] [--state STATE] [--availability FILTER] [--conflict FILTER]
    [--roast-uuid UUID]
artisan [GLOBAL FLAGS] inventory lot show LOT_ID
artisan [GLOBAL FLAGS] inventory lot ledger LOT_ID [--limit N] [--cursor CURSOR] [--all]
artisan [GLOBAL FLAGS] inventory lot reservations LOT_ID [--limit N] [--cursor CURSOR] [--all]
artisan [GLOBAL FLAGS] inventory lot conflicts LOT_ID [--limit N] [--cursor CURSOR] [--all]
```

With an administrator credential, list filters are `state=active|archived`,
`availability=positive|zero|negative`, and `conflict=open|none`, and the full
admin projection is returned. With a member credential, lot list uses the
active-only desktop projection and accepts only `--limit`, `--cursor`, and
`--all`; admin-only filters fail locally after the live identity check. IDs
accept compact or dashed UUID syntax and are normalized by the client.

```text
artisan [GLOBAL FLAGS] inventory lot create LOT-FLAGS
artisan [GLOBAL FLAGS] inventory lot update LOT_ID PATCH-FLAGS
artisan [GLOBAL FLAGS] inventory lot archive LOT_ID [--yes] [--idempotency-key KEY]
artisan [GLOBAL FLAGS] inventory lot restore LOT_ID [--idempotency-key KEY]
```

Create lot flags are:

```text
--name TEXT                         --origin TEXT
--producer TEXT                     --supplier TEXT
--external-reference TEXT           --received-date YYYY-MM-DD
--crop-year YEAR                    --varietal TEXT              (repeatable)
--sca-score SCORE                   --processing-method TEXT
--processing-detail TEXT            --altitude-min-metres METRES
--altitude-max-metres METRES        --notes TEXT
--opening-grams GRAMS               --opening-reason TEXT
--opening-reference TEXT            --idempotency-key KEY
--image FILE                        (repeatable, maximum eight)
--image-caption INDEX=TEXT          (repeatable, zero-based)
--image-alt-text INDEX=TEXT         (repeatable, zero-based)
--image-cover INDEX
--from-json FILE|-
```

`--name` is required for flag input. `--from-json` is strict, limited to 1 MiB,
and cannot be combined with lot-field or image-metadata flags; repeated
`--image` files are still supplied separately and must match the JSON image
metadata count. `artisan inventory lot create --help` prints the built-in
create/image syntax.

Update accepts the same lot field flags (not opening/image flags), repeatable
`--clear FIELD`, `--from-json FILE|-`, and `--idempotency-key KEY`. At least one
field must change. Use archive/restore rather than setting `state`. Clearable
fields accept exactly these `--clear` tokens (repeat the flag for multiple
fields):

```text
origin
producer
supplier
notes
varietals
external-reference
external_reference
received-date
received_date
crop-year
crop_year
sca-score
sca_score
processing-method
processing_method
processing-detail
processing_detail
altitude-min-metres
altitude_min_metres
altitude-max-metres
altitude_max_metres
```

## Manual adjustment

```text
artisan [GLOBAL FLAGS] inventory adjust LOT_ID --grams SIGNED_INTEGER
    --reason TEXT [--reference TEXT] [--occurred-at UTC_TIMESTAMP]
    [--idempotency-key KEY] [--yes]
```

The CLI supplies the current UTC time when `--occurred-at` is omitted.
`--grams` is a signed delta applied to stock, not a target stock value; zero is
rejected. It requires terminal
approval, or `--yes` for an already approved noninteractive operation.

## Images

Flags must precede positional IDs/files for image commands:

```text
artisan [GLOBAL FLAGS] inventory image add [--caption INDEX=TEXT]
    [--alt-text INDEX=TEXT] [--cover INDEX] [--idempotency-key KEY]
    LOT_ID FILE...
artisan [GLOBAL FLAGS] inventory image update [--caption TEXT|--clear-caption]
    [--alt-text TEXT|--clear-alt-text] [--cover=BOOL]
    [--idempotency-key KEY] LOT_ID IMAGE_ID
artisan [GLOBAL FLAGS] inventory image reorder [--idempotency-key KEY]
    LOT_ID IMAGE_ID...
artisan [GLOBAL FLAGS] inventory image delete [--yes] [--idempotency-key KEY]
    LOT_ID IMAGE_ID
artisan [GLOBAL FLAGS] inventory image download [--variant display|thumbnail]
    [--force] LOT_ID IMAGE_ID DESTINATION
```

Uploads accept one to eight validated JPEG/PNG files. Reorder requires the
complete unique image order and at most eight IDs. Download creates a new
regular destination by default; `--force` permits replacement under the
platform's safe-file rules. Built-in `--help` is implemented for the image
command group and each image operation.

## Reservations

```text
artisan [GLOBAL FLAGS] inventory reservation create
    --client-reservation-uuid UUID --client-instance-uuid UUID
    --roast-uuid UUID --lot-id UUID --planned-grams INTEGER
    --occurred-at UTC_TIMESTAMP [--idempotency-key KEY]
artisan [GLOBAL FLAGS] inventory reservation finalize CLIENT_RESERVATION_UUID
    [--actual-grams INTEGER] --occurred-at UTC_TIMESTAMP [--idempotency-key KEY]
artisan [GLOBAL FLAGS] inventory reservation release CLIENT_RESERVATION_UUID
    --occurred-at UTC_TIMESTAMP [--idempotency-key KEY]
```

Create requires every listed reservation field. Planned grams must be positive;
actual grams, when present, must be at least 1. Timestamps use canonical UTC
form with six fractional digits, for example `2026-08-07T12:34:56.000000Z`.

## Conflicts

```text
artisan [GLOBAL FLAGS] inventory conflict list --lot LOT_ID
    [--limit N] [--cursor CURSOR] [--all]
artisan [GLOBAL FLAGS] inventory conflict show CONFLICT_ID
artisan [GLOBAL FLAGS] inventory conflict resolve CONFLICT_ID --note TEXT
    [--idempotency-key KEY] [--yes]
```

Conflict resolution records a note; it does **not** adjust stock. Resolution
requires terminal approval or `--yes` after explicit approval.

## Skill, version, and help

```text
artisan [--json] skill show
artisan [--json] skill install --directory ROOT [--force]
artisan [--json] version
```

`skill --help`, `skill show --help`, `skill install --help`, image help, and lot
create help are implemented and return success. Other `--help` placements are
not implemented by this release and return a usage error rather than a command
catalog. See [agent skill installation](agent-skill.md).

SIGINT, and SIGTERM on platforms that support it, cancel active work and run
normal cleanup before the process exits with status 130. See [JSON, pagination,
idempotency, and exit codes](json-and-exit-codes.md) and [security](security.md).
