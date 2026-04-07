#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rvx"
PROJECT_DIR="$TMP_DIR/project"
PKG_SRC_DIR="$TMP_DIR/localpkg"
BUILD_DIR="$TMP_DIR/build"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
RSCRIPT_PATH="$(command -v Rscript)"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"
export RS_INSTALL_BACKEND=native

cd "$ROOT_DIR"

build_local_pkg() {
  rm -rf "$PKG_SRC_DIR" "$BUILD_DIR"
  mkdir -p "$PKG_SRC_DIR/R" "$BUILD_DIR"

  cat >"$PKG_SRC_DIR/DESCRIPTION" <<'EOF'
Package: localpkg
Version: 0.1.0
Title: Cache Rebuild Fixture
Description: Minimal local package used for cache rebuild CI.
Authors@R: person("rs", "ci", email = "ci@example.com", role = c("aut", "cre"))
License: MIT
Encoding: UTF-8
LazyData: true
EOF

  cat >"$PKG_SRC_DIR/NAMESPACE" <<'EOF'
export(hello)
EOF

  cat >"$PKG_SRC_DIR/R/hello.R" <<'EOF'
hello <- function() "cache-rebuild"
EOF

  (
    cd "$BUILD_DIR"
    R CMD build "$PKG_SRC_DIR" >/dev/null
  )
}

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

echo "==> building local package fixture"
build_local_pkg

mkdir -p "$PROJECT_DIR"
mkdir -p "$PROJECT_DIR/vendor"
cp "$BUILD_DIR"/localpkg_0.1.0.tar.gz "$PROJECT_DIR/vendor/localpkg_0.1.0.tar.gz"
cat >"$SCRIPT_PATH" <<'EOF'
cat(localpkg::hello(), "\n")
EOF

echo "==> initialize project and materialize managed library"
"$RS_BIN" init --rscript "$RSCRIPT_PATH" "$PROJECT_DIR"
"$RS_BIN" add --project-dir "$PROJECT_DIR" --source local --path vendor/localpkg_0.1.0.tar.gz localpkg
"$RS_BIN" lock "$SCRIPT_PATH"
"$RS_BIN" check "$SCRIPT_PATH"
"$RS_BIN" run --locked "$SCRIPT_PATH" | tee "$TMP_DIR/run-before.txt"
grep -q 'cache-rebuild' "$TMP_DIR/run-before.txt"

MANAGED_LIBRARY="$(sed -n 's/.*"library": "\(.*\)".*/\1/p' "$PROJECT_DIR/rs.lock.json" | head -n 1)"
if [ -z "$MANAGED_LIBRARY" ]; then
  echo "failed to resolve managed library path from lockfile"
  cat "$PROJECT_DIR/rs.lock.json"
  exit 1
fi
test -d "$MANAGED_LIBRARY"

echo "==> remove active managed library through rvx cache rm"
"$RS_BIN" cache rm "$MANAGED_LIBRARY" | tee "$TMP_DIR/cache-rm.txt"
grep -q '\[ok\] cache rm removed 1 managed library' "$TMP_DIR/cache-rm.txt"
test ! -d "$MANAGED_LIBRARY"

echo "==> check should fail while the managed library is missing"
if "$RS_BIN" check "$SCRIPT_PATH" >"$TMP_DIR/check-missing.txt" 2>&1; then
  echo "expected rs check to fail after removing the managed library"
  cat "$TMP_DIR/check-missing.txt"
  exit 1
fi
grep -q 'package not installed in managed library: localpkg' "$TMP_DIR/check-missing.txt"

echo "==> run --locked should rebuild the managed library"
"$RS_BIN" run --locked "$SCRIPT_PATH" | tee "$TMP_DIR/run-rebuilt.txt"
grep -q 'cache-rebuild' "$TMP_DIR/run-rebuilt.txt"
test -d "$MANAGED_LIBRARY"

echo "==> validation should pass again after rebuild"
"$RS_BIN" check "$SCRIPT_PATH"
"$RS_BIN" cache ls --json "$SCRIPT_PATH" | tee "$TMP_DIR/cache-ls.json"
grep -q "\"path\": \"$MANAGED_LIBRARY\"" "$TMP_DIR/cache-ls.json"
grep -q '"active": true' "$TMP_DIR/cache-ls.json"

echo "Cache rebuild E2E passed"
