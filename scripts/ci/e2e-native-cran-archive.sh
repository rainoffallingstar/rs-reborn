#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
RSCRIPT_PATH="$(command -v Rscript)"
REPO_ROOT="$TMP_DIR/repo"
PKG_WORK="$TMP_DIR/pkg-work"
PKG_BUILD="$TMP_DIR/pkg-build"
SERVER_LOG="$TMP_DIR/repo-server.log"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"
export RS_INSTALL_BACKEND=native

build_pkg() {
  local name="$1"
  local version="$2"
  local imports="$3"
  local body="$4"

  local src_dir="$PKG_WORK/$name"
  rm -rf "$src_dir"
  mkdir -p "$src_dir/R"

  cat >"$src_dir/DESCRIPTION" <<EOF
Package: $name
Version: $version
Title: Native Archive Fixture
Description: Test fixture for native CRAN archive fallback.
Authors@R: person("rs", "ci", email = "ci@example.com", role = c("aut", "cre"))
License: MIT
Encoding: UTF-8
LazyData: true
Imports: $imports
EOF

  cat >"$src_dir/NAMESPACE" <<EOF
export(hello)
EOF

  cat >"$src_dir/R/hello.R" <<EOF
hello <- function() {
$body
}
EOF

  (
    cd "$PKG_BUILD"
    R CMD build "$src_dir" >/dev/null
  )
}

cd "$ROOT_DIR"

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

mkdir -p "$PROJECT_DIR" "$PKG_WORK" "$PKG_BUILD" "$REPO_ROOT/src/contrib/Archive/pkgA"

echo "==> building fixture packages"
build_pkg "pkgA" "1.0.0" "" '  "pkgA-archive-1.0.0"'
build_pkg "pkgA" "2.0.0" "" '  "pkgA-main-2.0.0"'
build_pkg "pkgB" "1.0.0" "pkgA (< 2.0.0)" '  pkgA::hello()'

cp "$PKG_BUILD/pkgA_2.0.0.tar.gz" "$REPO_ROOT/src/contrib/"
cp "$PKG_BUILD/pkgB_1.0.0.tar.gz" "$REPO_ROOT/src/contrib/"
cp "$PKG_BUILD/pkgA_1.0.0.tar.gz" "$REPO_ROOT/src/contrib/Archive/pkgA/"

echo "==> writing fake CRAN index"
Rscript -e 'tools::write_PACKAGES(commandArgs(TRUE)[1], type = "source")' "$REPO_ROOT/src/contrib"

PORT="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
(
  cd "$REPO_ROOT"
  python3 -m http.server "$PORT" >"$SERVER_LOG" 2>&1
) &
SERVER_PID=$!
trap 'kill "$SERVER_PID" >/dev/null 2>&1 || true; wait "$SERVER_PID" 2>/dev/null || true; rm -rf "$TMP_DIR"' EXIT

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:$PORT/src/contrib/PACKAGES" >/dev/null; then
    break
  fi
  sleep 0.2
done

if ! curl -fsS "http://127.0.0.1:$PORT/src/contrib/PACKAGES" >/dev/null; then
  echo "failed to start fake CRAN server"
  cat "$SERVER_LOG"
  exit 1
fi

cat >"$SCRIPT_PATH" <<'EOF'
cat(pkgB::hello(), "\n")
EOF

echo "==> initialize project with fake CRAN repo"
"$RS_BIN" init --rscript "$RSCRIPT_PATH" "$PROJECT_DIR"
python3 - "$PROJECT_DIR/rs.toml" "$PORT" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
port = sys.argv[2]
text = path.read_text()
text = text.replace('repo = "https://cloud.r-project.org"', f'repo = "http://127.0.0.1:{port}"')
path.write_text(text)
PY
"$RS_BIN" add --project-dir "$PROJECT_DIR" pkgB

echo "==> lock through native backend with archive fallback"
"$RS_BIN" lock "$SCRIPT_PATH" 2>&1 | tee "$TMP_DIR/lock.txt"
python3 - "$PROJECT_DIR/rs.lock.json" <<'PY'
import json
from pathlib import Path
import sys

with open(sys.argv[1]) as fh:
    data = json.load(fh)

packages = {pkg["name"]: pkg for pkg in data["packages"]}
if packages["pkgB"]["version"] != "1.0.0":
    raise SystemExit(f'unexpected pkgB version: {packages["pkgB"]["version"]}')

library = Path(data["library"])
desc = (library / "pkgA" / "DESCRIPTION").read_text()
if "Version: 1.0.0" not in desc:
    raise SystemExit("installed pkgA is not the archive version")
PY
if grep -q 'falling back to legacy' "$TMP_DIR/lock.txt"; then
  echo "unexpected legacy fallback while resolving archive package"
  cat "$TMP_DIR/lock.txt"
  exit 1
fi

echo "==> run locked project and verify archive version was installed"
"$RS_BIN" run --locked "$SCRIPT_PATH" 2>&1 | tee "$TMP_DIR/run.txt"
grep -q 'pkgA-archive-1.0.0' "$TMP_DIR/run.txt"
if grep -q 'falling back to legacy' "$TMP_DIR/run.txt"; then
  echo "unexpected legacy fallback while running archive package"
  cat "$TMP_DIR/run.txt"
  exit 1
fi

echo "Native CRAN archive E2E passed"
