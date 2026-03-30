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
cat("native-r-guidance\n")
EOF

echo "==> run should explain how to install R when Rscript is unavailable"
if env PATH="$SANITIZED_PATH" HOME="${HOME:-$TMP_DIR}" TMPDIR="${TMPDIR:-$TMP_DIR}" RS_HOME="$TMP_DIR/rs-home" "$RS_BIN" run "$SCRIPT_PATH" >"$TMP_DIR/run.txt" 2>&1; then
  echo "expected rs run to fail without a managed or external Rscript"
  cat "$TMP_DIR/run.txt"
  exit 1
fi

grep -q 'next step:' "$TMP_DIR/run.txt"
grep -q 'explicit auto-install: set RS_AUTO_INSTALL_R=1 and retry' "$TMP_DIR/run.txt"

case "$(uname -s)" in
  Darwin)
    grep -q 'next step: install a managed R version with rs or set rs.toml rscript manually: rs r install 4.4' "$TMP_DIR/run.txt"
    ;;
  Linux)
    grep -q 'next step: install a managed R version with rs or set rs.toml rscript manually: rs r install 4.4' "$TMP_DIR/run.txt"
    ;;
  *)
    echo "unsupported OS for native guidance test: $(uname -s)"
    exit 1
    ;;
esac

echo "==> doctor should surface the same next steps"
if env PATH="$SANITIZED_PATH" HOME="${HOME:-$TMP_DIR}" TMPDIR="${TMPDIR:-$TMP_DIR}" RS_HOME="$TMP_DIR/rs-home" "$RS_BIN" doctor "$SCRIPT_PATH" >"$TMP_DIR/doctor.txt" 2>&1; then
  echo "expected rs doctor to report blocking setup issues"
  cat "$TMP_DIR/doctor.txt"
  exit 1
fi

grep -q '\[next\] explicitly allow rs to install R automatically and rerun: RS_AUTO_INSTALL_R=1 rs run ' "$TMP_DIR/doctor.txt"
case "$(uname -s)" in
  Darwin)
    grep -q '\[next\] install a managed R version with rs or set rs.toml rscript manually: rs r install 4.4' "$TMP_DIR/doctor.txt"
    ;;
  Linux)
    grep -q '\[next\] install a managed R version with rs or set rs.toml rscript manually: rs r install 4.4' "$TMP_DIR/doctor.txt"
    ;;
esac

echo "native R bootstrap guidance E2E passed"
