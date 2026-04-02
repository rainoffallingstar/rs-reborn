#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_BASE="${TMPDIR:-/tmp}"
OUT_DIR="${RS_BENCH_OUT:-${TMP_BASE%/}/rs-installer-bench}"
GO_CACHE_DIR="${RS_BENCH_GOCACHE:-${TMPDIR:-/tmp}/rs-go-build}"
PKG="./internal/installer"
BENCH_RE='^Benchmark(LoadInstalledPackageFromLibraryStoreStateFastPath|FindReusablePackagesInLibrary|DiscoverReusablePackagesInLibraries|PrefetchPlannedPackagesCachedArtifacts|InstallPlanLayersLargeGraph|SplitRepoBatchChunksLargeBatch|FormatSlowInstallSummary)$'
PROFILE_BENCH='^BenchmarkPrefetchPlannedPackagesCachedArtifacts$'

usage() {
  cat <<'EOF'
Usage:
  scripts/bench/installer-bench.sh [bench|profile|trace|all]

Modes:
  bench    Run installer microbenchmarks with benchmem output.
  profile  Capture CPU and memory profiles for the prefetch benchmark.
  trace    Capture a Go execution trace for the prefetch benchmark.
  all      Run bench, profile, and trace in sequence.

Environment:
  RS_BENCH_OUT  Override the output directory for profile/trace artifacts.
  RS_BENCH_GOCACHE  Override the Go build cache used by this script.
EOF
}

run_bench() {
  echo "==> installer benchmarks"
  cd "${ROOT_DIR}"
  mkdir -p "${GO_CACHE_DIR}"
  GOCACHE="${GO_CACHE_DIR}" go test -run '^$' -bench "${BENCH_RE}" -benchmem "${PKG}"
}

run_profile() {
  mkdir -p "${OUT_DIR}"
  mkdir -p "${GO_CACHE_DIR}"
  echo "==> installer CPU/memory profiles -> ${OUT_DIR}"
  cd "${ROOT_DIR}"
  GOCACHE="${GO_CACHE_DIR}" go test -run '^$' -bench "${PROFILE_BENCH}" -benchmem \
    -cpuprofile "${OUT_DIR}/installer.cpu.pprof" \
    -memprofile "${OUT_DIR}/installer.mem.pprof" \
    "${PKG}"
}

run_trace() {
  mkdir -p "${OUT_DIR}"
  mkdir -p "${GO_CACHE_DIR}"
  echo "==> installer execution trace -> ${OUT_DIR}/installer.trace"
  cd "${ROOT_DIR}"
  GOCACHE="${GO_CACHE_DIR}" go test -run '^$' -bench "${PROFILE_BENCH}" -benchtime=3x \
    -trace "${OUT_DIR}/installer.trace" \
    "${PKG}"
}

mode="${1:-bench}"
case "${mode}" in
  bench)
    run_bench
    ;;
  profile)
    run_profile
    ;;
  trace)
    run_trace
    ;;
  all)
    run_bench
    run_profile
    run_trace
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
