#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
RS_HOME_DIR="$TMP_DIR/rs-home"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
export RS_HOME="$RS_HOME_DIR"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

echo "==> installing R from source via the native rs manager"
"$RS_BIN" r install --method source 4.4
"$RS_BIN" r list | tee "$TMP_DIR/r-list.txt"
grep -q 'managed' "$TMP_DIR/r-list.txt"
grep -q '4\.4' "$TMP_DIR/r-list.txt"

mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
cat("native-r-source-e2e\n")
EOF

"$RS_BIN" init "$PROJECT_DIR"
"$RS_BIN" r use --project-dir "$PROJECT_DIR" 4.4
"$RS_BIN" r which "$PROJECT_DIR" | tee "$TMP_DIR/r-which.txt"
grep -q "$RS_HOME_DIR/r/versions/" "$TMP_DIR/r-which.txt"
grep -q '4\.4' "$TMP_DIR/r-which.txt"

"$RS_BIN" run "$SCRIPT_PATH" | tee "$TMP_DIR/run.txt"
grep -q 'native-r-source-e2e' "$TMP_DIR/run.txt"

echo "native R manager source-install E2E passed"
