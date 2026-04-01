#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

DIST_DIR="$TMP_DIR/dist"
INSTALL_DIR="$TMP_DIR/bin"
TAG="2099-01-01"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

echo "==> building host release artifact"
RS_RELEASE_ONLY_HOST=1 bash scripts/ci/build-release-artifacts.sh "$DIST_DIR" "$TAG"

echo "==> installing rs from the locally built release artifact"
RS_INSTALL_TAG="$TAG" \
RS_INSTALL_BASE_URL="file://$DIST_DIR" \
RS_INSTALL_DIR="$INSTALL_DIR" \
bash "$ROOT_DIR/install.sh" | tee "$TMP_DIR/install.txt"

test -x "$INSTALL_DIR/rs"
grep -q "installed rs $TAG" "$TMP_DIR/install.txt"
grep -q "verified sha256" "$TMP_DIR/install.txt"

echo "==> running the installed binary"
"$INSTALL_DIR/rs" --help | tee "$TMP_DIR/help.txt"
grep -q "Usage:" "$TMP_DIR/help.txt"
"$INSTALL_DIR/rs" version | tee "$TMP_DIR/version.txt"
grep -q "rs $TAG" "$TMP_DIR/version.txt"

echo "release install smoke E2E passed"
