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
export RS_INSTALL_BACKEND=native

cd "$ROOT_DIR"

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

echo "==> non-mutating example coverage"
"$RS_BIN" scan "$ROOT_DIR/examples/cran-basic/analysis.R" | tee "$TMP_DIR/scan.txt"
grep -q "jsonlite" "$TMP_DIR/scan.txt"

"$RS_BIN" list --json "$ROOT_DIR/examples/multi-script/scripts/report.R" | tee "$TMP_DIR/list-example.json"
grep -q '"script_profile": "scripts/report.R"' "$TMP_DIR/list-example.json"

"$RS_BIN" doctor --summary-only "$ROOT_DIR/examples/bioc-rnaseq/rnaseq.R" | tee "$TMP_DIR/doctor-bioconductor.txt"
grep -q "status=" "$TMP_DIR/doctor-bioconductor.txt"

echo "==> creating smoke project"
mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
args <- commandArgs(trailingOnly = TRUE)
cat(jsonlite::toJSON(list(args = args, lib = .libPaths()[1]), auto_unbox = TRUE), "\n")
EOF

"$RS_BIN" init --rscript "$RSCRIPT_PATH" --from "$SCRIPT_PATH" "$PROJECT_DIR"
grep -q 'rscript = ' "$PROJECT_DIR/rs.toml"

echo "==> dependency planning and diagnostics"
"$RS_BIN" list --json "$SCRIPT_PATH" | tee "$TMP_DIR/list.json"
grep -q '"rscript_path":' "$TMP_DIR/list.json"
grep -q '"cran_packages":' "$TMP_DIR/list.json"

"$RS_BIN" doctor --json "$SCRIPT_PATH" | tee "$TMP_DIR/doctor.json"
grep -q '"rscript_path":' "$TMP_DIR/doctor.json"

echo "==> lock lifecycle"
"$RS_BIN" lock "$SCRIPT_PATH"
test -f "$PROJECT_DIR/rs.lock.json"

"$RS_BIN" check "$SCRIPT_PATH"
"$RS_BIN" check --json "$SCRIPT_PATH" | tee "$TMP_DIR/check.json"
grep -q '"valid": true' "$TMP_DIR/check.json"

echo "==> exec, shell, run, and cache"
"$RS_BIN" exec --frozen -e 'cat(jsonlite::toJSON(list(exec=TRUE), auto_unbox=TRUE), "\n")' "$SCRIPT_PATH" | tee "$TMP_DIR/exec.txt"
grep -q '{"exec":true}' "$TMP_DIR/exec.txt"

printf 'cat(jsonlite::toJSON(list(shell=TRUE), auto_unbox=TRUE), "\\n"); q("no")\n' | \
  "$RS_BIN" shell --frozen "$SCRIPT_PATH" | tee "$TMP_DIR/shell.txt"
grep -q '{"shell":true}' "$TMP_DIR/shell.txt"

"$RS_BIN" run --locked "$SCRIPT_PATH" alpha beta | tee "$TMP_DIR/run-locked.txt"
grep -q '"args":\["alpha","beta"\]' "$TMP_DIR/run-locked.txt"

"$RS_BIN" run --frozen --rscript "$RSCRIPT_PATH" "$SCRIPT_PATH" gamma | tee "$TMP_DIR/run-frozen.txt"
grep -Eq '"args":"gamma"|"args":\["gamma"\]' "$TMP_DIR/run-frozen.txt"

"$RS_BIN" cache dir "$SCRIPT_PATH" | tee "$TMP_DIR/cache-dir.txt"
test -s "$TMP_DIR/cache-dir.txt"

"$RS_BIN" cache ls --json "$SCRIPT_PATH" | tee "$TMP_DIR/cache-ls.json"
grep -q '"active": true' "$TMP_DIR/cache-ls.json"

"$RS_BIN" prune --dry-run "$SCRIPT_PATH" | tee "$TMP_DIR/prune.txt"
grep -q '\[ok\] prune' "$TMP_DIR/prune.txt"

echo "==> project interpreter commands"
"$RS_BIN" r which "$PROJECT_DIR" | tee "$TMP_DIR/r-which-before.txt"
grep -Fq "$RSCRIPT_PATH" "$TMP_DIR/r-which-before.txt"

"$RS_BIN" r use --project-dir "$PROJECT_DIR" "$RSCRIPT_PATH"
"$RS_BIN" r which "$PROJECT_DIR" | tee "$TMP_DIR/r-which-after.txt"
grep -Fq "$RSCRIPT_PATH" "$TMP_DIR/r-which-after.txt"

echo "CLI smoke E2E passed"
