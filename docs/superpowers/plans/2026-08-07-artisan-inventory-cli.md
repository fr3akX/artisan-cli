# Artisan Inventory CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and release the native `artisan` command and portable `artisan-inventory` Agent Skill for complete Artisan Server inventory administration.

**Architecture:** A small standard-library-first Go command dispatches into focused config/auth, HTTP API, inventory command, confirmation, output, and embedded-skill packages. It consumes the additive `/api/v1/inventory/admin` API from the companion server plan while retaining existing reservation endpoints; GitHub Actions emits static cross-platform binaries and verifies one against a disposable pinned server stack.

**Tech Stack:** Go 1.23+, standard library, `golang.org/x/term`, `golang.org/x/sys`, `httptest`, GitHub Actions, Docker Compose disposable Artisan Server integration.

## Global Constraints

- Canonical design: `docs/superpowers/specs/2026-08-07-artisan-inventory-cli-design.md`.
- Server prerequisite plan: `fr3akX/artisan-server/docs/superpowers/plans/2026-08-07-inventory-bearer-admin-api.md`.
- Executable and command name: `artisan`.
- Module path: `github.com/fr3akX/artisan-cli`; minimum Go version 1.23.
- Release builds set `CGO_ENABLED=0` and require no runtime dependencies.
- Use the standard library except narrowly pinned `golang.org/x/term` and `golang.org/x/sys` modules.
- Never accept a bearer token as a command-line argument or print it in any output/error.
- HTTPS is mandatory except for loopback HTTP; authenticated redirects always fail.
- JSON mode emits exactly one envelope on stdout; diagnostics use stderr.
- Sensitive mutations follow the exact confirmation/`--yes` policy in the design.
- API quantities are integer grams and server responses are authoritative.
- No production inventory mutation is part of validation.

---

### Task 1: Go foundation, root command, output envelope, and version

**Files:**
- Create: `go.mod`
- Create: `cmd/artisan/main.go`
- Create: `internal/command/root.go`
- Create: `internal/command/runtime.go`
- Create: `internal/command/root_test.go`
- Create: `internal/output/output.go`
- Create: `internal/output/output_test.go`
- Create: `internal/release/version.go`
- Create: `internal/release/version_test.go`
- Create: `THIRD_PARTY_NOTICES.txt`
- Modify: `.gitignore`

**Interfaces:**
- Produces:
  - `type command.Runtime struct { In io.Reader; Out io.Writer; Err io.Writer; Getenv func(string) string }`.
  - `func command.Run(ctx context.Context, args []string, runtime Runtime) int`.
  - `type output.Error struct { ExitCode int; Code string; Message string; HTTPStatus *int }`.
  - `func output.WriteSuccess(w io.Writer, jsonMode bool, data any, human func(io.Writer) error) error`.
  - `func output.WriteFailure(w io.Writer, jsonMode bool, failure Error) error`.
  - build variables `release.Version` and `release.Commit` plus `release.Info()`.

- [ ] **Step 1: Write failing root/output tests**

Cover `artisan version`, `artisan --json version`, unknown command exit `2`, global
`--json` before a command, no ANSI/prose outside a JSON envelope, and exact output:

```json
{"ok":true,"data":{"version":"dev","commit":"unknown"}}
```

Assert `output.WriteFailure` omits `http_status` when nil and emits one newline-
terminated JSON object.

- [ ] **Step 2: Run RED**

```bash
go test ./...
```

Expected: fail because the module/packages do not exist.

- [ ] **Step 3: Add module and minimal dispatch**

Use:

```go
module github.com/fr3akX/artisan-cli

go 1.23.0
```

`main()` constructs real stdio/getenv runtime and exits with `command.Run`.
Implement a manual root parser using `flag.FlagSet` with `ContinueOnError`; do not
add Cobra or another CLI framework. Global options are `--json`, `--server`, and
`--timeout`, and must precede the subcommand as specified.

- [ ] **Step 4: Implement output and version contracts**

Use typed envelopes:

```go
type successEnvelope struct {
    OK   bool `json:"ok"`
    Data any  `json:"data"`
}
type errorEnvelope struct {
    OK    bool  `json:"ok"`
    Error Error `json:"error"`
}
```

Reject empty version/commit at build-info construction and normalize development
values to `dev`/`unknown`. Run:

```bash
gofmt -w $(find cmd internal -name '*.go' -type f)
go test ./...
go vet ./...
go build -o ./artisan ./cmd/artisan
./artisan version
./artisan --json version
```

Expected: all pass and binary name is `artisan`.

- [ ] **Step 5: Commit**

```bash
git add go.mod cmd internal .gitignore THIRD_PARTY_NOTICES.txt
git commit -m "feat: bootstrap artisan command"
```

---

### Task 2: Secure configuration, URL policy, and credential persistence

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/auth/store.go`
- Create: `internal/auth/store_test.go`
- Create: `internal/auth/permissions_unix.go`
- Create: `internal/auth/permissions_unix_test.go`
- Create: `internal/auth/permissions_windows.go`
- Create: `internal/auth/permissions_windows_test.go`
- Modify: `go.mod`
- Create: `go.sum`
- Modify: `THIRD_PARTY_NOTICES.txt`

**Interfaces:**
- Produces:
  - `type config.Values struct { ServerURL string; Token string; Source config.Source }`.
  - `func config.NormalizeServerURL(raw string) (string, error)`.
  - `func config.Load(configDir string, getenv func(string) string) (Values, error)`.
  - `func config.SaveServer(configDir, serverURL string) error`.
  - `type auth.Store interface { Save(token string) error; Load() (string, error); Remove() error }`.
  - `func auth.NewFileStore(configDir string) Store`.

- [ ] **Step 1: Write URL/config precedence tests**

Table-test valid HTTPS origins and loopback HTTP (`localhost`, `127.0.0.1`,
`127.42.0.1`, `[::1]`). Reject non-loopback HTTP, credentials, query, fragment,
API path, missing host, and unsupported schemes. Assert normalization removes only
the trailing slash.

Test precedence:

```text
ARTISAN_SERVER_URL/TOKEN > stored config/credentials > missing error
```

Environment overrides must not rewrite files.

- [ ] **Step 2: Write platform permission tests and run RED**

Unix tests create group/other-readable credentials and expect `unsafe_credentials`.
Windows tests verify the resulting DACL does not grant generic users/groups. Add a
cross-compile check:

```bash
go test ./internal/config ./internal/auth
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/auth
```

Expected: missing package failures.

- [ ] **Step 3: Implement atomic config and Unix storage**

Use `os.UserConfigDir()` plus `artisan`; create directories as `0700`, write a
same-directory temporary file, `Sync`, chmod, close, and rename. Store server in
`config.json` and token in `credentials.json`. Validate JSON with `DisallowUnknownFields`.
Reject blank or newline-containing tokens.

- [ ] **Step 4: Implement Windows private ACL and pin dependencies**

Use pinned `golang.org/x/sys/windows` APIs to create/verify an ACL for the current
user plus required `SYSTEM`/Administrators access; do not rely on `os.Chmod`.
Use `golang.org/x/term` later for hidden input but pin it now and record both BSD
licenses in `THIRD_PARTY_NOTICES.txt`.

Run:

```bash
gofmt -w $(find internal/config internal/auth -name '*.go' -type f)
go test ./internal/config ./internal/auth
go vet ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/auth
rm -f auth.test.exe
git diff --check
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum THIRD_PARTY_NOTICES.txt internal/config internal/auth
git commit -m "feat: store server credentials securely"
```

---

### Task 3: Hardened HTTP client, API errors, retries, and idempotency

**Files:**
- Create: `internal/api/client.go`
- Create: `internal/api/client_test.go`
- Create: `internal/api/errors.go`
- Create: `internal/api/errors_test.go`
- Create: `internal/api/retry.go`
- Create: `internal/api/retry_test.go`
- Create: `internal/api/idempotency.go`
- Create: `internal/api/idempotency_test.go`

**Interfaces:**
- Produces:
  - `type api.Client struct` created by `api.NewClient(serverURL, token string, timeout time.Duration) (*Client, error)`.
  - `type api.Request struct { Method string; Path string; Query url.Values; Body func() (io.ReadCloser, string, error); IdempotencyKey string }`.
  - `func (c *Client) Do(ctx context.Context, request Request, destination any) *output.Error`.
  - `func api.NewIdempotencyKey() (string, error)` and `ValidateIdempotencyKey(string) error`.

- [ ] **Step 1: Write transport security and redaction tests**

With `httptest.Server`, assert exact bearer header, user agent, bounded response
reads, timeout behavior, and that server/token never appear in returned errors.
Assert every redirect status (`301`, `302`, `303`, `307`, `308`) fails before a
second request and the redirect target receives no Authorization header.

- [ ] **Step 2: Write retry/error/idempotency tests and run RED**

Prove safe reads retry bounded `502/503/504`; mutations recreate replayable bodies
with the same idempotency key; invalid/nonreplayable mutation bodies do not retry.
Map fixed exit categories `4` through `9`. Generate keys from `crypto/rand` and
validate the server's exact `[A-Za-z0-9][A-Za-z0-9._:-]{0,254}` contract.

```bash
go test ./internal/api -run . -count=1
```

Expected: missing package failure.

- [ ] **Step 3: Implement no-redirect, bounded HTTP transport**

Configure:

```go
http.Client{
    Timeout: timeout,
    CheckRedirect: func(*http.Request, []*http.Request) error {
        return http.ErrUseLastResponse
    },
}
```

Treat any `3xx` as `redirect_refused`. Limit JSON success/error bodies before
decoding. Decode API errors as `{error:{code,message,details}}`; malformed success
or error responses become `invalid_server_response` exit `9`.

- [ ] **Step 4: Implement bounded retries and stable error mapping**

Use a maximum of three attempts with context-aware deterministic backoff. Preserve
the same `Idempotency-Key` and call `Body()` for each attempt. Do not retry expected
`4xx`. Map `401/403/404/other 4xx/network/5xx` to `4/5/6/7/8/9`.

Run:

```bash
gofmt -w $(find internal/api -name '*.go' -type f)
go test ./internal/api -race
go vet ./...
```

Expected: all pass and tests assert no secret leakage.

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "feat: add hardened Artisan API client"
```

---

### Task 4: Authentication commands and hidden token input

**Files:**
- Create: `internal/command/auth.go`
- Create: `internal/command/auth_test.go`
- Modify: `internal/command/root.go`
- Modify: `internal/command/root_test.go`
- Create: `internal/api/auth.go`
- Create: `internal/api/auth_test.go`
- Modify: `internal/command/runtime.go`

**Interfaces:**
- Consumes: config/auth store and API client.
- Produces:
  - `type api.Identity struct { User User; Organization Organization; Role string }`.
  - `func (c *Client) Identity(ctx context.Context) (Identity, *output.Error)` using `GET /api/v1/auth/me`.
  - `artisan auth login|logout|status`.

- [ ] **Step 1: Write failing auth command tests**

Inject terminal detection/password-read functions in `Runtime`. Assert login:

- requires `--server` when none stored;
- reads hidden terminal input or explicit `--token-stdin` only;
- rejects `--token` as unknown usage;
- verifies token via `/api/v1/auth/me` before persisting;
- persists normalized server and token only after success;
- never echoes the token in human/JSON/errors.

Assert status reports user nickname, organization name/slug, and current role; logout
removes token idempotently and leaves server config.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/command ./internal/api -run 'Auth|Identity' -count=1
```

Expected: missing handlers.

- [ ] **Step 3: Implement strict identity decoding and auth dispatch**

Model the existing response fields exactly but allow unknown additive fields. Use
`term.ReadPassword` only for an actual terminal file descriptor. `--token-stdin`
reads one bounded line and strips CR/LF only; blank or multiline content fails.

- [ ] **Step 4: Verify output, filesystem, and token redaction**

```bash
gofmt -w $(find internal/command internal/api -name '*.go' -type f)
go test ./internal/command ./internal/api -race
go test ./... | tee /tmp/artisan-cli-tests.log
! grep -F 'test-secret-token' /tmp/artisan-cli-tests.log
rm -f /tmp/artisan-cli-tests.log
```

Expected: all pass and grep finds no token.

- [ ] **Step 5: Commit**

```bash
git add internal/command internal/api
git commit -m "feat: authenticate with desktop credentials"
```

---

### Task 5: Inventory wire models, pagination, and read commands

**Files:**
- Create: `internal/api/inventory_models.go`
- Create: `internal/api/inventory_models_test.go`
- Create: `internal/api/inventory_reads.go`
- Create: `internal/api/inventory_reads_test.go`
- Create: `internal/command/inventory.go`
- Create: `internal/command/inventory_read.go`
- Create: `internal/command/inventory_read_test.go`
- Create: `internal/output/table.go`
- Create: `internal/output/table_test.go`
- Modify: `internal/command/root.go`

**Interfaces:**
- Produces typed lot/image/ledger/reservation/conflict models, paginated page types,
  and commands `lot list/show/ledger/reservations/conflicts` plus `conflict list/show`.

- [ ] **Step 1: Write strict consumed-field model tests**

Use server-contract fixtures containing canonical IDs, timestamps, scores, links,
and exact balances. Accept unknown additive fields. Reject missing required fields,
invalid IDs/timestamps, noninteger grams, `available != on_hand-reserved`, invalid
state enums, duplicate image positions, and admin links with the wrong root.

- [ ] **Step 2: Write pagination and command RED tests**

Test all list filters and URL escaping, one-page cursor output, `--all`, repeated-
cursor rejection, and maximum aggregate bound. Human tables must never truncate
IDs or grams. JSON uses the exact success envelope and retains `next_cursor`.

```bash
go test ./internal/api ./internal/command ./internal/output -run 'Inventory|Lot|Page|Table'
```

Expected: missing implementations.

- [ ] **Step 3: Implement read API and validation**

Use `/api/v1/inventory/admin/...`. Define a generic internal page loop that accepts
`func(cursor string) (items, nextCursor, error)` and a seen-cursor set. Default one
page; `--all` follows until empty cursor or configured item ceiling.

When the admin namespace itself returns `404`, distinguish it from entity `404`
using the requested route shape and report `server_upgrade_required`; entity
routes preserve `bean_lot_not_found`/conflict errors.

- [ ] **Step 4: Implement human/JSON read commands**

Support exact flags from the design, dashed/compact input UUID normalization, and
stable labeled detail output. Run:

```bash
gofmt -w $(find internal -name '*.go' -type f)
go test ./internal/api ./internal/command ./internal/output -race
go vet ./...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/command internal/output
git commit -m "feat: read Artisan inventory"
```

---

### Task 6: Lot creation/update, adjustment, and confirmation

**Files:**
- Create: `internal/api/inventory_mutations.go`
- Create: `internal/api/inventory_mutations_test.go`
- Create: `internal/api/multipart.go`
- Create: `internal/api/multipart_test.go`
- Create: `internal/confirm/confirm.go`
- Create: `internal/confirm/confirm_test.go`
- Create: `internal/command/inventory_lot_write.go`
- Create: `internal/command/inventory_lot_write_test.go`
- Create: `internal/command/inventory_adjust.go`
- Create: `internal/command/inventory_adjust_test.go`
- Modify: `internal/command/inventory.go`

**Interfaces:**
- Produces lot create/patch/adjust API methods and `lot create|update|archive|restore`,
  `inventory adjust`; confirmation returns exit `10` without issuing an HTTP request.

- [ ] **Step 1: Write request construction and idempotency tests**

Assert lot creation wraps the exact `BeanLotCreateManifest` JSON in a multipart
`manifest` field even when there are no images. Assert patches/adjustments use exact
strict JSON fields, integer grams, canonical dates/timestamps, repeated varietals,
nullable clears, state-only archive/restore patches, and the same key on retry.
Reject combining `--from-json` with field flags, unknown clear fields, floating
grams, blank reason, and invalid processing/altitude combinations before network
access where locally knowable.

- [ ] **Step 2: Write confirmation RED tests**

Inject TTY/non-TTY behavior. `adjust` and `archive` show exact ID/change and accept
only explicit affirmative input; non-TTY requires `--yes`; declined/missing
confirmation returns `10` and records zero HTTP requests. Restore/update/create do
not prompt.

- [ ] **Step 3: Implement confirmation and JSON body APIs**

Use one generated idempotency key per command unless a validated advanced
`--idempotency-key` is supplied. Implement a replayable manifest-only multipart
body for lot creation; Task 8 extends it with streamed image files. Implement
`--from-json FILE|-` as a bounded read that is decoded/validated and sent
canonically inside the manifest part; never log source content.

- [ ] **Step 4: Implement handlers and verify authoritative refresh behavior**

Mutation responses are authoritative lot projections. Human output shows resulting
on-hand/reserved/available balances. At this task boundary, lot creation supports
zero images; Task 8 adds the approved repeated image declarations without changing
the command or API method. Run:

```bash
gofmt -w $(find internal -name '*.go' -type f)
go test ./internal/api ./internal/command ./internal/confirm -race
go vet ./...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/command internal/confirm
git commit -m "feat: manage bean lots and adjustments"
```

---

### Task 7: Reservation and conflict workflows

**Files:**
- Create: `internal/api/inventory_reservations.go`
- Create: `internal/api/inventory_reservations_test.go`
- Create: `internal/command/inventory_reservation.go`
- Create: `internal/command/inventory_reservation_test.go`
- Create: `internal/command/inventory_conflict.go`
- Create: `internal/command/inventory_conflict_test.go`
- Modify: `internal/command/inventory.go`

**Interfaces:**
- Produces `reservation create|finalize|release` against existing non-admin routes
  and `conflict list|show|resolve` against admin routes.

- [ ] **Step 1: Write reservation contract tests**

Cover every strict server field, canonical UUIDs, planned/actual grams, occurred-at,
reason, and idempotency. Assert paths remain:

```text
/api/v1/inventory/reservations
/api/v1/inventory/reservations/{uuid}/finalize
/api/v1/inventory/reservations/{uuid}/release
```

Do not move them under `/admin`; member credentials must retain this behavior.

- [ ] **Step 2: Write conflict resolution safety tests**

`conflict list --lot` and `show` are reads. `resolve --note` prompts or requires
`--yes`, uses one key, and issues no request on decline. Preserve server conflict
errors instead of trying an automatic adjustment.

- [ ] **Step 3: Implement API methods and handlers**

Decode reservation mutation projections including balance/conflict/replay fields.
Use admin routes only for conflict reads/resolution. Do not infer omitted actual
weight or resolution note.

- [ ] **Step 4: Run focused and full command tests**

```bash
gofmt -w $(find internal -name '*.go' -type f)
go test ./internal/api ./internal/command ./internal/confirm -race
go test ./...
go vet ./...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/command
git commit -m "feat: manage reservations and conflicts"
```

---

### Task 8: Multipart image management and atomic downloads

**Files:**
- Modify: `internal/api/multipart.go`
- Modify: `internal/api/multipart_test.go`
- Create: `internal/api/inventory_images.go`
- Create: `internal/api/inventory_images_test.go`
- Create: `internal/command/inventory_image.go`
- Create: `internal/command/inventory_image_test.go`
- Modify: `internal/command/inventory.go`

**Interfaces:**
- Produces `image add|update|reorder|delete|download`, replayable streaming multipart
  bodies, and atomic private WebP downloads.

- [ ] **Step 1: Write streaming multipart RED tests**

Use temporary JPEG/PNG files and a recording server. Assert ordered `manifest`
then `images` parts, exact filenames/content types, no whole-file buffering,
reopening on retry, same manifest/key, max eight images, and local missing/file-
changed failures before ambiguous retry.

- [ ] **Step 2: Write mutation/download safety tests**

Cover metadata patch, complete reorder list, delete confirmation/`--yes`, display
and thumbnail selection, bounded stream copy, temporary-file cleanup on failure,
atomic rename, no overwrite without `--force`, and private file permissions.

- [ ] **Step 3: Implement replayable multipart and image mutations**

`Request.Body` must reopen files and reproduce identical manifest metadata for
each retry. Record file size/mtime before first send and reject retry if identity
changes. Let the server validate image bytes and normalize them.

- [ ] **Step 4: Implement atomic download and run tests**

Download to a same-directory private temporary file, sync/close, then rename.
Reject redirects/content-type mismatch and remove partial files.

```bash
gofmt -w $(find internal -name '*.go' -type f)
go test ./internal/api ./internal/command -race
go vet ./...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/command
git commit -m "feat: manage inventory images"
```

---

### Task 9: Embedded portable Agent Skill

**Files:**
- Create: `skills/artisan-inventory/SKILL.md`
- Create: `internal/skill/content.go`
- Create: `internal/skill/content_generated.go`
- Create: `internal/skill/content_test.go`
- Create: `internal/skill/cmd/embedskill/main.go`
- Create: `internal/command/skill.go`
- Create: `internal/command/skill_test.go`
- Modify: `internal/command/root.go`

**Interfaces:**
- Produces `artisan skill show` and `artisan skill install --directory ROOT [--force]`.

- [ ] **Step 1: Write failing skill content/installer tests**

Assert frontmatter name `artisan-inventory`, portable command-only instructions,
and required rules from the design. Scan for token-request/login instructions and
forbid them. Installation must atomically create
`ROOT/artisan-inventory/SKILL.md`, preserve identical existing content, refuse a
different file unless `--force`, and never require network access.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/skill ./internal/command -run Skill -count=1
```

Expected: missing packages/commands.

- [ ] **Step 3: Write the portable skill**

Include exact workflows for auth status, lot resolution, reads, create/update,
adjustment, images, reservations, and conflicts. Require `--json`, integer grams,
explicit approval before `--yes`, authoritative post-mutation reread, safe
pagination, and idempotency-key preservation. State that the agent never requests,
reads, prints, persists, or passes the token and never logs in for the user.

- [ ] **Step 4: Embed and install atomically**

Keep `skills/artisan-inventory/SKILL.md` as the single hand-edited source. Add this
directive to `internal/skill/content.go`:

```go
//go:generate go run ./cmd/embedskill ../../skills/artisan-inventory/SKILL.md content_generated.go
```

The generator reads the source, formats it as a Go byte literal assigned to
`Content`, and atomically writes `content_generated.go`. `content_test.go` reads
the root source and asserts byte equality with `Content`, so stale generated
content fails CI. The release workflow runs `go generate ./internal/skill` and
fails if `git diff --exit-code` detects drift.

Run:

```bash
go generate ./internal/skill
gofmt -w $(find internal/skill internal/command -name '*.go' -type f)
go test ./...
go run ./cmd/artisan skill show | grep -F 'name: artisan-inventory'
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add skills internal/skill internal/command
git commit -m "feat: bundle Artisan inventory agent skill"
```

---

### Task 10: Pinned live integration against Artisan Server

**Files:**
- Create: `integration/artisan-server.ref`
- Create: `integration/inventory_cli_test.go`
- Create: `integration/README.md`
- Create: `.github/workflows/integration.yml`
- Modify: `README.md`

**Interfaces:**
- Consumes: exact completed server commit from the companion plan and compiled CLI.
- Produces: disposable loopback proof of login/status, read, mutation, image, and
  post-mutation balance behavior.

- [ ] **Step 1: Pin server commit and write failing integration test**

Store exactly one 40-character server commit SHA in `integration/artisan-server.ref`.
The test requires `ARTISAN_CLI_BINARY`, a loopback base URL, and disposable admin
bootstrap values. It creates a desktop credential through browser CSRF/session
APIs, feeds it to `artisan auth login --token-stdin`, then executes compiled
commands in `--json` mode.

- [ ] **Step 2: Exercise representative complete flow**

Create a lot with opening grams, show/list it, adjust with `--yes`, add/download an
image, and assert post-mutation authoritative balances. Exercise one existing
reservation command. Use unique IDs/names and the disposable stack only. Verify
captured logs never contain the issued raw token.

- [ ] **Step 3: Add guarded integration workflow**

Pin actions by full commit SHA. Checkout CLI and server at the exact ref, start the
server's guarded Compose E2E project on loopback, build `artisan` with
`CGO_ENABLED=0`, run integration tests, and always tear down volumes. Reuse the
server's disposable marker/project-name safeguards; never accept an arbitrary
base URL.

- [ ] **Step 4: Run locally and document**

Follow `integration/README.md`, then:

```bash
go build -o dist/artisan-integration ./cmd/artisan
ARTISAN_CLI_BINARY="$PWD/dist/artisan-integration" \
  go test ./integration -v
```

Expected: pass against the disposable stack; teardown leaves no project containers
or volumes.

- [ ] **Step 5: Commit**

```bash
git add integration .github/workflows/integration.yml README.md
git commit -m "test: verify CLI against Artisan Server"
```

---

### Task 11: Cross-platform CI, static release artifacts, and documentation

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`
- Create: `scripts/build-release.sh`
- Create: `scripts/build-release.ps1`
- Create: `docs/installation.md`
- Create: `docs/commands.md`
- Create: `docs/json-and-exit-codes.md`
- Create: `docs/security.md`
- Create: `docs/agent-skill.md`
- Modify: `README.md`
- Modify: `THIRD_PARTY_NOTICES.txt`

**Interfaces:**
- Produces tested Linux/macOS/Windows amd64/arm64 archives, checksums, provenance,
  version metadata, and complete user/security documentation.

- [ ] **Step 1: Add CI contract tests before workflows**

Add Go tests or a small standard-library validation package that parses workflow
YAML as text and asserts required OS matrix entries, `CGO_ENABLED: 0`, full-SHA
action pins, race test on supported hosts, release asset names, and embedded-skill
inclusion. Add archive smoke scripts that run `artisan --json version`.

- [ ] **Step 2: Add CI workflow**

On pull requests/pushes run `gofmt` cleanliness, `go vet ./...`, `go test ./...`,
race tests where supported, and native host builds on Linux/macOS/Windows. Use Go
1.23.x and pinned actions. Do not grant write permissions to PR jobs.

- [ ] **Step 3: Add tagged release workflow**

For `v*` tags build with:

```text
CGO_ENABLED=0
-ldflags=-s -w -X github.com/fr3akX/artisan-cli/internal/release.Version=$VERSION -X github.com/fr3akX/artisan-cli/internal/release.Commit=$GITHUB_SHA
```

Produce Linux/macOS `amd64,arm64` tarballs and Windows `amd64,arm64` zip files,
containing the binary, AGPL license, notices, and skill. Publish SHA-256 checksums
and GitHub build provenance. Start every built target where runner architecture
supports execution and inspect the others' archive contents.

- [ ] **Step 4: Write user/security documentation and run full checks**

Document downloads/checksums, config paths, login, all commands, JSON envelopes,
exit codes, compatible server version/ref, skill installation/common roots,
unsigned-binary limitation, token/storage threat model, HTTPS/redirect policy, and
approved confirmation behavior.

Run:

```bash
gofmt -w $(find cmd internal integration -name '*.go' -type f)
go test ./... -race
go vet ./...
CGO_ENABLED=0 go build -trimpath -o dist/artisan ./cmd/artisan
./dist/artisan --json version
git diff --check
git status --short
```

Also cross-build all six target pairs and confirm no dynamic dependency on the
native Linux artifact with `file` and `ldd` (which should report not dynamic).

- [ ] **Step 5: Commit**

```bash
git add .github scripts docs README.md THIRD_PARTY_NOTICES.txt
git commit -m "ci: build static cross-platform releases"
```

---

## CLI completion gate

Before tagging the first release:

1. Independently review the entire CLI and skill against every acceptance criterion
   in the design.
2. Run server full tests, CLI full/race tests, and disposable cross-repository E2E
   from clean checkouts.
3. Confirm release archives contain one executable plus license/notices/skill and
   that the executable itself runs without runtime dependencies.
4. Search source, tests, logs, artifacts, and git history for disposable token
   values; none may remain.
5. Record the minimum compatible Artisan Server commit/version in release notes.
6. Deploy the server API before publishing the CLI release.
7. Update and close `fr3akX/artisan-server#4` only after release assets and the
   portable skill are available.
