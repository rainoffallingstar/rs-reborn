#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rvx"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
RSCRIPT_PATH="$(command -v Rscript)"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"
export RS_INSTALL_BACKEND=native

cd "$ROOT_DIR"

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
cat(jsonlite::toJSON(list(value = "native-github"), auto_unbox = TRUE), "\n")
EOF

echo "==> configure standard GitHub source"
"$RS_BIN" init --rscript "$RSCRIPT_PATH" "$PROJECT_DIR"
"$RS_BIN" add --project-dir "$PROJECT_DIR" --source github --github-repo jeroen/jsonlite jsonlite

echo "==> lock and run github source with native backend"
"$RS_BIN" lock "$SCRIPT_PATH" 2>&1 | tee "$TMP_DIR/lock.txt"
grep -q '"source": "github"' "$PROJECT_DIR/rs.lock.json"
grep -q '"source_location": "jeroen/jsonlite"' "$PROJECT_DIR/rs.lock.json"
grep -q '"source_commit":' "$PROJECT_DIR/rs.lock.json"
if grep -q 'falling back to legacy' "$TMP_DIR/lock.txt"; then
  echo "unexpected legacy fallback while locking GitHub source"
  exit 1
fi

"$RS_BIN" run --locked "$SCRIPT_PATH" 2>&1 | tee "$TMP_DIR/run.txt"
grep -q '{"value":"native-github"}' "$TMP_DIR/run.txt"
if grep -q 'falling back to legacy' "$TMP_DIR/run.txt"; then
  echo "unexpected legacy fallback while running GitHub source"
  exit 1
fi

echo "Native GitHub backend E2E passed"
