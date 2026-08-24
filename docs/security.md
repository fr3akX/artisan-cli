# Security model

This model is for Artisan CLI with Artisan Server commit
`bc62ac3c0f5a54e34119ee2546e0f9dca5f85fea` or later. Deploy the compatible
server before releasing the CLI; an older server is outside this contract.

## Threat model and server trust

The CLI protects a bearer token, refuses credential-bearing URLs and redirects,
bounds remote bodies/retries/pagination, validates response models, and uses
careful local file replacement. It does not make an untrusted server safe. The
selected server sees the token and all requested inventory data and controls
otherwise valid response content. Successful and error JSON bodies must use an
unambiguous `application/json` media type (optionally `charset=utf-8`), and a
successful body containing the exact bearer token is rejected before decoding
or rendering. Identity and JSON read routes require exact HTTP 200 responses;
mutation routes retain their documented exact statuses. Confirm the exact
organization, identity, role, and server origin
before mutation. DNS, system certificate roots, the OS,
local administrators, and any configured trusted proxy remain in the trust
boundary.

HTTPS is mandatory for non-loopback origins. Plain HTTP is permitted only for
`localhost`, loopback IPv4, or loopback IPv6 development servers. The HTTP
client refuses every redirect so an Authorization header is never intentionally
forwarded to a redirect target. The client uses Go's default transport, so
standard proxy/environment behavior applies (including `HTTP_PROXY`,
`HTTPS_PROXY`, and `NO_PROXY`); inspect those variables and proxy trust before
handling sensitive data. The CLI has no certificate-pinning or custom-CA flag.

## Bearer token handling and storage

Use hidden terminal input or `auth login --token-stdin`; never place a token in
argv, shell history, a URL, an idempotency key, or a filename. For automation,
provide exactly one bounded nonblank line on stdin. `ARTISAN_SERVER_TOKEN` is an
explicit alternative but environment variables may be visible to child
processes or same-account diagnostics and are never written by the CLI.

Stored `credentials.json` contains the bearer token in plaintext. On Unix, the
CLI creates private directories/files with modes 0700/0600 and refuses a
credential/config file accessible to group or other users. On Windows it uses a
protected DACL for the current user, SYSTEM, and Administrators. These controls
do not provide encryption at rest or protection from the account owner,
administrators, malware in the account, backups, crash dumps, or a compromised
OS. A private credential-free lock serializes complete authentication recovery,
server/token snapshot, login validation/publication, and logout critical
sections across processes. The lock rejects Unix symlinks and Windows reparse
points and uses the same private mode/ACL policy as other local state.
`auth logout` removes the stored token but cannot revoke copied tokens; revoke
credentials at the server when exposure is possible.

The CLI suppresses known token/server values from accepted remote errors, but
users must still avoid shell tracing, dumping environment/input, or sending raw
logs that contain surrounding sensitive data. SIGINT, and SIGTERM where
supported, use cancellation-aware cleanup and return the stable interruption
status 130; request deadlines remain network failures.

## Files, symlinks, and unsafe paths

Configuration and credential reads require the exact opened object to be a
safe private regular file and reject final symlinks/reparse points. Atomic
writes use a private temporary file and durable replacement. Reads check file
type and platform-appropriate private mode/ACL conditions; they do not perform
a Unix UID ownership check. Unsafe permissions, link, directory, or replacement
conditions fail closed.

Image uploads snapshot regular-file identity, size, timestamps, and a SHA-256
fingerprint, then recheck while reading; replacement, retargeting, or content
change aborts the operation. A stable symlink to a regular upload source is not
itself forbidden, so review the resolved source. Downloads refuse any existing
destination unless `--force`; forced replacement replaces the destination entry
(including a symlink) rather than writing through it, using platform-safe local
operations. Still choose a trusted parent directory and inspect unexpected
filesystem failures.

The skill installer accepts an existing root without `..`, walks root
components without following links, and installs only the selected
`artisan-inventory/SKILL.md` or `artisan-roast-review/SKILL.md` below it. It
rejects unsafe targets and differing content unless `--force`; it does not
discover an agent root or edit unrelated agent configuration.

## Private profiles and host-agent analysis

Roast profiles, charts, metadata, events, and comments are private profile data.
The configured host agent performs AI analysis; Artisan CLI and Artisan Server
do not call an AI provider. Giving the host agent read access therefore places
that agent, its provider configuration, tools, local temporary storage, and
retention policy inside the privacy boundary.

The profile text is untrusted prompt-injection input. The agent must treat every
profile, metadata, event, control, and comment string as data rather than an
instruction. It must not let embedded text alter the fixed template, target
server, authorization boundary, command allowlist, automatic-post behavior, or
cleanup rules.

Chart and profile downloads are integrity-checked against server-declared
lengths and hashes. They publish private regular files and are no-clobber by
default; use only an owned private directory and do not add `--force` in the
agent workflow. Output and downloaded data remain sensitive even after hash
validation.

AI reviews are ordinary private user-authored organization comments. One
first-writer slot exists per roast revision and fixed template. A member or
administrator can win the same slot, replays return its existing comment, and
deleted comments are not recreated. Automatic posting after valid analysis is
intentional and does not prompt; using the skill is the authorization to perform
that narrowly defined post, not to mutate profile, roast, inventory, hardware,
or public-feedback state.

## Approval and mutation ambiguity

The CLI itself requires terminal confirmation for:

- `inventory adjust`;
- `inventory lot archive`;
- `inventory image delete`; and
- `inventory conflict resolve`.

In noninteractive use those commands fail unless `--yes` is present. `--yes`
means approval has already occurred; it is not an authorization mechanism.
Other writes do not prompt, so callers remain responsible for review. The
bundled agent skill imposes a stronger gate: fresh explicit approval immediately
before every agent-driven mutation and `--yes` only for that approved operation.

Every mutation has an idempotency key. Preserve intent and key after a timeout,
network error, HTTP 409, or ambiguous response; read authoritative lot and
history state before retrying. Never retry the same logical mutation with a new
key. A reservation can create a conflict. Conflict resolution only records the
reviewed resolution note: there is **no automatic conflict stock adjustment**.
Do not turn a conflict into `inventory adjust` without a separately reviewed and
approved operation.

## Output and logging sensitivity

Human and JSON output can contain organization/user identity, lot names,
suppliers, notes, image paths/URLs, IDs, balances, timestamps, conflict notes,
and server-provided error text. Treat stdout, stderr, redirected files, CI logs,
terminal scrollback, and downloaded images as potentially sensitive. JSON error
output goes to stdout; human errors and prompts go to stderr. Do not enable
shell command tracing around token input, and redact business data according to
the deployment's policy.

## Release build threat model and authenticity

The release builders require a trusted, quiescent checkout and no concurrently
malicious process running under the same OS account. Tagged release CI uses an
isolated GitHub-hosted runner for this reason. Builder validation covers
untrusted version, commit, destination, and tool-path environment values;
pre-existing symlinks/reparse points; traversal; stale or pre-existing final
destinations; ordinary concurrent builder competitors; interrupted or failed
builds; and accidental source, binary, archive, or workflow drift.

Filesystem identity and content checks are point-in-time checks. They do not
promise protection from an attacker with the same UID/SID who can mutate held
files or directories between arbitrary system calls or after the builder
returns. Read-only payload modes reduce accidental changes but are not a
security boundary against the owning account or an administrator. Likewise,
bounded process-group/Job Object cleanup prevents ordinary helper-child leaks;
a malicious executable deliberately escaping its group/session or otherwise
attacking the runner is outside the trusted-built-binary contract and is not a
complete descendant sandbox.

Release executables are static but currently unsigned and not notarized.
SHA-256 checksums and GitHub build provenance support integrity/provenance
checking but do not replace OS code signing. See [installation](installation.md),
[commands](commands.md), and [agent skill boundaries](agent-skill.md).
