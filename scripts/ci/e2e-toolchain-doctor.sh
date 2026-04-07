#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RS_BIN="$TMP_DIR/rvx"
PROJECT_DIR="$TMP_DIR/project"
BROKEN_DIR="$TMP_DIR/broken-project"
ENV_DIR="$TMP_DIR/env-only"
export GOCACHE="$TMP_DIR/go-build"
export GOMODCACHE="$TMP_DIR/gomodcache"
export RS_HOME="$TMP_DIR/rvx-home"

cd "$ROOT_DIR"

echo "==> building rvx"
go build -o "$RS_BIN" ./cmd/rvx

echo "==> project-config toolchain-only doctor should pass"
mkdir -p "$PROJECT_DIR/.toolchain/bin" "$PROJECT_DIR/pkgconfig"
cat >"$PROJECT_DIR/rs.toml" <<'EOF'
toolchain_prefixes = [".toolchain"]
pkg_config_path = ["pkgconfig"]
EOF
cat >"$PROJECT_DIR/.toolchain/bin/pkg-config" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$PROJECT_DIR/.toolchain/bin/pkg-config"

PATH="$TMP_DIR/empty" "$RS_BIN" doctor --toolchain-only --json "$PROJECT_DIR" >"$TMP_DIR/project-doctor.json"
grep -q '"status": "ok"' "$TMP_DIR/project-doctor.json"
grep -q "\"toolchain_prefixes\": \[" "$TMP_DIR/project-doctor.json"
grep -q "\"toolchain_path\": \[" "$TMP_DIR/project-doctor.json"
grep -q "\"toolchain_cppflags\": \[" "$TMP_DIR/project-doctor.json"
grep -q "\"toolchain_ldflags\": \[" "$TMP_DIR/project-doctor.json"
grep -q "\"toolchain_pkg_config_path\": \[" "$TMP_DIR/project-doctor.json"
grep -q "$PROJECT_DIR/.toolchain" "$TMP_DIR/project-doctor.json"
grep -q "$PROJECT_DIR/pkgconfig" "$TMP_DIR/project-doctor.json"

echo "==> environment-only toolchain-only doctor should fall back to env vars"
mkdir -p "$ENV_DIR/prefix/bin" "$ENV_DIR/pkgconfig"
cat >"$ENV_DIR/prefix/bin/pkg-config" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$ENV_DIR/prefix/bin/pkg-config"

(
  cd "$ENV_DIR"
  RS_TOOLCHAIN_PREFIXES="$ENV_DIR/prefix" \
  RS_PKG_CONFIG_PATH="$ENV_DIR/pkgconfig" \
  PATH="$TMP_DIR/empty" \
  "$RS_BIN" doctor --toolchain-only --json >"$TMP_DIR/env-doctor.json"
)
grep -q '"status": "ok"' "$TMP_DIR/env-doctor.json"
grep -q "$ENV_DIR/prefix" "$TMP_DIR/env-doctor.json"
grep -q "$ENV_DIR/pkgconfig" "$TMP_DIR/env-doctor.json"

echo "==> broken toolchain config should fail clearly"
mkdir -p "$BROKEN_DIR"
cat >"$BROKEN_DIR/rs.toml" <<'EOF'
toolchain_prefixes = ["missing-prefix"]
pkg_config_path = ["missing-pkgconfig"]
EOF
if "$RS_BIN" doctor --toolchain-only "$BROKEN_DIR" >"$TMP_DIR/broken.txt" 2>&1; then
  echo "expected broken toolchain config to fail"
  cat "$TMP_DIR/broken.txt"
  exit 1
fi
grep -q '\[error\] toolchain prefix does not exist:' "$TMP_DIR/broken.txt"
grep -q '\[error\] pkg-config path does not exist:' "$TMP_DIR/broken.txt"
grep -q '\[next\] fix toolchain_prefixes/pkg_config_path' "$TMP_DIR/broken.txt"
grep -q '\[summary\] status=error' "$TMP_DIR/broken.txt"

echo "toolchain-only doctor E2E passed"
