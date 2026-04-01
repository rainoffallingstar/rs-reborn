#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <output-dir> <tag>" >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_DIR="$1"
TAG="$2"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd)"

platforms=(
  "linux amd64 tar.gz"
  "linux arm64 tar.gz"
  "darwin amd64 tar.gz"
  "darwin arm64 tar.gz"
  "windows amd64 zip"
  "windows arm64 zip"
)

if [ "${RS_RELEASE_ONLY_HOST:-}" = "1" ]; then
  case "$(uname -s)" in
    Linux)
      host_os="linux"
      ;;
    Darwin)
      host_os="darwin"
      ;;
    *)
      echo "unsupported host operating system: $(uname -s)" >&2
      exit 1
      ;;
  esac

  case "$(uname -m)" in
    x86_64|amd64)
      host_arch="amd64"
      ;;
    arm64|aarch64)
      host_arch="arm64"
      ;;
    *)
      echo "unsupported host architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac

  platforms=("$host_os $host_arch tar.gz")
fi

cd "$ROOT_DIR"

commit="unknown"
if command -v git >/dev/null 2>&1; then
  commit="$(git rev-parse HEAD 2>/dev/null || printf 'unknown')"
fi
build_date="${RS_BUILD_DATE:-$(date -u +%FT%TZ)}"
version_ldflags="-X github.com/rainoffallingstar/rs-reborn/internal/cli.cliVersion=$TAG -X github.com/rainoffallingstar/rs-reborn/internal/cli.cliCommit=$commit -X github.com/rainoffallingstar/rs-reborn/internal/cli.cliBuildDate=$build_date"

for target in "${platforms[@]}"; do
  read -r goos goarch archive <<<"$target"

  artifact_base="rs_${TAG}_${goos}_${goarch}"
  staging_dir="$TMP_DIR/$artifact_base"
  binary_name="rs"
  if [ "$goos" = "windows" ]; then
    binary_name="rs.exe"
  fi

  mkdir -p "$staging_dir"
  echo "==> building $artifact_base"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w ${version_ldflags}" -o "$staging_dir/$binary_name" ./cmd/rs

  case "$archive" in
    tar.gz)
      tar -C "$staging_dir" -czf "$OUTPUT_DIR/$artifact_base.tar.gz" "$binary_name"
      ;;
    zip)
      (
        cd "$staging_dir"
        zip -q "$OUTPUT_DIR/$artifact_base.zip" "$binary_name"
      )
      ;;
    *)
      echo "unsupported archive format: $archive" >&2
      exit 1
      ;;
  esac
done

echo "==> writing checksums"
(
  cd "$OUTPUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum rs_* > SHA256SUMS
  else
    shasum -a 256 rs_* > SHA256SUMS
  fi
)

echo "release artifacts written to $OUTPUT_DIR"
