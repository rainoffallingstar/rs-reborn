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
export R_LIBS_USER="$TMP_DIR/r-user-lib"
export RS_INSTALL_BACKEND=pak

cd "$ROOT_DIR"

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

echo "==> installing pak into isolated user library"
mkdir -p "$R_LIBS_USER"
Rscript -e 'options(repos = c(CRAN = "https://cloud.r-project.org"), timeout = max(300, getOption("timeout"))); ok <- FALSE; err <- NULL; for (attempt in 1:3) { cat(sprintf("pak install attempt %d/3\n", attempt)); err <- tryCatch({ install.packages("pak", lib = Sys.getenv("R_LIBS_USER")); NULL }, error = function(e) e); if (is.null(err)) { ok <- TRUE; break }; message(conditionMessage(err)); if (attempt < 3) Sys.sleep(2) }; if (!ok) stop(err)'
Rscript -e 'cat("pak=", requireNamespace("pak", quietly = TRUE), "\n", sep = "")' | tee "$TMP_DIR/pak-check.txt"
grep -q 'pak=TRUE' "$TMP_DIR/pak-check.txt"

mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
cat(jsonlite::toJSON(list(value = "pak-backend"), auto_unbox = TRUE), "\n")
EOF

echo "==> initialize project and force pak backend"
"$RS_BIN" init --rscript "$RSCRIPT_PATH" "$PROJECT_DIR"
"$RS_BIN" add --project-dir "$PROJECT_DIR" jsonlite

echo "==> run through pak backend"
"$RS_BIN" lock "$SCRIPT_PATH" 2>&1 | tee "$TMP_DIR/lock.txt"
grep -Eq 'installing via pak:|installing packages via pak backend' "$TMP_DIR/lock.txt"
if grep -q 'falling back to legacy' "$TMP_DIR/lock.txt"; then
  echo "unexpected legacy fallback while RS_INSTALL_BACKEND=pak"
  cat "$TMP_DIR/lock.txt"
  exit 1
fi

"$RS_BIN" check "$SCRIPT_PATH"
"$RS_BIN" run --locked "$SCRIPT_PATH" 2>&1 | tee "$TMP_DIR/run.txt"
grep -q '{"value":"pak-backend"}' "$TMP_DIR/run.txt"
if grep -q 'falling back to legacy' "$TMP_DIR/run.txt"; then
  echo "unexpected legacy fallback while running with RS_INSTALL_BACKEND=pak"
  cat "$TMP_DIR/run.txt"
  exit 1
fi

echo "Pak backend E2E passed"
