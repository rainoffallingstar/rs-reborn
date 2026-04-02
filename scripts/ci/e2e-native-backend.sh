#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
RSCRIPT_PATH="$(command -v Rscript)"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"
export RS_INSTALL_BACKEND=auto

cd "$ROOT_DIR"

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
cat(jsonlite::toJSON(list(value = "native-backend"), auto_unbox = TRUE), "\n")
EOF

echo "==> initialize project and verify auto backend resolves through native installer"
"$RS_BIN" init --rscript "$RSCRIPT_PATH" "$PROJECT_DIR"
"$RS_BIN" add --project-dir "$PROJECT_DIR" jsonlite

echo "==> lock through auto/native backend"
"$RS_BIN" lock "$SCRIPT_PATH" 2>&1 | tee "$TMP_DIR/lock.txt"
grep -q 'native package install completed' "$TMP_DIR/lock.txt"
if grep -q 'falling back to legacy' "$TMP_DIR/lock.txt"; then
  echo "unexpected legacy fallback while RS_INSTALL_BACKEND=auto"
  exit 1
fi

echo "==> run through auto/native backend"
"$RS_BIN" run --locked "$SCRIPT_PATH" 2>&1 | tee "$TMP_DIR/run.txt"
grep -q '{"value":"native-backend"}' "$TMP_DIR/run.txt"
if grep -q 'falling back to legacy' "$TMP_DIR/run.txt"; then
  echo "unexpected legacy fallback while RS_INSTALL_BACKEND=auto"
  exit 1
fi

echo "Auto/native backend E2E passed"
