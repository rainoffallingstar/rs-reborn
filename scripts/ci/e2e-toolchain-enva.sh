#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
BIN_DIR="$TMP_DIR/bin"
HOME_DIR="$TMP_DIR/home"
ENVA_PREFIX="$HOME_DIR/.local/share/rattler/envs/rs-sysdeps"
MICROMAMBA_PREFIX="$HOME_DIR/micromamba/envs/rs-sysdeps"
ENVA_LOG="$TMP_DIR/enva.log"

mkdir -p "$BIN_DIR" "$HOME_DIR"
export HOME="$HOME_DIR"
export ENVA_LOG
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"
export PATH="$BIN_DIR:$PATH"

cd "$ROOT_DIR"

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

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

echo "==> doctor --bootstrap-toolchain should prefer enva over micromamba and create the toolchain prefix"
"$RS_BIN" doctor --toolchain-only --bootstrap-toolchain --json >"$TMP_DIR/doctor.json" 2>"$TMP_DIR/doctor.stderr"
grep -q '"status": "ok"' "$TMP_DIR/doctor.json"
grep -q '"toolchain_prefixes": \[' "$TMP_DIR/doctor.json"
grep -q "$ENVA_PREFIX" "$TMP_DIR/doctor.json"
grep -q '\[rs\] bootstrapping rootless toolchain preset: enva' "$TMP_DIR/doctor.stderr"
grep -q '^create ' "$ENVA_LOG"
if grep -q 'micromamba' "$TMP_DIR/doctor.stderr"; then
  echo "expected enva bootstrap path without micromamba fallback"
  cat "$TMP_DIR/doctor.stderr"
  exit 1
fi

echo "==> once the enva prefix exists, auto bootstrap plan should point at enva"
"$RS_BIN" toolchain bootstrap auto >"$TMP_DIR/bootstrap.txt"
grep -q '\[bootstrap\] preset: enva (detected complete layout, recommended)' "$TMP_DIR/bootstrap.txt"
grep -q '\[next\] initialize project defaults: rs init --toolchain-preset enva' "$TMP_DIR/bootstrap.txt"
grep -q '\[next\] validate toolchain configuration: rs doctor --toolchain-only' "$TMP_DIR/bootstrap.txt"

echo "==> when both prefixes exist, auto-detect should still recommend enva first"
mkdir -p \
  "$MICROMAMBA_PREFIX" \
  "$MICROMAMBA_PREFIX/lib/pkgconfig" \
  "$MICROMAMBA_PREFIX/share/pkgconfig"
"$RS_BIN" toolchain detect --json >"$TMP_DIR/detect.json"
first_preset="$(awk -F'"' '/"preset": / { print $4; exit }' "$TMP_DIR/detect.json")"
if [ "$first_preset" != "enva" ]; then
  echo "expected first detected preset to be enva, got: $first_preset"
  cat "$TMP_DIR/detect.json"
  exit 1
fi

echo "==> rs init --toolchain-preset auto should write enva defaults"
PROJECT_DIR="$TMP_DIR/project"
"$RS_BIN" init --toolchain-preset auto "$PROJECT_DIR" >"$TMP_DIR/init.txt"
grep -q 'toolchain_prefixes = \["'"$ENVA_PREFIX"'"\]' "$PROJECT_DIR/rs.toml"
grep -q 'pkg_config_path = \["'"$ENVA_PREFIX"'/lib/pkgconfig", "'"$ENVA_PREFIX"'/share/pkgconfig"\]' "$PROJECT_DIR/rs.toml"

echo "toolchain enva-priority E2E passed"
