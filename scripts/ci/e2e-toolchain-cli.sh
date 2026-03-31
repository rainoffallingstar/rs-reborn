#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rs"
HOME_DIR="$TMP_DIR/home"
HOMEBREW_PREFIX="$HOME_DIR/homebrew"
ENVA_PREFIX="$HOME_DIR/.local/share/rattler/envs/rs-sysdeps"
MAMBA_PREFIX="$HOME_DIR/.local/share/mamba/envs/rs-sysdeps"
CONDA_PREFIX="$HOME_DIR/.conda/envs/rs-sysdeps"
export HOME="$HOME_DIR"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"

cd "$ROOT_DIR"

echo "==> building rs"
go build -o "$RS_BIN" ./cmd/rs

echo "==> preparing a detected homebrew-style toolchain layout"
mkdir -p \
  "$HOMEBREW_PREFIX" \
  "$HOMEBREW_PREFIX/lib/pkgconfig" \
  "$HOMEBREW_PREFIX/share/pkgconfig"

echo "==> toolchain detect should report a complete homebrew candidate"
"$RS_BIN" toolchain detect >"$TMP_DIR/detect.txt"
grep -q '\[detect\] homebrew (complete, recommended)' "$TMP_DIR/detect.txt"
grep -q '\[next\] prepare user-local prefix: "'"$HOMEBREW_PREFIX"'/bin/brew" install pkg-config gcc' "$TMP_DIR/detect.txt"
grep -q '\[next\] preview template: rs toolchain template homebrew --check' "$TMP_DIR/detect.txt"
grep -q '\[next\] initialize project defaults: rs init --toolchain-preset homebrew' "$TMP_DIR/detect.txt"

echo "==> toolchain detect --json should expose structured candidates"
"$RS_BIN" toolchain detect --json >"$TMP_DIR/detect.json"
grep -q '"preset": "homebrew"' "$TMP_DIR/detect.json"
grep -q '"complete": true' "$TMP_DIR/detect.json"
grep -q '"recommended": true' "$TMP_DIR/detect.json"
grep -q '"suggested_init_command": "rs init --toolchain-preset homebrew"' "$TMP_DIR/detect.json"
grep -q '"suggested_setup_command": ' "$TMP_DIR/detect.json"
grep -q 'install pkg-config gcc' "$TMP_DIR/detect.json"
grep -q "$HOMEBREW_PREFIX" "$TMP_DIR/detect.json"

echo "==> toolchain bootstrap should print a one-shot setup plan"
"$RS_BIN" toolchain bootstrap auto >"$TMP_DIR/bootstrap.txt"
grep -q '\[bootstrap\] preset: homebrew (detected complete layout, recommended)' "$TMP_DIR/bootstrap.txt"
grep -q '\[next\] initialize project defaults: rs init --toolchain-preset homebrew' "$TMP_DIR/bootstrap.txt"
grep -q '\[next\] validate toolchain configuration: rs doctor --toolchain-only' "$TMP_DIR/bootstrap.txt"

echo "==> toolchain bootstrap --json should expose the same plan structurally"
"$RS_BIN" toolchain bootstrap auto --json >"$TMP_DIR/bootstrap.json"
grep -q '"preset": "homebrew"' "$TMP_DIR/bootstrap.json"
grep -q '"init_command": "rs init --toolchain-preset homebrew"' "$TMP_DIR/bootstrap.json"
grep -q '"doctor_command": "rs doctor --toolchain-only"' "$TMP_DIR/bootstrap.json"

echo "==> toolchain template should print toml and env variants"
"$RS_BIN" toolchain template homebrew >"$TMP_DIR/template.toml"
grep -q 'toolchain_prefixes = \["'"$HOMEBREW_PREFIX"'"\]' "$TMP_DIR/template.toml"
grep -q 'pkg_config_path = \["'"$HOMEBREW_PREFIX"'/lib/pkgconfig", "'"$HOMEBREW_PREFIX"'/share/pkgconfig"\]' "$TMP_DIR/template.toml"

"$RS_BIN" toolchain template auto >"$TMP_DIR/template-auto.toml"
grep -q 'toolchain_prefixes = \["'"$HOMEBREW_PREFIX"'"\]' "$TMP_DIR/template-auto.toml"

"$RS_BIN" toolchain template homebrew --format env >"$TMP_DIR/template.env"
grep -q "export RS_TOOLCHAIN_PREFIXES='$HOMEBREW_PREFIX'" "$TMP_DIR/template.env"
grep -q "export RS_PKG_CONFIG_PATH='$HOMEBREW_PREFIX/lib/pkgconfig:$HOMEBREW_PREFIX/share/pkgconfig'" "$TMP_DIR/template.env"

"$RS_BIN" toolchain template enva >"$TMP_DIR/template-enva.toml"
grep -q 'toolchain_prefixes = \["'"$ENVA_PREFIX"'"\]' "$TMP_DIR/template-enva.toml"
grep -q 'pkg_config_path = \["'"$ENVA_PREFIX"'/lib/pkgconfig", "'"$ENVA_PREFIX"'/share/pkgconfig"\]' "$TMP_DIR/template-enva.toml"

"$RS_BIN" toolchain template mamba >"$TMP_DIR/template-mamba.toml"
grep -q 'toolchain_prefixes = \["'"$MAMBA_PREFIX"'"\]' "$TMP_DIR/template-mamba.toml"
grep -q 'pkg_config_path = \["'"$MAMBA_PREFIX"'/lib/pkgconfig", "'"$MAMBA_PREFIX"'/share/pkgconfig"\]' "$TMP_DIR/template-mamba.toml"

"$RS_BIN" toolchain template conda >"$TMP_DIR/template-conda.toml"
grep -q 'toolchain_prefixes = \["'"$CONDA_PREFIX"'"\]' "$TMP_DIR/template-conda.toml"
grep -q 'pkg_config_path = \["'"$CONDA_PREFIX"'/lib/pkgconfig", "'"$CONDA_PREFIX"'/share/pkgconfig"\]' "$TMP_DIR/template-conda.toml"

echo "==> rs init should be able to use the auto-detected preset directly"
PROJECT_DIR="$TMP_DIR/project"
"$RS_BIN" init --toolchain-preset auto "$PROJECT_DIR" >"$TMP_DIR/init-auto.txt"
grep -q "^wrote " "$TMP_DIR/init-auto.txt"
grep -q 'toolchain_prefixes = \["'"$HOMEBREW_PREFIX"'"\]' "$PROJECT_DIR/rs.toml"
grep -q 'pkg_config_path = \["'"$HOMEBREW_PREFIX"'/lib/pkgconfig", "'"$HOMEBREW_PREFIX"'/share/pkgconfig"\]' "$PROJECT_DIR/rs.toml"

echo "==> toolchain template --check should pass for detected homebrew paths"
"$RS_BIN" toolchain template homebrew --check >"$TMP_DIR/check-ok.txt"
grep -q '\[ok\] all preset toolchain paths exist on this machine' "$TMP_DIR/check-ok.txt"

echo "==> toolchain template --check should fail clearly for missing micromamba paths"
if "$RS_BIN" toolchain template micromamba --check >"$TMP_DIR/check-missing.txt" 2>&1; then
  echo "expected missing micromamba preset check to fail"
  cat "$TMP_DIR/check-missing.txt"
  exit 1
fi
grep -q '\[check\] toolchain prefix missing:' "$TMP_DIR/check-missing.txt"
grep -q '\[summary\] preset paths are missing on this machine' "$TMP_DIR/check-missing.txt"

echo "==> toolchain template --check should also fail clearly for missing mamba and conda paths"
if "$RS_BIN" toolchain template mamba --check >"$TMP_DIR/check-mamba-missing.txt" 2>&1; then
  echo "expected missing mamba preset check to fail"
  cat "$TMP_DIR/check-mamba-missing.txt"
  exit 1
fi
grep -q '\[check\] toolchain prefix missing:' "$TMP_DIR/check-mamba-missing.txt"
grep -q "$MAMBA_PREFIX" "$TMP_DIR/check-mamba-missing.txt"

if "$RS_BIN" toolchain template conda --check >"$TMP_DIR/check-conda-missing.txt" 2>&1; then
  echo "expected missing conda preset check to fail"
  cat "$TMP_DIR/check-conda-missing.txt"
  exit 1
fi
grep -q '\[check\] toolchain prefix missing:' "$TMP_DIR/check-conda-missing.txt"
grep -q "$CONDA_PREFIX" "$TMP_DIR/check-conda-missing.txt"

echo "toolchain CLI E2E passed"
