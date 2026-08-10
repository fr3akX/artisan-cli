# Artisan CLI Inventory Pricing and Totals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Release Artisan CLI v0.3.0 with exact EUR lot pricing, server-calculated filtered inventory totals, member-capable financial reads, and complete safe-read parity against the compatible Artisan Server.

**Architecture:** Extend strict Go wire models and API clients with nullable integer-cent fields and a dedicated `/api/v1/inventory/read` root, while preserving administrator mutation and reservation-write routes. Parse human decimal prices without floating point, render deterministic EUR strings, add an `inventory totals` Cobra leaf, regenerate the embedded Agent Skill, and verify the compiled CLI against a pinned disposable server before the coordinated server-first deployment and six-platform release.

**Tech Stack:** Go 1.23+, Cobra/pflag, standard `net/http` and `encoding/json`, `httptest`, FastAPI companion server, Docker Compose disposable integration, GitHub Actions release archives/provenance.

## Global Constraints

- Canonical design: `docs/superpowers/specs/2026-08-10-inventory-pricing-totals-cli-design.md`.
- Server prerequisite: `fr3akX/artisan-server/docs/superpowers/plans/2026-08-10-inventory-bearer-read-api.md`; merge and deploy that server commit before publishing CLI v0.3.0.
- Executable and command name remains `artisan`; module path remains `github.com/fr3akX/artisan-cli`; minimum Go version remains 1.23.
- Unit price is nullable EUR cents per kilogram in `0..2147483647`; zero is priced and null is unpriced.
- Human `--price-per-kg-eur` accepts only canonical unsigned decimal EUR with zero, one, or two fractional digits and maximum `21474836.47`; never parse or format money through binary floating point.
- Totals, valuation, and reservation costs are server-authoritative; the CLI validates invariants but never calculates authoritative aggregates or roast costs.
- Every safe inventory GET uses `/api/v1/inventory/read`; all create/update/archive/adjust/image/conflict mutations remain `/api/v1/inventory/admin`; reservation mutations retain their existing routes.
- Both active `admin` and `member` bearer identities may perform safe reads; only administrators may mutate prices or use administrator routes.
- Never accept a bearer token as argv or print it in output/errors; HTTPS remains mandatory except canonical loopback HTTP and authenticated redirects remain forbidden.
- JSON mode emits exactly one stable envelope and preserves integer cents/null; human output alone contains `€` presentation strings.
- Existing desktop/public response models remain unchanged; do not add price to `DesktopBeanLotView`.
- Release builds remain `CGO_ENABLED=0`, dependency-free, unsigned, and unnotarized for the six existing platform targets.
- No production inventory mutation is permitted during validation or release smoke testing.
- Companion ordering: complete Tasks 1–6 of this plan, then execute `docs/superpowers/plans/2026-08-10-bean-lot-public-description-cli.md`, then return for Tasks 7–8. Do not review, package, deploy, tag, or publish v0.3.0 before the companion plan passes.

---

### Task 1: Exact money helpers and strict financial wire models

**Files:**
- Create: `internal/command/inventory_money.go`
- Create: `internal/command/inventory_money_test.go`
- Modify: `internal/api/inventory_models.go`
- Modify: `internal/api/inventory_models_test.go`
- Modify: `internal/api/inventory_mutations.go`
- Modify: `internal/api/inventory_mutations_test.go`

**Interfaces:**
- Produces:
  - `func parsePricePerKgEUR(raw string) (int64, *output.Error)`.
  - `func formatEURCents(cents int64) string`, `func formatSignedEURCents(cents int64) string`, and `func optionalEURCents(cents *int64) string`.
  - `BeanLotSummary.PricePerKgEURCents *int64` with JSON name `price_per_kg_eur_cents`.
  - `InventoryReservation.RoastCostEURCents *int64` with JSON name `roast_cost_eur_cents`.
  - `type InventoryTotals struct` containing the seven server aggregate fields.
  - `BeanLotFields.PricePerKgEURCents *int64` for strict create JSON and sparse patch support.

- [ ] **Step 1: Write failing decimal parser/formatter tests**

Use a table with exact accepted outputs:

```go
accepted := map[string]int64{
    "0": 0, "0.0": 0, "0.00": 0,
    "1": 100, "12.3": 1230, "12.34": 1234,
    "21474836.47": 2147483647,
}
```

Reject `""`, `" 1"`, `"1 "`, `"+1"`, `"-1"`, `"00"`, `"01"`, `".1"`, `"1."`, `"1.234"`, `"1,00"`, `"1_00"`, `"1e2"`, `"NaN"`, non-ASCII digits, and `"21474836.48"`. Assert rejection is exit 2 with code `invalid_price_per_kg_eur`.

Formatter assertions:

```go
want := map[int64]string{0: "€0.00", 1: "€0.01", 1234: "€12.34"}
```

`optionalEURCents(nil)` must return `"-"`.

- [ ] **Step 2: Run parser RED**

```bash
go test ./internal/command -run 'TestParsePricePerKgEUR|TestFormatEURCents' -count=1
```

Expected: compile failure because the helpers do not exist.

- [ ] **Step 3: Implement digit-only money handling**

Implement parsing by splitting at most once on `.`; reject leading zeroes except the single whole-part `0`; right-pad one fractional digit to two; parse only digit strings with `strconv.ParseInt`; combine `whole*100+fraction`; compare against `2_147_483_647`. Do not call `ParseFloat`, `FormatFloat`, or locale APIs.

Implement formatting with integer quotient/remainder:

```go
func formatEURCents(cents int64) string {
    return fmt.Sprintf("€%d.%02d", cents/100, cents%100)
}

func formatSignedEURCents(cents int64) string {
    if cents < 0 {
        return "-" + formatEURCents(-cents)
    }
    return formatEURCents(cents)
}
```

`formatEURCents` receives only validated nonnegative unit prices/costs. Totals valuation uses `formatSignedEURCents`; its safe-integer bound makes negation safe because `math.MinInt64` is impossible.

- [ ] **Step 4: Write failing model and invariant tests**

Update canonical summary/detail/reservation JSON fixtures so the new fields are always present. Add `InventoryTotals` fixtures for:

```json
{"lot_count":3,"on_hand_grams":1000,"reserved_grams":250,"available_grams":750,"on_hand_value_eur_cents":1234,"priced_lot_count":2,"unpriced_lot_count":1}
```

and the valid no-priced case with null valuation and zero priced count. Reject:

- missing required financial fields;
- null `price_per_kg_eur_cents`/`roast_cost_eur_cents` only where nullable is not declared correctly;
- price below 0 or above 2147483647;
- roast cost below 0 or above 9007199254740991;
- fractional, boolean, string, or overflow JSON numbers;
- negative counts or counts above the JavaScript-safe integer bound;
- `priced_lot_count + unpriced_lot_count != lot_count`;
- `available_grams != on_hand_grams - reserved_grams`;
- null valuation with priced lots or non-null valuation with no priced lots;
- valuation outside the signed safe-integer range.

- [ ] **Step 5: Run model RED**

```bash
go test ./internal/api -run 'Test.*Price|Test.*RoastCost|TestInventoryTotals|TestInventoryModels' -count=1
```

Expected: failures because fields are ignored/missing and totals has no decoder.

- [ ] **Step 6: Add strict fields, decoders, and validators**

Add constants:

```go
const (
    maxPricePerKgEURCents int64 = 2_147_483_647
    maxSafeInteger        int64 = 9_007_199_254_740_991
)
```

Define:

```go
type InventoryTotals struct {
    LotCount               int64  `json:"lot_count"`
    OnHandGrams            int64  `json:"on_hand_grams"`
    ReservedGrams          int64  `json:"reserved_grams"`
    AvailableGrams         int64  `json:"available_grams"`
    OnHandValueEURCents    *int64 `json:"on_hand_value_eur_cents"`
    PricedLotCount         int64  `json:"priced_lot_count"`
    UnpricedLotCount       int64  `json:"unpriced_lot_count"`
}
```

Require `price_per_kg_eur_cents` in summary/detail decode, `roast_cost_eur_cents` in reservation decode, and all seven totals fields. Add nullable names only for the three nullable money fields. Preserve unknown-field tolerance for server responses.

Add `price_per_kg_eur_cents: "price"` to `NewBeanLotPatch`, accept nil for that kind, and validate exact integer values in range. Add the field to `BeanLotFields`; strict create JSON still uses `DisallowUnknownFields()` and therefore accepts only the exact API key.

- [ ] **Step 7: Run GREEN and commit**

```bash
gofmt -w internal/command/inventory_money.go internal/command/inventory_money_test.go internal/api/inventory_models.go internal/api/inventory_models_test.go internal/api/inventory_mutations.go internal/api/inventory_mutations_test.go
go test ./internal/command ./internal/api -count=1
go vet ./internal/command ./internal/api

git add internal/command/inventory_money.go internal/command/inventory_money_test.go internal/api/inventory_models.go internal/api/inventory_models_test.go internal/api/inventory_mutations.go internal/api/inventory_mutations_test.go
git commit -m "feat: add exact inventory money contracts"
```

---

### Task 2: Member-capable read client and totals request

**Files:**
- Modify: `internal/api/inventory_reads.go`
- Modify: `internal/api/inventory_reads_test.go`
- Modify: `internal/api/inventory_images.go`
- Modify: `internal/api/inventory_images_test.go`
- Modify: `internal/api/inventory_models.go`
- Modify: `internal/api/inventory_models_test.go`

**Interfaces:**
- Consumes: server `READ_ROOT = '/api/v1/inventory/read'` and Task 1 models.
- Produces:
  - `inventoryReadRoot = "/api/v1/inventory/read"`.
  - `type InventoryTotalsOptions struct { Query, State, Availability, Conflict, RoastUUID string }`.
  - `func ValidateInventoryTotalsOptions(InventoryTotalsOptions) *output.Error`.
  - `func (c *Client) InventoryTotals(context.Context, InventoryTotalsOptions) (InventoryTotals, *output.Error)`.
  - All lot/history/conflict/image GETs target the read root; mutation methods retain `inventoryAdminRoot`.

- [ ] **Step 1: Write failing httptest route tests**

For `ListBeanLots`, `BeanLot`, `BeanLotLedger`, `BeanLotReservations`, `BeanLotConflicts`, `InventoryConflict`, and `DownloadInventoryImage`, assert the server observes `/api/v1/inventory/read/...`. Assert no call to `Client.Identity()` occurs before list.

Add a totals request assertion with all filters:

```text
/api/v1/inventory/read/bean-lots/totals?availability=negative&conflict=open&q=guji&roast_uuid=11111111111141118111111111111111&state=active
```

The exact query ordering may follow `url.Values.Encode()`; compare parsed values instead of raw parameter order.

- [ ] **Step 2: Run read-client RED**

```bash
go test ./internal/api -run 'Test.*ReadRoot|TestInventoryTotals|Test.*DownloadInventoryImage' -count=1
```

Expected: requests still target `/inventory/admin` and no totals method exists.

- [ ] **Step 3: Split pagination from shared filters**

Refactor query construction into:

```go
func lotFilterQuery(query, state, availability, conflict, roastUUID string) (url.Values, *output.Error)
func lotListQuery(options LotListOptions) (url.Values, *output.Error)
func inventoryTotalsQuery(options InventoryTotalsOptions) (url.Values, *output.Error)
```

`lotListQuery` merges validated pagination with `lotFilterQuery`; totals uses only `lotFilterQuery`. It must be impossible for totals to serialize `limit`, `cursor`, or `all`.

- [ ] **Step 4: Move every safe read to the new root**

Change the full lot, ledger, reservation, conflict, conflict-detail, and download URL construction to `inventoryReadRoot`. Keep reduced desktop helpers available for compatibility tests but stop calling them from command code. Do not change paths in `CreateBeanLotWithImages`, `PatchBeanLot`, `AdjustBeanLot`, image mutations, or conflict resolution.

Implement:

```go
func (c *Client) InventoryTotals(ctx context.Context, options InventoryTotalsOptions) (InventoryTotals, *output.Error) {
    query, failure := inventoryTotalsQuery(options)
    if failure != nil { return InventoryTotals{}, failure }
    var totals InventoryTotals
    failure = c.doInventoryRead(ctx, inventoryReadRoot+"/bean-lots/totals", query, false, &totals)
    return totals, failure
}
```

- [ ] **Step 5: Generalize server-upgrade classification**

Rename the admin-only 404 classifier to `classifyInventoryAPIFailure`. Preserve an entity 404 only when `preserveEntityNotFound` is true and the code is `bean_lot_not_found`; map every other read-root 404 to:

```go
&output.Error{
    ExitCode: 9,
    Code: "server_upgrade_required",
    Message: "The server does not provide the inventory read API; upgrade Artisan Server",
    HTTPStatus: statusPointer(http.StatusNotFound),
}
```

Apply the same classification to image-download non-200 handling without consuming/logging a token or leaving temporary files.

- [ ] **Step 6: Make link validation accept one coherent root**

Replace admin-only image/link regexes with validation that accepts exactly one of:

```go
var inventoryProjectionRoots = []string{
    "/api/v1/inventory/admin",
    "/api/v1/inventory/read",
}
```

A detail response is valid only when self, ledger, reservations, cover image, and every detail image all use the same root and the same lot/image IDs. Reject browser/public roots and mixed admin/read responses. This keeps admin mutation responses valid while requiring read responses to be internally coherent.

- [ ] **Step 7: Run GREEN and commit**

```bash
gofmt -w internal/api/inventory_reads.go internal/api/inventory_reads_test.go internal/api/inventory_images.go internal/api/inventory_images_test.go internal/api/inventory_models.go internal/api/inventory_models_test.go
go test ./internal/api -count=1
go vet ./internal/api

git add internal/api/inventory_reads.go internal/api/inventory_reads_test.go internal/api/inventory_images.go internal/api/inventory_images_test.go internal/api/inventory_models.go internal/api/inventory_models_test.go
git commit -m "feat: use member inventory read API"
```

---

### Task 3: Totals command and financial human output

**Files:**
- Modify: `internal/command/inventory.go`
- Modify: `internal/command/inventory_read.go`
- Modify: `internal/command/inventory_read_test.go`
- Modify: `internal/command/inventory_conflict_test.go`
- Modify: `internal/command/inventory_image_test.go`

**Interfaces:**
- Consumes: `InventoryTotalsOptions`, `Client.InventoryTotals()`, and Task 1 money formatter.
- Produces: legacy execution path `runInventoryTotals(...)`, member/admin full read behavior, `PRICE/KG`, `Price per kg`, `ROAST COST`, and totals detail output.

- [ ] **Step 1: Write failing member/full-read tests**

Replace the old member reduced-list expectation with a full `BeanLotPage` response containing price. Assert:

- member lists accept all existing filters;
- list performs one inventory request and no `/auth/status` identity preflight;
- member lot show, ledger, reservations, conflicts, conflict detail, and image download succeed against read-root fixtures;
- JSON emits exact integer cents/null with no `€` strings.

- [ ] **Step 2: Write failing human output tests**

Expected list header includes `PRICE/KG` and rows include `€12.34/kg`, `€0.00/kg`, or `-`. Detail includes:

```text
Price per kg: €12.34/kg
```

Reservation table includes `ROAST COST` with `€6.17` or `-`. Totals output uses these exact labels:

```text
Matching lots: 3
On-hand grams: 1000
Reserved grams: 250
Available grams: 750
On-hand EUR value: €12.34
Priced lots: 2
Unpriced lots: 1
```

A null value prints `On-hand EUR value: -`. Include a signed totals formatter case such as `-€12.34` if the server returns a valid negative valuation.

- [ ] **Step 3: Run command RED**

```bash
go test ./internal/command -run 'TestInventory.*(Member|Price|Totals|Reservation|Read)' -count=1
```

Expected: member path still performs identity/reduced list and no totals output exists.

- [ ] **Step 4: Remove role-dependent list dispatch**

Delete the `client.Identity()` branch from `runInventoryLotList`; always call `ListBeanLots` or `ListAllBeanLots`. Preserve bounded pagination, cursor checks, JSON envelope behavior, and local option validation.

Update lot/detail/reservation writers to use exact format helpers. Do not change ledger/conflict numeric output.

- [ ] **Step 5: Add the legacy totals execution path**

Add `case "totals"` to `runInventory`. Parse only `--q`, `--state`, `--availability`, `--conflict`, and `--roast-uuid` with `flag.ContinueOnError`. Reject positional arguments and all unknown pagination flags locally with exit 2. Execute one client request and render `InventoryTotals` through `output.WriteDetails`.

- [ ] **Step 6: Run GREEN and commit**

```bash
gofmt -w internal/command/inventory.go internal/command/inventory_read.go internal/command/inventory_read_test.go internal/command/inventory_conflict_test.go internal/command/inventory_image_test.go
go test ./internal/command -count=1
go vet ./internal/command

git add internal/command/inventory.go internal/command/inventory_read.go internal/command/inventory_read_test.go internal/command/inventory_conflict_test.go internal/command/inventory_image_test.go
git commit -m "feat: show inventory prices and totals"
```

---

### Task 4: Create, update, and clear lot prices

**Files:**
- Modify: `internal/command/inventory_lot_write.go`
- Modify: `internal/command/inventory_lot_write_test.go`
- Modify: `internal/api/inventory_mutations.go`
- Modify: `internal/api/inventory_mutations_test.go`

**Interfaces:**
- Consumes: `parsePricePerKgEUR()` and Task 1 `BeanLotFields`/patch validation.
- Produces: `--price-per-kg-eur` on lot create/update and clear aliases `price-per-kg-eur`/`price_per_kg_eur`.

- [ ] **Step 1: Write failing create/update request-body tests**

Capture exact multipart manifest or JSON body and assert:

- create omission serializes `"price_per_kg_eur_cents":null` because the strict create model is complete;
- create `--price-per-kg-eur 12.34` serializes integer `1234`;
- update omission does not include the sparse field;
- update `--price-per-kg-eur 0` serializes integer `0`;
- `--clear price-per-kg-eur` and `--clear price_per_kg_eur` serialize explicit null;
- setting and clearing price together is exit 2 `conflicting_field` and sends zero requests;
- every invalid decimal from Task 1 sends zero requests;
- strict create JSON accepts integer/null only; patch JSON null clears; float, bool, string, negative, and overflow inputs fail locally.

- [ ] **Step 2: Run mutation RED**

```bash
go test ./internal/command ./internal/api -run 'Test.*PricePerKg|Test.*PriceClear' -count=1
```

Expected: unknown flag/field failures.

- [ ] **Step 3: Add the string flag to legacy assembly**

Add `pricePerKgEUR string` to `lotFieldFlags` and register:

```go
flags.StringVar(&values.pricePerKgEUR, "price-per-kg-eur", "", "EUR price per kilogram")
```

When visited, parse once and assign a fresh `int64` pointer to create fields or the sparse update map key `price_per_kg_eur_cents`. Never infer presence from an empty/default string; use `visitedFlagNames`.

- [ ] **Step 4: Add clear aliases and conflict checks**

Extend `clearable`:

```go
"price-per-kg-eur": "price_per_kg_eur_cents",
"price_per_kg_eur": "price_per_kg_eur_cents",
```

The existing `fields[field]` collision check must reject simultaneous set/clear. Price changes remain ordinary idempotent admin patches and do not add a separate `--yes` prompt.

- [ ] **Step 5: Verify retry/idempotency and response rendering**

Extend replay tests so a transient network failure reuses the same exact integer-cent body and idempotency key. Assert successful create/update output includes the authoritative returned price and a later read remains the required verification path in docs/skill; do not locally assume the request value was persisted.

- [ ] **Step 6: Run GREEN and commit**

```bash
gofmt -w internal/command/inventory_lot_write.go internal/command/inventory_lot_write_test.go internal/api/inventory_mutations.go internal/api/inventory_mutations_test.go
go test ./internal/command ./internal/api -count=1
go vet ./internal/command ./internal/api

git add internal/command/inventory_lot_write.go internal/command/inventory_lot_write_test.go internal/api/inventory_mutations.go internal/api/inventory_mutations_test.go
git commit -m "feat: manage inventory lot prices"
```

---

### Task 5: Cobra, help, documentation, and embedded Agent Skill

**Files:**
- Modify: `internal/command/cobra_inventory.go`
- Modify: `internal/command/cobra_inventory_write.go`
- Modify: `internal/command/cobra_inventory_test.go`
- Modify: `internal/command/cobra_root_test.go`
- Modify: `internal/command/root_test.go`
- Modify: `skills/artisan-inventory/SKILL.md`
- Regenerate: `internal/skill/content_generated.go`
- Modify: `internal/skill/content_test.go`
- Modify: `README.md`
- Modify: `docs/commands.md`
- Modify: `docs/json-and-exit-codes.md`
- Modify: `docs/agent-skill.md`
- Modify: `RELEASE_NOTES.md`
- Modify: `internal/releasecontract/release_contract_test.go`

**Interfaces:**
- Consumes: Tasks 3–4 command runners.
- Produces: discoverable Cobra totals/price flags, stable completions/help, updated operator/JSON/agent contracts, and byte-identical generated skill content.

- [ ] **Step 1: Write failing Cobra/help/completion tests**

Assert:

- `artisan inventory totals --help` lists exactly the five filters and no pagination flags;
- totals `state`, `availability`, and `conflict` completion values match lot list;
- create/update help includes `--price-per-kg-eur`;
- update `--clear` completion includes both price aliases;
- `knownInventoryCommandPath` and parse-failure mapping recognize `inventory totals`;
- global flags remain valid before/after the new leaf;
- legacy/manual and Cobra parse paths produce identical requests and output.

- [ ] **Step 2: Run Cobra RED**

```bash
go test ./internal/command -run 'Test.*(Totals|Price).*Cobra|Test.*Help|Test.*Completion' -count=1
```

Expected: unknown command/flag failures.

- [ ] **Step 3: Register the command and flags**

Add `newInventoryTotalsCommand(ctx, state)` directly under `inventory`. Register the same static completions as list and disable file completion for every totals/price flag. Add the string price flag to both create/update Cobra builders; canonical legacy argument conversion must preserve exact decimal text.

- [ ] **Step 4: Write documentation/skill contract tests first**

Require these exact concepts in source and generated content:

- command example `inventory totals --state active --availability positive`;
- human flag example `--price-per-kg-eur 12.34`;
- JSON key `price_per_kg_eur_cents` with integer cents/null;
- partial valuation requires reporting priced and unpriced lot counts;
- authoritative totals must never be summed from paginated list output;
- price mutation requires admin identity, idempotency, and authoritative lot reread;
- members may perform every safe read but no admin mutation;
- production smoke is read-only.

Update release-contract tests to read the pinned SHA from `integration/artisan-server.ref` rather than embedding the old `4c0136...` literal. Require that same loaded SHA in README and release notes.

- [ ] **Step 5: Update canonical docs and skill**

Document exact accepted/rejected price syntax, null/zero semantics, totals filters/no-pagination rule, `PRICE/KG`/`ROAST COST`/coverage rendering, read-role behavior, server-upgrade error, and JSON invariants. In the Agent Skill add totals to the initial read/re-read gate and state that agents must not compute totals or costs locally.

- [ ] **Step 6: Regenerate and verify the embedded skill**

```bash
go generate ./internal/skill
gofmt -w internal/skill/content_generated.go
FIRST_HASH=$(sha256sum internal/skill/content_generated.go | cut -d' ' -f1)
go generate ./internal/skill
SECOND_HASH=$(sha256sum internal/skill/content_generated.go | cut -d' ' -f1)
test "$FIRST_HASH" = "$SECOND_HASH"
go test ./internal/skill ./internal/releasecontract ./internal/command -count=1
```

The two hashes prove generation is idempotent while allowing the intended generated-file change to remain staged for this task's commit.

- [ ] **Step 7: Commit**

```bash
git add internal/command/cobra_inventory.go internal/command/cobra_inventory_write.go internal/command/cobra_inventory_test.go internal/command/cobra_root_test.go internal/command/root_test.go skills/artisan-inventory/SKILL.md internal/skill/content_generated.go internal/skill/content_test.go README.md docs/commands.md docs/json-and-exit-codes.md docs/agent-skill.md RELEASE_NOTES.md internal/releasecontract/release_contract_test.go
git commit -m "docs: publish inventory pricing commands"
```

---

### Task 6: Pin and exercise the compatible server

**Files:**
- Modify: `integration/artisan-server.ref`
- Modify: `.github/workflows/integration.yml`
- Modify: `integration/inventory_cli_test.go`
- Modify: `integration/README.md`
- Modify: `internal/releasecontract/release_contract_test.go`
- Modify: `README.md`
- Modify: `RELEASE_NOTES.md`

**Interfaces:**
- Consumes: merged Artisan Server read-API commit and compiled CLI.
- Produces: one exact server pin used consistently by integration checkout, tests, minimum-version docs, and release contracts.

- [ ] **Step 1: Merge and obtain the verified server commit**

After the server plan is implemented and independently reviewed, fast-forward its feature branch into `artisan-server/main`, push, and require the exact commit's GitHub Actions run to pass:

```bash
cd /home/maris/projects/artisan-server
git fetch origin
git switch main
git pull --ff-only origin main
git merge --ff-only feature/inventory-bearer-read-api
git push origin main
SERVER_COMMIT=$(git rev-parse HEAD)
[[ "$SERVER_COMMIT" =~ ^[0-9a-f]{40}$ ]]
gh run list --commit "$SERVER_COMMIT" --limit 10
gh run watch "$(gh run list --commit "$SERVER_COMMIT" --json databaseId,status,conclusion --jq 'map(select(.status != "completed" or .conclusion == "success"))[0].databaseId')" --exit-status
```

If the run selection is empty or ambiguous, inspect `gh run list --commit "$SERVER_COMMIT" --json databaseId,name,event,status,conclusion,headSha` and explicitly watch the required push CI run. Do not advance the pin on a failed/nonexistent run.

- [ ] **Step 2: Write failing integration assertions**

Extend live structs with `PricePerKgEURCents *int64`, `RoastCostEURCents *int64`, and totals. Add disposable-stack assertions for:

1. admin creates an unpriced lot and member/admin both read null price;
2. admin updates price to `12.34`, both roles read `1234`, and filtered totals are identical;
3. member update attempt exits with permission failure and price remains unchanged;
4. reservation create/finalize produces server-projected costs using planned/actual grams;
5. released/unpriced reservation cost is null;
6. admin clears price and both roles observe null plus updated valuation coverage;
7. member can read ledger, conflicts, and download an image through the read namespace;
8. reduced desktop endpoint still omits the financial fields.

All mutations occur only inside the loopback disposable stack and use one idempotency key per logical operation.

- [ ] **Step 3: Advance every pin atomically**

From the CLI worktree:

```bash
cd /home/maris/projects/artisan-cli/.worktrees/inventory-pricing-totals-cli
SERVER_COMMIT=$(git -C /home/maris/projects/artisan-server rev-parse origin/main)
[[ "$SERVER_COMMIT" =~ ^[0-9a-f]{40}$ ]]
printf '%s\n' "$SERVER_COMMIT" > integration/artisan-server.ref
python3 - "$SERVER_COMMIT" <<'PY'
from pathlib import Path
import sys
sha = sys.argv[1]
paths = [Path('.github/workflows/integration.yml'), Path('integration/inventory_cli_test.go')]
for path in paths:
    text = path.read_text()
    old = '4c0136fe98f6728f4bb94e416c5abe570e7f4831'
    if old not in text:
        raise SystemExit(f'{path}: old pin not found')
    path.write_text(text.replace(old, sha))
PY
```

Update README/release notes through their dynamic documentation-contract expectations so they name the same `integration/artisan-server.ref` value. Assert exactly one 40-hex line and no stale old pin remains.

- [ ] **Step 4: Run unit/contract RED then the live disposable suite**

```bash
go test ./integration ./internal/releasecontract -count=1
```

Expected before implementing the new live flow: fixture/contract failures. Then build the CLI and run the guarded loopback environment exactly as `integration/README.md` documents, using the server Compose wrapper and the pinned checkout. The final command is:

```bash
ARTISAN_CLI_BINARY="$PWD/dist/artisan-integration" \
ARTISAN_INTEGRATION_BASE_URL=http://127.0.0.1:18080 \
  go test ./integration -count=1 -v
```

Use the complete required admin/member environment produced by the existing workflow bootstrap; never substitute production credentials or a non-loopback URL.

- [ ] **Step 5: Validate workflow and pin contracts**

```bash
go test ./integration ./internal/releasecontract -count=1
if grep -R "4c0136fe98f6728f4bb94e416c5abe570e7f4831" .github integration README.md RELEASE_NOTES.md internal/releasecontract; then false; fi
test "$(wc -l < integration/artisan-server.ref)" -eq 1
test "$(cat integration/artisan-server.ref)" = "$SERVER_COMMIT"
```

Expected: all tests pass and the grep finds no stale pin.

- [ ] **Step 6: Commit**

```bash
git add integration/artisan-server.ref .github/workflows/integration.yml integration/inventory_cli_test.go integration/README.md internal/releasecontract/release_contract_test.go README.md RELEASE_NOTES.md
git commit -m "test: pin inventory pricing server"
```

---

### Task 7: Full CLI verification and independent review

**Files:**
- Modify only files implicated by verified failures.

**Interfaces:**
- Consumes: completed Tasks 1–6.
- Produces: a clean release candidate whose generated content, static builds, docs, and disposable integration all pass.

- [ ] **Step 1: Run formatting, generation, unit, static, and race checks**

```bash
test -z "$(gofmt -l $(find cmd internal integration -name '*.go' -type f))"
go generate ./internal/skill
git diff --exit-code -- internal/skill/content_generated.go
go vet ./...
go test ./... -count=1
go test ./... -race -timeout=20m -count=1
```

Expected: every command exits zero.

- [ ] **Step 2: Build and smoke the local static binary**

```bash
rm -f artisan-v0.3.0-rc
CGO_ENABLED=0 go build -trimpath -o artisan-v0.3.0-rc ./cmd/artisan
./artisan-v0.3.0-rc --json version
./artisan-v0.3.0-rc inventory totals --help
file artisan-v0.3.0-rc
if command -v ldd >/dev/null 2>&1; then ! ldd artisan-v0.3.0-rc; fi
rm -f artisan-v0.3.0-rc
```

Expected: version envelope is valid, totals help is present, and Linux reports no dynamic dependency.

- [ ] **Step 3: Build all release archives locally**

```bash
RC_COMMIT=$(git rev-parse HEAD)
rm -rf dist/release-candidate
scripts/build-release.sh v0.3.0 "$RC_COMMIT" release-candidate
find dist/release-candidate -maxdepth 1 -type f -print | sort
(cd dist/release-candidate && sha256sum -c checksums.txt)
test "$(find dist/release-candidate -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.zip' \) | wc -l)" -eq 6
```

Expected: six archives plus `checksums.txt`, all checksums valid, and archive contract tests already proved license/notices/skill/version contents.

- [ ] **Step 4: Run the pinned disposable integration again**

Run the complete guarded environment from Task 6 with a freshly built binary and clean Compose project. Expected: admin/member reads, totals parity, disposable price mutations, cost projection, image/history reads, and desktop compatibility all pass.

- [ ] **Step 5: Request independent code review**

Use the `superpowers:requesting-code-review` skill. Require review of:

- exact-money overflow/sign/null handling;
- response required-field and totals invariants;
- read/admin route separation and 404 classification;
- token/log/temp-file safety;
- Cobra/manual parity and stable output;
- integration pin/workflow integrity;
- docs/skill accuracy and release compatibility.

Apply only technically verified findings through focused RED/GREEN tests. Commit each accepted correction with a descriptive `fix:` message; do not create a review-only empty commit.

- [ ] **Step 6: Final repository audit**

```bash
git diff --check origin/main..HEAD
git status --short
git log --oneline --decorate origin/main..HEAD
find . -maxdepth 2 -type f -name '*token*' -o -name '*.tmp'
```

Expected: only intended commits/files, no build output, no token/temp artifact, and no unrelated worktree change.

---

### Task 8: Server-first production rollout and CLI v0.3.0 release

**Files:**
- No source edits after the verified release candidate except a separately tested correction commit.

**Interfaces:**
- Consumes: green merged server commit, green CLI release candidate, production Compose project `archive-api` in `/home/maris/projects/artisan-server`.
- Produces: verified read-only server deployment and published CLI tag/assets `v0.3.0`.

- [ ] **Step 1: Verify production topology before backup**

```bash
cd /home/maris/projects/artisan-server
git status --short
SERVER_COMMIT=$(git rev-parse HEAD)
test "$SERVER_COMMIT" = "$(git rev-parse origin/main)"
docker-compose -p archive-api ps
MINIO_CID=$(docker-compose -p archive-api ps -q minio)
[[ -n "$MINIO_CID" ]]
docker inspect "$MINIO_CID" --format '{{range .Mounts}}{{println .Destination .Type .Source}}{{end}}' | grep -E '^/data (volume|bind) '
curl --fail --silent --show-error http://127.0.0.1/api/v1/health/ready
```

Stop if MinIO has no persistent `/data` mount, readiness fails, server HEAD differs from the green pushed commit, or unrelated tracked changes exist.

- [ ] **Step 2: Create and verify one coordinated backup plus rollback image**

Use Compose v1 and the production project explicitly:

```bash
BACKUP_ID="$(date -u +%Y%m%dT%H%M%SZ)-pre-inventory-read"
BACKUP_ROOT="/home/maris/backups/artisan-server/$BACKUP_ID"
HOST_UID=$(id -u)
HOST_GID=$(id -g)
mkdir -p "$BACKUP_ROOT/objects"
API_CID=$(docker-compose -p archive-api ps -q api)
OLD_API_IMAGE=$(docker inspect "$API_CID" --format '{{.Image}}')
docker tag "$OLD_API_IMAGE" "artisan-server-api:pre-inventory-read-$BACKUP_ID"
docker-compose -p archive-api stop -t 30 api
trap 'docker-compose -p archive-api start api >/dev/null' EXIT

docker-compose -p archive-api exec -T postgres \
  pg_dump -U artisan --format=custom artisan > "$BACKUP_ROOT/database.pgdump"
docker-compose -p archive-api run --rm --no-deps \
  -e HOST_UID -e HOST_GID \
  -v "$BACKUP_ROOT/objects:/backup" \
  --entrypoint /bin/sh minio-init -ec '
    mc alias set local http://minio:9000 artisan "$(cat /run/secrets/minio_password)"
    mc mirror --overwrite local/artisan-profiles /backup
    chown -R "$HOST_UID:$HOST_GID" /backup
  '
(
  cd "$BACKUP_ROOT"
  sha256sum database.pgdump > SHA256SUMS
  find objects -type f -print0 | sort -z | xargs -0 -r sha256sum >> SHA256SUMS
  sha256sum --check SHA256SUMS
)
docker-compose -p archive-api images > "$BACKUP_ROOT/IMAGES"
printf '%s\n' "$OLD_API_IMAGE" > "$BACKUP_ROOT/PRE_DEPLOY_API_IMAGE"
docker-compose -p archive-api exec -T postgres \
  psql -U artisan -d artisan -Atc 'SELECT version_num FROM alembic_version' \
  > "$BACKUP_ROOT/ALEMBIC_VERSION"
test "$(cat "$BACKUP_ROOT/ALEMBIC_VERSION")" = '0010_inventory_price_constraint'
docker-compose -p archive-api start api
trap - EXIT
```

Retain the backup, checksum manifest, image identity, and rollback tag together. A partial backup is not a backup.

- [ ] **Step 3: Build and replace only the API service**

```bash
docker-compose -p archive-api build api
NEW_API_IMAGE=$(docker-compose -p archive-api images -q api)
[[ -n "$NEW_API_IMAGE" && "$NEW_API_IMAGE" != "$OLD_API_IMAGE" ]]
docker-compose -p archive-api stop -t 30 api
docker-compose -p archive-api rm -f api
docker-compose -p archive-api up -d --no-deps api
docker-compose -p archive-api ps
```

Do not recreate PostgreSQL, MinIO, web, volumes, or network.

- [ ] **Step 4: Verify production without mutating inventory**

```bash
curl --fail --silent --show-error http://127.0.0.1/api/v1/health/live
curl --fail --silent --show-error http://127.0.0.1/api/v1/health/ready
RUNNING_API_CID=$(docker-compose -p archive-api ps -q api)
test "$(docker inspect "$RUNNING_API_CID" --format '{{.Image}}')" = "$NEW_API_IMAGE"
test "$(docker-compose -p archive-api exec -T postgres psql -U artisan -d artisan -Atc 'SELECT version_num FROM alembic_version')" = '0011_bean_lot_public_description'
HTTP_STATUS=$(curl --silent --show-error --output /tmp/inventory-read-unauth.json --write-out '%{http_code}' \
  http://127.0.0.1/api/v1/inventory/read/bean-lots)
test "$HTTP_STATUS" = '401'
python3 - <<'PY'
import json
from pathlib import Path
body = json.loads(Path('/tmp/inventory-read-unauth.json').read_text())
assert body['error']['code'] == 'authentication_required'
Path('/tmp/inventory-read-unauth.json').unlink()
PY
docker-compose -p archive-api logs --no-color --tail 200 api | head -c 65536
```

Expected unauthenticated status is 401. If an existing safely configured CLI credential is available outside argv, run only `auth status`, lot list/show, totals, ledger/conflict/image reads; do not create, update, clear, reserve, adjust, archive, delete, or resolve anything.

- [ ] **Step 5: Merge and push the CLI after server health is proven**

```bash
cd /home/maris/projects/artisan-cli
git fetch origin
git switch main
git pull --ff-only origin main
git merge --ff-only feature/inventory-pricing-totals-cli
git push origin main
CLI_COMMIT=$(git rev-parse HEAD)
[[ "$CLI_COMMIT" =~ ^[0-9a-f]{40}$ ]]
gh run list --commit "$CLI_COMMIT" --limit 10
```

Require both normal CI and pinned-server integration for the exact CLI commit to conclude success before tagging.

- [ ] **Step 6: Tag and publish v0.3.0**

```bash
git status --short
test -z "$(git status --porcelain)"
git tag -a v0.3.0 -m 'Artisan CLI v0.3.0'
git push origin v0.3.0
gh run list --commit "$CLI_COMMIT" --workflow Release --limit 5
gh run watch "$(gh run list --commit "$CLI_COMMIT" --workflow Release --json databaseId --jq '.[0].databaseId')" --exit-status
```

Stop if the tag does not point to the green CLI commit or if release workflow selection is empty/ambiguous.

- [ ] **Step 7: Download and independently verify published assets**

```bash
VERIFY_DIR=$(mktemp -d)
gh release download v0.3.0 --dir "$VERIFY_DIR"
test "$(find "$VERIFY_DIR" -maxdepth 1 -type f | wc -l)" -eq 7
test "$(find "$VERIFY_DIR" -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.zip' \) | wc -l)" -eq 6
(cd "$VERIFY_DIR" && sha256sum -c checksums.txt)
for archive in "$VERIFY_DIR"/*.tar.gz; do tar -tzf "$archive" | grep -E '/(artisan|SKILL.md|RELEASE_NOTES.md)$'; done
for archive in "$VERIFY_DIR"/*.zip; do unzip -l "$archive" | grep -E '(artisan.exe|SKILL.md|RELEASE_NOTES.md)$'; done
rm -rf "$VERIFY_DIR"
gh release view v0.3.0 --json tagName,targetCommitish,assets,url
```

Verify published provenance is present in the successful release run and the tag resolves to `CLI_COMMIT`.

- [ ] **Step 8: Preserve evidence and clean only owned worktrees**

Record server/CLI commits, Actions run IDs, backup path/checksum result, old/new API image IDs, rollback tag, migration head, production health/read-only smoke results, release URL, asset/checksum verification, and any unavailable authenticated production read. Then remove only the completed feature branches/worktrees after confirming merges; leave unrelated `.github/hooks/`, `.impeccable/`, Artisan repo `.pi-subagents/`, and all unrelated worktrees untouched.
