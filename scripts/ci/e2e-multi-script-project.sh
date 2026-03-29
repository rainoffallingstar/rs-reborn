#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
PROJECT_DIR="$TMP_DIR/project"
PKG_SRC_DIR="$TMP_DIR/pkgsrc"
BUILD_DIR="$TMP_DIR/build"
SCRIPT_A="$PROJECT_DIR/scripts/a.R"
SCRIPT_B="$PROJECT_DIR/scripts/b.R"
RSCRIPT_PATH="$(command -v Rscript)"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"
export RS_INSTALL_BACKEND=native

cd "$ROOT_DIR"

build_local_pkg() {
  local package_name="$1"
  local greeting="$2"

  rm -rf "$PKG_SRC_DIR" "$BUILD_DIR"
  mkdir -p "$PKG_SRC_DIR/R" "$BUILD_DIR"

  cat >"$PKG_SRC_DIR/DESCRIPTION" <<EOF
Package: $package_name
Version: 0.1.0
Title: Multi Script Fixture
Description: Minimal local package used for multi-script rs CI.
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

mkdir -p "$PROJECT_DIR/scripts" "$PROJECT_DIR/vendor"
cat >"$SCRIPT_A" <<'EOF'
cat(pkga::hello(), "\n")
EOF
cat >"$SCRIPT_B" <<'EOF'
cat(pkgb::hello(), "\n")
EOF

echo "==> building local package fixtures"
build_local_pkg "pkga" "profile-a"
cp "$BUILD_DIR"/pkga_0.1.0.tar.gz "$PROJECT_DIR/vendor/pkga_0.1.0.tar.gz"
build_local_pkg "pkgb" "profile-b"
cp "$BUILD_DIR"/pkgb_0.1.0.tar.gz "$PROJECT_DIR/vendor/pkgb_0.1.0.tar.gz"

echo "==> configuring project-specific script profiles"
"$RS_BIN" init --rscript "$RSCRIPT_PATH" "$PROJECT_DIR"
"$RS_BIN" add --project-dir "$PROJECT_DIR" --script scripts/a.R --source local --path vendor/pkga_0.1.0.tar.gz pkga
"$RS_BIN" add --project-dir "$PROJECT_DIR" --script scripts/b.R --source local --path vendor/pkgb_0.1.0.tar.gz pkgb

echo "==> list a.R profile"
"$RS_BIN" list --json "$SCRIPT_A" | tee "$TMP_DIR/list-a.json"
grep -q '"script_profile": "scripts/a.R"' "$TMP_DIR/list-a.json"
grep -q '"package": "pkga"' "$TMP_DIR/list-a.json"
if grep -q '"package": "pkgb"' "$TMP_DIR/list-a.json"; then
  echo "unexpected pkgb source in a.R profile"
  cat "$TMP_DIR/list-a.json"
  exit 1
fi

echo "==> list b.R profile"
"$RS_BIN" list --json "$SCRIPT_B" | tee "$TMP_DIR/list-b.json"
grep -q '"script_profile": "scripts/b.R"' "$TMP_DIR/list-b.json"
grep -q '"package": "pkgb"' "$TMP_DIR/list-b.json"
if grep -q '"package": "pkga"' "$TMP_DIR/list-b.json"; then
  echo "unexpected pkga source in b.R profile"
  cat "$TMP_DIR/list-b.json"
  exit 1
fi

echo "==> lock, check, and run a.R"
"$RS_BIN" lock "$SCRIPT_A"
"$RS_BIN" check "$SCRIPT_A"
"$RS_BIN" run --locked "$SCRIPT_A" | tee "$TMP_DIR/run-a.txt"
grep -q 'profile-a' "$TMP_DIR/run-a.txt"

echo "==> lock, check, and run b.R"
"$RS_BIN" lock "$SCRIPT_B"
"$RS_BIN" check "$SCRIPT_B"
"$RS_BIN" run --locked "$SCRIPT_B" | tee "$TMP_DIR/run-b.txt"
grep -q 'profile-b' "$TMP_DIR/run-b.txt"

echo "==> relock a.R and verify profile isolation remains intact"
"$RS_BIN" lock "$SCRIPT_A"
"$RS_BIN" check "$SCRIPT_A"
"$RS_BIN" run --locked "$SCRIPT_A" | tee "$TMP_DIR/run-a-again.txt"
grep -q 'profile-a' "$TMP_DIR/run-a-again.txt"

echo "Multi-script project E2E passed"
