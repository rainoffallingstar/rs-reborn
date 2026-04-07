#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
RS_HOME_DIR="$TMP_DIR/rs-home"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
MANAGED_ROOT="$RS_HOME_DIR/r/versions/4.5.3-$(go env GOOS)-$(go env GOARCH)"
EXTERNAL_ROOT="$TMP_DIR/miniconda3/bin"
MANAGED_RSCRIPT="$MANAGED_ROOT/bin/Rscript"
EXTERNAL_RSCRIPT="$EXTERNAL_ROOT/Rscript"
export RS_HOME="$RS_HOME_DIR"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

mkdir -p "$PROJECT_DIR" "$(dirname "$MANAGED_RSCRIPT")" "$EXTERNAL_ROOT" "$RS_HOME_DIR/r"

cat >"$MANAGED_RSCRIPT" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-e" ]]; then
  expr="${2:-}"
  if [[ "$expr" == *'cat(file.path(R.home("bin"), "R"))'* ]]; then
    printf '%s\n4.5.3' "$(dirname "$0")/R"
    exit 0
  fi
  if [[ "$expr" == *"cat(as.character(getRversion()))"* ]]; then
    printf '4.5.3'
    exit 0
  fi
  cat <<'META'
version	4.5.3
platform	x86_64-pc-linux-gnu
arch	x86_64
os	linux-gnu
pkg_type	source
META
  exit 0
fi
printf 'managed-current-selected\n'
EOF
chmod +x "$MANAGED_RSCRIPT"
cat >"$MANAGED_ROOT/bin/R" <<'EOF'
#!/usr/bin/env bash
printf 'managed-R-binary\n'
EOF
chmod +x "$MANAGED_ROOT/bin/R"

cat >"$EXTERNAL_RSCRIPT" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-e" ]]; then
  expr="${2:-}"
  if [[ "$expr" == *'cat(file.path(R.home("bin"), "R"))'* ]]; then
    printf '%s\n4.4.3' "$(dirname "$0")/R"
    exit 0
  fi
  if [[ "$expr" == *"cat(as.character(getRversion()))"* ]]; then
    printf '4.4.3'
    exit 0
  fi
  cat <<'META'
version	4.4.3
platform	x86_64-conda-linux-gnu
arch	x86_64
os	linux-gnu
pkg_type	source
META
  exit 0
fi
printf 'external-conda-selected\n'
EOF
chmod +x "$EXTERNAL_RSCRIPT"
cat >"$EXTERNAL_ROOT/R" <<'EOF'
#!/usr/bin/env bash
printf 'external-R-binary\n'
EOF
chmod +x "$EXTERNAL_ROOT/R"

printf '%s\n' "$MANAGED_ROOT" >"$RS_HOME_DIR/r/current"

cat >"$SCRIPT_PATH" <<'EOF'
cat("managed-current-default-e2e\n")
EOF

echo "==> initialize project without explicit interpreter"
"$RS_BIN" init "$PROJECT_DIR"

echo "==> managed current should stay preferred over external conda R"
PATH="$EXTERNAL_ROOT:$PATH" "$RS_BIN" r list | tee "$TMP_DIR/r-list.txt"
grep -q 'managed' "$TMP_DIR/r-list.txt"
grep -q 'external' "$TMP_DIR/r-list.txt"
grep -q '4\.5\.3' "$TMP_DIR/r-list.txt"
grep -q '4\.4\.3' "$TMP_DIR/r-list.txt"

PATH="$EXTERNAL_ROOT:$PATH" "$RS_BIN" r which "$PROJECT_DIR" | tee "$TMP_DIR/r-which.txt"
grep -q "$MANAGED_RSCRIPT" "$TMP_DIR/r-which.txt"

PATH="$EXTERNAL_ROOT:$PATH" "$RS_BIN" run "$SCRIPT_PATH" | tee "$TMP_DIR/run.txt"
grep -q 'managed-current-selected' "$TMP_DIR/run.txt"
if grep -q 'external-conda-selected' "$TMP_DIR/run.txt"; then
  echo "expected rvx run to prefer current managed R over external conda R"
  cat "$TMP_DIR/run.txt"
  exit 1
fi

echo "managed current default selection E2E passed"
