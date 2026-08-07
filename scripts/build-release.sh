#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: scripts/build-release.sh VERSION COMMIT [DESTINATION]" >&2
  exit 2
}

[[ $# -ge 2 && $# -le 3 ]] || usage
VERSION=$1
COMMIT=$2
DESTINATION=${3:-dist/release}

[[ "$VERSION" =~ ^v[0-9A-Za-z][0-9A-Za-z._-]{0,63}$ ]] || { echo "VERSION must be a safe v-prefixed tag value" >&2; exit 2; }
[[ "$COMMIT" =~ ^[0-9a-fA-F]{40}$ ]] || { echo "COMMIT must be exactly 40 hexadecimal characters" >&2; exit 2; }
case "$DESTINATION" in
  dist/*) ;;
  *) echo "DESTINATION must be a repository-relative path below dist/" >&2; exit 2 ;;
esac
[[ "$DESTINATION" != *".."* && "$DESTINATION" != *"//"* && "$DESTINATION" =~ ^dist/[A-Za-z0-9._/-]+$ ]] || {
  echo "DESTINATION contains unsafe path components" >&2
  exit 2
}

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$ROOT"
for source in LICENSE THIRD_PARTY_NOTICES.txt skills/artisan-inventory/SKILL.md; do
  [[ -f "$source" && ! -L "$source" ]] || { echo "required regular source file is missing or unsafe: $source" >&2; exit 1; }
done
for tool in tar gzip zip unzip sha256sum file ldd; do
  command -v "$tool" >/dev/null 2>&1 || { echo "required tool is unavailable: $tool" >&2; exit 1; }
done
GO_BIN=${GO:-go}
command -v "$GO_BIN" >/dev/null 2>&1 || { echo "Go tool is unavailable: $GO_BIN" >&2; exit 1; }

if [[ -L "$DESTINATION" ]]; then
  echo "DESTINATION must not be a symlink" >&2
  exit 1
fi
parent=${DESTINATION%/*}
mkdir -p "$parent"
if [[ -e "$DESTINATION" ]]; then
  [[ -d "$DESTINATION" && ! -L "$DESTINATION" ]] || { echo "DESTINATION must be a regular directory" >&2; exit 1; }
  rm -rf -- "$DESTINATION"
fi
mkdir -p "$DESTINATION"
STAGING=$(mktemp -d "$ROOT/dist/.release-build.XXXXXX")
trap 'rm -rf -- "$STAGING"' EXIT

LDFLAGS="-s -w -X github.com/fr3akX/artisan-cli/internal/release.Version=$VERSION -X github.com/fr3akX/artisan-cli/internal/release.Commit=$COMMIT"
HOST_OS=$($GO_BIN env GOHOSTOS)
HOST_ARCH=$($GO_BIN env GOHOSTARCH)
archives=()

inspect_archive() {
  local archive=$1 top=$2 binary=$3
  local expected actual
  expected=$(printf '%s\n' \
    "$top/" \
    "$top/$binary" \
    "$top/LICENSE" \
    "$top/THIRD_PARTY_NOTICES.txt" \
    "$top/skills/" \
    "$top/skills/artisan-inventory/" \
    "$top/skills/artisan-inventory/SKILL.md" | LC_ALL=C sort)
  if [[ "$archive" == *.zip ]]; then
    actual=$(unzip -Z1 "$archive" | LC_ALL=C sort)
  else
    actual=$(tar -tzf "$archive" | LC_ALL=C sort)
  fi
  [[ "$actual" == "$expected" ]] || { echo "archive has unexpected contents: $archive" >&2; diff -u <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") >&2 || true; exit 1; }
  if printf '%s\n' "$actual" | grep -Eq '(^/|(^|/)\.\.(/|$))'; then
    echo "archive has an unsafe path: $archive" >&2
    exit 1
  fi
}

for goos in linux darwin windows; do
  for goarch in amd64 arm64; do
    top="artisan-$VERSION-$goos-$goarch"
    stage="$STAGING/$top"
    binary=artisan
    extension=.tar.gz
    if [[ "$goos" == windows ]]; then
      binary=artisan.exe
      extension=.zip
    fi
    mkdir -p "$stage/skills/artisan-inventory"
    cp -- LICENSE "$stage/LICENSE"
    cp -- THIRD_PARTY_NOTICES.txt "$stage/THIRD_PARTY_NOTICES.txt"
    cp -- skills/artisan-inventory/SKILL.md "$stage/skills/artisan-inventory/SKILL.md"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO_BIN" build -trimpath -ldflags="$LDFLAGS" -o "$stage/$binary" ./cmd/artisan
    chmod 0755 "$stage/$binary"
    "$GO_BIN" version -m "$stage/$binary" >"$STAGING/$top.buildinfo"
    grep -Fq 'github.com/fr3akX/artisan-cli' "$STAGING/$top.buildinfo" || { echo "missing Go build metadata for $top" >&2; exit 1; }
    file "$stage/$binary"

    find "$stage" -exec touch -t 200001010000 {} +
    archive="$ROOT/$DESTINATION/$top$extension"
    if [[ "$goos" == windows ]]; then
      (cd "$STAGING" && zip -X -q -r "$archive" "$top")
    elif tar --version 2>/dev/null | grep -q 'GNU tar'; then
      (cd "$STAGING" && tar --sort=name --mtime='UTC 2000-01-01' --owner=0 --group=0 --numeric-owner -cf - "$top" | gzip -n > "$archive")
    else
      (cd "$STAGING" && COPYFILE_DISABLE=1 tar -cf - "$top" | gzip -n > "$archive")
    fi
    inspect_archive "$archive" "$top" "$binary"
    archives+=("$top$extension")

    if [[ "$goos" == "$HOST_OS" && "$goarch" == "$HOST_ARCH" ]]; then
      version_output=$("$stage/$binary" --json version)
      [[ "$version_output" == *'"version":"'"$VERSION"'"'* && "$version_output" == *'"commit":"'"$COMMIT"'"'* ]] || {
        echo "native target reported unexpected version metadata" >&2
        exit 1
      }
      file "$stage/$binary"
      if [[ "$goos" == linux ]]; then
        file "$stage/$binary" | grep -Eq 'statically linked|static-pie linked' || { echo "native Linux executable is not static" >&2; exit 1; }
        ldd_output=$(ldd "$stage/$binary" 2>&1 || true)
        printf '%s\n' "$ldd_output"
        printf '%s\n' "$ldd_output" | grep -Eqi 'not a dynamic executable|not dynamically linked|statically linked' || {
          echo "ldd did not confirm a static executable" >&2
          exit 1
        }
      fi
    fi
  done
done

: > "$DESTINATION/checksums.txt"
for archive in "${archives[@]}"; do
  (cd "$DESTINATION" && sha256sum "$archive") >> "$DESTINATION/checksums.txt"
done
[[ $(wc -l < "$DESTINATION/checksums.txt") -eq 6 ]] || { echo "checksum manifest must contain exactly six entries" >&2; exit 1; }
(cd "$DESTINATION" && sha256sum -c checksums.txt)
find "$DESTINATION" -mindepth 1 -maxdepth 1 -type f | LC_ALL=C sort
