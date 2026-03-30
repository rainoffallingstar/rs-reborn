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

cd "$ROOT_DIR"

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
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" -o "$staging_dir/$binary_name" ./cmd/rs

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
