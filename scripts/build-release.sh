#!/usr/bin/env bash
set -euo pipefail

[[ $# -ge 2 && $# -le 3 ]] || {
  echo "Usage: scripts/build-release.sh VERSION COMMIT [DESTINATION_LEAF]" >&2
  exit 2
}

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
GO_BIN=${GO:-go}
cd "$ROOT"
exec "$GO_BIN" run ./internal/releasebuilder/cmd "$1" "$2" "${3:-release}"
