#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
SANITIZED_PATH="/usr/bin:/bin"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
cat("rig-guidance\n")
EOF

echo "==> run should explain how to install rig when both rig and Rscript are unavailable"
if env PATH="$SANITIZED_PATH" HOME="${HOME:-$TMP_DIR}" TMPDIR="${TMPDIR:-$TMP_DIR}" RS_HOME="$TMP_DIR/rs-home" "$RS_BIN" run "$SCRIPT_PATH" >"$TMP_DIR/run.txt" 2>&1; then
  echo "expected rs run to fail without rig or Rscript"
  cat "$TMP_DIR/run.txt"
  exit 1
fi

grep -q 'rig is required but is not available on PATH' "$TMP_DIR/run.txt"
grep -q 'explicit auto-install: set RS_AUTO_INSTALL_RIG=1 and retry' "$TMP_DIR/run.txt"

case "$(uname -s)" in
  Darwin)
    grep -q 'next step: install rig for your platform from the official releases page and make sure it is available on PATH' "$TMP_DIR/run.txt"
    ;;
  Linux)
    grep -q 'install rig from the official Debian/Ubuntu repository and rerun rs' "$TMP_DIR/run.txt"
    ;;
  *)
    echo "unsupported OS for rig guidance test: $(uname -s)"
    exit 1
    ;;
esac

echo "==> doctor should surface the same next steps"
if env PATH="$SANITIZED_PATH" HOME="${HOME:-$TMP_DIR}" TMPDIR="${TMPDIR:-$TMP_DIR}" RS_HOME="$TMP_DIR/rs-home" "$RS_BIN" doctor "$SCRIPT_PATH" >"$TMP_DIR/doctor.txt" 2>&1; then
  echo "expected rs doctor to report blocking setup issues"
  cat "$TMP_DIR/doctor.txt"
  exit 1
fi

grep -q '\[next\] explicitly allow rs to install rig automatically and rerun: RS_AUTO_INSTALL_RIG=1 rs run ' "$TMP_DIR/doctor.txt"
case "$(uname -s)" in
  Darwin)
    grep -q '\[next\] install rig for your platform from the official releases page and make sure it is available on PATH' "$TMP_DIR/doctor.txt"
    ;;
  Linux)
    grep -q '\[next\] install rig from the official Debian/Ubuntu repository and rerun rs' "$TMP_DIR/doctor.txt"
    ;;
esac

echo "rig guidance E2E passed"
