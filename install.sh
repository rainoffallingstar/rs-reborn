#!/usr/bin/env bash
set -euo pipefail

REPO_OWNER="rainoffallingstar"
REPO_NAME="rs-reborn"
BIN_DIR="${RS_INSTALL_DIR:-$HOME/.cargo/bin}"
BIN_NAME="rs"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

detect_os() {
  case "$(uname -s)" in
    Linux)
      echo "linux"
      ;;
    Darwin)
      echo "darwin"
      ;;
    *)
      echo "unsupported operating system: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)
      echo "amd64"
      ;;
    arm64|aarch64)
      echo "arm64"
      ;;
    *)
      echo "unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

github_auth_header() {
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    printf 'Authorization: Bearer %s' "$GITHUB_TOKEN"
    return
  fi
  if [ -n "${GH_TOKEN:-}" ]; then
    printf 'Authorization: Bearer %s' "$GH_TOKEN"
    return
  fi
  if [ -n "${GITHUB_PAT:-}" ]; then
    printf 'Authorization: Bearer %s' "$GITHUB_PAT"
    return
  fi
  return 1
}

latest_tag() {
  local api_url json auth_header
  api_url="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest"
  if auth_header="$(github_auth_header)"; then
    json="$(curl -fsSL -H "$auth_header" -H "Accept: application/vnd.github+json" "$api_url")"
  else
    json="$(curl -fsSL -H "Accept: application/vnd.github+json" "$api_url")"
  fi
  printf '%s' "$json" | tr -d '\n' | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'
}

download_release() {
  local url output auth_header
  url="$1"
  output="$2"
  if auth_header="$(github_auth_header)"; then
    curl -fsSL -H "$auth_header" "$url" -o "$output"
  else
    curl -fsSL "$url" -o "$output"
  fi
}

need_cmd curl
need_cmd tar
need_cmd mktemp
need_cmd install

OS="$(detect_os)"
ARCH="$(detect_arch)"
TAG="${RS_INSTALL_TAG:-$(latest_tag)}"

if [ -z "$TAG" ]; then
  echo "failed to determine latest release tag" >&2
  exit 1
fi

ASSET="rs_${TAG}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${TAG}/${ASSET}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

ARCHIVE_PATH="$TMP_DIR/$ASSET"
EXTRACT_DIR="$TMP_DIR/extract"
mkdir -p "$EXTRACT_DIR"

echo "==> downloading $URL"
download_release "$URL" "$ARCHIVE_PATH"

echo "==> extracting $ASSET"
tar -xzf "$ARCHIVE_PATH" -C "$EXTRACT_DIR"

if [ ! -f "$EXTRACT_DIR/$BIN_NAME" ]; then
  echo "downloaded archive did not contain $BIN_NAME" >&2
  exit 1
fi

mkdir -p "$BIN_DIR"
install -m 0755 "$EXTRACT_DIR/$BIN_NAME" "$BIN_DIR/$BIN_NAME"

echo "installed $BIN_NAME $TAG to $BIN_DIR/$BIN_NAME"
case ":$PATH:" in
  *":$BIN_DIR:"*)
    ;;
  *)
    echo "note: $BIN_DIR is not currently on PATH" >&2
    ;;
esac
