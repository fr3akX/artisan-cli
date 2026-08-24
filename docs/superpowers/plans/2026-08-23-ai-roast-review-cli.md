# Agent-Native AI Roast Review CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add strict roast metadata/profile/comment commands and a separately installable Agent Skill that analyzes one immutable roast revision and automatically posts one evidence-based private review comment.

**Architecture:** Add typed roast/archive API clients, bounded checksum-verified chart/raw downloads, a fixed review-body/key client, and a discoverable Cobra `roast` hierarchy. The host AI agent follows the embedded `artisan-roast-review` skill; the CLI remains provider-free and delegates one-review atomicity to the companion server endpoint.

**Tech Stack:** Go 1.23+, Cobra/pflag, standard `net/http`, `encoding/json`, `compress/gzip`, `crypto/sha256`, platform-specific secure filesystem operations, `httptest`, disposable Artisan Server integration, GitHub Actions native Linux/macOS/Windows CI.

**Spec:** `docs/superpowers/specs/2026-08-23-ai-roast-review-design.md`

## Global Constraints

- Server prerequisite: complete `fr3akX/artisan-server/docs/superpowers/plans/2026-08-23-ai-roast-review-api.md`, then pin its exact reviewed merged commit in `integration/artisan-server.ref`.
- The CLI never invokes an AI provider and never accepts/stores provider keys, models, prompts, token budgets, or costs.
- Every agent command uses `--json --server "$TRUSTED_SERVER"`; the skill never runs `auth login` or handles a bearer token.
- Members and administrators may read private roasts and post dedicated review comments.
- Expose both readable decompressed chart JSON and exact raw `.alog` bytes.
- Downloads refuse redirects, are bounded and checksum-verified, and become visible only through protected atomic installation.
- Existing destinations are preserved unless `--force`; failed/stale downloads never remove or replace a pre-existing destination.
- The fixed initial template is exactly `artisan-roast-review-v1`; comments begin with the exact three-line marker from the spec.
- Review posting is automatic and has no `--yes`; this exception does not alter `artisan-inventory` approval gates.
- The CLI computes the canonical review key; users cannot override it.
- Same-slot replay is success and returns the server's original comment; stale unclaimed revisions cause one bounded agent restart.
- Existing `artisan-inventory` skill source, default `skill show/install` behavior, command compatibility, JSON envelopes, exit classes, and six release targets remain compatible.
- Use the clean Go 1.23.2+ toolchain; host `/usr/local/go` is known corrupt and must not be used for verification.
- Never run an authenticated mutation integration or smoke test against a non-loopback/production server.

---

### Task 1: Strict roast, revision, and comment read models

**Files:**
- Create: `internal/api/roast_models.go`
- Create: `internal/api/roast_models_test.go`
- Create: `internal/api/roast_reads.go`
- Create: `internal/api/roast_reads_test.go`
- Modify: `internal/api/inventory_reads.go`
- Modify: `internal/api/inventory_models.go`

**Interfaces:**
- Consumes: existing `Client.Do`, bounded page collection, JSON single-document helpers, bearer configuration, server archive/comment response shapes.
- Produces:
  - `type RoastListOptions` with limit/cursor/search/time/machine/state/label fields.
  - `RoastListItem`, `RoastDetail`, `RoastRevision`, `CommentView`, and page types.
  - `func NormalizeRoastUUID(raw string) (string, *output.Error)`.
  - `Client.ListRoasts`, `Client.ListAllRoasts`, `Client.Roast`, `Client.RoastRevisions`, `Client.AllRoastRevisions`, `Client.RoastComments`, and `Client.AllRoastComments`.

- [ ] **Step 1: Write failing strict-model tests**

Use server-shaped fixtures containing every required field, null boundary, one label, one current revision, metadata object, links, and a deleted comment. Assert successful decoding tolerates unrelated future fields but rejects:

- missing/null required fields;
- wrong booleans/numbers/strings;
- malformed compact UUIDs or SHA-256;
- invalid states/parse states/temperature units;
- negative or incoherent revision counts;
- nonpositive revision numbers/byte sizes;
- current revision inconsistent with roast state/count;
- invalid RFC 3339 timestamps;
- non-object metadata/links;
- null array elements;
- comment roast UUID mismatch, invalid deleted/body coherence, and invalid permission coherence; and
- multiple JSON documents or invalid surrogate escapes.

Define representative structs in tests and validate exact JSON names, including `current_metadata`, `current_revision`, `reparse_recommended`, `can_edit`, and `can_delete`.

- [ ] **Step 2: Run model tests and verify RED**

```bash
export PATH=/tmp/go1.23.2-clean/bin:$PATH
go test ./internal/api -run 'TestRoast|TestComment' -count=1
```

Expected: compile failures because roast types are absent.

- [ ] **Step 3: Extract generic UUID normalization without changing inventory behavior**

Introduce a private helper used by both domains:

```go
func normalizeCompactUUID(raw, code, message string) (string, *output.Error)
```

It accepts lowercase compact or standard dashed UUID input, strips dashes, rejects uppercase/noncanonical/versionless malformed values exactly as current inventory validation does, and emits the caller's error code/message. Keep `normalizeInventoryUUID()` behavior and tests byte-for-byte stable. Add:

```go
func NormalizeRoastUUID(raw string) (string, *output.Error) {
    return normalizeCompactUUID(raw, "invalid_roast_uuid", "Roast UUID must be compact or standard dashed form")
}
```

- [ ] **Step 4: Implement typed models and custom `UnmarshalJSON` validation**

Mirror server schemas without copying private storage fields. Reuse `decodeRequiredObject`, `rejectNullArrayElements`, and UTF-8/surrogate validation. Use `json.RawMessage` for `CurrentMetadata` to preserve arbitrary object contents while requiring an object root.

Validate cross-field invariants in this exact method:

```go
func (value RoastDetail) validate() error {
    switch value.State {
    case "awaiting_profile":
        if value.RevisionCount != 0 || value.CurrentRevision != nil { return errInvalidRoast }
    case "parsed", "parse_failed":
        if value.RevisionCount < 1 || value.CurrentRevision == nil ||
            value.CurrentRevision.RevisionNumber != value.RevisionCount { return errInvalidRoast }
    default:
        return errInvalidRoast
    }
    return nil
}
```

Canonical server roast/comment UUIDs are compact lowercase. Keep identity UUIDs in `auth` dashed because that existing contract is unrelated.

- [ ] **Step 5: Write failing read-client tests**

With `httptest.Server`, assert exact paths and query mappings:

```text
GET /api/v1/roasts
GET /api/v1/roasts/{uuid}
GET /api/v1/roasts/{uuid}/revisions
GET /api/v1/roasts/{uuid}/comments
```

Cover all filters, omission of zero values, canonical UTC timestamp query values, pagination bounds, repeated cursor rejection, 1,000-page/10,000-item ceilings, entity UUID coherence, required `X-Roast-UUID` and `X-Roast-Revisions-Version: 1` coherence when present, bearer header presence without token reflection, redirects, malformed responses, and `404` classification as either entity `not_found` or `server_upgrade_required` when the top-level roast API is absent.

- [ ] **Step 6: Implement read methods and validation**

Use `MaxRoastAggregateItems = 10_000` and `MaxRoastAggregatePages = 1_000`. Require canonical timezone-aware RFC 3339 filters and reject `from > to` locally. List states are exactly `awaiting_profile`, `parsed`, and `parse_failed`. Normalize label ID through the generic compact UUID helper.

Use `Client.Do` for JSON reads. Do not make identity preflight requests inside API methods. Require every returned nested entity to match the requested roast UUID.

- [ ] **Step 7: Run GREEN and regressions**

```bash
gofmt -w internal/api/roast_models.go internal/api/roast_models_test.go \
  internal/api/roast_reads.go internal/api/roast_reads_test.go \
  internal/api/inventory_reads.go internal/api/inventory_models.go
go test ./internal/api -count=1
go vet ./internal/api
git diff --check
```

Expected: all API tests pass and no inventory request/output contract changes.

- [ ] **Step 8: Commit**

```bash
git add internal/api/roast_models.go internal/api/roast_models_test.go \
  internal/api/roast_reads.go internal/api/roast_reads_test.go \
  internal/api/inventory_reads.go internal/api/inventory_models.go
git commit -m "feat: read private roast archive data"
```

---

### Task 2: Reusable protected download destination and raw profile download

**Files:**
- Create: `internal/api/download_target.go`
- Create: `internal/api/download_target_test.go`
- Modify: `internal/api/inventory_images.go`
- Modify: `internal/api/inventory_images_test.go`
- Create: `internal/api/roast_profile_download.go`
- Create: `internal/api/roast_profile_download_test.go`

**Interfaces:**
- Consumes: current `downloadOperations`, platform atomic no-replace/replace functions, secure private-file protection, Task 1 revision reads.
- Produces:
  - package-private `downloadTarget` that owns one protected same-directory temp file and delays visibility until `Install(force)`.
  - unchanged `DownloadInventoryImage` behavior through the shared target.
  - `type RoastProfileDownload` and `Client.DownloadRoastProfile(ctx, roastUUID, revisionNumber, destination string, force bool)`.

- [ ] **Step 1: Write RED tests for delayed atomic installation**

Cover:

- invalid destination and missing parent;
- existing destination preserved without `--force`;
- temporary file mode/ACL is private;
- reset between retries;
- `Abort()` removes only the owned temporary;
- `Install(false)` is atomic no-replace;
- `Install(true)` atomically replaces only at the final step;
- file sync, close, parent durability, visible-but-durability-uncertain results;
- destination creation/replacement races; and
- no partial target under read/write/cancel/sync/close/install failures.

Inject operations rather than sleeping or racing nondeterministically.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/api -run 'TestDownloadTarget' -count=1
```

Expected: compile failure because `downloadTarget` is absent.

- [ ] **Step 3: Extract the target lifecycle and preserve image behavior**

Move destination validation, temp creation/protection, reset, sync/close, no-replace/replace, parent sync, and cleanup ownership out of `DownloadInventoryImage`. Keep body reading and image response validation in `inventory_images.go`.

Use an explicit terminal state so `Abort()` cannot remove an installed destination. Return a result flag when installation became visible before a later durability error, preserving current image error wording and `ImageDownload` result behavior.

Run all image-download tests before adding profile behavior:

```bash
go test ./internal/api -run 'Test.*InventoryImage.*Download|TestDownloadTarget' -count=1
```

Expected: pass with unchanged HTTP paths, limits, errors, and human/API results.

- [ ] **Step 4: Write failing raw-profile download tests**

Create a fixture revision page and exact raw bytes. Assert the method:

1. finds the requested positive revision through bounded revision pagination;
2. calls `/api/v1/roasts/{uuid}/revisions/{number}/download`;
3. requires `application/x-artisan-profile` without untrusted parameters;
4. validates one safe attachment filename but never uses it as the local path;
5. requires coherent `Content-Length`, `ETag`, `X-Content-SHA256`, `X-Checksum-SHA256`, and `X-Revision-Number`;
6. streams no more than the revision's declared `byte_size` and the 16 MiB server profile ceiling;
7. verifies exact SHA before install; and
8. returns path, roast UUID, revision number, bytes, and SHA.

Cover absent revision, zero/overflow revision numbers, redirects, transient retry/reset, short/long bodies, wrong media/header/hash, duplicate headers, network/cancellation/write failures, destination races, and token/server reflection in error JSON.

- [ ] **Step 5: Implement raw download with delayed install**

Add a small header parser that requires one canonical value for security-sensitive headers; do not use comma-joined `Header.Get` where duplicate values could be hidden. Stream through `io.LimitReader(expectedBytes+1)` into `io.MultiWriter(target.Writer(), sha256.New())`, close every body, compare exact count/hash, then call `target.Install(force)`.

Retry only before installation. On every retry call `target.Reset()`. Classify a top-level route absence as `server_upgrade_required`; preserve entity `not_found`.

- [ ] **Step 6: Run GREEN, cross-platform compile, and regressions**

```bash
gofmt -w internal/api/download_target.go internal/api/download_target_test.go \
  internal/api/inventory_images.go internal/api/inventory_images_test.go \
  internal/api/roast_profile_download.go internal/api/roast_profile_download_test.go
go test ./internal/api -count=1
go vet ./internal/api
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/api -o /tmp/artisan-api-windows.test.exe
rm -f /tmp/artisan-api-windows.test.exe
```

Expected: all pass; no generated binaries remain in the worktree.

- [ ] **Step 7: Commit**

```bash
git add internal/api/download_target.go internal/api/download_target_test.go \
  internal/api/inventory_images.go internal/api/inventory_images_test.go \
  internal/api/roast_profile_download.go internal/api/roast_profile_download_test.go
git commit -m "feat: download immutable roast profiles"
```

---

### Task 3: Bounded chart download and revision-stability fence

**Files:**
- Create: `internal/api/roast_chart_download.go`
- Create: `internal/api/roast_chart_download_test.go`

**Interfaces:**
- Consumes: Task 1 `Client.Roast`; Task 2 delayed `downloadTarget`; existing chart route contract.
- Produces: `type RoastChartDownload` and `Client.DownloadRoastChart(ctx, roastUUID, destination string, force bool)`.

- [ ] **Step 1: Write failing successful-chart tests**

Build deterministic raw JSON and `gzip` it with fixed metadata. The server returns the compressed body plus current headers. Assert the method:

- pre-reads a parsed current revision;
- sets `Accept-Encoding: gzip` itself so Go does not transparently decompress checksum-covered bytes;
- accepts only `application/json`, exactly one `Content-Encoding: gzip`, and supported schema `1`;
- validates compressed length, ETag/checksum, parser version, and schema headers;
- hashes compressed bytes before decompression;
- writes exact decompressed JSON bytes without reserialization;
- post-reads the roast and requires the same revision SHA; and
- returns both compressed/file sizes and hashes plus roast/revision/parser/schema identity.

Use the chart top-level fixture:

```json
{
  "control": {"markers": [], "steps": []},
  "core": {"bt": [100.0], "bt_ror": [null], "et": [120.0], "et_ror": [null], "time_seconds": [0.0]},
  "events": {"milestones": [], "special": []},
  "extra": {"series": []},
  "parser_version": "artisan-4-v1",
  "schema_version": 1,
  "source_temperature_unit": "C",
  "summary": {"duration_seconds": 0.0, "extra_series_count": 0, "sample_count": 1, "special_event_count": 0}
}
```

- [ ] **Step 2: Write failing hostile-response and local-file tests**

Cover:

- auto-decompressed response attempts;
- duplicate/conflicting security headers;
- absent/invalid content length;
- compressed body over 64 MiB;
- gzip expansion over 64 MiB;
- malformed/trailing gzip members;
- compressed checksum mismatch;
- invalid UTF-8/JSON, multiple JSON documents, non-object root;
- missing/null/wrong top-level/core fields;
- parser/schema mismatch;
- unsupported units;
- core array/sample-count incoherence;
- redirects, timeout, cancellation, retry/reset, short writes, and durability failures;
- revision changes between pre-read and post-read; and
- an existing destination surviving every stale/failure case even with `force=true`.

- [ ] **Step 3: Run RED**

```bash
go test ./internal/api -run 'TestDownloadRoastChart|TestRoastChart' -count=1
```

Expected: compile failure because chart download is absent.

- [ ] **Step 4: Implement wire verification, bounded decompression, and schema validation**

Manually set `Accept-Encoding: gzip`; stream the wire body through a 64 MiB+1 limiter into a second protected same-directory temporary file while hashing it. After the compressed length/hash pass, seek that file to offset zero. Decompress through `gzip.NewReader`, call `Multistream(false)`, require the compressed source to be at exact EOF after the first member (rejecting concatenated members and trailing bytes), and copy no more than 64 MiB+1 into the delayed destination while hashing file bytes. Close and remove the compressed temporary on every exit path.

Validate the destination bytes with `json.Decoder.UseNumber()` and the existing single-document/surrogate guards. Require top-level objects/arrays and these invariants:

```text
summary.sample_count == len(core.time_seconds)
len(bt) == len(bt_ror) == len(et) == len(et_ror) == sample_count
summary.extra_series_count == len(extra.series)
summary.special_event_count == len(events.special)
parser_version == X-Parser-Version
schema_version == X-Chart-Schema-Version == 1
source_temperature_unit is "C", "F", or null
```

Do not interpret review quality or alter chart bytes.

- [ ] **Step 5: Add the pre/post revision fence before visibility**

Call `Client.Roast()` before creating the request and after all body/schema validation but before `Install()`. If state/current revision/SHA changed, abort the temp and return code `roast_revision_changed`, exit class 7, HTTP 409-equivalent local reconciliation metadata omitted. Never install then delete, because `--force` may be preserving an older destination.

- [ ] **Step 6: Run GREEN and static checks**

```bash
gofmt -w internal/api/roast_chart_download.go internal/api/roast_chart_download_test.go
go test ./internal/api -count=1
go vet ./internal/api
git diff --check
```

Expected: all pass with bounded memory/disk behavior demonstrated by tests.

- [ ] **Step 7: Commit**

```bash
git add internal/api/roast_chart_download.go internal/api/roast_chart_download_test.go
git commit -m "feat: download validated roast chart data"
```

---

### Task 4: Fixed review file, stable identity, and replay-safe post client

**Files:**
- Create: `internal/securefile/open_regular.go`
- Create: `internal/securefile/open_regular_unix.go`
- Create: `internal/securefile/open_regular_windows.go`
- Create: `internal/securefile/open_regular_test.go`
- Create: `internal/api/roast_review.go`
- Create: `internal/api/roast_review_test.go`
- Modify: `internal/api/client.go`

**Interfaces:**
- Consumes: Task 1 roast detail/current revision, server review endpoint, existing replayable `Request.Body`, platform no-follow primitives.
- Produces:
  - `securefile.ReadRegularSnapshot(path string, maxBytes int64) ([]byte, error)` that rejects symlink/reparse traversal and path/content changes.
  - `ReviewTemplateVersion = "artisan-roast-review-v1"`.
  - `CanonicalRoastReviewKey`, `ReadRoastReviewFile`, `RoastReviewRequest`, `RoastReviewResult`, and `Client.PostRoastReview`.
  - optional response-header capture in `Request` without weakening existing response validation.

- [ ] **Step 1: Write cross-platform RED tests for safe bounded file reads**

Test a nested regular file, empty/oversized files, final symlink/reparse point, parent symlink/reparse point, directory/device/nonregular source, replacement before/during/after read, size/modtime/identity changes, short read, cancellation-independent deterministic hooks, and no path or content in returned errors.

Unix implementation must walk from an opened root/current-directory handle with `openat(..., O_NOFOLLOW|O_DIRECTORY)` for parents and `O_NOFOLLOW` for the final regular file. Windows must open and inspect every path component with `FILE_FLAG_OPEN_REPARSE_POINT`, reject reparse attributes, and read the exact final handle. Both must recheck handle identity/size and path component identity after reading.

- [ ] **Step 2: Run secure-file RED**

```bash
go test ./internal/securefile -run 'TestReadRegularSnapshot' -count=1
```

Expected: compile failure for the missing function.

- [ ] **Step 3: Implement the bounded regular snapshot helper**

Return an owned byte slice only after reading exactly one nonempty regular file no larger than `maxBytes`, observing EOF immediately after the declared size, and confirming every opened component still identifies the same non-link/reparse object. Do not require a pre-existing file to have private mode/ACL; the skill creates private files, but the CLI contract accepts any safely reached regular input.

Run Unix tests and Windows cross-compilation/native-compatible unit tests.

- [ ] **Step 4: Write review contract/key/file RED tests**

Independently calculate the exact key:

```go
sum := sha256.Sum256([]byte(
    "artisan-roast-review\x00" + roastUUID + "\x00" + revisionSHA + "\x00" + template,
))
want := "review-" + hex.EncodeToString(sum[:])
```

Accept only a body beginning with the exact three lines and matching positive revision/SHA/template. Reject CR/CRLF, invalid UTF-8, NUL/disallowed controls, leading text, mismatched marker, unsupported template, over 4,000 runes, over 16,000 bytes, empty body, changed file, and unsafe path. Normalize surrounding Unicode whitespace and retain internal LF.

- [ ] **Step 5: Write review HTTP RED tests**

Assert `PostRoastReview`:

1. normalizes the roast UUID and validates current parsed revision before POST;
2. sends strict JSON fields and the computed key;
3. sends no browser cookie/CSRF or user-overridable key;
4. retries only exact body bytes with the same key;
5. requires `201`, `Cache-Control: no-store`, canonical `Location`, one `X-Idempotent-Replay`, matching SHA/template headers, and a coherent `CommentView`;
6. accepts first creation and replay, exposing `IdempotentReplay bool`;
7. maps `roast_revision_changed`, `review_idempotency_conflict`, `not_found`, auth failures, and missing endpoint correctly; and
8. rejects reflected token/server/body data, redirects, malformed JSON, mismatched comment roast UUID, and hostile headers.

- [ ] **Step 6: Extend `Client.Do` narrowly for trusted response metadata**

Add this exact optional callback to `Request`:

```go
type ResponseValidator func(status int, header http.Header) *output.Error

type Request struct {
    Method            string
    Path              string
    Query             url.Values
    Body              func() (io.ReadCloser, string, error)
    IdempotencyKey    string
    ExpectedStatus    int
    ValidateResponse  ResponseValidator
}
```

Invoke `ValidateResponse` only after status/body safety checks and before success return. Pass a cloned header map so a caller cannot mutate transport state. Existing requests leave it nil and retain exact behavior. Unit-test duplicate headers and ensure validators never receive a token or server URL.

- [ ] **Step 7: Implement review validation and post client**

Use `ReadRegularSnapshot(..., 16<<10)`, `utf8.RuneCount`, exact marker parsing, and deterministic JSON encoding through `newJSONBody`. Re-read current roast before POST and require the supplied SHA. Do not add `--yes` or an idempotency override.

Validate server response headers with exact single values. Return the ordinary comment plus revision SHA/template/replay; never return the key or raw body in result/error types.

- [ ] **Step 8: Run GREEN and cross-platform checks**

```bash
gofmt -w internal/securefile/open_regular*.go internal/api/roast_review*.go \
  internal/api/client.go
go test ./internal/securefile ./internal/api -count=1
go vet ./internal/securefile ./internal/api
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/securefile -o /tmp/artisan-securefile-windows.test.exe
rm -f /tmp/artisan-securefile-windows.test.exe
```

Expected: all pass; existing inventory/admin request retry tests remain unchanged.

- [ ] **Step 9: Commit**

```bash
git add internal/securefile/open_regular* internal/api/roast_review* internal/api/client.go
git commit -m "feat: post one revision-bound roast review"
```

---

### Task 5: Cobra roast commands and deterministic human/JSON output

**Files:**
- Create: `internal/command/roast.go`
- Create: `internal/command/roast_test.go`
- Create: `internal/command/cobra_roast.go`
- Create: `internal/command/cobra_roast_test.go`
- Modify: `internal/command/cobra_root.go`
- Modify: `internal/command/cobra_root_test.go`

**Interfaces:**
- Consumes: Tasks 1–4 API methods and existing `inventoryReadClient` configuration/auth lock discipline.
- Produces the exact `roast list/show/revisions/chart download/profile download/comment list/review post` hierarchy from the spec.

- [ ] **Step 1: Write failing Cobra tree/help/completion tests**

Assert root help includes `roast`, root short text becomes `Artisan Server command line client`, and these paths appear exactly:

```text
artisan roast list
artisan roast show ROAST_UUID
artisan roast revisions ROAST_UUID
artisan roast chart download ROAST_UUID DESTINATION
artisan roast profile download ROAST_UUID REVISION_NUMBER DESTINATION
artisan roast comment list ROAST_UUID
artisan roast review post ROAST_UUID
```

Assert exact positional counts, static completion for roast states and template version, no file completion for IDs/cursors/scalar fields, file completion only for destination/body-file positions where intended, hidden default help behavior, and JSON help envelope behavior.

Cover legacy single-dash forms for every roast flag and global options without misclassifying dash-prefixed destination/body paths after `--`.

- [ ] **Step 2: Run Cobra RED**

```bash
go test ./internal/command -run 'TestCobraRoast|TestRoot.*Roast' -count=1
```

Expected: failures because the command is absent.

- [ ] **Step 3: Implement Cobra registration and parse-failure routing**

Add `newRoastCommand(ctx, state)` in root registration. Build focused parent/leaf commands in `cobra_roast.go`; do not enlarge inventory routing. Extend `knownCommandPath`, parse-failure messages, and single-dash normalization for roast paths.

Declare list filters and page flags exactly as the spec. Download leaves use `--force`. Review post requires changed nonempty `--revision-sha256`, `--template-version`, and `--body-file` flags; omission is a local usage error before configuration/network access.

- [ ] **Step 4: Write command execution/output RED tests**

Using `httptest.Server` and isolated config, cover each command in human and JSON modes. Expected human shapes:

- roast list/revisions/comments: stable tables plus optional `Next cursor`;
- roast show: labeled details plus current revision/labels;
- downloads: `Downloaded <bytes> bytes to <escaped path>` plus checksum/revision details;
- review post: comment UUID, author, revision SHA, template, and `Created` or `Existing review`.

Assert JSON outputs preserve typed API result objects inside one envelope and never embed chart/profile file bytes. Validate output escaping for hostile titles, nicknames, metadata, paths, and comment bodies. Human tables must use `output.EscapeVisible`; automation never parses them.

- [ ] **Step 5: Implement command execution with shared authenticated client loading**

Rename `inventoryReadClient` to this domain-neutral helper:

```go
func authenticatedClient(
    ctx context.Context, runtime Runtime, jsonMode bool,
    serverOverride string, timeout time.Duration,
) (*api.Client, int)
```

Keep auth-state locking, login-transaction recovery, configuration precedence, server binding, and errors unchanged. Inventory callers delegate to it.

Implement local parsing/validation first, then one authenticated client. Rename `writeInventorySuccess` to `writeAPISuccess`, update every existing inventory caller without changing output bytes, and route downloads/review post through that exact helper.

- [ ] **Step 6: Add error/secret/output regressions**

Test usage failures before config reads, member/admin success, auth/network/server-upgrade errors, stale revision, existing destination, durability uncertainty, cancellation exit 130, broken stdout/stderr, and forbidden corpus absence. Review-post tests must prove there is no confirmation prompt and no `--yes` flag.

- [ ] **Step 7: Run GREEN and command regressions**

```bash
gofmt -w internal/command/roast.go internal/command/roast_test.go \
  internal/command/cobra_roast.go internal/command/cobra_roast_test.go \
  internal/command/cobra_root.go internal/command/cobra_root_test.go
go test ./internal/command ./cmd/artisan -count=1
go vet ./internal/command ./cmd/artisan
git diff --check
```

Expected: all pass and existing auth/inventory/skill command outputs remain compatible.

- [ ] **Step 8: Commit**

```bash
git add internal/command/roast.go internal/command/roast_test.go \
  internal/command/cobra_roast.go internal/command/cobra_roast_test.go \
  internal/command/cobra_root.go internal/command/cobra_root_test.go
git commit -m "feat: add roast review CLI commands"
```

---

### Task 6: Multi-skill registry and `artisan-roast-review` workflow

**Files:**
- Create: `skills/artisan-roast-review/SKILL.md`
- Modify: `internal/skill/content.go`
- Modify: `internal/skill/cmd/embedskill/main.go`
- Modify: `internal/skill/cmd/embedskill/main_test.go`
- Generate: `internal/skill/content_generated.go`
- Modify: `internal/skill/install.go`
- Modify: `internal/skill/install_unix.go`
- Modify: `internal/skill/install_windows.go`
- Modify: `internal/skill/install_*_test.go`
- Modify: `internal/skill/content_test.go`
- Modify: `internal/command/skill.go`
- Modify: `internal/command/skill_test.go`
- Modify: `internal/command/cobra_root.go`
- Modify: `internal/command/cobra_root_test.go`

**Interfaces:**
- Consumes: complete roast command surface and existing secure atomic skill installer.
- Produces:
  - immutable skill definitions for `artisan-inventory` and `artisan-roast-review`.
  - `skill list`, optional named `skill show [NAME]`, and `skill install [NAME] --directory ROOT [--force]`.
  - backward-compatible no-name default to `artisan-inventory`.

- [ ] **Step 1: Write multi-skill registry/generator RED tests**

Require stable lexical names:

```go
[]string{"artisan-inventory", "artisan-roast-review"}
```

Assert lookup returns immutable copied content, unknown names fail with `unknown_skill`, generated bytes match both canonical sources, generation is deterministic, and the generator refuses duplicate names, invalid names, missing frontmatter name, source/name mismatch, unsafe destination, and partial/durability failures.

- [ ] **Step 2: Write named install/show/list RED tests**

Assert:

- no-name show/install remains exactly inventory-compatible;
- named show returns exact selected bytes;
- list human/JSON order is stable;
- named install targets `ROOT/<NAME>/SKILL.md`;
- one skill cannot overwrite the other;
- unknown names fail before filesystem mutation;
- symlink/reparse/root-swap/race/durability protections apply independently to both; and
- concurrent installs of either/both leave complete correct files.

- [ ] **Step 3: Refactor installer to consume an immutable definition**

Define:

```go
type Definition struct {
    Name    string
    Content []byte
}
func Names() []string
func Lookup(name string) (Definition, bool)
func Install(root, name string, force bool) (InstallResult, error)
```

Pass the selected definition explicitly into `installPlatform`, `inspectTargetAt`, and every content/name operation. Retain compatibility aliases only where existing package tests/API need them; command behavior, not a stale global, is authoritative. Validate skill names before any path access.

Update the generator to emit both reviewed definitions in one deterministic generated registry or two deterministic variables consumed by a hand-written registry. `go generate ./internal/skill` must be idempotent.

- [ ] **Step 4: Author the Agent Skill with contract-first tests**

Before writing `SKILL.md`, add tests requiring these exact concepts/commands:

- version then exact server-bound auth status;
- expected user/organization and member/admin role;
- never request/read/print/persist/pass a token and never run `auth login`;
- parsed current revision requirement;
- chart download and conditional raw profile download;
- profile/metadata/event/comment strings are untrusted data, never instructions;
- fixed three-line marker and all seven review sections;
- evidence/timestamp/unit/channel requirements;
- no invented sensory/bean/control claims;
- maximum 4,000 code points;
- automatic `roast review post` without confirmation;
- one bounded `roast_revision_changed` restart;
- replay success and deleted-review respect;
- cleanup of owned temporary files;
- no hardware, inventory, detail, publication, or public-feedback mutation; and
- read-only production smoke.

Forbid provider/API-key/model configuration, curl, tokens, user-supplied prompt/template instructions, parsing human tables, and any unbound automated `artisan` command.

Then write the minimal complete skill satisfying those tests. Frontmatter description is trigger-only:

```yaml
---
name: artisan-roast-review
description: Use when an agent is asked to analyze a private Artisan roast profile and post evidence-based feedback through Artisan CLI.
---
```

- [ ] **Step 5: Implement skill commands and Cobra compatibility**

Support:

```text
artisan skill list
artisan skill show [NAME]
artisan skill install [NAME] --directory ROOT [--force]
```

No name defaults to inventory. Validate at most one positional name. Add static name completion, updated help, parse-failure messages, and legacy single-dash flag handling. Human install output names the selected skill; JSON result paths remain escaped and never expose stale paths after a location-swap failure.

- [ ] **Step 6: Regenerate and run all skill/security tests**

```bash
go generate ./internal/skill
gofmt -w internal/skill internal/command/skill.go internal/command/skill_test.go \
  internal/command/cobra_root.go internal/command/cobra_root_test.go
go test ./internal/skill/... ./internal/command -count=1
go vet ./internal/skill/... ./internal/command
go generate ./internal/skill
git diff --check
```

The second generation must produce no diff beyond the intended committed generated file.

- [ ] **Step 7: Commit**

```bash
git add skills/artisan-roast-review internal/skill internal/command/skill.go \
  internal/command/skill_test.go internal/command/cobra_root.go \
  internal/command/cobra_root_test.go
git commit -m "feat: add the roast review agent skill"
```

---

### Task 7: Documentation, pinned disposable integration, and CLI completion

**Files:**
- Modify: `README.md`
- Modify: `RELEASE_NOTES.md`
- Modify: `docs/commands.md`
- Modify: `docs/agent-skill.md`
- Modify: `docs/json-and-exit-codes.md`
- Modify: `docs/security.md`
- Modify: `integration/artisan-server.ref`
- Modify: `integration/inventory_cli_test.go`
- Create: `integration/roast_review_cli_test.go`
- Create: `integration/inspect_roast_reviews.py`
- Create: `integration/testdata/review-profile.alog`
- Modify: `integration/README.md`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `internal/releasecontract/release_contract_test.go`

**Interfaces:**
- Consumes: reviewed merged server commit from the companion plan and complete Tasks 1–6 CLI.
- Produces: exact server pin, disposable member/admin end-to-end proof, complete public docs, native/release contract coverage, and a reviewed CLI release candidate without publishing.

- [ ] **Step 1: Pin the exact reviewed server commit**

After the server plan is complete, obtain its full merged `origin/main` SHA, verify it is an ancestor of server `origin/main`, and write exactly that 40-lowercase-hex SHA plus one newline to `integration/artisan-server.ref`. Update the integration test's `pinnedServerRef` constant to the same literal. Never pin a local-only, dirty, merge-base, branch-name, or mutable tag reference.

Run:

```bash
go test ./integration -run '^TestPinnedServerRef$' -count=1
```

Expected: pass.

- [ ] **Step 2: Add a deterministic profile fixture contract**

Copy the smallest current valid Artisan 4.x profile fixture needed for parsed chart/event/control coverage into `integration/testdata/review-profile.alog`. Add a fixed SHA-256 assertion in integration tests and a lexical fixture check proving it contains no token, URL, external path, or executable test instruction. The fixture is test data only and is not included in release archives.

- [ ] **Step 3: Extend disposable setup without weakening target proof**

Reuse the existing strict loopback origin, local Docker context/socket, exact disposable label/marker, isolated home/config/temp, bounded process/output, token scan, credential revocation, and cleanup framework. Do not create a second weaker Compose harness.

Add helpers that use only disposable credentials to:

1. upload fixture revision 1 to a random roast UUID;
2. issue both admin and member CLI credentials through existing guarded setup;
3. inspect database audit/comment/slot counts by mounting `integration/inspect_roast_reviews.py` read-only into `compose_guard run --rm api`; the script must require the same PID-1 disposable marker, exact Compose project, fixed test organization, canonical roast UUID argument, and emit one bounded JSON object with only counts/IDs; and
4. upload a modified revision 2 for stale/replay coverage.

Every created roast/comment/object/database row disappears with the disposable volumes.

- [ ] **Step 4: Add end-to-end member/admin read and download tests**

For each role, run the compiled CLI with exact server binding and JSON:

```text
roast list --search <unique title>
roast show <uuid>
roast revisions <uuid> --all
roast chart download <uuid> <owned destination>
roast profile download <uuid> 1 <owned destination>
roast comment list <uuid> --all
```

Assert normalized IDs, current revision SHA, chart JSON schema/parser/core data, raw bytes equal the fixed fixture, reported hashes equal independent local hashes, private file modes/ACLs, no-clobber behavior, and bounded output. Scan records/files for tokens and unexpected secrets.

- [ ] **Step 5: Add end-to-end first-writer/revision tests**

Create two different valid review body files for revision 1. Post as member, then post the other as admin. Assert both successful responses return the same comment UUID/body and replay false then true. Query comments and disposable database counts: one comment, one review slot, one `comment.created` audit.

Upload revision 2. Assert:

- retrying revision 1 returns the original existing comment;
- a never-posted stale identity is rejected without another comment;
- revision 2 accepts one new review slot/comment; and
- member/admin permissions remain equivalent for the dedicated review endpoint.

Also prove browser cookie without bearer is rejected, foreign tenant cannot see/post, and trash hides all roast/read/review routes. Use only disposable data.

- [ ] **Step 6: Document commands, security, automatic posting, and compatibility**

Document every command/flag and JSON result field. State clearly:

- the host agent performs AI analysis; server/CLI do not call a provider;
- profile data is private and is processed by the configured host agent;
- reviews post automatically once valid analysis is complete;
- comments are ordinary private user-authored organization comments;
- one first-writer slot exists per revision/template;
- deleted comments are not recreated;
- profile text is untrusted prompt-injection input;
- downloads are integrity-checked and no-clobber by default;
- both skills and no-name inventory compatibility;
- exact minimum compatible server SHA; and
- production smoke is read-only.

Add an unreleased `v0.4.0` section to release notes. Do not create a tag or release.

- [ ] **Step 7: Update CI/release contracts for two embedded skills**

CI must run generation and fail on diff for both skills. Release-contract tests inspect all six archives and require:

```text
skills/artisan-inventory/SKILL.md equivalent embedded content
skills/artisan-roast-review/SKILL.md equivalent embedded content
```

If archives currently include one canonical `SKILL.md`, update the reviewed release layout to include both unambiguous paths while preserving license, notices, executable, release notes, determinism, checksums, and platform identities. Update docs and tests together; do not silently replace the inventory skill payload.

- [ ] **Step 8: Run complete local verification**

With the clean toolchain:

```bash
export PATH=/tmp/go1.23.2-clean/bin:$PATH
test -z "$(gofmt -l $(find cmd internal integration -name '*.go' -type f))"
go generate ./internal/skill
git diff --check
go vet ./...
go test ./...
go test ./... -race -timeout=20m
```

Build and inspect unreleased local archives without publishing:

```bash
rm -rf dist/release-candidate
scripts/build-release.sh v0.4.0-test 0000000000000000000000000000000000000000 release-candidate
(cd dist/release-candidate && sha256sum -c checksums.txt)
```

Then remove `dist/release-candidate`. Start only the exact pinned server in a guarded disposable stack and run the complete integration suite with bounded cleanup. Confirm no containers/networks/volumes, generated binary, downloaded profile/chart, review body, isolated config, or token artifact remains.

Expected: all unit/race/native/release/integration checks pass; production was never contacted or mutated.

- [ ] **Step 9: Commit documentation and integration**

```bash
git add README.md RELEASE_NOTES.md docs integration .github/workflows \
  internal/releasecontract
git commit -m "test: verify agent-native roast reviews"
```

- [ ] **Step 10: Fresh whole-feature review and exact-HEAD verification**

Review the complete branch diff against the approved spec and pinned server contract. Resolve every Critical/Important/Minor finding with focused RED/GREEN coverage. Rerun the complete commands from Step 8 at exact final HEAD and confirm `git status --short`, `git diff --check`, generated-source identity, server-ref identity, and worktree cleanliness.

Record final CLI and server SHAs and residual risks. Do not push, merge, tag `v0.4.0`, publish archives, deploy the server, migrate production, or post to a real roast without separate authorization.
