#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

echo "==> installing R via rs/rig"
"$RS_BIN" r install 4.4
"$RS_BIN" r list | tee "$TMP_DIR/rig-list.txt"
grep -q '4\.4' "$TMP_DIR/rig-list.txt"

mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
cat("rig-e2e\n")
EOF

"$RS_BIN" init "$PROJECT_DIR"
"$RS_BIN" r use --project-dir "$PROJECT_DIR" 4.4
"$RS_BIN" r which "$PROJECT_DIR" | tee "$TMP_DIR/r-which.txt"
grep -q '4\.4' "$TMP_DIR/r-which.txt"
grep -q 'rscript = ' "$PROJECT_DIR/rs.toml"

"$RS_BIN" run --rscript "$("$RS_BIN" r which "$PROJECT_DIR")" "$SCRIPT_PATH" | tee "$TMP_DIR/run.txt"
grep -q 'rig-e2e' "$TMP_DIR/run.txt"

echo "rig integration E2E passed"
