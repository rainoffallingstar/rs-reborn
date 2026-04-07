#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rvx"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
MISMATCH_PROJECT_DIR="$TMP_DIR/mismatch-project"
MISMATCH_SCRIPT_PATH="$MISMATCH_PROJECT_DIR/analysis.R"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

echo "==> installing R via the native rvx manager"
"$RS_BIN" r install 4.4
"$RS_BIN" r list | tee "$TMP_DIR/r-list.txt"
grep -q '4\.4' "$TMP_DIR/r-list.txt"

mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
cat("native-r-e2e\n")
EOF

"$RS_BIN" init "$PROJECT_DIR"
"$RS_BIN" r use --project-dir "$PROJECT_DIR" 4.4
"$RS_BIN" r which "$PROJECT_DIR" | tee "$TMP_DIR/r-which.txt"
grep -q '4\.4' "$TMP_DIR/r-which.txt"
grep -q 'r_version = "4.4"' "$PROJECT_DIR/rs.toml"
if grep -q '^rscript = ' "$PROJECT_DIR/rs.toml"; then
  echo "expected rvx r use 4.4 to write r_version instead of rscript"
  cat "$PROJECT_DIR/rs.toml"
  exit 1
fi

"$RS_BIN" run "$SCRIPT_PATH" | tee "$TMP_DIR/run.txt"
grep -q 'native-r-e2e' "$TMP_DIR/run.txt"

echo "==> mismatched r_version and rscript should fail clearly"
mkdir -p "$MISMATCH_PROJECT_DIR"
cat >"$MISMATCH_SCRIPT_PATH" <<'EOF'
cat("native-r-mismatch\n")
EOF

cat >"$MISMATCH_PROJECT_DIR/rs.toml" <<EOF
repo = "https://cloud.r-project.org"
cache_dir = ".rs-cache"
lockfile = "rs.lock.json"
rscript = "$("$RS_BIN" r which "$PROJECT_DIR")"
r_version = "9.9"
EOF

if "$RS_BIN" list "$MISMATCH_SCRIPT_PATH" >"$TMP_DIR/mismatch-list.txt" 2>&1; then
  echo "expected mismatched r_version/rscript configuration to fail"
  cat "$TMP_DIR/mismatch-list.txt"
  exit 1
fi
grep -q 'configured r_version "9.9" does not match selected interpreter runtime' "$TMP_DIR/mismatch-list.txt"

echo "native R manager integration E2E passed"
