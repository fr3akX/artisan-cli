# Artisan Inventory CLI Design

**Date:** 2026-08-07  
**Status:** Approved design  
**Primary repository:** [`fr3akX/artisan-cli`](https://github.com/fr3akX/artisan-cli)  
**Server repository:** [`fr3akX/artisan-server`](https://github.com/fr3akX/artisan-server)  
**Tracking issue:** [`fr3akX/artisan-server#4`](https://github.com/fr3akX/artisan-server/issues/4)

## Summary

Build a native command-line client named `artisan` for Artisan Roast Server. The
MVP provides complete green-coffee inventory management for administrators and
retains the existing desktop-token capabilities for ordinary members. It uses
the same opaque bearer credentials already issued for the Artisan desktop
application.

The client is written in Go and distributed as one statically linked executable
per supported operating-system and architecture pair. It has no runtime,
interpreter, package-manager, or shared-library dependency. A portable
`artisan-inventory` Agent Skill is included in the repository, release archives,
and executable.

The server gains an additive bearer-authenticated inventory administration API.
Existing browser and Artisan desktop API contracts remain unchanged.

## Goals

1. Provide complete inventory administration from scripts, terminals, and AI
   agents.
2. Reuse current desktop bearer credentials without introducing a second token
   type.
3. Authorize each request from the credential owner's current organization role.
4. Ship a dependency-free native executable for Linux, macOS, and Windows.
5. Offer stable human-readable and machine-readable output contracts.
6. Make audited or irreversible operations explicit and safe.
7. Preserve organization isolation, append-only inventory accounting,
   idempotency, image normalization, and existing audit attribution.
8. Bundle a portable AI skill that never handles credentials and requires human
   approval for sensitive mutations.

## Non-goals

The MVP does not:

- administer users, organizations, credentials, roasts, labels, publications, or
  public feedback;
- create a new credential scope or token format;
- replace the Artisan desktop inventory connector;
- support multiple inventory locations;
- edit or delete existing ledger entries;
- bypass server validation or calculate authoritative balances locally;
- provide a graphical or interactive full-screen interface;
- automatically update itself;
- sign binaries with Apple, Microsoft, or other platform signing identities;
- validate mutations against production inventory during development.

## Product decisions

- The executable and user-facing command are named `artisan`.
- The implementation language is Go.
- The source lives in the dedicated public AGPLv3 repository
  `fr3akX/artisan-cli`, on `main`.
- The CLI is a separately downloadable native client, not a command inside the
  server container.
- Administrator-owned desktop credentials receive full inventory administration.
- Member-owned credentials retain only the existing reduced lot-list and
  reservation capabilities. They do not gain the new administration routes.
- Role changes, user deactivation, credential expiration, and revocation take
  effect on the next request.
- The Agent Skill uses the portable `SKILL.md` format and no Pi-specific tools.

## Repository boundaries

### `artisan-cli`

The CLI repository owns:

- Go command parsing and execution;
- HTTP transport and wire models;
- local configuration and credential storage;
- human and JSON output;
- confirmation and exit-code behavior;
- the portable Agent Skill;
- cross-platform tests and release builds;
- CLI installation, compatibility, and security documentation.

Planned layout:

```text
cmd/artisan/                  executable entry point
internal/api/                 HTTP transport, requests, responses, wire validation
internal/auth/                token input, storage, loading, and redaction
internal/config/              OS-standard paths and server configuration
internal/command/             auth, inventory, version, and skill commands
internal/confirm/             TTY confirmation and --yes policy
internal/output/              human views and stable JSON envelopes
internal/release/             embedded version/build metadata
skills/artisan-inventory/     portable Agent Skill
.github/workflows/            validation and release workflows
docs/                         user, security, and compatibility documentation
```

### `artisan-server`

The server repository owns:

- bearer authentication and current-role authorization;
- HTTP request and response contracts;
- inventory validation, transactions, accounting, idempotency, and audit data;
- image verification, normalization, object storage, and streaming;
- database migrations if any become necessary;
- server unit, integration, and disposable-stack E2E tests;
- deployment-order and API documentation.

The repositories do not import each other's source or use filesystem-relative
shared code. The HTTP API is their only production interface.

## Language and dependencies

The module path is `github.com/fr3akX/artisan-cli`. The minimum supported Go
version is 1.23. Builds set `CGO_ENABLED=0`.

Use the standard library for argument parsing, HTTP, JSON, multipart encoding,
cryptographic randomness, file handling, tables, and tests. The only planned
external modules are narrowly scoped `golang.org/x/term` terminal support and
`golang.org/x/sys` platform primitives needed to establish or verify private
Windows credential-file access. These modules are compiled into the executable
and create no runtime dependency. Any transitive licenses are recorded in
`THIRD_PARTY_NOTICES.txt`.

Do not add a general CLI framework, HTTP framework, configuration framework,
logging framework, or automatic-update framework for the MVP.

## Authentication and authorization

The CLI accepts the opaque credential already created from Artisan Server's
**Desktop credentials** page. It sends the token only in:

```http
Authorization: Bearer <token>
```

The server resolves the credential, issuing user, organization, and current
membership on every request. New administration routes require a new
`require_bearer_admin` dependency that composes existing bearer lookup with the
existing `ensure_admin()` role check.

Authorization results are:

| Credential state | Existing desktop list/reservations | New admin API |
|---|---:|---:|
| Active, owner is administrator | allowed | allowed |
| Active, owner is member | allowed | `403 administrator_required` |
| Expired or revoked | `401 authentication_required` | `401 authentication_required` |
| Owner inactive or membership unavailable | `401 authentication_required` | `401 authentication_required` |

No role or scope is copied into the credential at issuance time. Demotion from
administrator to member therefore removes administration access immediately
without rotating the token.

The existing bearer identity endpoint `GET /api/v1/auth/me` supplies user,
organization, and current role for `artisan auth login` validation and
`artisan auth status`.

## Local configuration and credential storage

`artisan --server URL auth login` reads the token from a no-echo terminal prompt.
`--token-stdin` explicitly reads one line from standard input for controlled
automation. The token is never accepted as a command-line value because command
arguments may appear in process listings and shell history.

The client uses `os.UserConfigDir()` and an `artisan` child directory:

- Linux: `$XDG_CONFIG_HOME/artisan`, or `~/.config/artisan` when unset;
- macOS: `~/Library/Application Support/artisan`;
- Windows: `%AppData%\artisan`.

`config.json` stores the canonical server URL. `credentials.json` stores the raw
token separately. On Unix, the directory is mode `0700` and credential file is
mode `0600`; startup rejects a credential file accessible to group or others.
On Windows, the client establishes and verifies an ACL limited to the current
user and required system principals. It refuses persistent login if private
access cannot be established.

Environment variables override stored values without modifying files:

```text
ARTISAN_SERVER_URL
ARTISAN_SERVER_TOKEN
```

The token is never printed by `auth status`, JSON output, errors, debug output,
or the Agent Skill. `auth logout` removes only the stored token and leaves the
server URL. Revocation remains a server/browser operation.

## URL and transport security

- HTTPS is required for non-loopback servers.
- Plain HTTP is accepted only for `localhost`, `127.0.0.0/8`, and `[::1]`.
- URLs must not contain user information, fragments, or embedded credentials.
- The configured URL is normalized to an origin without a trailing slash or API
  path.
- Authenticated requests never follow redirects. A redirect is an error even
  when it appears to retain the same origin.
- The default request timeout is 30 seconds and is configurable with a bounded
  global `--timeout` duration.
- The Go transport may honor standard proxy environment variables, but bearer
  values are not included in diagnostics.
- TLS verification cannot be disabled by a general `--insecure` flag.
- Responses have bounded JSON and error-body reads. Image downloads stream to a
  temporary file and atomically rename on success.

## Server API

### Compatibility boundary

The existing reduced endpoint `GET /api/v1/inventory/bean-lots` has a strict
Artisan desktop response shape. It and existing reservation routes remain byte-
and behavior-compatible. Expanded operations therefore use a distinct namespace:

```text
/api/v1/inventory/admin
```

Every route in this namespace requires `require_bearer_admin`. Browser cookies
and CSRF tokens are not accepted there. Existing browser routes continue to use
browser sessions and CSRF protection.

### Read routes

```text
GET /api/v1/inventory/admin/bean-lots
GET /api/v1/inventory/admin/bean-lots/{lot_id}
GET /api/v1/inventory/admin/bean-lots/{lot_id}/ledger
GET /api/v1/inventory/admin/bean-lots/{lot_id}/reservations
GET /api/v1/inventory/admin/bean-lots/{lot_id}/conflicts
GET /api/v1/inventory/admin/conflicts/{conflict_id}
GET /api/v1/inventory/admin/bean-lots/{lot_id}/images/{image_id}/thumbnail
GET /api/v1/inventory/admin/bean-lots/{lot_id}/images/{image_id}/display
```

List/detail/filter projections reuse the strict browser inventory schemas. Their
hypermedia image and lot links use the admin namespace, not browser-cookie URLs.
The lot list accepts the existing `limit`, `cursor`, `q`, `state`, `availability`,
`conflict`, and `roast_uuid` filters. Ledger, reservation, and conflict lists use
existing cursor pagination and maximum page sizes.

Image responses remain private WebP streams with `Cache-Control: private,
no-store` and `X-Content-Type-Options: nosniff`.

### Mutation routes

```text
POST   /api/v1/inventory/admin/bean-lots
PATCH  /api/v1/inventory/admin/bean-lots/{lot_id}
POST   /api/v1/inventory/admin/bean-lots/{lot_id}/adjustments
POST   /api/v1/inventory/admin/bean-lots/{lot_id}/images
PATCH  /api/v1/inventory/admin/bean-lots/{lot_id}/images/{image_id}
PUT    /api/v1/inventory/admin/bean-lots/{lot_id}/images/order
DELETE /api/v1/inventory/admin/bean-lots/{lot_id}/images/{image_id}
POST   /api/v1/inventory/admin/conflicts/{conflict_id}/resolve
```

All mutations require the existing strict `Idempotency-Key` contract. Requests,
responses, status codes, image limits, multipart manifests, and validation rules
match the corresponding browser operation. Mutations call the same service
functions, so accounting, organization isolation, conflict rules, image cleanup,
and transaction semantics cannot diverge.

Audit and ledger actor attribution remains `desktop` with the actual API
credential ID. No browser user ID is substituted merely because the credential
owner is an administrator.

### Existing reservation routes

Reservation commands continue to call the current endpoints so both admin and
member credentials retain the same behavior:

```text
POST /api/v1/inventory/reservations
POST /api/v1/inventory/reservations/{client_reservation_uuid}/finalize
POST /api/v1/inventory/reservations/{client_reservation_uuid}/release
```

## Command model

Global commands and groups are:

```text
artisan version
artisan auth login|logout|status
artisan inventory lot list|show|create|update|archive|restore
artisan inventory lot ledger|reservations|conflicts
artisan inventory adjust
artisan inventory reservation create|finalize|release
artisan inventory conflict list|show|resolve
artisan inventory image add|update|reorder|delete|download
artisan skill show|install
```

Global options precede the command:

```text
--json
--server URL
--timeout DURATION
```

`--server` is a one-command override and does not persist except when supplied to
`auth login`. There is no `--token` option.

### Lot commands

- `lot list` supports all server filters, `--limit`, `--cursor`, and `--all`.
- `lot show LOT_ID` returns the complete lot projection.
- `lot create` accepts flags for every lot field, repeated `--varietal`, opening
  grams/reason/reference, and repeated image declarations.
- `lot update LOT_ID` accepts mutable field flags. Repeated `--clear FIELD`
  explicitly clears nullable values. It cannot clear `name`.
- `lot archive LOT_ID` and `lot restore LOT_ID` send state-only patches.
- `lot ledger LOT_ID`, `lot reservations LOT_ID`, and `lot conflicts LOT_ID`
  expose paginated history.

Create and update also accept `--from-json FILE` or `--from-json -`. The JSON
form is the exact API request model. Combining `--from-json` with field flags is
a usage error; this prevents ambiguous precedence.

### Adjustment, reservation, conflict, and image commands

- `adjust LOT_ID --grams SIGNED_INTEGER --reason TEXT [--reference TEXT]`
  records a manual adjustment.
- Reservation create/finalize/release expose every field of the existing strict
  reservation models and accept canonical UUIDs.
- `conflict list --lot LOT_ID` lists that lot's conflicts.
- `conflict show CONFLICT_ID` displays one conflict.
- `conflict resolve CONFLICT_ID --note TEXT` resolves it through the server's
  existing nonnegative-availability rule.
- `image add LOT_ID FILE...` accepts JPEG/PNG files plus per-image caption, alt
  text, and cover metadata.
- `image update` edits caption, alt text, or cover state.
- `image reorder` requires the complete ordered image-ID list.
- `image delete` removes one image.
- `image download` selects `display` or `thumbnail`, writes through a temporary
  file, and does not overwrite unless `--force` is present.

Input UUIDs may be canonical compact lowercase UUIDs or standard dashed UUIDs.
The CLI normalizes them to the server's canonical 32-character lowercase form.
Quantities are integer grams only. Dates and timestamps use the server's
canonical formats.

### Pagination

Without `--all`, list commands return one page and expose the next cursor. With
`--all`, the CLI follows cursors until exhausted while preserving server order.
It rejects repeated cursors and enforces a bounded maximum item count to prevent
an invalid or hostile server from causing an unbounded loop. Human output notes
when more data exists; JSON output includes `next_cursor` for one-page mode and
`next_cursor: null` after a completed `--all` traversal.

## Idempotency and retries

Every mutation receives a cryptographically random idempotency key generated
from `crypto/rand`. The same key is retained for the entire command execution,
including transport retries. Advanced orchestration may provide a validated
`--idempotency-key` value.

Safe reads may retry bounded transient connection failures and `502`, `503`, or
`504` responses with backoff. Mutations may retry only when the request body is
replayable and must reuse the same idempotency key. Multipart retries reopen the
same local files and reproduce the same manifest. The CLI never retries a
mutation with a newly generated key after an ambiguous result.

The server remains authoritative for replay detection and returns its existing
idempotent-replay information where the underlying contract supplies it.

## Confirmation policy

The following commands require confirmation:

- `inventory adjust`;
- `inventory lot archive`;
- `inventory image delete`;
- `inventory conflict resolve`.

On an interactive terminal, the CLI displays the target ID and exact proposed
change, then accepts only an explicit affirmative response. In noninteractive
execution, these commands fail unless `--yes` is supplied. A declined or missing
confirmation performs no request.

The Agent Skill must obtain explicit human approval before adding `--yes`.
Creation, metadata updates, restore, image add/update/reorder, reservation
operations, and reads do not prompt because they are reversible or already part
of the established desktop workflow.

## Output contract

Human-readable tables and labeled details are the default. Identifiers and exact
gram balances are never truncated. Secrets are never included.

Global `--json` writes exactly one JSON value to standard output. Success uses:

```json
{"ok":true,"data":{}}
```

Failure uses:

```json
{"ok":false,"error":{"code":"administrator_required","message":"Administrator required","http_status":403}}
```

`http_status` is omitted for local failures. JSON output contains no ANSI escape
sequences and no prose outside the envelope. Human diagnostics and progress go
to standard error. Successful image bytes are written only to the requested
file, never standard output.

The CLI tolerates unknown response object fields for forward compatibility but
requires and validates fields it consumes. Malformed success or error responses
fail as `invalid_server_response` rather than being presented as trusted data.

## Exit codes

| Code | Category |
|---:|---|
| 0 | Success |
| 2 | Command usage or local input validation |
| 3 | Missing or unsafe local configuration |
| 4 | Authentication failure (`401`) |
| 5 | Authorization failure (`403`) |
| 6 | Not found (`404`) |
| 7 | API validation, state conflict, rate limit, or other expected `4xx` |
| 8 | Network, DNS, timeout, proxy, or TLS failure |
| 9 | Server `5xx` or malformed server response |
| 10 | Required confirmation declined or unavailable |
| 130 | Interrupted by the user |

API error codes and messages are preserved after bounded validation. Raw response
bodies, headers, stack traces, and tokens are not printed.

## Portable Agent Skill

The skill is named `artisan-inventory`. It is distributed as:

1. `skills/artisan-inventory/SKILL.md` in source;
2. a file beside the binary in release archives;
3. content embedded into the executable.

Commands:

```text
artisan skill show
artisan skill install --directory AGENT_SKILL_ROOT
```

Installation creates `AGENT_SKILL_ROOT/artisan-inventory/SKILL.md` atomically.
It refuses to overwrite differing content unless `--force` is present. The skill
itself is portable and does not assume Pi, Claude Code, Codex, or another agent.
Documentation lists common agent skill-root locations without changing the
skill content.

The skill instructs an agent to:

- verify `artisan version` and `artisan --json auth status` first;
- use `--json` and stable exit codes for machine interaction;
- never request, inspect, print, persist, or pass the bearer token;
- never perform login for the user;
- resolve names to canonical lot IDs before mutation;
- explain the exact audited or irreversible change before requesting approval;
- invoke `--yes` only after explicit approval;
- never infer adjustment grams, reasons, resolution notes, or deletion intent;
- preserve integer-gram units exactly;
- use safe pagination and not assume the first page is complete;
- surface conflicts instead of silently applying corrective changes;
- re-read the affected lot after mutation and report authoritative balances;
- retain an externally supplied idempotency key when retrying an ambiguous
  operation and never invent a new one for that retry.

The skill includes workflows for lot creation/editing, adjustments, images,
reservations, and conflict resolution.

## Release design

GitHub Actions validates pushes and pull requests on Linux, macOS, and Windows.
Required checks include:

- `gofmt` cleanliness;
- `go vet ./...`;
- `go test ./...`;
- race-enabled tests on supported runners;
- successful native build on each host platform;
- license and embedded-skill contract checks.

Tags matching `v*` produce `CGO_ENABLED=0` executables for:

| OS | Architectures |
|---|---|
| Linux | amd64, arm64 |
| macOS | amd64, arm64 |
| Windows | amd64, arm64 |

Unix artifacts use `.tar.gz`; Windows artifacts use `.zip`. Release assets
include the binary, `LICENSE`, `THIRD_PARTY_NOTICES.txt`, and the portable skill.
GitHub publishes SHA-256 checksums and build provenance. Build flags embed the
version and commit; `artisan --json version` reports both without build paths or
host-specific data.

Platform code signing and notarization are deferred until signing identities are
available. Documentation states that limitation clearly.

## Testing strategy

### Server tests

Add focused tests that prove:

- every admin read and mutation succeeds with an admin-owned active credential;
- member credentials receive `403` from every admin route;
- expired/revoked credentials and inactive users receive `401`;
- demotion takes effect on the next request;
- cross-organization IDs remain indistinguishable from missing IDs;
- browser and existing desktop route shapes are unchanged;
- idempotent replay, concurrent mutation behavior, and actor credential IDs are
  preserved;
- multipart image verification, normalization, cleanup, limits, and private
  streams match browser behavior;
- invalid IDs, cursors, manifests, and payloads retain strict errors.

A disposable Compose E2E scenario issues separate admin and member desktop
credentials, exercises representative reads/mutations/images/reservations, and
proves persistent audit/balance results. It never targets a non-loopback server.

### CLI tests

Use standard `httptest.Server` fixtures and focused package tests for:

- command parsing and every command-to-request mapping;
- authorization headers without token leakage;
- URL normalization, HTTPS enforcement, and redirect rejection;
- environment/config precedence and Unix/Windows credential protection;
- hidden input and stdin login behavior;
- pagination completion, cursor-loop detection, and bounds;
- multipart streaming and exact manifest/file ordering;
- idempotency generation and same-key retry;
- timeout, transient retry, API error, malformed response, and exit-code mapping;
- confirmation behavior on TTY and non-TTY streams;
- human rendering and JSON golden envelopes;
- atomic image download and overwrite policy;
- embedded skill identity, display, and atomic installation;
- version metadata and release artifact startup.

The compiled CLI is also exercised against the disposable Artisan Server stack.
No test needs real roasting hardware, a production account, or a non-loopback
network service.

## Compatibility and rollout

1. Merge and deploy the additive Artisan Server API first.
2. Run full server validation and disposable-stack E2E tests.
3. Tag and publish the CLI only after the deployed server version supports the
   admin namespace.
4. If the namespace returns `404`, the CLI reports `server_upgrade_required`
   rather than treating it as a missing lot.
5. Document the minimum compatible Artisan Server version in each CLI release.
6. Perform production smoke validation with read-only commands first. Any
   production mutation requires a separately approved, intentional inventory
   change; test mutations are prohibited.

The CLI tolerates additive server fields. Removing or changing required fields,
paths, status codes, or semantics requires a new compatibility decision and
release note.

## Security properties

- Raw bearer credentials never enter command arguments, URLs, logs, JSON output,
  skill content, release metadata, or server audit details.
- Server-side organization and role checks apply to every operation.
- Existing CSRF protection remains isolated to cookie-authenticated browser
  routes.
- Redirects cannot forward bearer credentials.
- Local persistent credentials require user-private storage.
- Confirmation protects audited or irreversible human/agent actions but never
  replaces server authorization or validation.
- Idempotency prevents duplicate mutations during bounded retries.
- Image bytes remain bounded, verified, normalized, metadata-stripped, and
  privately streamed by the server.
- Agent instructions cannot authorize themselves to use `--yes`.

## Acceptance criteria

The MVP is complete when:

1. An administrator can install one native `artisan` executable, authenticate
   with an existing desktop credential, and perform every designed inventory
   read and mutation.
2. A member credential can still use existing reduced lot and reservation APIs
   but cannot access any new administration route.
3. Browser and Artisan desktop clients pass their unchanged compatibility tests.
4. Human output is usable and JSON/exit-code contracts are stable and tested.
5. Tokens remain absent from process arguments, output, logs, and test artifacts.
6. Confirmation and idempotency policies are proven by tests.
7. The embedded portable skill installs and guides an agent through safe
   inventory workflows.
8. Linux, macOS, and Windows amd64/arm64 artifacts build with `CGO_ENABLED=0`,
   start successfully, and have published checksums and provenance.
9. Server and CLI disposable-stack integration tests pass without production
   data or services.
