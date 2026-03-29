#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
PROJECT_DIR="$TMP_DIR/project"
PKG_SRC_DIR="$TMP_DIR/localpkg"
BUILD_DIR="$TMP_DIR/build"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
RSCRIPT_PATH="$(command -v Rscript)"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

build_local_pkg() {
  local greeting="$1"

  rm -rf "$PKG_SRC_DIR" "$BUILD_DIR"
  mkdir -p "$PKG_SRC_DIR/R" "$BUILD_DIR"

  cat >"$PKG_SRC_DIR/DESCRIPTION" <<EOF
Package: localpkg
Version: 0.1.0
Title: Local Package Fixture
Description: Minimal local package used for rs CI.
Authors@R: person("rs", "ci", email = "ci@example.com", role = c("aut", "cre"))
License: MIT
Encoding: UTF-8
LazyData: true
EOF

  cat >"$PKG_SRC_DIR/NAMESPACE" <<'EOF'
export(hello)
EOF

  cat >"$PKG_SRC_DIR/R/hello.R" <<EOF
hello <- function() "$greeting"
EOF

  (
    cd "$BUILD_DIR"
    R CMD build "$PKG_SRC_DIR" >/dev/null
  )
}

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

echo "==> building initial local package tarball"
build_local_pkg "hello-v1"
mkdir -p "$PROJECT_DIR/vendor"
cp "$BUILD_DIR"/localpkg_0.1.0.tar.gz "$PROJECT_DIR/vendor/localpkg_0.1.0.tar.gz"

cat >"$SCRIPT_PATH" <<'EOF'
cat(jsonlite::toJSON(list(value = localpkg::hello()), auto_unbox = TRUE), "\n")
EOF

echo "==> configuring project with local source"
"$RS_BIN" init --rscript "$RSCRIPT_PATH" "$PROJECT_DIR"
"$RS_BIN" add --project-dir "$PROJECT_DIR" jsonlite
"$RS_BIN" add --project-dir "$PROJECT_DIR" --source local --path vendor/localpkg_0.1.0.tar.gz localpkg

echo "==> lock and validate local source"
"$RS_BIN" lock "$SCRIPT_PATH"
"$RS_BIN" check "$SCRIPT_PATH"
"$RS_BIN" run --locked "$SCRIPT_PATH" | tee "$TMP_DIR/run-before.txt"
grep -q '{"value":"hello-v1"}' "$TMP_DIR/run-before.txt"

grep -q '"source_fingerprint":' "$PROJECT_DIR/rs.lock.json"
grep -q '"source_fingerprint_kind":' "$PROJECT_DIR/rs.lock.json"

echo "==> mutate local source tarball and expect drift"
build_local_pkg "hello-v2"
cp "$BUILD_DIR"/localpkg_0.1.0.tar.gz "$PROJECT_DIR/vendor/localpkg_0.1.0.tar.gz"

if "$RS_BIN" check "$SCRIPT_PATH" >"$TMP_DIR/check-after.txt" 2>&1; then
  echo "expected rs check to fail after local source drift"
  cat "$TMP_DIR/check-after.txt"
  exit 1
fi
grep -q 'source fingerprint mismatch for localpkg' "$TMP_DIR/check-after.txt"

if "$RS_BIN" run --locked "$SCRIPT_PATH" >"$TMP_DIR/run-locked-after.txt" 2>&1; then
  echo "expected rs run --locked to fail after local source drift"
  cat "$TMP_DIR/run-locked-after.txt"
  exit 1
fi
grep -q 'source fingerprint mismatch for localpkg' "$TMP_DIR/run-locked-after.txt"

if "$RS_BIN" run --frozen "$SCRIPT_PATH" >"$TMP_DIR/run-frozen-after.txt" 2>&1; then
  echo "expected rs run --frozen to fail after local source drift"
  cat "$TMP_DIR/run-frozen-after.txt"
  exit 1
fi
grep -q 'source fingerprint mismatch for localpkg' "$TMP_DIR/run-frozen-after.txt"

echo "Local source drift E2E passed"
