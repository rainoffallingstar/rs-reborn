#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rvx"
PROJECT_A="$TMP_DIR/project-a"
PROJECT_B="$TMP_DIR/project-b"
PROJECT_C="$TMP_DIR/project-c"
PKG_SRC_DIR="$TMP_DIR/localpkg"
BUILD_DIR="$TMP_DIR/build"
RSCRIPT_PATH="$(command -v Rscript)"
SCRIPT_A="$PROJECT_A/analysis.R"
SCRIPT_B="$PROJECT_B/analysis.R"
SCRIPT_C="$PROJECT_C/analysis.R"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"
export RS_INSTALL_BACKEND=native
export RS_HOME="$TMP_DIR/rs-home"

cd "$ROOT_DIR"

build_local_pkg() {
  rm -rf "$PKG_SRC_DIR" "$BUILD_DIR"
  mkdir -p "$PKG_SRC_DIR/R" "$BUILD_DIR"

  cat >"$PKG_SRC_DIR/DESCRIPTION" <<'EOF'
Package: localpkg
Version: 0.1.0
Title: Shared Store Fixture
Description: Minimal local package used for shared package-store CI.
Authors@R: person("rs", "ci", email = "ci@example.com", role = c("aut", "cre"))
License: MIT
Encoding: UTF-8
LazyData: true
EOF

  cat >"$PKG_SRC_DIR/NAMESPACE" <<'EOF'
export(hello)
EOF

  cat >"$PKG_SRC_DIR/R/hello.R" <<'EOF'
hello <- function() "shared-store"
EOF

  (
    cd "$BUILD_DIR"
    R CMD build "$PKG_SRC_DIR" >/dev/null
  )
}

init_project() {
  local project_dir="$1"
  local script_path="$2"

  mkdir -p "$project_dir/vendor"
  cp "$BUILD_DIR"/localpkg_0.1.0.tar.gz "$project_dir/vendor/localpkg_0.1.0.tar.gz"
  cat >"$script_path" <<'EOF'
cat(localpkg::hello(), "\n")
EOF

  "$RS_BIN" init --rscript "$RSCRIPT_PATH" "$project_dir"
  "$RS_BIN" add --project-dir "$project_dir" --source local --path vendor/localpkg_0.1.0.tar.gz localpkg
}

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

echo "==> building local package fixture"
build_local_pkg

echo "==> initialize two local-cache projects"
init_project "$PROJECT_A" "$SCRIPT_A"
init_project "$PROJECT_B" "$SCRIPT_B"

echo "==> materialize project A and seed the shared package store"
"$RS_BIN" lock "$SCRIPT_A"
"$RS_BIN" run --locked "$SCRIPT_A" | tee "$TMP_DIR/run-a.txt"
grep -q 'shared-store' "$TMP_DIR/run-a.txt"

STORE_ROOT="$RS_HOME/cache/pkgstore"
if [ ! -d "$STORE_ROOT" ]; then
  echo "shared package store root was not created"
  exit 1
fi
STORE_ENTRY="$(find "$STORE_ROOT" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
if [ -z "$STORE_ENTRY" ]; then
  echo "shared package store entry missing after project A run"
  find "$RS_HOME" -maxdepth 4 -type d
  exit 1
fi
test -f "$STORE_ENTRY/.rs-store-state.json"

echo "==> project B should reuse the shared package store despite local .rs-cache"
"$RS_BIN" lock --verbose "$SCRIPT_B" 2>&1 | tee "$TMP_DIR/lock-b.txt"
grep -q 'reused 1 stored package' "$TMP_DIR/lock-b.txt"
"$RS_BIN" run --locked "$SCRIPT_B" | tee "$TMP_DIR/run-b.txt"
grep -q 'shared-store' "$TMP_DIR/run-b.txt"

echo "==> cache surfaces should show both local managed libs and shared store"
"$RS_BIN" cache ls --json "$SCRIPT_B" | tee "$TMP_DIR/cache-ls-b.json"
grep -q "\"shared_package_store_root\": \"$STORE_ROOT\"" "$TMP_DIR/cache-ls-b.json"
grep -q "\"path\": \"$STORE_ENTRY\"" "$TMP_DIR/cache-ls-b.json"

echo "==> a broken matching store entry should warn, fall back to a fresh install, and heal the shared store"
printf '{bad-json}\n' >"$STORE_ENTRY/.rs-store-state.json"
init_project "$PROJECT_C" "$SCRIPT_C"
"$RS_BIN" lock --verbose "$SCRIPT_C" 2>&1 | tee "$TMP_DIR/lock-c.txt"
grep -q 'warning: shared package store lookup failed for localpkg:' "$TMP_DIR/lock-c.txt"
"$RS_BIN" run --locked "$SCRIPT_C" | tee "$TMP_DIR/run-c.txt"
grep -q 'shared-store' "$TMP_DIR/run-c.txt"
grep -q '"package": "localpkg"' "$STORE_ENTRY/.rs-store-state.json"
grep -q '"version": "0.1.0"' "$STORE_ENTRY/.rs-store-state.json"

echo "==> broken shared-store entries should be skipped with warnings"
BROKEN_STORE_ENTRY="$STORE_ROOT/ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
mkdir -p "$BROKEN_STORE_ENTRY/localpkg"
cat >"$BROKEN_STORE_ENTRY/localpkg/DESCRIPTION" <<'EOF'
Package: localpkg
Version: 0.1.0
EOF
printf '{bad-json}\n' >"$BROKEN_STORE_ENTRY/.rs-store-state.json"

"$RS_BIN" cache ls --json "$SCRIPT_B" | tee "$TMP_DIR/cache-ls-b-broken.json"
grep -q "\"warnings\":" "$TMP_DIR/cache-ls-b-broken.json"
grep -q "shared package store entry skipped: $BROKEN_STORE_ENTRY" "$TMP_DIR/cache-ls-b-broken.json"
grep -q "\"path\": \"$STORE_ENTRY\"" "$TMP_DIR/cache-ls-b-broken.json"
if grep -q "\"path\": \"$BROKEN_STORE_ENTRY\"" "$TMP_DIR/cache-ls-b-broken.json"; then
  echo "broken shared package store entry should not be listed as a healthy cache entry"
  exit 1
fi
if grep -q "shared package store entry skipped: $STORE_ENTRY" "$TMP_DIR/cache-ls-b-broken.json"; then
  echo "healed shared package store entry should not be reported as skipped"
  exit 1
fi

"$RS_BIN" prune --dry-run "$SCRIPT_B" | tee "$TMP_DIR/prune-b-broken.txt"
grep -q "\\[warn\\] shared package store entry skipped: $BROKEN_STORE_ENTRY" "$TMP_DIR/prune-b-broken.txt"
if grep -q "\\[warn\\] shared package store entry skipped: $STORE_ENTRY" "$TMP_DIR/prune-b-broken.txt"; then
  echo "healed shared package store entry should not be reported as skipped during prune"
  exit 1
fi
grep -q '\[ok\] prune' "$TMP_DIR/prune-b-broken.txt"

echo "Shared package store E2E passed"
