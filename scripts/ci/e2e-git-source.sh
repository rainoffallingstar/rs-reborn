#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
PROJECT_DIR="$TMP_DIR/project"
REPO_DIR="$TMP_DIR/gitpkg-repo"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
RSCRIPT_PATH="$(command -v Rscript)"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

build_git_pkg_repo() {
  rm -rf "$REPO_DIR"
  mkdir -p "$REPO_DIR/R"

  cat >"$REPO_DIR/DESCRIPTION" <<'EOF'
Package: gitpkg
Version: 0.1.0
Title: Git Source Fixture
Description: Minimal git-backed package used for rs CI.
Authors@R: person("rs", "ci", email = "ci@example.com", role = c("aut", "cre"))
License: MIT
Encoding: UTF-8
LazyData: true
EOF

  cat >"$REPO_DIR/NAMESPACE" <<'EOF'
export(hello)
EOF

  cat >"$REPO_DIR/R/hello.R" <<'EOF'
hello <- function() "hello-main"
EOF

  git init -q "$REPO_DIR"
  (
    cd "$REPO_DIR"
    git config user.name "rs ci"
    git config user.email "ci@example.com"
    git add .
    git commit -qm "main version"
    git branch -M main
    git checkout -b release >/dev/null 2>&1
    cat >R/hello.R <<'EOF'
hello <- function() "hello-release"
EOF
    git add R/hello.R
    git commit -qm "release version"
    git checkout main >/dev/null 2>&1
  )
}

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

echo "==> building git source fixture repository"
build_git_pkg_repo

mkdir -p "$PROJECT_DIR"
cat >"$SCRIPT_PATH" <<'EOF'
cat(gitpkg::hello(), "\n")
EOF

echo "==> configuring project with git source on main"
"$RS_BIN" init --rscript "$RSCRIPT_PATH" "$PROJECT_DIR"
"$RS_BIN" add --project-dir "$PROJECT_DIR" --source git --url "file://$REPO_DIR" --ref main gitpkg

echo "==> lock, check, and run git source"
"$RS_BIN" lock "$SCRIPT_PATH"
"$RS_BIN" check "$SCRIPT_PATH"
"$RS_BIN" run --locked "$SCRIPT_PATH" | tee "$TMP_DIR/run-main.txt"
grep -q 'hello-main' "$TMP_DIR/run-main.txt"

grep -q '"source": "git"' "$PROJECT_DIR/rs.lock.json"
grep -q "\"source_location\": \"file://$REPO_DIR\"" "$PROJECT_DIR/rs.lock.json"
grep -q '"source_ref": "main"' "$PROJECT_DIR/rs.lock.json"
grep -q '"source_commit":' "$PROJECT_DIR/rs.lock.json"

echo "==> change configured ref and expect lock drift"
"$RS_BIN" add --project-dir "$PROJECT_DIR" --source git --url "file://$REPO_DIR" --ref release gitpkg

if "$RS_BIN" check "$SCRIPT_PATH" >"$TMP_DIR/check-release-drift.txt" 2>&1; then
  echo "expected rs check to fail after git source ref drift"
  cat "$TMP_DIR/check-release-drift.txt"
  exit 1
fi
grep -q 'source ref mismatch for gitpkg' "$TMP_DIR/check-release-drift.txt"

if "$RS_BIN" run --locked "$SCRIPT_PATH" >"$TMP_DIR/run-release-drift.txt" 2>&1; then
  echo "expected rs run --locked to fail after git source ref drift"
  cat "$TMP_DIR/run-release-drift.txt"
  exit 1
fi
grep -q 'source ref mismatch for gitpkg' "$TMP_DIR/run-release-drift.txt"

echo "==> relock on release and verify updated output"
"$RS_BIN" lock "$SCRIPT_PATH"
"$RS_BIN" check "$SCRIPT_PATH"
"$RS_BIN" run --locked "$SCRIPT_PATH" | tee "$TMP_DIR/run-release.txt"
grep -q 'hello-release' "$TMP_DIR/run-release.txt"
grep -q '"source_ref": "release"' "$PROJECT_DIR/rs.lock.json"

echo "Git source E2E passed"
