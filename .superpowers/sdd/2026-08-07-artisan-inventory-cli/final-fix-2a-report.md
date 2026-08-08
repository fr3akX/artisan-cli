# Final integration fix 2a report

## Root causes

1. `inventory lot list` unconditionally called the administrator route and decoded only the full administrator projection. The CLI already had a strict live identity endpoint, but list dispatch did not consult it, so member credentials could not reach the pinned desktop lot-list contract.
2. Multipart preparation fingerprinted any readable regular JPEG/PNG regardless of size. Size was known from the already-open file descriptor before hashing, but no nonempty/10 MiB gate used it.
3. Adjustment validation normalized the eventual request internally, while the confirmation prompt was built independently from raw flag values and omitted reason, reference, and occurred-at.
4. The canonical skill retained an earlier unbound auth-status startup before the server-bound check.

## Implementation

- Added a strict `DesktopBeanLotView` and page decoder for pinned `GET /api/v1/inventory/bean-lots`, including required/null/type checks, UUID/enumeration/text/year/gram/balance/conflict invariants, opaque cursor validation, unknown additive-field tolerance, exact `limit`/`cursor`, and bounded `--all` collection.
- `inventory lot list` now performs a fresh `/api/v1/auth/me` identity check on every invocation. Administrators retain the existing full route, filters, JSON, and table. Members use only the reduced active projection; `--q`, `--state`, `--availability`, `--conflict`, and `--roast-uuid` fail with `member_list_filters_unsupported` before any reduced-list request.
- Added a reduced human table that renders only fields actually present in the member projection.
- Enforced `1 <= image size <= 10 * 1024 * 1024` from the opened file handle before SHA-256 work or HTTP construction. The rule flows through both lot create and image add.
- Exported exact adjustment normalization for command use. Confirmation now uses canonical lot/adjustment values and visibly escaped lot, signed delta, normalized reason, explicit `<omitted>` reference state, and canonical occurrence time.
- Updated adjustment help/docs to define `--grams` as a signed stock delta, not target stock.
- Restored canonical skill startup to `artisan version`, then `artisan --json --server "$TRUSTED_SERVER" auth status`; regenerated embedded content and aligned agent-skill documentation/content tests.
- Extended the compiled CLI integration workflow with a disposable member/user/desktop credential provisioner, separate isolated CLI state, member reduced pagination, member reservation create/finalize/release, and stable `403 administrator_required` checks for lot show/create/update/archive/restore/ledger/history, adjust, conflict, and image command families.

## TDD evidence

Focused RED initially failed because `ListDesktopBeanLots`, `ListAllDesktopBeanLots`, the multipart fingerprint hook, role dispatch, full adjustment confirmation, and the ordered startup sequence did not exist. After implementation, focused tests pass for:

- exact member route/query/page shape, additive fields, invalid member invariants, and bounded opaque pagination;
- fresh admin/member dispatch, stale-role rejection, reduced JSON/table fields, pagination, and local filter refusal;
- empty/exact/over-limit image sizes, exact-limit replay streaming, zero hashing for empty/over-limit, and zero create/add requests for over-limit;
- exact normalized confirmation, decline/no-config construction, human/JSON decline, and JSON `--yes` behavior;
- canonical generated skill ordering and unbound-first rejection;
- integration workflow structure and member provisioner compilation.

## Validation

- Fresh toolchain: `/tmp/go1.23.2/bin/go version` -> `go version go1.23.2 linux/amd64`.
- Focused API/command/skill/integration tests: passed.
- `go test ./... -count=1`: passed.
- `go test ./... -race -count=1`: passed; releasebuilder completed in about 394 seconds.
- `go vet ./...`: passed.
- `gofmt -l` over `cmd`, `internal`, and `integration`: no output.
- `python3 -m py_compile integration/provision_member.py`: passed; generated `__pycache__` was removed.
- `go generate ./internal/skill`: deterministic; generated SHA-256 stayed `b5cb2cef98a8c85d3f794688377686ff4b4fb7aa6fbaff4f6839365d0ecc5e38`.
- `git diff --check`: passed.
- Static CLI builds and 13 package test-compiles passed for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, and windows/arm64. The first cross command exhausted `/tmp` after completing the four Unix targets; rerunning both Windows targets with `GOTMPDIR` on `/home` passed.

## Docker/compiled E2E status

Docker was available and the pinned server was checked out at `4c0136fe98f6728f4bb94e416c5abe570e7f4831` in disposable worktrees. The compiled E2E reached and passed admin full list, member reduced list/pagination, all representative member 403 checks, reservation create, and reservation finalize. It then exposed reuse of one roast UUID for the release scenario (`409 invalid_inventory_transition`); the scenario was corrected to use a separate roast UUID. Subsequent fresh-stack attempts could not reach readiness because the disposable server reported `api` and/or `minio-init` unhealthy while the Docker root filesystem was at 100% (roughly 0.4–0.7 GiB free). All attempted projects, volumes, containers, images tagged by these runs, and temporary server worktrees were cleaned. The final release branch of the live scenario therefore remains environment-blocked; it was not weakened or skipped in source.

## Review

Self-review covered the full status/diff, pinned server response and permission contracts, exact routes, member field exposure, secret handling, cleanup ordering, multipart pre-hash placement, generated content, and workflow timeout/Compose guards. No code blocker was found. The only residual validation risk is the final compiled release path not completing after the distinct-roast correction because of Docker health/disk pressure described above.
