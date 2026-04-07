#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
RSCRIPT_PATH="$(command -v Rscript)"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

create_project() {
  local project_dir="$1"
  local script_path="$project_dir/analysis.R"

  mkdir -p "$project_dir"
  cat >"$script_path" <<'EOF'
cat("doctor-failure-fixture\n")
EOF

  "$RS_BIN" init --rscript "$RSCRIPT_PATH" "$project_dir"
}

echo "==> doctor failure: missing local source"
LOCAL_PROJECT="$TMP_DIR/local-missing"
create_project "$LOCAL_PROJECT"
"$RS_BIN" add --project-dir "$LOCAL_PROJECT" --source local --path vendor/missing-localpkg.tar.gz localpkg

if "$RS_BIN" doctor --rscript "$RSCRIPT_PATH" "$LOCAL_PROJECT/analysis.R" >"$TMP_DIR/local.txt" 2>&1; then
  echo "expected rvx doctor to fail for missing local source"
  cat "$TMP_DIR/local.txt"
  exit 1
fi
grep -q '\[error\] local source "localpkg" does not exist:' "$TMP_DIR/local.txt"

if "$RS_BIN" doctor --rscript "$RSCRIPT_PATH" --json "$LOCAL_PROJECT/analysis.R" >"$TMP_DIR/local.json" 2>&1; then
  echo "expected rvx doctor --json to fail for missing local source"
  cat "$TMP_DIR/local.json"
  exit 1
fi
grep -q '"status": "error"' "$TMP_DIR/local.json"
grep -q '"source_errors": \[' "$TMP_DIR/local.json"
grep -q '"kind": "missing_local_source"' "$TMP_DIR/local.json"
grep -q '"category": "source"' "$TMP_DIR/local.json"
grep -q '"blocking": true' "$TMP_DIR/local.json"

echo "==> doctor failure: missing git source"
GIT_PROJECT="$TMP_DIR/git-missing"
create_project "$GIT_PROJECT"
"$RS_BIN" add --project-dir "$GIT_PROJECT" --source git --url file:///tmp/rs-missing-git-source gitpkg

if "$RS_BIN" doctor --rscript "$RSCRIPT_PATH" "$GIT_PROJECT/analysis.R" >"$TMP_DIR/git.txt" 2>&1; then
  echo "expected rvx doctor to fail for missing git source"
  cat "$TMP_DIR/git.txt"
  exit 1
fi
grep -q '\[error\] git source "gitpkg" does not exist:' "$TMP_DIR/git.txt"

if "$RS_BIN" doctor --rscript "$RSCRIPT_PATH" --json "$GIT_PROJECT/analysis.R" >"$TMP_DIR/git.json" 2>&1; then
  echo "expected rvx doctor --json to fail for missing git source"
  cat "$TMP_DIR/git.json"
  exit 1
fi
grep -q '"status": "error"' "$TMP_DIR/git.json"
grep -q '"needs_git": true' "$TMP_DIR/git.json"
grep -q '"source_errors": \[' "$TMP_DIR/git.json"
grep -q '"kind": "missing_git_source"' "$TMP_DIR/git.json"
grep -q '"category": "source"' "$TMP_DIR/git.json"
grep -q '"blocking": true' "$TMP_DIR/git.json"

echo "==> doctor failure: missing GitHub token env"
TOKEN_PROJECT="$TMP_DIR/token-missing"
create_project "$TOKEN_PROJECT"
"$RS_BIN" add --project-dir "$TOKEN_PROJECT" --source github --github-repo owner/privatepkg --token-env RS_TEST_GH_TOKEN privpkg

if env -u RS_TEST_GH_TOKEN "$RS_BIN" doctor --rscript "$RSCRIPT_PATH" "$TOKEN_PROJECT/analysis.R" >"$TMP_DIR/token.txt" 2>&1; then
  echo "expected rvx doctor to fail for missing token env"
  cat "$TMP_DIR/token.txt"
  exit 1
fi
grep -q '\[error\] source "privpkg" requires environment variable RS_TEST_GH_TOKEN, but it is not set' "$TMP_DIR/token.txt"

if env -u RS_TEST_GH_TOKEN "$RS_BIN" doctor --rscript "$RSCRIPT_PATH" --json "$TOKEN_PROJECT/analysis.R" >"$TMP_DIR/token.json" 2>&1; then
  echo "expected rvx doctor --json to fail for missing token env"
  cat "$TMP_DIR/token.json"
  exit 1
fi
grep -q '"status": "error"' "$TMP_DIR/token.json"
grep -q '"network_errors": \[' "$TMP_DIR/token.json"
grep -q '"kind": "missing_token_env"' "$TMP_DIR/token.json"
grep -q '"category": "network"' "$TMP_DIR/token.json"
grep -q '"env_var": "RS_TEST_GH_TOKEN"' "$TMP_DIR/token.json"
grep -q '"kind": "set_env_var"' "$TMP_DIR/token.json"
grep -q '"blocking": true' "$TMP_DIR/token.json"

echo "Doctor failure E2E passed"
