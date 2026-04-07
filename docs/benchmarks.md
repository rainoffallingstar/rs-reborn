# Benchmarks

This repository keeps a small set of focused Go microbenchmarks for the hot paths we have been tightening during the native-runtime hardening work:

- dependency-layer planning
- repo batch chunking
- slow-install summary formatting
- dependency/source preview rendering
- cache/store reuse discovery
- cached prefetch behavior

The goal is not to perfectly model end-to-end wall time. The goal is to catch accidental regressions in the pure Go logic that sits on the critical path of `rvx run`, `rvx list`, `rvx doctor`, and the native installer.

## Commands

Installer-side microbenchmarks:

```bash
scripts/bench/installer-bench.sh
```

Runner/logging microbenchmarks:

```bash
scripts/bench/runner-bench.sh
```

Combined baseline capture bundle:

```bash
scripts/bench/capture-baseline.sh
```

By default that script also updates a sibling symlink named `<output-dir>-latest` so the most recent local capture always has a stable path.

Compact diff between two captures:

```bash
scripts/bench/diff-baseline.sh /path/to/older-bundle /path/to/newer-bundle
```

If you use `capture-baseline.sh` repeatedly with the default paths, it now also keeps a sibling `<output-dir>-previous` symlink, so you can simply run:

```bash
scripts/bench/diff-baseline.sh
```

Direct `go test` equivalents:

```bash
go test -run '^$' -bench 'Benchmark(LoadInstalledPackageFromLibraryStoreStateFastPath|FindReusablePackagesInLibrary|DiscoverReusablePackagesInLibraries|PrefetchPlannedPackagesCachedArtifacts|InstallPlanLayersLargeGraph|SplitRepoBatchChunksLargeBatch|FormatSlowInstallSummary)$' -benchmem ./internal/installer

go test -run '^$' -bench 'Benchmark(PreviewStringsLargeSlice|SourceSummaryAndPreview)$' -benchmem ./internal/runner
```

## Current Baseline

Local reference run captured on April 2, 2026 from `/Volumes/DataCenter_01/GitHub/gr`:

- machine: Apple M4
- OS: macOS (`darwin`, `arm64`)
- Go benchmark mode: default benchtime with `-benchmem`

Installer hot-path samples:

```text
BenchmarkInstallPlanLayersLargeGraph-10          2826      426692 ns/op   5009040 B/op   2050 allocs/op
BenchmarkSplitRepoBatchChunksLargeBatch-10     717232        1630 ns/op      8448 B/op     50 allocs/op
BenchmarkFormatSlowInstallSummary-10           296623        3818 ns/op      3713 B/op     22 allocs/op
```

Runner/logging hot-path samples:

```text
BenchmarkPreviewStringsLargeSlice-10          7895500       155.6 ns/op       288 B/op      4 allocs/op
BenchmarkSourceSummaryAndPreview-10            110372     10612 ns/op      13334 B/op    262 allocs/op
```

These numbers are not portable performance budgets. They are a reference point for:

- same-machine before/after comparisons
- PR review sanity checks
- spotting obvious regressions after logging or planner refactors

## How To Use This

When you change planner, logging, preview, or installer batching code:

1. run `go test ./...`
2. run the relevant bench script
3. compare against the previous local run on the same machine

If a benchmark moves materially in the wrong direction, check whether the regression is:

- algorithmic
- allocation-related
- only in a debug/verbose path
- an acceptable tradeoff for a real user-facing win

If the benchmark meaning changes substantially, update this document with the new baseline instead of letting stale numbers linger.

## Optional CI Job

The main CI workflow now exposes a manual `workflow_dispatch` boolean input named `run_benchmarks`.

When enabled, CI runs a non-blocking `Benchmarks (Optional)` job that:

- runs `scripts/bench/capture-baseline.sh`
- writes the generated markdown summary into the GitHub Actions step summary
- uploads the raw text outputs as an artifact named `benchmark-results-<run_id>-<run_attempt>-<sha>`

This job is intentionally:

- manual-only
- non-blocking (`continue-on-error: true`)
- informational rather than release-gating

That keeps push/PR latency stable while still giving us a reproducible CI entrypoint for performance spot-checks.

The captured bundle now includes:

- `SUMMARY.md`
- `installer-bench.txt`
- `runner-bench.txt`
- `installer-bench.json`
- `runner-bench.json`
- `benchmark.json`
- `metadata.env`
- `benchmark-diff.txt` and `DIFF.md` when a previous baseline is available

The script also records a `Bundle ID` in both `SUMMARY.md` and `metadata.env`, updates a latest-link pointer unless you override `RS_BENCH_LATEST_LINK`, and rotates the prior latest capture into a previous-link pointer unless you override `RS_BENCH_PREVIOUS_LINK`.

`SUMMARY.md`, `metadata.env`, and `benchmark.json` all record the git branch/commit plus GitHub workflow run metadata when the script is executed inside Actions.

When a previous local capture exists, `capture-baseline.sh` also runs `diff-baseline.sh` automatically and stores the compact comparison in both `benchmark-diff.txt` and `DIFF.md`. The optional CI benchmark job appends that markdown diff to the Actions step summary as well.

`benchmark.json` is the easiest file to feed into simple diff tooling because it includes:

- the bundle metadata
- relative artifact names
- parsed installer benchmark rows
- parsed runner benchmark rows

`scripts/bench/diff-baseline.sh` understands either bundle directories or direct `benchmark.json` paths, and it supports:

- `--threshold <pct>` to ignore small ns/op movement
- `--fail-on-regression` to exit non-zero if a slowdown crosses the threshold
