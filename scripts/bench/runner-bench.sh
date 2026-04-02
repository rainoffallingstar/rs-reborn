#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
GO_CACHE_DIR="${RS_BENCH_GOCACHE:-${TMPDIR:-/tmp}/rs-go-build}"
PKG="./internal/runner"
BENCH_RE='^Benchmark(PreviewStringsLargeSlice|SourceSummaryAndPreview)$'

usage() {
  cat <<'EOF'
Usage:
  scripts/bench/runner-bench.sh

Runs the runner/logging microbenchmarks with benchmem output.

Environment:
  RS_BENCH_GOCACHE  Override the Go build cache used by this script.
EOF
}

case "${1:-bench}" in
  bench)
    echo "==> runner benchmarks"
    cd "${ROOT_DIR}"
    mkdir -p "${GO_CACHE_DIR}"
    GOCACHE="${GO_CACHE_DIR}" go test -run '^$' -bench "${BENCH_RE}" -benchmem "${PKG}"
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
