# Cobra Command Interface Design

**Date:** 2026-08-08
**Status:** Proposed
**Scope:** Artisan CLI command parsing, generated help, static shell completion, and clearer persisted-server login usage

## Goal

Replace the hand-written `flag.FlagSet` command router with a Cobra command tree so command names, positional arguments, flags, examples, help, and shell completion are discoverable and generated from one definition. Preserve the CLI's existing API behavior, security boundaries, JSON contracts, exit codes, persistent authentication, release format, and supported platforms.

The normal setup flow becomes obvious:

```sh
printf '%s\n' "$TOKEN" | artisan auth login \
  --server https://inventory.example \
  --token-stdin
artisan inventory lot list
```

A successful login continues to validate the identity and atomically persist the explicit server URL with the token. Later commands load the stored server and token without requiring `--server`.

## Non-goals

- No server API changes.
- No changes to inventory semantics, authorization, pagination, confirmations, or idempotency.
- No dynamic, network-backed completion of lot, image, reservation, or conflict IDs.
- No interactive login redesign or browser-based authentication.
- No removal of currently supported command spellings or global flag positions.
- No weakening of the embedded agent skill's explicit trusted-server binding on every automated command.
- No changes to release archive contents beyond the updated executable and documentation.

## Architecture

Add `github.com/spf13/cobra` as a pinned Go dependency. Cobra owns:

- command and subcommand routing;
- flag declarations and parsing;
- positional argument count validation;
- generated text help and usage;
- command-name suggestions;
- static Bash, Zsh, Fish, and PowerShell completion generation.

`command.Run` remains the process-independent entry point. It normalizes the injected `Runtime`, constructs a fresh root command for every invocation, executes it with the supplied context and arguments, and returns an exit code. No Cobra command or mutable flag value is package-global; repeated and parallel tests therefore cannot leak parser state.

The Cobra layer is an adapter over existing execution logic. Existing authentication transactions, configuration loading, API calls, output encoding, confirmation behavior, secure-file operations, and signal-aware contexts remain authoritative. Parsing is moved to Cobra command definitions, while execution helpers receive parsed typed values or a small command-options structure. Business logic is not rewritten merely to adopt Cobra.

## Command tree

The generated hierarchy is:

```text
artisan
├── auth
│   ├── login
│   ├── status
│   └── logout
├── inventory
│   ├── lot
│   │   ├── list
│   │   ├── show
│   │   ├── create
│   │   ├── update
│   │   ├── archive
│   │   ├── restore
│   │   ├── ledger
│   │   ├── reservations
│   │   └── conflicts
│   ├── adjust
│   ├── image
│   │   ├── add
│   │   ├── update
│   │   ├── reorder
│   │   ├── delete
│   │   └── download
│   ├── reservation
│   │   ├── create
│   │   ├── finalize
│   │   └── release
│   └── conflict
│       ├── list
│       ├── show
│       └── resolve
├── skill
│   ├── show
│   └── install
├── completion
│   ├── bash
│   ├── zsh
│   ├── fish
│   └── powershell
└── version
```

Every leaf command declares:

- a `Use` string that names positional arguments;
- a concise `Short` description;
- required and optional flags with meaningful descriptions;
- exact positional cardinality or a custom validator;
- examples for nontrivial syntax;
- completion directives that avoid irrelevant filesystem completion for IDs and scalar values.

Parent commands list and describe their children. File-valued arguments and flags retain filesystem completion where useful. Repeated indexed image metadata flags continue to document their zero-based `INDEX=VALUE` syntax.

## Global flags and server behavior

The compatibility-preserving persistent flags are:

```text
--json
--server URL
--timeout DURATION
```

Cobra accepts persistent flags before or after subcommands, so both forms work:

```sh
artisan --server https://inventory.example auth login --token-stdin
artisan auth login --server https://inventory.example --token-stdin
```

The second form is the documented login syntax. `--server` remains globally accepted to avoid breaking scripts and to minimize migration risk, although routine commands should rely on stored configuration. Existing precedence remains unchanged:

1. explicit invocation `--server`;
2. `ARTISAN_SERVER_URL`;
3. stored `config.json`.

Token precedence remains environment override followed by stored credentials. Environment overrides are never persisted. During a successful login, only an explicit `--server` is persisted with the newly validated token; login without it reuses an environment or stored server according to the existing rules. `auth logout` continues to remove the stored token while retaining the stored server.

Server URL validation, HTTPS requirements, loopback HTTP exception, timeout default, and five-minute maximum remain unchanged.

## Help and discoverability

`artisan --help` shows global flags and all top-level commands. Every parent and leaf command supports `-h` and `--help`. Help follows a consistent structure:

1. description;
2. usage with positional arguments;
3. examples when needed;
4. command-specific flags;
5. inherited global flags;
6. available subcommands for parent commands.

Explicit help is successful and writes to stdout. Missing commands, unknown commands, invalid flags, and invalid positional arguments remain usage failures with exit code 2. Human-readable usage failures are concise and include the most relevant `--help` hint rather than dumping unrelated root help. Cobra's command-name suggestions may be included for close misspellings.

The existing JSON behavior is preserved. In JSON mode, explicit help is a success envelope containing the generated usage text:

```json
{"ok":true,"data":{"usage":"..."}}
```

Usage and parse failures continue to use the existing error envelope and stable error codes. Help generation has no network or credential dependency.

## Completion

Add:

```text
artisan completion bash
artisan completion zsh
artisan completion fish
artisan completion powershell
```

Each command writes a shell completion program to stdout and exits successfully. Completion covers command names, flags, enum-like static values where practical, and file paths. It does not read stored credentials, call Artisan Server, or enumerate server-backed identifiers.

Documentation includes installation examples for each shell. Completion scripts are generated at runtime from the same Cobra tree as help, so they cannot drift from command and flag names.

## Output, errors, and security

Cobra's default automatic error and usage printing is disabled. Parser and validator failures are translated into the existing `output.Error` pathway so:

- human failures remain on stderr;
- JSON failures remain on stdout;
- exit codes remain stable;
- write failures retain their existing handling;
- invalid values, bearer tokens, server credentials, and sensitive paths are not echoed accidentally.

There remains no `--token` flag. Login token input continues through hidden terminal input or bounded `--token-stdin`. Completion and help never inspect token contents. The login lock, crash-recovery journal, atomic server/token publication, configuration permission checks, logout behavior, and same-snapshot configuration guarantees are unchanged.

## Compatibility strategy

The migration preserves:

- every existing command and flag spelling;
- existing positional argument order;
- old global-flag-before-command syntax;
- text and JSON success payloads for operational commands;
- documented exit codes and error codes;
- current environment and stored-configuration precedence;
- release metadata and archive contracts.

Intentional behavior changes are limited to:

- generated help at every command level;
- global flags accepted naturally after subcommands;
- static completion commands;
- clearer command descriptions, examples, and argument labels;
- relevant help hints and typo suggestions on usage errors.

Tests that asserted the old absence of help or rejected post-subcommand global flags are updated intentionally. Domain and security behavior tests remain unchanged unless their parser setup must call the Cobra adapter.

## Test isolation correction

`TestZeroRuntimeDoesNotPanic` currently allows a zero `Runtime` to resolve the real user's default configuration directory. Once a developer has logged in, `auth status` may succeed and violate the test's assumption that configuration is missing. This is an existing environment-dependent test defect, not a Cobra behavior change.

The test will use a temporary `Runtime.ConfigDir` while still leaving other runtime fields zero. It will continue to prove normalization does not panic and that missing configuration returns exit code 3, without reading or modifying the developer's real credentials.

## Testing

Focused tests cover:

- the complete command hierarchy and unique command paths;
- generated root, parent, and leaf help;
- positional argument labels and validators;
- required, optional, repeated, boolean, duration, and enum-like flags;
- global flags before and after subcommands;
- old and new login syntax;
- successful login followed by an authenticated command without `--server`;
- stored, environment, and explicit server precedence;
- JSON help, parse failures, output streams, exit codes, and redaction;
- Bash, Zsh, Fish, and PowerShell completion generation;
- completion's lack of network and credential access;
- isolated zero-runtime behavior;
- existing authentication crash recovery, locking, and security tests.

Before merging:

```sh
go test ./... -count=1
go test -race -timeout=20m ./... -count=1
go vet ./...
GOOS=darwin GOARCH=amd64 go test -exec=/bin/true ./...
GOOS=darwin GOARCH=arm64 go test -exec=/bin/true ./...
GOOS=windows GOARCH=amd64 go test -exec=/bin/true ./...
```

Release-contract tests, generated skill checks, formatting checks, and native Linux/macOS/Windows CI must pass. The private pinned-server integration workflow must also remain green.

## Documentation and release

Update:

- `README.md` quick-start login syntax;
- `docs/commands.md` generated hierarchy, flexible flag placement, and complete help behavior;
- `docs/installation.md` shell completion setup;
- `docs/agent-skill.md` to distinguish the human stored-login workflow from the agent's stricter explicit trusted-server binding.

The embedded agent skill continues to pass `--server <EXPECTED_SERVER_URL>` on every automated status, read, and mutation command. Its source and generated content change only if needed to describe flexible flag placement; the trusted-server safety gate must remain intact.

After merge and green native CI, publish a patch release after `v0.1.0` so downloadable binaries expose the new interface. The exact patch version is selected at release time according to the repository's tag history.
