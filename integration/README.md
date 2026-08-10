# Pinned Artisan Server integration

`inventory_cli_test.go` proves the compiled CLI against the exact Artisan Server commit in [`artisan-server.ref`](artisan-server.ref). The live test covers public-description create/read/update/clear, administrator/member price reads, identical filtered totals, administrator-only price mutation, authoritative reservation costs, member ledger/conflict/image reads, and description omission from lot-list and reduced desktop projections (which also omit financial fields). The live test accepts only a canonical numeric IPv4/IPv6 loopback HTTP(S) origin, disables proxies and redirects, and skips when none of its opt-in environment is present. Hostnames (including `localhost`), zones, alternate IP spellings, and IPv4-mapped IPv6 literals are rejected; mapped loopback is deliberately rejected to avoid URL/transport interpretation differences. A partially configured environment fails instead of skipping.

The harness resolves a trusted locally built CLI binary before execution and rejects a symlink in its final path component. Parent-directory symlink resolution and replacement races remain within the trusted local build/workspace premise; the harness is not a sandbox for an attacker-controlled binary. Every command runs from a separate temporary working directory with isolated home, config, state, and temp directories. Each invocation has a context deadline, bounded child-pipe wait, and bounded stdout/stderr capture. Unix commands run in a new process group that is terminated and checked before return; Windows commands start suspended and are assigned to a kill-on-close Job Object before they resume. This containment completes before deferred token scans begin.

The test logs in through the browser CSRF/session APIs, issues a disposable desktop credential, and passes its one-time token only to the compiled CLI's `auth login --token-stdin`. Captured CLI records never include stdin and are scanned for the raw token before diagnostics. Deferred cleanup attempts logout on every post-issuance failure, then scans every isolated tree and captured record, while credential revocation and Compose teardown remain additional cleanup boundaries.

## Local run

Use only a dedicated, disposable checkout of `fr3akX/artisan-server` at the pinned ref. The server wrapper requires the local default Unix Docker context, exact absolute Compose files, a random compliant project name, and its tracked disposable marker. Do not replace the wrapper with direct Compose commands.

From the server checkout:

```bash
set -euo pipefail
SERVER_ROOT="$PWD"
CLI_ROOT=/absolute/path/to/artisan-cli
PINNED_REF="$(cat "$CLI_ROOT/integration/artisan-server.ref")"
test "$(git rev-parse HEAD)" = "$PINNED_REF"
test "$(git remote get-url origin)" = 'git@github.com:fr3akX/artisan-server.git' || \
  test "$(git remote get-url origin)" = 'https://github.com/fr3akX/artisan-server.git'

export ARTISAN_SERVER_E2E_PROJECT_NAME="artisan-server-e2e-$(openssl rand -hex 6)"
export ARTISAN_SERVER_HTTP_PORT=18080
export ARTISAN_SERVER_E2E_PUBLIC_ORIGIN=http://127.0.0.1:18080
export ARTISAN_SERVER_E2E_POSTGRES_PORT=15432
export ARTISAN_SERVER_E2E_MINIO_PORT=19000
export ARTISAN_SERVER_E2E_MAILPIT_HTTP_PORT=18025
export ARTISAN_INTEGRATION_BASE_URL=http://127.0.0.1:18080
export ARTISAN_INTEGRATION_ADMIN_EMAIL=owner@example.com
export ARTISAN_INTEGRATION_ADMIN_NICKNAME=Owner
export ARTISAN_INTEGRATION_MEMBER_EMAIL=member@example.com
export ARTISAN_INTEGRATION_MEMBER_NICKNAME=Member
export ARTISAN_INTEGRATION_MEMBER_PASSWORD="$(openssl rand -base64 36 | tr -d '\n')"
export ARTISAN_INTEGRATION_ADMIN_ORGANIZATION='My Roastery'
export ARTISAN_INTEGRATION_ADMIN_ORGANIZATION_SLUG=my-roastery
export ARTISAN_INTEGRATION_ADMIN_PASSWORD="$(openssl rand -base64 36 | tr -d '\n')"

mkdir -p secrets
chmod 0700 secrets
openssl rand -base64 24 > secrets/postgres_password.txt
openssl rand -base64 24 > secrets/minio_password.txt
openssl rand -base64 48 > secrets/session_secret.txt
printf '%s' "$ARTISAN_INTEGRATION_ADMIN_PASSWORD" > secrets/admin_password.txt
chmod 0444 secrets/*.txt

compose_guard() {
  timeout --signal=TERM --kill-after=30s 12m \
    "$SERVER_ROOT/scripts/e2e_compose.py" \
    --project "$ARTISAN_SERVER_E2E_PROJECT_NAME" \
    -f "$SERVER_ROOT/compose.yaml" -f "$SERVER_ROOT/compose.e2e.yaml" "$@"
}
trap 'compose_guard down -v --remove-orphans' EXIT
compose_guard config --quiet
compose_guard down -v --remove-orphans
compose_guard up -d --build
```

Poll the fixed loopback readiness URL with a bounded timeout; do not use an unbounded wait, redirects, proxies, or a fixed sleep:

```bash
timeout --signal=TERM --kill-after=5s 125s python3 - <<'PY'
import json, time, urllib.error, urllib.request
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file_pointer, code, message, headers, new_url):
        return None
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
deadline = time.monotonic() + 120
while True:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise SystemExit('Artisan Server did not become ready within 120 seconds')
    try:
        with opener.open('http://127.0.0.1:18080/api/v1/health/ready', timeout=min(2, remaining)) as response:
            body = response.read(65537)
            if len(body) <= 65536 and response.status == 200 and json.loads(body).get('components') == {'database': 'ok', 'object_store': 'ok'}:
                break
    except (OSError, urllib.error.URLError, json.JSONDecodeError):
        pass
    time.sleep(min(1, max(0, deadline - time.monotonic())))
PY
```

The readiness loop has an internal monotonic 120-second deadline. Its outer workflow/local guard permits 125 seconds before sending `TERM`, then up to the five-second `--kill-after` interval; it is therefore not a strict 120-second process ceiling. On workflow failure, Compose `logs --tail 200` retains up to 200 lines **per container**, while the following `head -c 65536` caps the aggregate emitted log stream at 65,536 bytes.

Then bootstrap and run the compiled CLI:

```bash
compose_guard run --rm api python -m app.cli bootstrap-admin \
  --email "$ARTISAN_INTEGRATION_ADMIN_EMAIL" \
  --nickname "$ARTISAN_INTEGRATION_ADMIN_NICKNAME" \
  --organization "$ARTISAN_INTEGRATION_ADMIN_ORGANIZATION" \
  --slug "$ARTISAN_INTEGRATION_ADMIN_ORGANIZATION_SLUG" \
  --password-file /run/secrets/admin_password

workspace_input="$CLI_ROOT"
[[ -d "$workspace_input" && ! -L "$workspace_input" ]]
workspace="$(realpath -e -- "$workspace_input")"
[[ "$workspace" == "$workspace_input" ]]
script_input="$workspace/integration/provision_member.py"
[[ -f "$script_input" && ! -L "$script_input" ]]
script="$(realpath -e -- "$script_input")"
[[ "$script" == "$script_input" && "$script" == "$workspace/"* ]]
issued_file="$(mktemp)"
if ! compose_guard run --rm \
  -e "ARTISAN_E2E_MEMBER_EMAIL=$ARTISAN_INTEGRATION_MEMBER_EMAIL" \
  -e "ARTISAN_E2E_MEMBER_NICKNAME=$ARTISAN_INTEGRATION_MEMBER_NICKNAME" \
  -e "ARTISAN_E2E_MEMBER_PASSWORD=$ARTISAN_INTEGRATION_MEMBER_PASSWORD" \
  -e "ARTISAN_E2E_ORGANIZATION_SLUG=$ARTISAN_INTEGRATION_ADMIN_ORGANIZATION_SLUG" \
  -v "$script:/tmp/provision_member.py:ro" \
  api python /tmp/provision_member.py > "$issued_file"; then
  rm -f "$issued_file"
  unset issued_file
  false
fi
if ! IFS=$'\t' read -r ARTISAN_INTEGRATION_MEMBER_TOKEN ARTISAN_INTEGRATION_MEMBER_CREDENTIAL_ID < <(tail -n 1 "$issued_file") ||
  [[ ! "$ARTISAN_INTEGRATION_MEMBER_TOKEN" =~ ^[^[:space:]]+$ ]] ||
  [[ ! "$ARTISAN_INTEGRATION_MEMBER_CREDENTIAL_ID" =~ ^[0-9a-f-]{36}$ ]]; then
  rm -f "$issued_file"
  unset issued_file
  false
fi
export ARTISAN_INTEGRATION_MEMBER_TOKEN ARTISAN_INTEGRATION_MEMBER_CREDENTIAL_ID
rm -f "$issued_file"
unset issued_file

cd "$CLI_ROOT"
mkdir -p dist
GOTOOLCHAIN=go1.23.12 CGO_ENABLED=0 go build -trimpath -o "$PWD/dist/artisan-integration" ./cmd/artisan
export ARTISAN_CLI_BINARY="$PWD/dist/artisan-integration"
GOTOOLCHAIN=go1.23.12 go test ./integration -count=1 -v
```

The `EXIT` trap always removes containers, volumes, and orphans through the guarded wrapper. On success, unset the exported integration/bootstrap values and remove the disposable server checkout's generated `secrets` directory.
