# Cobra Command Interface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Artisan CLI's hand-written command router with a discoverable Cobra command tree that generates complete help and static shell completion while retaining stored-server login behavior and all existing security/output contracts.

**Architecture:** `command.Run` constructs a fresh Cobra tree per invocation. Cobra parses root and leaf flags, validates positional shape, renders help, and generates completion; leaf adapters canonicalize Cobra-parsed local flags back into the existing leaf executors so authentication, configuration, API, confirmation, and secure-file logic do not need a risky rewrite. Migration proceeds by command group, using temporary legacy passthrough nodes until each group has explicit Cobra children.

**Tech Stack:** Go 1.23.x, `github.com/spf13/cobra` v1.10.2, `github.com/spf13/pflag` v1.0.10, existing `flag`-based leaf executors, pytest-free Go tests, GitHub Actions native Linux/macOS/Windows jobs.

## Global Constraints

- Construct a fresh Cobra tree for every `command.Run`; no mutable package-global commands or flag values.
- Preserve operational text output, JSON success/error envelopes, exit codes, error codes, timeout bounds, secret redaction, and output-stream selection.
- Preserve all command/flag spellings, positional order, old global-flags-before-command syntax, and environment/stored configuration precedence.
- Recommend `artisan auth login --server URL --token-stdin`; a successful explicit-server login must persist the URL and token so later commands omit `--server`.
- Keep global `--server` accepted as a compatibility override; do not remove it in this release.
- Do not add a token command-line flag or expose token/server values in parser errors, help, or completion.
- Completion is static and local: no configuration reads, credential reads, or Artisan Server calls.
- Preserve the embedded agent skill's explicit `--server <EXPECTED_SERVER_URL>` binding on every automated command.
- Keep Linux, macOS, Windows, amd64, and arm64 release contracts intact.
- Use `/tmp/go1.23.2/bin/go` with `TMPDIR=/home/maris/t` and `GOTMPDIR=/home/maris/t` on this host because `/usr/local/go` is corrupt and `/` has little free space.

---

### Task 1: Cobra Core, Root, Skill, Version, and Test Isolation

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `internal/command/root.go`
- Create: `internal/command/cobra_root.go`
- Create: `internal/command/cobra_root_test.go`
- Modify: `internal/command/root_test.go`
- Modify: `internal/command/skill_test.go`

**Interfaces:**
- Consumes: existing `Runtime`, `normalizeRuntime`, `writeFailure`, `writeCommandHelp`, `runAuth`, `runInventory`, `runSkill`, and `release.Info`.
- Produces: `type cobraState`, `func newRootCommand(context.Context, Runtime, []string) (*cobra.Command, *cobraState)`, `func canonicalLegacyArgs(*cobra.Command, []string) []string`, `func setCommandExit(*cobraState, int)`, and temporary `newLegacyGroupCommand` nodes used by Tasks 2–4.

- [ ] **Step 1: Isolate the existing zero-runtime regression test**

Change only the configuration directory in `TestZeroRuntimeDoesNotPanic`; leave all other runtime fields zero:

```go
func TestZeroRuntimeDoesNotPanic(t *testing.T) {
	runtime := Runtime{ConfigDir: t.TempDir()}
	if code := Run(context.Background(), []string{"version"}, runtime); code != 0 {
		t.Fatalf("version code = %d, want 0", code)
	}
	if code := Run(context.Background(), nil, runtime); code != usageExitCode {
		t.Fatalf("empty command code = %d, want %d", code, usageExitCode)
	}
	if code := Run(context.Background(), []string{"auth", "status"}, runtime); code != 3 {
		t.Fatalf("auth status code = %d, want 3", code)
	}
}
```

- [ ] **Step 2: Run the isolated regression test**

Run:

```bash
mkdir -p /home/maris/t
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run TestZeroRuntimeDoesNotPanic -count=1 -v
```

Expected: PASS even though `/home/maris/.config/artisan` contains a valid login.

- [ ] **Step 3: Add failing Cobra root tests**

Create tests that require generated root/skill/version help, no parser-state leakage, and canonical reconstruction of local flags:

```go
func TestCobraRootHelpListsDiscoverableCommands(t *testing.T) {
	result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "--help")
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("root help = %#v", result)
	}
	for _, want := range []string{"Authentication and saved credentials", "Manage green-coffee inventory", "Install or inspect the embedded agent skill", "version"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("root help missing %q:\n%s", want, result.stdout)
		}
	}
}

func TestCanonicalLegacyArgsPreservesRepeatedAndExplicitFalseFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "leaf"}
	cmd.Flags().StringArray("item", nil, "repeatable item")
	cmd.Flags().Bool("cover", true, "cover state")
	if err := cmd.Flags().Parse([]string{"--item=a", "value", "--item=b", "--cover=false"}); err != nil {
		t.Fatal(err)
	}
	got := canonicalLegacyArgs(cmd, cmd.Flags().Args())
	want := []string{"--cover=false", "--item=a", "--item=b", "value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical args = %q, want %q", got, want)
	}
}
```

Also replace old tests that expected root help to be an unknown command. Preserve exact operational `version` output tests.

- [ ] **Step 4: Run the new root tests to verify they fail**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run 'TestCobraRootHelp|TestCanonicalLegacyArgs' -count=1 -v
```

Expected: FAIL because Cobra and the helper do not exist.

- [ ] **Step 5: Add the pinned Cobra dependency**

Run:

```bash
/tmp/go1.23.2/bin/go get github.com/spf13/cobra@v1.10.2
/tmp/go1.23.2/bin/go mod tidy
```

Verify `go.mod` directly requires Cobra and pins compatible transitive `pflag`/`mousetrap` versions in `go.sum`; do not update unrelated dependencies.

- [ ] **Step 6: Implement fresh per-run Cobra state and canonical leaf arguments**

In `cobra_root.go`, define state owned by one invocation:

```go
type cobraState struct {
	runtime        Runtime
	jsonMode       bool
	serverOverride string
	timeout        time.Duration
	exitCode       int
}

func setCommandExit(state *cobraState, code int) {
	state.exitCode = code
}
```

Implement `canonicalLegacyArgs` by visiting only changed local, non-persistent flags. Expand `stringArray` values one occurrence at a time and append positionals last so the existing standard-library parsers accept flags that Cobra originally found after positionals:

```go
func canonicalLegacyArgs(cmd *cobra.Command, positionals []string) []string {
	result := make([]string, 0, cmd.LocalNonPersistentFlags().NFlag()+len(positionals))
	cmd.LocalNonPersistentFlags().Visit(func(item *pflag.Flag) {
		if item.Value.Type() == "stringArray" {
			values, err := cmd.Flags().GetStringArray(item.Name)
			if err != nil {
				panic(err)
			}
			for _, value := range values {
				result = append(result, "--"+item.Name+"="+value)
			}
			return
		}
		result = append(result, "--"+item.Name+"="+item.Value.String())
	})
	return append(result, positionals...)
}
```

The helper must not include inherited `--json`, `--server`, or `--timeout` in leaf arguments.

- [ ] **Step 7: Implement root parsing, help adaptation, and safe parser failures**

Replace the manual switch in `Run` with construction and execution of a fresh root. Retain `jsonModeForParseFailure` for malformed flags that occur before Cobra can set `jsonMode`:

```go
func Run(ctx context.Context, args []string, runtime Runtime) int {
	runtime = normalizeRuntime(runtime)
	root, state := newRootCommand(ctx, runtime, args)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		return writeCobraFailure(runtime, state, args, err)
	}
	return state.exitCode
}
```

Configure `SilenceErrors`, `SilenceUsage`, `SetOut(runtime.Out)`, and `SetErr(runtime.Err)`. Add persistent `--json`, `--server`, and `--timeout` flags. A persistent pre-run validates `config.NormalizeServerURL`, positive timeout, and the existing five-minute maximum before invoking a leaf.

Capture Cobra's default help renderer before installing a custom help function. Render into a buffer and pass the resulting text to `writeCommandHelp`, preserving JSON help envelopes. Parser errors must be mapped to generic command-specific messages and must never write Cobra's raw error text containing a supplied value.

Root/parent execution without a child remains exit 2. Explicit `-h`/`--help` remains exit 0.

- [ ] **Step 8: Migrate `version` and `skill`, and add temporary legacy groups**

Register generated commands with these `Use` strings:

```text
version
skill
skill show
skill install --directory ROOT [--force]
```

`version` keeps the existing output closure. `skill show` may call `runSkill(ctx, []string{"show"}, ...)`. `skill install` declares `--directory` and `--force`, validates zero positionals, reconstructs local flags, and calls `runSkillInstall`.

Register temporary `auth` and `inventory` nodes using `DisableFlagParsing: true`; their `Run` functions call `runAuth` and `runInventory` with untouched arguments and the parsed root options. These nodes exist only to keep the complete existing suite operational while Tasks 2–4 replace them.

- [ ] **Step 9: Run focused and full tests**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command ./internal/skill -count=1
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./... -count=1
```

Expected: PASS. Confirm tests that exercise auth/inventory still traverse temporary legacy groups and operational outputs remain unchanged.

- [ ] **Step 10: Commit Task 1**

```bash
git add go.mod go.sum internal/command/root.go internal/command/cobra_root.go internal/command/cobra_root_test.go internal/command/root_test.go internal/command/skill_test.go
git commit -m "feat: add Cobra command root"
```

---

### Task 2: Authentication Tree and Remembered-Server Login UX

**Files:**
- Create: `internal/command/cobra_auth.go`
- Modify: `internal/command/cobra_root.go`
- Modify: `internal/command/auth_test.go`
- Modify: `internal/command/root_test.go`

**Interfaces:**
- Consumes: Task 1 `cobraState`, `canonicalLegacyArgs`, root persistent flags, existing `runAuthLogin`, `runAuthStatus`, and `runAuthLogout`.
- Produces: `func newAuthCommand(context.Context, *cobraState) *cobra.Command` and generated `auth login|status|logout` help.

- [ ] **Step 1: Write failing natural-login and generated-auth-help tests**

Add a login test that places global flags after the leaf and proves the stored server is used by a later status call without `--server`:

```go
func TestCobraLoginPersistsPostSubcommandServerForLaterCommands(t *testing.T) {
	dir := t.TempDir()
	server := identityServer(t, nil)
	defer server.Close()

	login := runAuthCommand(t, Runtime{
		In: strings.NewReader(commandTestToken + "\n"), ConfigDir: dir,
	}, "auth", "login", "--server", server.URL, "--timeout", "2s", "--token-stdin")
	if login.code != 0 {
		t.Fatalf("login = %#v", login)
	}
	if got, err := config.LoadStoredServer(dir); err != nil || got != server.URL {
		t.Fatalf("stored server = %q, %v", got, err)
	}
	status := runAuthCommand(t, Runtime{ConfigDir: dir}, "auth", "status")
	if status.code != 0 {
		t.Fatalf("status without --server = %#v", status)
	}
}
```

Add table tests for `auth --help`, `auth login --help`, `auth status --help`, `auth logout --help`, and `--json auth login --help`. Assert usage includes `auth login`, `--server`, `--token-stdin`, and that JSON help remains `ok:true` with `data.usage`.

Update `TestGlobalFlagsAfterAuthCommandAreNotTreatedAsGlobal` into a success/validation test proving `--json` and `--timeout` work after `auth status`.

- [ ] **Step 2: Run the auth tests to verify they fail**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run 'TestCobraLogin|TestCobraAuthHelp|TestGlobalFlagsAfterAuth' -count=1 -v
```

Expected: FAIL because the temporary legacy auth node does not parse persistent flags after the subcommand or generate child help.

- [ ] **Step 3: Build explicit auth commands**

Implement:

```go
func newAuthCommand(ctx context.Context, state *cobraState) *cobra.Command
```

Use strings and validators:

```text
auth
auth login
auth status
auth logout
```

`login` declares local boolean `--token-stdin`; its handler calls:

```go
setCommandExit(state, runAuthLogin(
	ctx,
	canonicalLegacyArgs(cmd, args),
	state.runtime,
	state.jsonMode,
	state.serverOverride,
	state.timeout,
))
```

`status` and `logout` require zero positional arguments and call their existing leaf functions directly. Parent `auth` without a child calls `authUsageFailure` with the existing exit/error contract. Add `Short`, `Long`, and `Example` text showing hidden prompt and stdin token forms without a literal token.

Replace the temporary root auth node with `newAuthCommand`.

- [ ] **Step 4: Verify redaction and both login spellings**

Keep/add table cases for:

```text
artisan --server URL auth login --token-stdin
artisan auth login --server URL --token-stdin
artisan --json auth status
artisan auth status --json
```

Add malformed `--timeout=secret-looking-value` and invalid `--server=test-secret-token` cases. Assert neither supplied string appears in stdout/stderr, text errors use stderr, JSON errors use stdout, and usage exit remains 2.

- [ ] **Step 5: Run auth, lock, recovery, and full command tests**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run 'Auth|Login|Logout|Global|Cobra' -count=1
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -count=1
```

Expected: PASS, including crash checkpoints and auth lock integration tests. No production auth transaction file should require modification.

- [ ] **Step 6: Commit Task 2**

```bash
git add internal/command/cobra_auth.go internal/command/cobra_root.go internal/command/auth_test.go internal/command/root_test.go
git commit -m "feat: generate authentication command help"
```

---

### Task 3: Inventory Read, Adjustment, Reservation, and Conflict Commands

**Files:**
- Create: `internal/command/cobra_inventory.go`
- Create: `internal/command/cobra_inventory_test.go`
- Modify: `internal/command/cobra_root.go`
- Modify: `internal/command/inventory_read_test.go`
- Modify: `internal/command/inventory_adjust_test.go`
- Modify: `internal/command/inventory_reservation_test.go`
- Modify: `internal/command/inventory_conflict_test.go`

**Interfaces:**
- Consumes: Task 1 `canonicalLegacyArgs`; existing leaf functions `runInventoryLotList`, `runInventoryLotShow`, `runInventoryLotHistory`, `executeInventoryConflicts`, `runInventoryAdjust`, `runInventoryReservationCreate`, `runInventoryReservationTransition`, and `runInventoryConflictResolve`.
- Produces: `func newInventoryCommand(context.Context, *cobraState) *cobra.Command`, explicit read/adjust/reservation/conflict leaves, and temporary lot-write/image passthrough leaves consumed by Task 4.

- [ ] **Step 1: Write a failing inventory command-manifest test**

Add a helper that walks `root.Commands()` recursively and compare the required paths for this task:

```go
func TestCobraInventoryReadCommandPaths(t *testing.T) {
	root, _ := newRootCommand(context.Background(), normalizeRuntime(Runtime{}), nil)
	got := commandPathSet(root)
	for _, want := range []string{
		"artisan inventory lot list",
		"artisan inventory lot show",
		"artisan inventory lot ledger",
		"artisan inventory lot reservations",
		"artisan inventory lot conflicts",
		"artisan inventory adjust",
		"artisan inventory reservation create",
		"artisan inventory reservation finalize",
		"artisan inventory reservation release",
		"artisan inventory conflict list",
		"artisan inventory conflict show",
		"artisan inventory conflict resolve",
	} {
		if !got[want] {
			t.Errorf("missing command %q", want)
		}
	}
}
```

Add help assertions for `lot list` flags, `adjust LOT_ID`, reservation required fields, and conflict resolve flags.

- [ ] **Step 2: Run the inventory command-manifest test to verify it fails**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run TestCobraInventoryReadCommandPaths -count=1 -v
```

Expected: FAIL because inventory is still a legacy passthrough node.

- [ ] **Step 3: Implement local flag registration helpers**

In `cobra_inventory.go`, add focused helpers that declare flags but do not perform API work:

```go
func addPageFlags(flags *pflag.FlagSet) {
	flags.Int("limit", 0, "Maximum items per page")
	flags.String("cursor", "", "Opaque continuation cursor")
	flags.Bool("all", false, "Read all bounded pages")
}

func runLegacyLeaf(cmd *cobra.Command, args []string, state *cobraState, run func([]string) int) {
	setCommandExit(state, run(canonicalLegacyArgs(cmd, args)))
}
```

Use `String`, `StringArray`, `Int`, `Int64`, `Bool`, and `Duration` according to the existing standard-library flag type. Do not mark business-required flags with Cobra's `MarkFlagRequired`; the existing executors must retain their stable error codes/messages after Cobra canonicalizes whether a flag was visited.

- [ ] **Step 4: Implement inventory read/history and adjustment leaves**

Create explicit nodes and exact positional validators:

```text
inventory lot list
inventory lot show LOT_ID
inventory lot ledger LOT_ID
inventory lot reservations LOT_ID
inventory lot conflicts LOT_ID
inventory adjust LOT_ID
```

Declare all documented list/filter/page flags. Register static flag completion for `--state active|archived`, `--availability positive|zero|negative`, and `--conflict open|none`; the callbacks return constants and never load configuration or call the server. For `adjust`, declare `--grams`, `--reason`, `--reference`, `--occurred-at`, `--yes`, and `--idempotency-key`. Call existing leaf functions with canonicalized local args. Disable file completion for IDs and scalar arguments with `cobra.ShellCompDirectiveNoFileComp`.

- [ ] **Step 5: Implement reservation and conflict leaves**

Create explicit nodes:

```text
inventory reservation create
inventory reservation finalize CLIENT_RESERVATION_UUID
inventory reservation release CLIENT_RESERVATION_UUID
inventory conflict list
inventory conflict show CONFLICT_ID
inventory conflict resolve CONFLICT_ID
```

Register every existing flag with the same type/default. `StringArray` is not needed in this group. For conflict list/show, call `runInventoryConflict` with a canonical prefix (`list` or `show`); for resolve call `runInventoryConflictResolve` directly. Parent nodes without a child retain usage exit 2.

Add temporary explicit child nodes for lot create/update/archive/restore and an image passthrough node so Cobra can route the hierarchy while Task 4 still delegates their raw arguments to existing routers.

Replace the root's temporary inventory node with `newInventoryCommand`.

- [ ] **Step 6: Prove interspersed local flags preserve behavior**

Add tests using forms that the old parser rejected but Cobra now canonicalizes:

```text
artisan inventory adjust LOT_ID --grams -25 --reason count --yes
artisan inventory lot ledger LOT_ID --limit 100
artisan inventory reservation finalize UUID --actual-grams 900 --occurred-at TIMESTAMP
artisan inventory conflict resolve UUID --note counted --yes
```

For each, assert the same API request body/header and output as the existing flags-before-positionals test. Keep the original forms as compatibility cases.

- [ ] **Step 7: Run focused and full command tests**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run 'Inventory|CobraInventory' -count=1
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -count=1
```

Expected: PASS with no changes to API payload, confirmation, pagination, idempotency, or role checks.

- [ ] **Step 8: Commit Task 3**

```bash
git add internal/command/cobra_inventory.go internal/command/cobra_inventory_test.go internal/command/cobra_root.go internal/command/inventory_read_test.go internal/command/inventory_adjust_test.go internal/command/inventory_reservation_test.go internal/command/inventory_conflict_test.go
git commit -m "feat: generate inventory read command help"
```

---

### Task 4: Lot Write and Image Command Flags

**Files:**
- Create: `internal/command/cobra_inventory_write.go`
- Create: `internal/command/cobra_inventory_image.go`
- Modify: `internal/command/cobra_inventory.go`
- Modify: `internal/command/inventory_lot_write_test.go`
- Modify: `internal/command/inventory_image_test.go`

**Interfaces:**
- Consumes: Task 1 `canonicalLegacyArgs`; Task 3 inventory parents; existing `runInventoryLotCreate`, `runInventoryLotUpdate`, `runInventoryLotState`, `runInventoryImageAdd`, `runInventoryImageUpdate`, `runInventoryImageReorder`, `runInventoryImageDelete`, and `runInventoryImageDownload`.
- Produces: complete explicit lot/image Cobra leaves with generated flags and no remaining legacy group passthrough nodes.

- [ ] **Step 1: Write failing complete lot/image help tests**

Replace hand-written exact help expectations with generated-help assertions. Require:

```go
func TestCobraLotCreateHelpDocumentsEveryInputFamily(t *testing.T) {
	result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, "inventory", "lot", "create", "--help")
	for _, want := range []string{
		"Create an inventory lot",
		"--name", "--varietal", "--opening-grams", "--from-json",
		"--image", "--image-caption", "--image-alt-text", "--image-cover",
		"zero-based INDEX=TEXT",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("create help missing %q", want)
		}
	}
}
```

Add corresponding tests for image add/update/reorder/delete/download, including positional labels and defaults (`--variant display`).

- [ ] **Step 2: Run the lot/image help tests to verify they fail**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run 'TestCobraLotCreateHelp|TestCobraImageHelp' -count=1 -v
```

Expected: FAIL because temporary passthrough nodes do not expose all flags.

- [ ] **Step 3: Declare all lot field and mutation flags**

Implement reusable metadata-only declarations:

```go
func addCobraLotFieldFlags(flags *pflag.FlagSet) {
	flags.String("name", "", "Lot name")
	flags.String("origin", "", "Origin")
	flags.String("producer", "", "Producer")
	flags.String("supplier", "", "Supplier")
	flags.String("external-reference", "", "External reference")
	flags.String("received-date", "", "Received date (YYYY-MM-DD)")
	flags.Int64("crop-year", 0, "Crop year")
	flags.StringArray("varietal", nil, "Varietal; repeat for multiple values")
	flags.String("sca-score", "", "SCA score")
	flags.String("processing-method", "", "Processing method")
	flags.String("processing-detail", "", "Processing detail")
	flags.Int64("altitude-min-metres", 0, "Minimum altitude in metres")
	flags.Int64("altitude-max-metres", 0, "Maximum altitude in metres")
	flags.String("notes", "", "Notes")
}
```

Create exact `lot create`, `update LOT_ID`, `archive LOT_ID`, and `restore LOT_ID` commands. Use `StringArray` for `--varietal`, `--clear`, and `--image`. Use `StringArray` for repeated indexed caption/alt-text values so repeated order survives canonicalization. Call existing leaf executors.

Use Cobra flag filename annotations for `--from-json` and `--image`; allow `-` explicitly for stdin JSON. Do not perform filesystem reads during completion.

- [ ] **Step 4: Declare all image flags and positional shapes**

Create:

```text
inventory image add LOT_ID FILE...
inventory image update LOT_ID IMAGE_ID
inventory image reorder LOT_ID IMAGE_ID...
inventory image delete LOT_ID IMAGE_ID
inventory image download LOT_ID IMAGE_ID DESTINATION
```

Use `StringArray` for repeated `--caption` and `--alt-text` on add, string `--cover` for the single index, boolean `--cover` on update, and the remaining existing types/defaults. Keep one-to-eight image and reorder bounds in existing executors; Cobra validates only minimum/exact positional shape.

Mark upload files and download destination as filename-completable. Register static `display|thumbnail` completion for `--variant`. Disable file completion for UUID arguments.

- [ ] **Step 5: Test repeated flags and flags after positionals**

Add a request-level regression with:

```text
artisan inventory image add LOT_ID first.png second.png \
  --caption 0=First --caption 1=Second --cover 1
```

Assert captions remain associated with indexes 0/1 and the second image remains cover. Add lot create/update cases with repeated varietals/clears and flags after the positional lot ID. These tests prove `canonicalLegacyArgs` does not collapse repeated values.

- [ ] **Step 6: Remove temporary passthrough nodes and obsolete manual help routing**

Delete the temporary lot-write/image passthrough constructors from `cobra_inventory.go`. Remove obsolete group-level `--help` branches and manual usage constants only when no direct test or lock helper calls them. Keep direct leaf executor functions and their standard-library parsing until a future internal cleanup; this release intentionally uses them as validated execution adapters.

- [ ] **Step 7: Run all command tests and repeated-flag stress**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -count=1
for i in $(seq 1 50); do
  TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run 'Cobra.*Repeated|Image.*Metadata' -count=1 >/dev/null || exit 1
done
```

Expected: PASS. No repeated value is lost or reordered within one flag.

- [ ] **Step 8: Commit Task 4**

```bash
git add internal/command/cobra_inventory.go internal/command/cobra_inventory_write.go internal/command/cobra_inventory_image.go internal/command/inventory_lot_write_test.go internal/command/inventory_image_test.go
git commit -m "feat: generate inventory mutation command help"
```

---

### Task 5: Completion, Exhaustive Help Contracts, and Documentation

**Files:**
- Modify: `internal/command/cobra_root.go`
- Modify: `internal/command/cobra_root_test.go`
- Modify: `internal/command/cobra_inventory_test.go`
- Modify: `internal/command/root_test.go`
- Modify: `README.md`
- Modify: `docs/commands.md`
- Modify: `docs/installation.md`
- Modify: `docs/agent-skill.md`
- Inspect only unless safety wording truly needs a placement clarification: `skills/artisan-inventory/SKILL.md`
- Regenerate only if the skill source changes: `internal/skill/content_generated.go`

**Interfaces:**
- Consumes: complete Cobra tree from Tasks 1–4 and Cobra `GenBashCompletionV2`, `GenZshCompletion`, `GenFishCompletion`, and `GenPowerShellCompletionWithDesc`.
- Produces: `artisan completion bash|zsh|fish|powershell`, exhaustive help/completion contracts, and user-facing documentation.

- [ ] **Step 1: Write failing completion tests with inert runtime boundaries**

Create a runtime whose configuration and terminal hooks panic if completion touches them:

```go
func TestCompletionIsStaticAndDoesNotReadConfiguration(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			result := runAuthCommand(t, Runtime{
				ConfigDir: filepath.Join(t.TempDir(), "missing"),
				Getenv: func(string) string { panic("completion read environment") },
				IsTerminal: func(int) bool { panic("completion inspected terminal") },
			}, "completion", shell)
			if result.code != 0 || result.stderr != "" || !strings.Contains(result.stdout, "artisan") {
				t.Fatalf("%s completion = %#v", shell, result)
			}
		})
	}
}
```

Also assert each script contains representative nested commands/flags (`inventory`, `--server`, `--token-stdin`) and no token/server values.

- [ ] **Step 2: Run completion tests to verify they fail**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run 'Completion' -count=1 -v
```

Expected: FAIL because explicit completion leaves are not yet registered.

- [ ] **Step 3: Add explicit shell completion commands**

Register parent `completion` with four exact-argument-free children. Each child writes directly to `state.runtime.Out` and sets exit code 0:

```go
switch shell {
case "bash":
	err = root.GenBashCompletionV2(state.runtime.Out, true)
case "zsh":
	err = root.GenZshCompletion(state.runtime.Out)
case "fish":
	err = root.GenFishCompletion(state.runtime.Out, true)
case "powershell":
	err = root.GenPowerShellCompletionWithDesc(state.runtime.Out)
}
```

Map generator write failures through the existing write-error path. Completion output is always the raw shell program even if `--json` is supplied; document this because wrapping would make the script unusable.

Disable Cobra's implicit completion command if one appears; expose only the documented `completion` hierarchy and Cobra's hidden protocol command required by generated scripts.

- [ ] **Step 4: Add exhaustive command/help contract tests**

Define the exact expected command-path set from the design and compare it to all non-hidden commands. Fail on both missing and unexpected public commands.

For every public command, execute `COMMAND --help` in text mode and assert exit 0, nonempty stdout, empty stderr, and a `Usage:` section. Repeat representative root/auth/inventory/skill leaves in JSON mode, decode the envelope, and assert `ok:true` plus nonempty `data.usage`.

Add tests for missing child, unknown child, too few/many positionals, malformed integer/duration, and invalid URL. Assert exit 2 and stable generic, redacted output. Keep the existing output-contract tests for operational commands unchanged.

- [ ] **Step 5: Update quick-start and command documentation**

Change README login to:

```sh
printf '%s\n' "$TOKEN" | artisan auth login \
  --server https://inventory.example \
  --token-stdin
artisan --json inventory lot list --limit 100
```

Remove claims that global flags must precede commands. In `docs/commands.md`, document generated help at every level, flexible global/local flag placement, the persisted-server flow, static completion, and the fact that `auth logout` retains the stored server.

In `docs/installation.md`, add:

```sh
artisan completion bash > "$HOME/.local/share/bash-completion/completions/artisan"
artisan completion fish > "$HOME/.config/fish/completions/artisan.fish"
```

and safe Zsh/PowerShell equivalents. Tell users to create target directories first and restart/source their shell as appropriate.

- [ ] **Step 6: Preserve the embedded agent skill's stricter server binding**

Update `docs/agent-skill.md` to state that human login stores a default server, but agents still bind `--server "$TRUSTED_SERVER"` on every command to prevent confused-server operation. Do not replace explicit server flags in `skills/artisan-inventory/SKILL.md`.

Run:

```bash
/tmp/go1.23.2/bin/go generate ./internal/skill
git diff -- skills/artisan-inventory/SKILL.md internal/skill/content_generated.go
```

Expected: no generated skill diff unless only flag-placement wording was intentionally changed. Any skill change must retain every trusted-server command assertion in `internal/skill/content_test.go`.

- [ ] **Step 7: Run docs, generated-content, command, and full tests**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command ./internal/skill ./internal/releasecontract -count=1
/tmp/go1.23.2/bin/go generate ./internal/skill
git diff --exit-code -- internal/skill/content_generated.go
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./... -count=1
```

Expected: PASS and generated content clean.

- [ ] **Step 8: Commit Task 5**

```bash
git add internal/command/cobra_root.go internal/command/cobra_root_test.go internal/command/cobra_inventory_test.go internal/command/root_test.go README.md docs/commands.md docs/installation.md docs/agent-skill.md skills/artisan-inventory/SKILL.md internal/skill/content_generated.go
git commit -m "docs: document generated help and completion"
```

If the skill files are unchanged, omit them from `git add` rather than forcing an empty path change.

---

### Task 6: Full Verification, Native CI, Merge, and Patch Release

**Files:**
- Inspect: all files changed by Tasks 1–5
- Modify only for evidence-driven fixes: files implicated by a failing focused regression
- No dependency, release-note, or workflow changes unless a verified failure requires them

**Interfaces:**
- Consumes: complete Cobra implementation and documentation.
- Produces: reviewed green feature branch, merged `main`, and patch release `v0.1.1` after native CI succeeds.

- [ ] **Step 1: Format, tidy, and inspect the complete diff**

Run:

```bash
/tmp/go1.23.2/bin/gofmt -w internal/command/*.go
/tmp/go1.23.2/bin/go mod tidy
git diff --check
git status --short
git diff --stat main...HEAD
git diff main...HEAD -- go.mod go.sum internal/command README.md docs skills internal/skill/content_generated.go
```

Expected: only Cobra/interface/docs/test changes; no token, credential, server secret, generated drift, or unrelated dependency update.

- [ ] **Step 2: Run focused stress and contract tests**

Run:

```bash
for i in $(seq 1 100); do
  TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/command -run 'Cobra|Help|Completion|Login.*Server|Repeated' -count=1 >/dev/null || exit 1
done
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./internal/releasecontract ./internal/skill ./integration -count=1
```

Expected: all iterations PASS.

- [ ] **Step 3: Run full, race, vet, generation, and cross-target checks**

Run:

```bash
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./... -count=1
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test -race -timeout=20m ./... -count=1
/tmp/go1.23.2/bin/go vet ./...
/tmp/go1.23.2/bin/go generate ./internal/skill
git diff --exit-code
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t GOOS=darwin GOARCH=amd64 /tmp/go1.23.2/bin/go test -exec=/bin/true ./...
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t GOOS=darwin GOARCH=arm64 /tmp/go1.23.2/bin/go test -exec=/bin/true ./...
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t GOOS=windows GOARCH=amd64 /tmp/go1.23.2/bin/go test -exec=/bin/true ./...
```

Expected: PASS. Cross-target commands prove compilation only; native CI remains authoritative.

- [ ] **Step 4: Request independent whole-change review**

Give a fresh reviewer `main...HEAD`, the design spec, and this plan. Require explicit review of:

- fresh per-run Cobra state;
- canonical repeated-flag reconstruction;
- parse-error redaction and JSON stream selection;
- login server persistence and compatibility syntax;
- completion's lack of config/network access;
- agent skill trusted-server binding;
- command/help completeness and release compatibility.

Fix only evidence-backed Critical/Important findings, then rerun the narrow regression and Step 3.

- [ ] **Step 5: Commit final evidence-driven fixes**

```bash
git add -A
git diff --cached --check
git commit -m "fix: finalize Cobra command compatibility"
```

Skip this commit if review produced no changes. Confirm `git status --short` is empty.

- [ ] **Step 6: Push the feature branch and monitor native workflows**

```bash
git push -u origin feature/cobra-cli
gh run list --repo fr3akX/artisan-cli --branch feature/cobra-cli --limit 10
```

Monitor every CI and Artisan Server integration run for the branch through completion. On a native-only failure, fetch `gh run view RUN_ID --log-failed`, add a focused regression, fix minimally, rerun local checks, push, and monitor replacement runs.

- [ ] **Step 7: Use the finishing-branch gate after branch CI is green**

Invoke `superpowers:finishing-a-development-branch` and present its integration options. If the user chooses local merge, run from the clean main worktree:

```bash
git switch main
git pull --ff-only origin main
git merge --no-ff feature/cobra-cli -m "Merge feature/cobra-cli"
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t /tmp/go1.23.2/bin/go test ./... -count=1
git push origin main
```

Verify `origin/main` equals the local merge commit and monitor both main workflows to success before removing the owned worktree/local feature branch. If the user selects PR or keep-as-is, preserve the worktree and do not run Step 8 until the change is merged and main CI is green.

- [ ] **Step 8: Publish and verify patch release `v0.1.1`**

After main CI is green and `v0.1.1` is unused, run the exact local release preflight with version `v0.1.1` and the main merge commit:

```bash
set -euo pipefail
merge_sha=$(git rev-parse HEAD)
leaf=preflight-v0.1.1
rm -rf "dist/$leaf"
TMPDIR=/home/maris/t GOTMPDIR=/home/maris/t GO=/tmp/go1.23.2/bin/go \
  scripts/build-release.sh v0.1.1 "$merge_sha" "$leaf"
test "$(find "dist/$leaf" -maxdepth 1 -type f | wc -l)" -eq 7
test "$(find "dist/$leaf" -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.zip' \) | wc -l)" -eq 6
(cd "dist/$leaf" && sha256sum -c checksums.txt)
chmod -R u+w "dist/$leaf"
rm -rf "dist/$leaf"
git tag -a v0.1.1 -m "Artisan CLI v0.1.1" "$merge_sha"
git push origin refs/tags/v0.1.1
```

Monitor the Release workflow to success. Independently download all seven assets and run:

```bash
sha256sum -c checksums.txt
```

Expected: all six archives report `OK`. Report the release URL and direct platform asset names.
