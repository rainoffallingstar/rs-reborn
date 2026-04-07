#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rvx"
RS_HOME_DIR="$TMP_DIR/rvx-home"
PROJECT_DIR="$TMP_DIR/project"
SCRIPT_PATH="$PROJECT_DIR/analysis.R"
BIN_DIR="$TMP_DIR/bin"
ENVA_LOG="$TMP_DIR/enva.log"
MANAGED_ROOT="$RS_HOME_DIR/r/versions/4.5.3-$(go env GOOS)-$(go env GOARCH)"
MANAGED_RSCRIPT="$MANAGED_ROOT/bin/Rscript"

export HOME="$TMP_DIR/home"
export RS_HOME="$RS_HOME_DIR"
export ENVA_LOG
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

mkdir -p "$HOME" "$BIN_DIR" "$PROJECT_DIR" "$RS_HOME_DIR/r" "$(dirname "$MANAGED_RSCRIPT")"
export PATH="$BIN_DIR:$PATH"

cd "$ROOT_DIR"

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

echo "==> preparing fake managed R"
cat >"$MANAGED_RSCRIPT" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-e" ]]; then
  expr="${2:-}"
  if [[ "$expr" == *'cat(file.path(R.home("bin"), "R"))'* ]] || [[ "$expr" == *'cat(file.path(R.home("bin"), "R.exe"))'* ]]; then
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
printf 'runtime-selected=managed\n'
printf 'toolchain-prefixes=%s\n' "${RS_TOOLCHAIN_PREFIXES:-}"
printf 'toolchain-pkg-config=%s\n' "${RS_PKG_CONFIG_PATH:-}"
printf 'toolchain-cppflags=%s\n' "${CPPFLAGS:-}"
printf 'toolchain-ldflags=%s\n' "${LDFLAGS:-}"
printf 'pkg-config-path=%s\n' "$(command -v pkg-config || true)"
printf 'script=%s\n' "${1:-}"
EOF
chmod +x "$MANAGED_RSCRIPT"
cat >"$MANAGED_ROOT/bin/R" <<'EOF'
#!/usr/bin/env bash
printf 'managed-R-binary\n'
EOF
chmod +x "$MANAGED_ROOT/bin/R"
printf '%s\n' "$MANAGED_ROOT" >"$RS_HOME_DIR/r/current"

echo "==> preparing fake enva and micromamba executables"
cat >"$BIN_DIR/enva" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$ENVA_LOG"
if [ "${1:-}" != "create" ]; then
  echo "unexpected enva command: $*" >&2
  exit 1
fi
prefix="$HOME/.local/share/rattler/envs/rs-sysdeps"
mkdir -p "$prefix/bin" "$prefix/lib/pkgconfig" "$prefix/share/pkgconfig"
cat >"$prefix/bin/pkg-config" <<'PKG'
#!/bin/sh
exit 0
PKG
chmod +x "$prefix/bin/pkg-config"
EOF
chmod +x "$BIN_DIR/enva"

cat >"$BIN_DIR/micromamba" <<'EOF'
#!/bin/sh
echo "micromamba should not have been called: $*" >&2
exit 97
EOF
chmod +x "$BIN_DIR/micromamba"

cat >"$SCRIPT_PATH" <<'EOF'
cat("toolchain-enva-run-e2e\n")
EOF

echo "==> initialize project without explicit toolchain config"
"$RS_BIN" init "$PROJECT_DIR" >"$TMP_DIR/init.txt"

echo "==> rvx run --bootstrap-toolchain should invoke enva and inject the new prefix into runtime env"
"$RS_BIN" run --bootstrap-toolchain "$SCRIPT_PATH" >"$TMP_DIR/run.out" 2>"$TMP_DIR/run.err"
grep -q 'runtime-selected=managed' "$TMP_DIR/run.out"
grep -q "toolchain-prefixes=$HOME/.local/share/rattler/envs/rs-sysdeps" "$TMP_DIR/run.out"
grep -q "toolchain-pkg-config=$HOME/.local/share/rattler/envs/rs-sysdeps/lib/pkgconfig:$HOME/.local/share/rattler/envs/rs-sysdeps/share/pkgconfig" "$TMP_DIR/run.out"
grep -q "toolchain-cppflags=-I$HOME/.local/share/rattler/envs/rs-sysdeps/include" "$TMP_DIR/run.out"
grep -q "toolchain-ldflags=-L$HOME/.local/share/rattler/envs/rs-sysdeps/lib" "$TMP_DIR/run.out"
grep -q "pkg-config-path=$HOME/.local/share/rattler/envs/rs-sysdeps/bin/pkg-config" "$TMP_DIR/run.out"
grep -q '^create ' "$ENVA_LOG"
grep -q '\[rvx\] bootstrapping rootless toolchain preset: enva' "$TMP_DIR/run.err"
if grep -q 'micromamba' "$TMP_DIR/run.err"; then
  echo "expected enva runtime bootstrap path without micromamba fallback"
  cat "$TMP_DIR/run.err"
  exit 1
fi

echo "toolchain enva runtime E2E passed"
