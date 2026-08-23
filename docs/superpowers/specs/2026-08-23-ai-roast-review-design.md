# Artisan Agent-Native AI Roast Review Design

**Date:** 2026-08-23
**Status:** Approved design
**Primary repository:** [`fr3akX/artisan-cli`](https://github.com/fr3akX/artisan-cli)
**Server repository:** [`fr3akX/artisan-server`](https://github.com/fr3akX/artisan-server)

## Summary

Add an agent-native roast-review capability to Artisan CLI. The CLI exposes authenticated, tenant-scoped commands for discovering a roast, reading its metadata and revisions, downloading its parsed chart JSON and original `.alog` profile, and posting one review comment for a specific immutable profile revision. A separately installable `artisan-roast-review` Agent Skill tells the host AI agent how to analyze the profile and post a concise, evidence-based review.

The CLI does not call an AI provider. It contains no AI SDK, model selection, provider API key, prompt runner, or cost policy. The host agent that invoked the skill performs the analysis. Artisan Server remains authoritative for identity, organization scope, roast visibility, immutable revisions, object integrity, current-revision state, comment authorship, audit, and the one-review-per-revision guarantee.

Reviews are ordinary private organization comments attributed to the authenticated member. Their body begins with the stable visible heading `AI roast analysis`; no bot account or synthetic author is introduced. A companion server record reserves one review result for each organization, immutable roast revision, and review-template version so repeated or concurrent analyses do not create duplicate comments.

## Approved decisions

1. AI execution is agent-native, not built into the CLI.
2. The CLI exposes both parsed chart data and the raw `.alog` profile.
3. The review is an ordinary user-authored comment with an explicit AI heading.
4. A requested review posts automatically after successful analysis; no preview confirmation is required.
5. Reviews use a fixed, versioned roast-review template rather than free-form or user-supplied prompts.
6. The first successful review for a profile revision and template version wins; reruns return it instead of appending or replacing comments.
7. Any active organization member or administrator may perform the workflow.
8. Roast-review guidance is a separate `artisan-roast-review` skill, not an expansion of `artisan-inventory`.

## Goals

1. Let an authenticated member find and inspect one organization roast from Artisan CLI.
2. Give an AI agent complete, integrity-checked access to roast metadata, parsed chart data, immutable revision metadata, and raw profile bytes.
3. Produce a concise, repeatable review grounded in measured profile evidence and explicit uncertainty.
4. Post the review automatically as a private comment under the authenticated member's nickname.
5. Prevent stale-revision comments and duplicate reviews under retries, reruns, or concurrency.
6. Preserve browser CSRF protection, tenant non-disclosure, roast-trash behavior, revision immutability, and comment audit semantics.
7. Keep downloads bounded, checksum-verified, race-safe, private, and cross-platform.
8. Preserve all existing CLI commands, JSON envelopes, exit classes, skill installation behavior, and release targets.

## Non-goals

This release does not add:

- an OpenAI, Anthropic, local-model, or other AI provider client to the CLI;
- AI-provider credentials, model configuration, token budgets, or cost tracking;
- a server-side AI job queue or background analysis worker;
- public AI reviews or public-feedback creation;
- a bot/service account or synthetic comment author;
- machine control, roast replay, or changes to Artisan desktop;
- sensory-score prediction presented as fact;
- user-defined prompts or review templates;
- automatic editing or replacement of an existing review;
- recreation of a review that a user deleted;
- analysis of trashed roasts;
- a frontend-specific AI badge or new comment renderer;
- production comment creation as a deployment smoke test.

## Architecture and trust boundaries

```text
Human requests a roast review
        |
        v
Host AI agent loads artisan-roast-review
        |
        v
Artisan CLI verifies version, exact server, identity, tenant, and role
        |
        v
Artisan Server returns private metadata, revision, chart, and raw profile
        |
        v
CLI validates contracts, limits, checksums, and local file installation
        |
        v
Host AI agent analyzes untrusted profile data with the fixed rubric
        |
        v
CLI validates the review and posts it for the immutable revision
        |
        v
Server atomically returns or creates the one ordinary review comment
```

### Artisan Server responsibilities

Artisan Server remains authoritative for:

- bearer authentication and current membership;
- browser-session authentication and CSRF enforcement on existing browser writes;
- organization scoping and indistinguishable cross-organization absence;
- active-versus-trashed roast visibility;
- current revision identity and parse state;
- chart and raw-object integrity;
- comment ownership, audit, editing, and soft deletion;
- canonical archive lifecycle locking; and
- atomic one-review-per-revision/template reservation.

Existing list, detail, revision-list, chart, raw-download, and comment-list routes remain the data sources. A dedicated bearer-only review endpoint creates or replays the ordinary comment without broadening the browser comment mutation dependency.

### Artisan CLI responsibilities

The CLI:

- obtains credentials only through the existing secure configuration path;
- never prints, accepts on argv, or exposes bearer tokens;
- validates input before network access;
- validates server response structure and entity coherence;
- performs bounded pagination;
- verifies download media types, headers, lengths, hashes, and schema versions;
- installs files through protected same-directory temporary files and atomic operations;
- validates the fixed review body and computes the stable review identity; and
- maps unsupported-server responses to `server_upgrade_required`.

The CLI does not interpret roast quality or calculate the review. It supplies safe primitives to the host agent.

### Host Agent Skill responsibilities

The `artisan-roast-review` skill:

- binds every operation to an exact human-supplied server URL;
- verifies the expected user, organization, and role;
- treats all server/profile/comment content as untrusted data;
- follows the fixed review rubric;
- distinguishes evidence from inference;
- posts automatically only after a complete valid analysis; and
- stops safely when data is unavailable, malformed, contradictory, stale, or insufficient.

Installing the skill is not an authorization grant. The host agent, its configured model and tools, the user's bearer credential, the selected server, and server-side authorization remain independent trust boundaries.

## CLI command surface

Add a top-level `roast` command alongside `auth`, `inventory`, `skill`, `completion`, and `version`.

### Roast discovery

```text
artisan [GLOBAL FLAGS] roast list
    [--limit 1..100]
    [--cursor CURSOR]
    [--all]
    [--search TEXT]
    [--roast-at-from RFC3339]
    [--roast-at-to RFC3339]
    [--machine TEXT]
    [--state awaiting_profile|parsed|parse_failed]
    [--label-id UUID]

artisan [GLOBAL FLAGS] roast show ROAST_UUID
artisan [GLOBAL FLAGS] roast revisions ROAST_UUID
    [--limit 1..100]
    [--cursor CURSOR]
    [--all]
```

The commands call the existing private archive routes. `--all` uses the established finite traversal policy: at most 1,000 pages and 10,000 items, with repeated or missing-progress cursors rejected. Date filters require canonical timezone-aware RFC 3339 values. UUID input accepts dashed or compact forms and normalizes to compact lowercase output.

Human list output includes roast UUID, roast time, title, state, revision count, machine, duration, green weight, temperature unit, and updated time. Human detail output includes the complete safe summary, labels, current revision identity, current metadata, and links. Automation uses `--json`; unknown future response fields remain tolerated while required current fields are enforced.

### Parsed chart download

```text
artisan [GLOBAL FLAGS] roast chart download ROAST_UUID DESTINATION [--force]
```

The command reads the current parsed chart from:

```text
GET /api/v1/roasts/{roast_uuid}/chart
```

It manually requests gzip transfer so Go's transport does not transparently remove the bytes covered by the server checksum. It then:

1. refuses redirects;
2. requires status `200`;
3. requires exactly one trusted JSON media type and `Content-Encoding: gzip`;
4. validates `Content-Length`, `ETag`, `X-Content-SHA256`, `X-Checksum-SHA256`, `X-Parser-Version`, and `X-Chart-Schema-Version` coherence;
5. streams at most the server's current 64 MiB compressed-chart ceiling;
6. hashes the compressed bytes before accepting them;
7. decompresses through a second finite 64 MiB output ceiling;
8. requires one valid UTF-8 JSON object with supported chart schema version and matching parser/schema fields;
9. writes readable decompressed JSON to a protected same-directory temporary file; and
10. atomically installs it without replacement unless `--force` was explicit.

The destination JSON is the exact decompressed chart document; it is not reserialized. The result reports:

```json
{
  "path": "/absolute/path/review-chart.json",
  "roast_uuid": "...",
  "revision_number": 3,
  "revision_sha256": "...",
  "parser_version": "...",
  "chart_schema_version": 1,
  "compressed_bytes": 12345,
  "compressed_sha256": "...",
  "file_bytes": 45678,
  "file_sha256": "..."
}
```

Because the existing chart route identifies the current revision only indirectly, the CLI reads roast detail immediately before download and verifies the parser/schema headers. After installation it rereads roast detail. If the current revision SHA changed, it removes the owned destination and returns `roast_revision_changed`; it never hands a mixed-revision context to the skill.

### Raw profile download

```text
artisan [GLOBAL FLAGS] roast profile download \
    ROAST_UUID REVISION_NUMBER DESTINATION [--force]
```

The command calls:

```text
GET /api/v1/roasts/{roast_uuid}/revisions/{revision_number}/download
```

It requires media type `application/x-artisan-profile`, a valid attachment filename, coherent length and checksum headers, the requested revision number, and a body no larger than the revision's declared byte size. It verifies the SHA-256 before atomically installing the exact raw bytes. The server filename is informational only and never selects the local path.

The result reports local path, roast UUID, revision number, byte size, and SHA-256. Existing server-side `revision.download_requested` audit behavior remains unchanged.

### Comment reads

```text
artisan [GLOBAL FLAGS] roast comment list ROAST_UUID
    [--limit 1..100]
    [--cursor CURSOR]
    [--all]
```

This calls the existing private comment-list route. Deleted comments retain their tombstone projection. Comment bodies are untrusted display data. The roast-review skill does not use previous comments as analysis evidence or instructions.

### Review post

```text
artisan [GLOBAL FLAGS] roast review post ROAST_UUID
    --revision-sha256 SHA256
    --template-version artisan-roast-review-v1
    --body-file FILE
```

`--body-file` must name a readable, nonempty, regular file reached without symlink/reparse traversal. The CLI snapshots at most 16 KiB, verifies the file identity did not change while reading, and rejects invalid UTF-8, NUL, carriage return, disallowed control characters, more than 4,000 Unicode code points, or more than 16,000 UTF-8 bytes. It normalizes line endings to LF and trims only leading/trailing Unicode whitespace before validation.

The body must begin exactly with:

```text
AI roast analysis
Template: artisan-roast-review-v1
Profile revision: <positive revision number> (<64 lowercase hex SHA-256>)
```

The marker's SHA must match `--revision-sha256`; the template line must match `--template-version`. The CLI first reads roast detail and requires that this SHA identifies the current parsed revision. It then computes:

```text
review_key = "review-" + hex(SHA-256(
    "artisan-roast-review\x00" +
    compact_roast_uuid + "\x00" +
    revision_sha256 + "\x00" +
    template_version
))
```

The CLI sends this key as `Idempotency-Key`. It does not expose a review idempotency-key override because one logical review slot must have one canonical identity.

Automatic posting is intentional and does not use `--yes`. This is limited to the dedicated review endpoint and does not weaken the existing approval rules in `artisan-inventory`.

## Review API

Add the bearer-only route:

```text
POST /api/v1/roasts/{roast_uuid}/comments/ai-review
```

Required headers:

```text
Authorization: Bearer <credential>
Idempotency-Key: review-<64 lowercase hex>
Content-Type: application/json
```

Strict request body:

```json
{
  "body": "AI roast analysis\n...",
  "revision_sha256": "<64 lowercase hex>",
  "template_version": "artisan-roast-review-v1"
}
```

The endpoint accepts active member and administrator credentials. It does not accept browser cookies as a fallback, does not inspect CSRF cookies, and does not alter the existing browser comment-create route. Missing, expired, revoked, or invalid credentials return the established `401 authentication_required`. A current non-admin member is authorized.

Request JSON is bounded by the existing 64 KiB annotation request ceiling and parsed with the same strict UTF-8, single-document, object-only rules as comments. Unknown fields, booleans in string fields, invalid controls, invalid heading/marker coherence, unsupported template versions, malformed SHA values, and invalid canonical idempotency keys return `422 invalid_review` without revealing request content.

### Transaction and lock order

The review service owns one PostgreSQL transaction and follows this order:

1. validate and normalize the bounded request outside database locks;
2. recompute and compare the canonical review key;
3. acquire an organization-and-review-key transaction advisory lock;
4. acquire the canonical organization-and-roast archive lifecycle lock;
5. select the active tenant-scoped roast row `FOR UPDATE` and return non-disclosing `404 not_found` if absent or trashed;
6. load any existing key claim and reject `409 review_idempotency_conflict` only when that key belongs to a different roast/revision/template identity;
7. load an existing review slot for the requested revision/template and return its original comment when present;
8. otherwise require that the requested SHA is the roast's current revision, that the revision is parsed, and that chart data is available;
9. create the ordinary comment, review-slot record, and existing `comment.created` audit event; and
10. commit all three atomically.

Every review writer uses the same canonical roast lifecycle lock as revision upload, trash/restore, detail editing, labels, and comments. It never calls a helper that commits independently while the review transaction is open.

### First-writer-wins replay semantics

The review slot identity is:

```text
organization + immutable roast revision + template version
```

The first committed body and authenticated author win. Any concurrent or later request for the same slot returns that original comment with:

```text
X-Idempotent-Replay: true
```

The initial creation returns `X-Idempotent-Replay: false`. Both responses use the original successful status `201`, identical `Location`, and the ordinary `CommentView` body. This specialized first-writer contract intentionally ignores different wording from a later nondeterministic AI run; it does not edit or append another comment.

A canonical review key reused for a different roast/revision/template returns `409 review_idempotency_conflict`. A new key cannot bypass slot uniqueness. The response never echoes the key.

If a completed review is retried after a later profile revision was uploaded, the existing old-revision slot still replays. If no old slot exists and the supplied SHA is no longer current, the endpoint returns `409 roast_revision_changed`. The agent discards the stale draft, refetches, and may restart analysis once.

If the result comment was edited, replay returns the edited ordinary comment. If it was soft-deleted, replay returns the tombstone and never recreates it.

### Response and caching

The response is the existing `CommentView` schema. Bearer-created review comments report `can_edit: false` and `can_delete: false` through bearer reads, matching current comment projection behavior. Users may manage their comment through the authorized browser UI.

Responses include:

```text
Cache-Control: no-store
Location: /api/v1/roasts/{roast_uuid}/comments/{comment_uuid}
X-Idempotent-Replay: true|false
X-Roast-Revision-SHA256: <sha256>
X-Review-Template-Version: artisan-roast-review-v1
```

No response, error, audit payload, or operational log includes the bearer token or server URL. Errors do not echo comment text, profile metadata, titles, or event labels.

## Persistence

Add Alembic revision `0015_roast_ai_reviews` after current head `0014_roast_detail_editing`.

Create `roast_review_comments` with:

- UUID primary key;
- `organization_id`;
- `roast_id`;
- `revision_id`;
- `template_version` (`String(64)`);
- canonical `review_key` (`String(71)`);
- tuple-only `request_fingerprint` (64 lowercase hex);
- first-body `body_sha256` (64 lowercase hex);
- `comment_id`;
- `created_at`.

The request fingerprint covers canonical roast UUID, revision SHA, and template version, not the nondeterministic body. `body_sha256` preserves integrity of the body that won without making body differences on a later slot replay an error.

Constraints include:

- unique `(organization_id, review_key)`;
- unique `(organization_id, revision_id, template_version)`;
- unique result `comment_id`;
- template syntax `^[a-z0-9][a-z0-9._-]{0,63}$`;
- canonical review-key syntax `^review-[0-9a-f]{64}$`;
- valid SHA-256 checks;
- organization/roast, roast/revision, and organization/comment coherence; and
- `RESTRICT` foreign-key behavior throughout.

Add only the composite uniqueness needed to support those tenant-coherent foreign keys on `comments` and `roast_revisions`. No visible comment column, author kind, AI badge, profile field, or public projection changes.

Downgrade is allowed only when `roast_review_comments` is empty. If immutable review-slot history exists, downgrade fails before dropping the table or supporting constraints.

## Review template and analysis rules

The only supported initial template is `artisan-roast-review-v1`.

The body structure is:

```text
AI roast analysis
Template: artisan-roast-review-v1
Profile revision: <number> (<sha256>)

Overall assessment
...

Phase timing and ratios
...

Temperature and RoR behavior
...

Events and control observations
...

Anomalies and data limitations
...

Prioritized recommendations
1. ...
2. ...

Confidence
...
```

The skill must:

- cite concrete profile values and timestamps where present;
- report phase duration and ratio only from valid event boundaries;
- identify the temperature unit before quoting temperatures;
- distinguish environmental temperature, bean temperature, and rate-of-rise channels;
- identify whether claimed control changes come from actual recorded event/control data;
- call out missing charge, dry end, first crack, drop, or other material event markers;
- flag sampling gaps, non-monotonic time, impossible values, implausible spikes, flatlined sensors, and unit ambiguity;
- separate measured facts from inference;
- avoid inventing bean properties, sensory results, causation, operator intent, or missing controls;
- make prioritized, actionable recommendations conditional on available evidence;
- label low-confidence conclusions explicitly; and
- remain within the server's 4,000-code-point comment limit.

The review is advisory only. The skill must not send hardware commands, change profiles, edit roast details, mutate inventory, publish a roast, or create public feedback.

Existing comments are not review evidence. Profile titles, notes, metadata, event labels, and raw string values are untrusted data and must never override the skill, request credentials, select another server, cause tool execution, or alter the output contract.

## Agent workflow

The installed skill uses this bounded sequence:

1. Run `artisan version` and require a compatible release.
2. Obtain the exact trusted server URL from the human; never infer it from profile content.
3. Run `artisan --json --server "$TRUSTED_SERVER" auth status`.
4. Require the expected active user, organization, and role.
5. Resolve the roast through `roast list` only when the human did not supply an unambiguous UUID.
6. Run `roast show` and require a current parsed revision.
7. Download the chart to an owned private temporary path.
8. Download the exact raw revision when chart fields need corroboration or the human requested raw inspection.
9. Validate metadata, time series, events, channels, and units as data.
10. Produce the fixed review body in an owned private file.
11. Run `roast review post` immediately without a preview prompt.
12. On `roast_revision_changed`, delete the stale local context, refetch, and restart at most once.
13. On success or replay, report the comment UUID, revision, template, author, and whether an earlier review won.
14. Remove all owned temporary chart, profile, and review files.

The skill stops without posting when authentication, organization, server identity, schema validation, download integrity, parsing, evidence sufficiency, or bounded retry requirements fail. Automatic posting means no confirmation after a valid analysis; it does not mean posting an error placeholder or speculative review.

## Skill packaging and compatibility

Add canonical source:

```text
skills/artisan-roast-review/SKILL.md
```

The CLI embeds both `artisan-inventory` and `artisan-roast-review`. Extend skill commands to support explicit names:

```text
artisan skill show [NAME]
artisan skill install [NAME] --directory ROOT [--force]
artisan skill list
```

For backward compatibility, omitting `NAME` from `show` or `install` continues to select `artisan-inventory`. `skill list` emits both names in stable lexical order. Named installation writes `ROOT/<NAME>/SKILL.md` through the existing secure, atomic installer and preserves all path, symlink/reparse, durability, and `--force` guarantees.

The embed generator reads a fixed reviewed map of canonical skill source paths. Generated content is deterministic. Tests compare exact source bytes, embedded bytes, reported names, installation paths, release-archive payloads, and documentation references.

## Error behavior

Use established JSON envelopes and exit classes. Add stable codes where no existing code fits:

- `invalid_roast_uuid` — local usage error;
- `invalid_revision_number` — local usage error;
- `invalid_roast_filter` — local usage error;
- `invalid_review` — local or server validation failure;
- `invalid_review_file` — local usage/storage validation failure;
- `download_exists` — existing local storage class;
- `download_failed` — local/network storage class without sensitive detail;
- `roast_revision_changed` — HTTP 409 reconciliation failure;
- `review_idempotency_conflict` — HTTP 409 key identity conflict;
- `chart_unavailable` — preserved server API error;
- `not_found` — preserved non-disclosing entity absence; and
- `server_upgrade_required` — CLI classification when the compatible route or contract is absent.

Safe reads retain bounded transient retries. Chart and profile downloads retry only before destination installation and reset the owned temporary file between attempts. `review post` retries only the exact immutable body with the same computed key. On timeout or ambiguous transport exhaustion, the skill invokes the same command again with the same body; the server review slot reconciles the result.

Malformed success responses, unexpected status, redirects, conflicting headers, reflected credentials/server URLs, or oversized bodies return `invalid_server_response` without installing files or claiming success.

## Observability and privacy

The first successful review emits exactly one existing `comment.created` audit event. Replays emit no second comment event. The companion review row provides the revision/template link needed for correctness; it is not exposed as a public comment-author badge.

Privacy-safe operational logs may include organization ID, roast UUID, revision number, template version, comment UUID, result (`created` or `replayed`), duration bucket, and HTTP status. They must not include:

- profile or chart bytes;
- metadata, title, notes, operator, machine text, or event labels;
- review body or body excerpts;
- review key or bearer token;
- server credentials; or
- raw local paths supplied by the client.

## Documentation

Update CLI documentation for:

- roast command hierarchy and examples;
- JSON result contracts;
- download integrity and local-file behavior;
- the automatic-posting boundary;
- the fixed template and one-review semantics;
- member authorization;
- error and exit behavior;
- installing either embedded skill; and
- the exact minimum compatible Artisan Server commit.

Update server API and release documentation for the new route, migration, privacy boundary, and server-first rollout. Public documentation must not imply that Artisan Server itself runs AI or that installing a skill grants authorization.

## Testing

### Server model and migration tests

Use disposable PostgreSQL and cover:

- fresh upgrade through `0015_roast_ai_reviews`;
- upgrade from `0014_roast_detail_editing` with existing comments and revisions;
- empty-table downgrade success;
- nonempty-table downgrade refusal without partial schema loss;
- composite tenant/revision/comment foreign keys;
- unique slot, unique key, unique result comment, syntax, hash, and `RESTRICT` constraints;
- Alembic single-head and model/migration parity; and
- no changes to existing comment, revision, trash, or roast-detail data.

### Server service and API tests

Cover:

- member and administrator bearer success;
- missing, malformed, expired, revoked, inactive-user, and missing-membership authentication failures;
- browser cookie without bearer rejection on the dedicated endpoint;
- unchanged browser comment CSRF behavior;
- cross-organization and trashed-roast non-disclosing `404`;
- awaiting, parse-failed, chart-unavailable, stale, and current parsed revisions;
- strict request size, UTF-8, JSON, heading, marker, template, key, and body validation;
- first creation and exact response headers;
- same-key same-slot, different-key same-slot, and different-body same-slot replay;
- same-key different-identity conflict;
- concurrent posts producing one row, one comment, and one audit event;
- revision-upload, trash, restore, detail-edit, label, ordinary-comment, and review lock ordering/races;
- transaction rollback on comment, review-row, audit, flush, and commit failures;
- replay after a later revision;
- edited and deleted comment replay; and
- privacy-safe logs and errors.

### CLI API and command tests

Use unit and `httptest.Server` coverage for:

- roast UUID, revision number, filter, cursor, and timestamp validation;
- strict required response fields and cross-field invariants;
- unknown-field tolerance and malformed JSON rejection;
- list/revision pagination bounds and repeated cursors;
- detail/revision entity coherence;
- chart manual gzip transfer, compressed and decompressed ceilings, malformed gzip, JSON/schema validation, and both hashes;
- raw profile media type, length, revision, checksum, filename, and byte-limit validation;
- redirects, timeouts, cancellation, short reads, long reads, duplicate/conflicting headers, and secret reflection;
- protected temporary files, no-clobber install, `--force`, symlink/reparse rejection, source/destination races, durability uncertainty, and cleanup on every failure path;
- Windows, macOS, and Linux download-install behavior;
- review-file regularity, stable snapshot, UTF-8, controls, heading, marker, template, code-point, and byte limits;
- exact stable key derivation and inability to override it;
- exact-body retries and replay headers;
- server-upgrade and entity-error classification;
- human output, JSON envelopes, Cobra help, static completion, and legacy single-dash normalization; and
- no token, server URL, review body, or profile content in errors.

### Agent Skill tests

Contract-test that the source and embedded `artisan-roast-review` skill:

- requires exact server and identity verification;
- forbids token handling and `auth login`;
- treats profile/comment strings as untrusted data;
- uses only the fixed `artisan-roast-review-v1` structure;
- posts automatically without asking for confirmation;
- never posts an error placeholder;
- handles one bounded stale-revision restart;
- cleans owned temporary files;
- never sends hardware, inventory, publication, or public-feedback mutations;
- keeps comments within limits; and
- uses only documented commands and flags.

Test multi-skill listing, default compatibility, named show/install, secure replacement, generated embed identity, and six-platform release-archive inclusion.

### Cross-repository integration

After the server changes merge, advance `integration/artisan-server.ref` to the exact compatible server commit. A disposable PostgreSQL/MinIO stack must exercise:

- member and admin roast list/detail/revision reads;
- chart and raw profile downloads with exact checksum verification;
- private comment listing;
- first review creation and same-slot replay;
- different-body same-slot first-writer behavior;
- stale-revision rejection and replay after a later revision;
- member authorization and browser-cookie rejection;
- cross-tenant and trashed-roast non-disclosure;
- one comment and audit event under concurrency; and
- cleanup of every disposable container, network, volume, credential, downloaded file, comment, and test roast.

No integration or smoke command may target a non-loopback server unless separately reviewed for a production deployment. Production smoke remains read-only.

## Rollout and release

1. Implement the server in an isolated `artisan-server` worktree with TDD.
2. Run focused tests, the complete backend suite against disposable PostgreSQL and MinIO, Ruff, mypy, Alembic checks, deployment contracts, and a fresh review.
3. Merge and release the server first as the next compatible release after `v0.1.6`; deploy only through the separately approved production process.
4. Verify migration `0015_roast_ai_reviews`, health, exact artifact identity, tenant/auth boundaries, storage identity, and bounded privacy-safe logs. Do not create a production AI review for smoke testing.
5. Implement the CLI and skill in the isolated `artisan-cli` worktree against the merged exact server commit.
6. Run focused tests, full Go tests with the clean Go 1.23.2 toolchain, vet/static checks, native platform CI, release-contract checks, and disposable cross-repository integration.
7. Merge the CLI only after the server is deployed and healthy.
8. Release the CLI as the next minor version (`v0.4.0` given current `v0.3.0`), publish the six established platform archives plus checksums/provenance, and independently verify embedded skills, version/commit identity, and every artifact.
9. Preserve unrelated main-checkout files and worktrees throughout; remove only owned feature/release worktrees after accepted integration.

Release and production deployment are separate approval-bearing operations. Completing implementation does not itself authorize a tag, artifact publication, production database migration, deployment, or real-roast comment creation.

## Acceptance criteria

The feature is complete when:

1. An active member or administrator can use Artisan CLI to list/show roasts and revisions and retrieve both validated chart JSON and exact raw `.alog` bytes.
2. The separate installed Agent Skill can analyze a current parsed revision with the fixed evidence-based rubric and post automatically.
3. The result is one private ordinary comment attributed to the authenticated member and visibly headed `AI roast analysis`.
4. Concurrent requests, network retries, and later reruns produce or return exactly one comment for the organization/revision/template slot.
5. A profile revision change cannot cause a new stale analysis comment; an already committed older review remains replayable.
6. Edited or deleted review comments are respected and never automatically replaced or recreated.
7. Browser CSRF, bearer authentication, member authorization, tenant isolation, trash visibility, revision immutability, audit, and lifecycle locking remain correct.
8. Downloads are bounded, integrity-checked, private, race-safe, and cross-platform, with no partial destination on failure.
9. Existing CLI behavior and the default `artisan-inventory` skill installation remain compatible.
10. Server and CLI focused/full/static/native/integration checks pass, the exact server compatibility commit is pinned, and no production mutation was used as a smoke test.
