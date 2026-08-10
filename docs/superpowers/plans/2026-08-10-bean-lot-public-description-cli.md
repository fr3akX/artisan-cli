# Bean Lot Public Description CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the server's optional public bean-lot description through strict Artisan CLI lot create, update, clear, show, JSON, help, documentation, and Agent Skill contracts.

**Architecture:** Extend the existing strict full-detail wire model and generic mutation normalization with one nullable multiline field, then route one `--description` flag through both the legacy execution layer and Cobra compatibility layer. Keep list/desktop projections unchanged and build this work on top of the pricing/totals implementation so one CLI v0.3.0 release matches the complete deployed server contract.

**Tech Stack:** Go 1.23+, Cobra/pflag, standard `flag`, strict `encoding/json`, `golang.org/x/text/unicode/norm`, Go tests, generated embedded Agent Skill Markdown.

## Global Constraints

- Before this plan, complete Tasks 1–6 of `docs/superpowers/plans/2026-08-10-inventory-pricing-totals-cli.md`.
- Complete this entire plan before Tasks 7–8 (verification and release) of the pricing/totals plan.
- The compatible server must include `artisan-server` migration `0011_bean_lot_public_description`; server deployment precedes CLI v0.3.0 publication.
- Use the JSON/API name `description`, CLI flag/clear name `description`, and human detail label `Public description`.
- The field is nullable multiline plain text: 2,000 Unicode code points and 8,000 UTF-8 bytes.
- Normalize line endings to LF, normalize NFC, trim, reject forbidden controls/invalid UTF-8, and convert blank optional input to JSON null.
- `--description` and `--clear description` in one update are a local usage error.
- Require `description` as string or null in strict full-detail responses; reject omission, wrong types, and unknown fields.
- Keep descriptions out of lot-list/desktop projections and list tables.
- Preserve exact price/totals fields, role boundaries, exit codes, JSON envelopes, idempotency, pagination, completion behavior, and the six static release targets.

---

### Task 1: Strict API models and mutation normalization

**Files:**
- Modify: `internal/api/inventory_models.go`
- Modify: `internal/api/inventory_mutations.go`
- Modify: `internal/api/inventory_models_test.go`
- Modify: `internal/api/inventory_mutations_test.go`
- Modify: shared inventory response fixture builders in `internal/api/*_test.go`

**Interfaces:**
- Produces: `BeanLotFields.Description *string` with JSON tag `description`
- Produces: `BeanLotDetail.Description *string` with JSON tag `description`
- Produces: sparse patch support for `description` value/null/omission
- Consumes: `normalizeOptionalRequestText(..., 2000, 8000, true)` and `validResponseOptionalText(..., 2000, 8000, true)`

- [ ] **Step 1: Add failing create/patch normalization tests**

In `inventory_mutations_test.go`, extend the create normalization case:

```go
description := "  Cafe\u0301 story\r\nSecond paragraph  "
manifest := BeanLotCreateManifest{
    Fields: BeanLotFields{
        Name:        "Lot",
        Description: &description,
        Varietals:   []string{},
    },
    Images: []ImageUploadManifest{},
}
normalized, failure := normalizeBeanLotCreateManifest(manifest)
if failure != nil {
    t.Fatalf("normalizeBeanLotCreateManifest() failure = %#v", failure)
}
if normalized.Fields.Description == nil || *normalized.Fields.Description != "Café story\nSecond paragraph" {
    t.Fatalf("description = %#v", normalized.Fields.Description)
}
```

Extend strict JSON decoding:

```go
manifest, failure := DecodeBeanLotCreateManifest([]byte(`{"fields":{"name":"Lot","description":" first\rsecond ","varietals":[]},"images":[]}`))
if failure != nil || manifest.Fields.Description == nil || *manifest.Fields.Description != "first\nsecond" {
    t.Fatalf("DecodeBeanLotCreateManifest() = %#v, %#v", manifest, failure)
}

patch, failure := DecodeBeanLotPatch([]byte(`{"description":" first\r\nsecond "}`))
if failure != nil || !patch.HasField("description") {
    t.Fatalf("DecodeBeanLotPatch() = %#v, %#v", patch, failure)
}
encoded, _ := json.Marshal(patch)
if string(encoded) != `{"description":"first\nsecond"}` {
    t.Fatalf("patch JSON = %s", encoded)
}

clear, failure := DecodeBeanLotPatch([]byte(`{"description":null}`))
if failure != nil || !clear.HasField("description") {
    t.Fatalf("clear patch = %#v, %#v", clear, failure)
}
encoded, _ = json.Marshal(clear)
if string(encoded) != `{"description":null}` {
    t.Fatalf("clear JSON = %s", encoded)
}
```

Add boundary rejection for 2,001 runes, malformed UTF-8, NUL, and C1 controls. Keep an exact 2,000 four-byte-rune value as the accepted upper bound.

- [ ] **Step 2: Add failing full-detail response tests**

Update the canonical full-detail JSON fixture to include:

```json
"description":"Public story\nSecond paragraph"
```

Assert:

```go
var detail BeanLotDetail
if err := decodeOneJSON([]byte(validDetailJSON()), &detail); err != nil {
    t.Fatalf("decodeOneJSON() error = %v", err)
}
if detail.Description == nil || *detail.Description != "Public story\nSecond paragraph" {
    t.Fatalf("description = %#v", detail.Description)
}
```

Add malformed response cases:

```go
strings.Replace(validDetailJSON(), `"description":"Public story\nSecond paragraph",`, "", 1)
strings.Replace(validDetailJSON(), `"description":"Public story\nSecond paragraph"`, `"description":123`, 1)
strings.Replace(validDetailJSON(), `"description":"Public story\nSecond paragraph"`, `"description":"line\rbreak"`, 1)
strings.Replace(validDetailJSON(), `"description":"Public story\nSecond paragraph"`, `"description":"`+strings.Repeat("x", 2001)+`"`, 1)
```

Each must return `invalid_server_response`. Add a valid-null fixture and assert `Description == nil`. Do not add `description` to `BeanLotSummary` or `DesktopBeanLotView` fixture keys.

- [ ] **Step 3: Run focused API tests and verify missing-field failures**

Run:

```bash
go test ./internal/api -run 'Description|BeanLot|Mutation' -count=1
```

Expected: failures because the field is not accepted, normalized, decoded, or validated.

- [ ] **Step 4: Add request and response fields**

In `BeanLotFields`, immediately before `Notes`, add:

```go
Description *string `json:"description"`
```

In `BeanLotDetail`, immediately before `Notes`, add:

```go
Description *string `json:"description"`
```

In the `BeanLotDetail.UnmarshalJSON` local `detailFields` struct, add the same field. Add `"description"` to both the nullable-required list and the exact allowed/required field list passed to `decodeRequiredObject`, then assign:

```go
Description: decoded.Description,
```

In `BeanLotDetail.validate`, require:

```go
validResponseOptionalText(value.Description, 2000, 8000, true)
```

Keep the existing private notes validation at 10,000/40,000.

- [ ] **Step 5: Add create and sparse patch normalization**

In `BeanLotFields.UnmarshalJSON`, add `Description *string` to the wire struct and final assignment.

In `normalizeBeanLotCreateManifest`, add to `optionalFields`:

```go
{&manifest.Fields.Description, 2000, 8000, true},
```

In `NewBeanLotPatch`, add:

```go
"description": "nullable-string",
```

In `patchTextLimits`, add before `notes`:

```go
case "description":
    return 2000, 8000, true
```

The existing nullable-string path then maps blank input to null and preserves explicit null.

- [ ] **Step 6: Update every strict full-detail fixture and run API tests**

Find response fixture builders with:

```bash
rg -l '"notes":|\\"notes\\":' internal/api --glob '*_test.go'
```

Add required `description` immediately before `notes`; use `null` unless a test specifically checks content. Do not alter summary-only JSON.

Run:

```bash
gofmt -w internal/api/inventory_models.go internal/api/inventory_mutations.go \
  internal/api/inventory_models_test.go internal/api/inventory_mutations_test.go \
  internal/api/*_test.go
go test ./internal/api -count=1
go vet ./internal/api
```

Expected: PASS.

- [ ] **Step 7: Commit the strict API contract**

```bash
git add internal/api/inventory_models.go internal/api/inventory_mutations.go internal/api/*_test.go
git commit -m "feat: support bean lot public descriptions"
```

Use `git diff --cached --name-only` before committing to ensure no unrelated files were captured by the test-file glob.

---

### Task 2: Lot commands, Cobra compatibility, and human output

**Files:**
- Modify: `internal/command/inventory_lot_write.go`
- Modify: `internal/command/inventory_lot_write_test.go`
- Modify: `internal/command/inventory_read.go`
- Modify: `internal/command/inventory_read_test.go`
- Modify: `internal/command/cobra_inventory_write.go`
- Modify: `internal/command/cobra_root.go`
- Modify: `internal/command/cobra_inventory_test.go`
- Modify: `internal/command/cobra_root_test.go`
- Modify: `internal/command/root_test.go`

**Interfaces:**
- Consumes: API fields and validation from Task 1
- Produces: `inventory lot create --description TEXT`
- Produces: `inventory lot update LOT_ID --description TEXT`
- Produces: `inventory lot update LOT_ID --clear description`
- Produces: `Public description` human detail line with visible `\n` escaping

- [ ] **Step 1: Add failing flag create/update/clear tests**

Extend the exact create request test:

```go
args := []string{
    "inventory", "lot", "create",
    "--name", "New Lot",
    "--description", "  Cafe\u0301 story\r\nSecond paragraph  ",
    "--idempotency-key", "create-key",
}
```

Decode the captured manifest and assert:

```go
if fields["description"] != "Café story\nSecond paragraph" {
    t.Fatalf("description = %#v", fields["description"])
}
```

Add update cases for:

```go
[]string{"inventory", "lot", "update", commandLotID, "--description", " New public story ", "--idempotency-key", "set-description"}
[]string{"inventory", "lot", "update", commandLotID, "--clear", "description", "--idempotency-key", "clear-description"}
```

Assert exact bodies `{"description":"New public story"}` and `{"description":null}`. Add a conflict case combining `--description` and `--clear description`; assert exit 2, `conflicting_field`, and zero HTTP requests.

- [ ] **Step 2: Add failing local boundary and human-output tests**

Add table-driven invalid flag values: 2,001 runes, NUL, and C1 control. Assert exit 2 and no request.

In the detail-output test fixture, set description to `First paragraph\nSecond paragraph` and assert the exact stable line:

```text
Public description: First paragraph\nSecond paragraph
```

The output package intentionally renders structural newlines as visible `\n`, so one value cannot inject additional detail lines. Assert lot-list headers and rows remain unchanged and do not include `DESCRIPTION`.

- [ ] **Step 3: Add failing Cobra help/completion and legacy-routing tests**

Assert create/update help contains:

```text
--description string   Public description shown on linked public roast pages
```

Add `description` to tests that enumerate non-file-completing lot flags. Add dash-prefixed-value compatibility coverage:

```go
[]string{"inventory", "lot", "update", commandLotID, "--description", "-bright coffee", "--idempotency-key", "key"}
```

Assert `canonicalLegacyArgs` keeps `-bright coffee` as the description value rather than treating it as a flag.

- [ ] **Step 4: Run command tests and verify missing-flag/output failures**

Run:

```bash
go test ./internal/command -run 'Lot|lot|Cobra|Completion|Description' -count=1
```

Expected: failures because description is not a known or rendered command field.

- [ ] **Step 5: Implement the legacy command field path**

Add `description string` to `lotFieldFlags` and register:

```go
flags.StringVar(&values.description, "description", "", "public description")
```

In `setOptionalLotFields`, add:

```go
if visited["description"] {
    fields.Description = &values.description
}
```

Add `"description": values.description` to `stringValues` in `patchFieldsFromFlags`, and add this clear mapping:

```go
"description": "description",
```

The API layer from Task 1 performs canonical normalization for both flag and JSON paths.

- [ ] **Step 6: Implement Cobra declaration, routing, and completion**

In `addCobraLotFieldFlags`, add:

```go
flags.String("description", "", "Public description shown on linked public roast pages")
```

Add `description` to:

- both `disableFlagFileCompletion(...)` lot-field lists;
- `cobraLotFieldFlag(...)`;
- `cobraFlagConsumesValue(...)`;
- every exact known lot-field enumeration in `cobra_root.go` and its tests.

Do not add a custom completion result; return the standard no-file completion directive.

- [ ] **Step 7: Render detail and run command tests**

In `writeLotDetail`, add immediately before private Notes:

```go
{Label: "Public description", Value: optionalString(lot.Description)},
```

Run:

```bash
gofmt -w internal/command/inventory_lot_write.go internal/command/inventory_lot_write_test.go \
  internal/command/inventory_read.go internal/command/inventory_read_test.go \
  internal/command/cobra_inventory_write.go internal/command/cobra_root.go \
  internal/command/cobra_inventory_test.go internal/command/cobra_root_test.go \
  internal/command/root_test.go
go test ./internal/command -count=1
go vet ./internal/command
```

Expected: PASS.

- [ ] **Step 8: Commit command exposure**

```bash
git add internal/command/inventory_lot_write.go internal/command/inventory_lot_write_test.go \
  internal/command/inventory_read.go internal/command/inventory_read_test.go \
  internal/command/cobra_inventory_write.go internal/command/cobra_root.go \
  internal/command/cobra_inventory_test.go internal/command/cobra_root_test.go \
  internal/command/root_test.go
git commit -m "feat: expose public descriptions in lot commands"
```

---

### Task 3: Documentation, Agent Skill, and generated content

**Files:**
- Modify: `README.md`
- Modify: `docs/commands.md`
- Modify: `docs/json-and-exit-codes.md`
- Modify: `docs/agent-skill.md`
- Modify: `RELEASE_NOTES.md`
- Modify: `skills/artisan-inventory/SKILL.md`
- Regenerate: `internal/skill/content_generated.go`
- Modify: `internal/skill/content_test.go`
- Modify: `internal/releasecontract/release_contract_test.go`

**Interfaces:**
- Consumes: commands from Task 2
- Produces: discoverable public/private semantics and safe automation examples
- Preserves: byte-identical generated skill source/embed contract

- [ ] **Step 1: Add failing docs and skill contract assertions**

Extend `internal/skill/content_test.go` required snippets with:

```go
"--description <PUBLIC_DESCRIPTION>",
"--clear description",
"Treat description as public-safe",
"notes remain private",
```

Extend `internal/releasecontract/release_contract_test.go` for `docs/commands.md` with:

```go
"--description",
"description",
"Public description",
"notes remain private",
```

Add a negative assertion that lot-list examples or columns do not claim descriptions are returned.

- [ ] **Step 2: Run contract tests and verify documentation failures**

Run:

```bash
go test ./internal/skill ./internal/releasecontract -count=1
```

Expected: failures because canonical docs and skill do not mention the feature.

- [ ] **Step 3: Update user and machine documentation**

In `docs/commands.md`:

- add `--description TEXT` to create and update field flags;
- add `description` to nullable `--clear` fields;
- add `description` / `description` JSON keys to strict create/patch examples;
- state the 2,000-code-point/8,000-byte multiline normalization rules;
- state that descriptions are public-safe and shown on linked public roast pages;
- state that private `notes` are never public;
- state that `lot show` includes the field while `lot list` does not.

In `docs/json-and-exit-codes.md`, add `description` to the full detail example as a nullable string and to strict mutation examples. Keep summary examples unchanged.

Update README command highlights, `docs/agent-skill.md`, and `RELEASE_NOTES.md` to describe create/update/clear/show support without claiming publication control or snapshots.

- [ ] **Step 4: Update the canonical inventory Agent Skill**

In the lot mutation section, add examples:

```sh
artisan --json --server <EXPECTED_SERVER_URL> inventory lot create --name <NAME> --description <PUBLIC_DESCRIPTION> --opening-grams <INTEGER_GRAMS> --opening-reason <REASON> --idempotency-key <KEY>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot update <LOT_ID> --description <PUBLIC_DESCRIPTION> --idempotency-key <KEY>
artisan --json --server <EXPECTED_SERVER_URL> inventory lot update <LOT_ID> --clear description --idempotency-key <KEY>
```

Add explicit safety copy:

```text
Treat description as public-safe customer-facing copy: it appears on public roast pages linked to the lot. Keep supplier-only, purchasing, or operational information in private notes; never copy private notes into description.
```

Retain the existing mutation approval, server binding, idempotency, and authoritative reread requirements.

- [ ] **Step 5: Regenerate and verify deterministic embedding**

Run:

```bash
go generate ./internal/skill
gofmt -w internal/skill/content_generated.go
FIRST_HASH=$(sha256sum internal/skill/content_generated.go | cut -d' ' -f1)
go generate ./internal/skill
SECOND_HASH=$(sha256sum internal/skill/content_generated.go | cut -d' ' -f1)
test "$FIRST_HASH" = "$SECOND_HASH"
git diff --exit-code -- skills/artisan-inventory/SKILL.md internal/skill/content_generated.go || true
go test ./internal/skill ./internal/releasecontract ./internal/command -count=1
```

Expected: hashes match and all tests pass. The `git diff` is inspected rather than required to be empty because both source and generated file intentionally changed.

- [ ] **Step 6: Commit docs and skill**

```bash
git add README.md docs/commands.md docs/json-and-exit-codes.md docs/agent-skill.md \
  RELEASE_NOTES.md skills/artisan-inventory/SKILL.md internal/skill/content_generated.go \
  internal/skill/content_test.go internal/releasecontract/release_contract_test.go
git commit -m "docs: document public lot descriptions"
```

---

### Task 4: Compatible server pin and complete CLI verification

**Files:**
- Modify: `integration/artisan-server.ref`
- Modify: `integration/inventory_cli_test.go`
- Modify: `integration/README.md`
- Modify if required by the existing pricing plan: compatibility/version documentation and fixtures that bind the server commit
- Verify: all CLI source, docs, generated skill, integration, packaging, and release contracts

**Interfaces:**
- Consumes: deployed Artisan Server with migration `0011_bean_lot_public_description`
- Produces: one exact server pin and a release-ready CLI v0.3.0 branch
- Hands off to: Tasks 7–8 of `2026-08-10-inventory-pricing-totals-cli.md`

- [ ] **Step 1: Pin the exact compatible server commit**

After the server implementation is merged and deployed, obtain its immutable commit from the clean server checkout:

```bash
SERVER_COMMIT=$(git -C /home/maris/projects/artisan-server rev-parse HEAD)
test "$(git -C /home/maris/projects/artisan-server status --short --untracked-files=no)" = ""
printf '%s\n' "$SERVER_COMMIT" > integration/artisan-server.ref
```

Verify that commit contains both migration and public/detail contracts:

```bash
git -C /home/maris/projects/artisan-server show "$SERVER_COMMIT:backend/alembic/versions/0011_bean_lot_public_description.py" >/dev/null
git -C /home/maris/projects/artisan-server grep -n 'description' "$SERVER_COMMIT" -- \
  backend/app/schemas/inventory.py backend/app/schemas/public_roast.py
```

Expected: both commands succeed.

- [ ] **Step 2: Run the pinned server integration suite**

Use the repository's existing integration harness established by pricing-plan Task 6:

```bash
go test ./integration -count=1 -v
```

Extend the integration lifecycle, if not already covered by API tests, to create a lot with `description`, read it back, update it, clear it, and verify list summaries remain description-free. Mutations use disposable test data only; no production mutation is permitted.

Expected: PASS against the exact pinned server commit.

- [ ] **Step 3: Run all local validation**

Run:

```bash
gofmt -w $(find cmd internal integration -name '*.go' -type f)
git diff --check
go generate ./internal/skill
git diff --exit-code -- internal/skill/content_generated.go
go test ./... -count=1
go vet ./...
CGO_ENABLED=0 go build -trimpath -o artisan-v0.3.0-description-check ./cmd/artisan
./artisan-v0.3.0-description-check inventory lot create --help | grep -- '--description'
./artisan-v0.3.0-description-check inventory lot update --help | grep -- '--description'
rm -f artisan-v0.3.0-description-check
```

Expected: all commands pass, generation is clean, and both help pages expose the flag.

- [ ] **Step 4: Build every release target without publishing**

Run the dry-run/release-candidate build command defined in pricing-plan Task 7 and verify:

- six archives are produced;
- checksums validate;
- every archive includes license, notices, docs, and the generated Agent Skill;
- `artisan version` reports v0.3.0 release-candidate metadata;
- Linux binaries remain static;
- no archive is uploaded or tagged yet.

- [ ] **Step 5: Review final compatibility boundaries**

Run:

```bash
git status --short
git diff origin/main...HEAD --stat
git diff origin/main...HEAD -- internal/api internal/command docs skills integration RELEASE_NOTES.md
```

Verify manually:

- full detail requires `description`, while summary/desktop structs do not define it;
- description and notes retain different labels, limits, and public/private documentation;
- no local pricing, totals, or description-publication decision was introduced;
- no token, credential, build archive, or temporary binary is tracked;
- the server pin is a full 40-character commit and includes migration 0011.

- [ ] **Step 6: Commit the server pin or verification fixes**

If only the pin changed:

```bash
git add integration/artisan-server.ref
git commit -m "test: pin public description server contract"
```

If integration fixtures or compatibility docs required changes, stage those exact files and include them in the commit body. Do not commit generated binaries or archives.

- [ ] **Step 7: Continue the pricing plan's final review and rollout tasks**

Resume Tasks 7–8 of `docs/superpowers/plans/2026-08-10-inventory-pricing-totals-cli.md`. During production verification, require:

```bash
test "$(docker-compose -p archive-api exec -T postgres \
  psql -U artisan -d artisan -Atc 'SELECT version_num FROM alembic_version')" \
  = '0011_bean_lot_public_description'
```

Run only safe reads in production: `auth status`, `inventory lot list`, `inventory totals`, and `inventory lot show` for a human-approved lot. Confirm JSON detail contains a `description` key whose value is string or null, and confirm list items do not contain that key. Do not create, update, clear, archive, adjust, reserve, or otherwise mutate production inventory during smoke testing.
