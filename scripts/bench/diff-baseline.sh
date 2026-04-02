#!/usr/bin/env bash
set -euo pipefail

DEFAULT_BUNDLE_DIR="${TMPDIR:-/tmp}/rs-bench-baseline"
DEFAULT_LATEST_LINK="${RS_BENCH_LATEST_LINK:-${DEFAULT_BUNDLE_DIR}-latest}"
DEFAULT_PREVIOUS_LINK="${RS_BENCH_PREVIOUS_LINK:-${DEFAULT_BUNDLE_DIR}-previous}"

usage() {
  cat <<'EOF'
Usage:
  scripts/bench/diff-baseline.sh
  scripts/bench/diff-baseline.sh <before> <after>
  scripts/bench/diff-baseline.sh --threshold 3 <before> <after>
  scripts/bench/diff-baseline.sh --fail-on-regression <before> <after>

Compares two benchmark baseline bundles or benchmark.json files and prints a
compact diff across installer/runner microbenchmarks.

Defaults:
  With no positional arguments, compares:
    ${RS_BENCH_PREVIOUS_LINK:-/tmp/rs-bench-baseline-previous}/benchmark.json
    ${RS_BENCH_LATEST_LINK:-/tmp/rs-bench-baseline-latest}/benchmark.json

Flags:
  --threshold <pct>         Minimum absolute ns/op percentage change to classify
                            a benchmark as faster/slower. Default: 5
  --fail-on-regression      Exit non-zero if any benchmark exceeds the slowdown threshold
  -h, --help                Show this help text

Notes:
  Each positional argument may be either a benchmark bundle directory or a
  direct path to benchmark.json.
EOF
}

resolve_benchmark_json() {
  local input="$1"
  if [ -d "${input}" ]; then
    printf '%s/benchmark.json\n' "${input%/}"
    return 0
  fi
  printf '%s\n' "${input}"
}

threshold="5"
fail_on_regression="0"
args=()

while [ "$#" -gt 0 ]; do
  case "$1" in
    --threshold)
      if [ "$#" -lt 2 ]; then
        echo "missing value for --threshold" >&2
        exit 1
      fi
      threshold="$2"
      shift 2
      ;;
    --fail-on-regression)
      fail_on_regression="1"
      shift
      ;;
    -h|--help|help)
      usage
      exit 0
      ;;
    *)
      args+=("$1")
      shift
      ;;
  esac
done

case "${#args[@]}" in
  0)
    before_path="${DEFAULT_PREVIOUS_LINK}/benchmark.json"
    after_path="${DEFAULT_LATEST_LINK}/benchmark.json"
    ;;
  2)
    before_path="$(resolve_benchmark_json "${args[0]}")"
    after_path="$(resolve_benchmark_json "${args[1]}")"
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac

if [ ! -f "${before_path}" ]; then
  echo "benchmark baseline not found: ${before_path}" >&2
  exit 1
fi
if [ ! -f "${after_path}" ]; then
  echo "benchmark baseline not found: ${after_path}" >&2
  exit 1
fi

python3 - "${before_path}" "${after_path}" "${threshold}" "${fail_on_regression}" <<'PY'
import json
import math
import sys
from pathlib import Path

SECONDARY_METRIC_THRESHOLD = 1.0


def load(path_str: str) -> dict:
    path = Path(path_str)
    with path.open("r", encoding="utf-8") as handle:
        return json.load(handle)


def flatten(data: dict) -> dict:
    rows = {}
    for suite, items in data.get("benchmarks", {}).items():
        for item in items:
            rows[f"{suite}/{item['name']}"] = item
    return rows


def pct(before: float, after: float):
    if before == 0:
        return None
    return ((after - before) / before) * 100.0


def fmt_pct(value):
    if value is None or math.isnan(value) or math.isinf(value):
        return "n/a"
    return f"{value:+.1f}%"


def fmt_value(value):
    number = float(value)
    if number.is_integer():
        return f"{int(number):,}"
    return f"{number:,.1f}"


def fmt_metric(before_row, after_row, metric):
    before = before_row.get(metric)
    after = after_row.get(metric)
    if before is None or after is None:
        return None
    delta = pct(float(before), float(after))
    if metric != "ns_per_op" and (delta is None or abs(delta) < SECONDARY_METRIC_THRESHOLD):
        return None
    return f"{metric} {fmt_pct(delta)} ({fmt_value(before)} -> {fmt_value(after)})"


before = load(sys.argv[1])
after = load(sys.argv[2])
threshold = float(sys.argv[3])
fail_on_regression = sys.argv[4] == "1"

before_rows = flatten(before)
after_rows = flatten(after)

common_keys = sorted(set(before_rows) & set(after_rows))
new_keys = sorted(set(after_rows) - set(before_rows))
missing_keys = sorted(set(before_rows) - set(after_rows))

slower = []
faster = []
unchanged = []

for key in common_keys:
    before_row = before_rows[key]
    after_row = after_rows[key]
    delta = pct(float(before_row["ns_per_op"]), float(after_row["ns_per_op"]))
    entry = {
        "key": key,
        "delta": delta,
        "summary": ", ".join(
            part
            for part in [
                fmt_metric(before_row, after_row, "ns_per_op"),
                fmt_metric(before_row, after_row, "bytes_per_op"),
                fmt_metric(before_row, after_row, "allocs_per_op"),
            ]
            if part is not None
        ),
    }
    if delta is None or abs(delta) < threshold:
        unchanged.append(entry)
    elif delta > 0:
        slower.append(entry)
    else:
        faster.append(entry)

slower.sort(key=lambda item: item["delta"], reverse=True)
faster.sort(key=lambda item: item["delta"])

print("Benchmark diff")
print(
    f"- before: {before.get('bundle_id', 'unknown')} "
    f"({before.get('git_short_commit', 'unknown')}, {before.get('timestamp_utc', 'unknown')})"
)
print(
    f"- after:  {after.get('bundle_id', 'unknown')} "
    f"({after.get('git_short_commit', 'unknown')}, {after.get('timestamp_utc', 'unknown')})"
)
print(f"- threshold: {threshold:.1f}% ns/op")
print(
    f"- compared: {len(common_keys)} shared, {len(slower)} slower, "
    f"{len(faster)} faster, {len(unchanged)} unchanged, "
    f"{len(new_keys)} new, {len(missing_keys)} missing"
)

if slower:
    print("")
    print("Slower")
    for item in slower:
        print(f"- {item['key']}: {item['summary']}")

if faster:
    print("")
    print("Faster")
    for item in faster:
        print(f"- {item['key']}: {item['summary']}")

if new_keys:
    print("")
    print("New benchmarks")
    for key in new_keys:
        print(f"- {key}")

if missing_keys:
    print("")
    print("Missing benchmarks")
    for key in missing_keys:
        print(f"- {key}")

if fail_on_regression and slower:
    sys.exit(2)
PY
