#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_DIR="${RS_BENCH_OUT:-${TMPDIR:-/tmp}/rs-bench-baseline}"
LATEST_LINK="${RS_BENCH_LATEST_LINK:-${OUT_DIR%/}-latest}"
PREVIOUS_LINK="${RS_BENCH_PREVIOUS_LINK:-${OUT_DIR%/}-previous}"
DIFF_THRESHOLD="${RS_BENCH_DIFF_THRESHOLD:-5}"

usage() {
  cat <<'EOF'
Usage:
  scripts/bench/capture-baseline.sh

Runs the installer and runner benchmark scripts, captures their raw output,
and writes a small machine-readable/local-readable baseline bundle.

Environment:
  RS_BENCH_OUT  Override the output directory for generated benchmark files.
  RS_BENCH_LATEST_LINK  Override the symlink updated to point at the latest captured bundle.
  RS_BENCH_PREVIOUS_LINK  Override the symlink updated to point at the prior captured bundle.
  RS_BENCH_DIFF_THRESHOLD  Override the ns/op percentage threshold used for generated diffs.
EOF
}

json_escape() {
  printf '%s' "${1}" | awk '
    BEGIN { first = 1 }
    {
      gsub(/\\/,"\\\\")
      gsub(/"/,"\\\"")
      gsub(/\r/,"\\r")
      gsub(/\t/,"\\t")
      if (!first) {
        printf "\\n"
      }
      printf "%s", $0
      first = 0
    }
  '
}

write_benchmark_array() {
  local suite="$1"
  local input_file="$2"
  awk -v suite="${suite}" '
    BEGIN {
      first = 1
      print "["
    }
    /^Benchmark/ {
      name = $1
      sub(/-[0-9]+$/, "", name)
      iterations = $2
      ns_per_op = $3
      bytes_per_op = $5
      allocs_per_op = $7
      if (!first) {
        print ","
      }
      printf "  {\"suite\":\"%s\",\"name\":\"%s\",\"iterations\":%s,\"ns_per_op\":%s", suite, name, iterations, ns_per_op
      if (bytes_per_op ~ /^[0-9.]+$/) {
        printf ",\"bytes_per_op\":%s", bytes_per_op
      }
      if (allocs_per_op ~ /^[0-9.]+$/) {
        printf ",\"allocs_per_op\":%s", allocs_per_op
      }
      printf "}"
      first = 0
    }
    END {
      if (!first) {
        print ""
      }
      print "]"
    }
  ' "${input_file}"
}

write_benchmark_bundle_json() {
  local artifact_extra_entries="${1:-}"
  cat > "${OUT_DIR}/benchmark.json" <<EOF
{
  "bundle_id": "$(json_escape "${bundle_id}")",
  "timestamp_utc": "$(json_escape "${timestamp_utc}")",
  "host": "$(json_escape "${host_name}")",
  "git_branch": "$(json_escape "${git_branch}")",
  "git_commit": "$(json_escape "${git_commit}")",
  "git_short_commit": "$(json_escape "${git_short_commit}")",
  "go_version": "$(json_escape "${go_version}")",
  "target": "$(json_escape "${go_os_arch}")",
  "kernel": "$(json_escape "${kernel}")",
  "workflow": "$(json_escape "${github_workflow}")",
  "run_id": "$(json_escape "${github_run_id}")",
  "run_attempt": "$(json_escape "${github_run_attempt}")",
  "artifacts": {
    "summary_markdown": "SUMMARY.md",
    "installer_text": "installer-bench.txt",
    "runner_text": "runner-bench.txt",
    "installer_json": "installer-bench.json",
    "runner_json": "runner-bench.json",
    "metadata_env": "metadata.env"${artifact_extra_entries}
  },
  "benchmarks": {
    "installer": $(cat "${OUT_DIR}/installer-bench.json"),
    "runner": $(cat "${OUT_DIR}/runner-bench.json")
  }
}
EOF
}

case "${1:-capture}" in
  capture)
    ;;
  -h|--help|help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac

mkdir -p "${OUT_DIR}"

old_latest_target=""
if [ -n "${LATEST_LINK}" ]; then
  old_latest_target="$(readlink "${LATEST_LINK}" 2>/dev/null || true)"
fi

timestamp_utc="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
host_name="$(hostname 2>/dev/null || echo unknown)"
go_version="$(go version)"
go_os_arch="$(go env GOOS)/$(go env GOARCH)"
kernel="$(uname -srmo 2>/dev/null || uname -a 2>/dev/null || echo unknown)"

cd "${ROOT_DIR}"

git_commit="${GITHUB_SHA:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
git_branch="${GITHUB_REF_NAME:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)}"
git_short_commit="$(printf '%s' "${git_commit}" | cut -c1-12)"
github_run_id="${GITHUB_RUN_ID:-local}"
github_run_attempt="${GITHUB_RUN_ATTEMPT:-local}"
github_workflow="${GITHUB_WORKFLOW:-local}"
bundle_id="$(printf '%s' "${timestamp_utc}" | tr -d ':-' | tr 'TZ' '__')-${git_short_commit}-${github_run_id}-${github_run_attempt}"

bash scripts/bench/installer-bench.sh | tee "${OUT_DIR}/installer-bench.txt"
bash scripts/bench/runner-bench.sh | tee "${OUT_DIR}/runner-bench.txt"

write_benchmark_array installer "${OUT_DIR}/installer-bench.txt" > "${OUT_DIR}/installer-bench.json"
write_benchmark_array runner "${OUT_DIR}/runner-bench.txt" > "${OUT_DIR}/runner-bench.json"

cat > "${OUT_DIR}/SUMMARY.md" <<EOF
# Benchmark Baseline

- Timestamp (UTC): \`${timestamp_utc}\`
- Host: \`${host_name}\`
- Bundle ID: \`${bundle_id}\`
- Git branch: \`${git_branch}\`
- Git commit: \`${git_commit}\`
- Git short commit: \`${git_short_commit}\`
- Go: \`${go_version}\`
- Target: \`${go_os_arch}\`
- Kernel: \`${kernel}\`
- Workflow: \`${github_workflow}\`
- Run ID: \`${github_run_id}\`
- Run attempt: \`${github_run_attempt}\`

## Installer

\`\`\`text
$(cat "${OUT_DIR}/installer-bench.txt")
\`\`\`

## Runner

\`\`\`text
$(cat "${OUT_DIR}/runner-bench.txt")
\`\`\`
EOF

cat > "${OUT_DIR}/metadata.env" <<EOF
RS_BENCH_TIMESTAMP_UTC=${timestamp_utc}
RS_BENCH_HOST=${host_name}
RS_BENCH_BUNDLE_ID=${bundle_id}
RS_BENCH_GIT_BRANCH=${git_branch}
RS_BENCH_GIT_COMMIT=${git_commit}
RS_BENCH_GIT_SHORT_COMMIT=${git_short_commit}
RS_BENCH_GO_VERSION=${go_version}
RS_BENCH_TARGET=${go_os_arch}
RS_BENCH_KERNEL=${kernel}
RS_BENCH_WORKFLOW=${github_workflow}
RS_BENCH_RUN_ID=${github_run_id}
RS_BENCH_RUN_ATTEMPT=${github_run_attempt}
EOF

artifact_extra_entries=""
write_benchmark_bundle_json ""
if [ -n "${old_latest_target}" ] && [ "${old_latest_target}" != "${OUT_DIR}" ] && [ -f "${old_latest_target%/}/benchmark.json" ]; then
  bash scripts/bench/diff-baseline.sh --threshold "${DIFF_THRESHOLD}" "${old_latest_target}" "${OUT_DIR}" | tee "${OUT_DIR}/benchmark-diff.txt"
  cat > "${OUT_DIR}/DIFF.md" <<EOF
# Benchmark Diff

\`\`\`text
$(cat "${OUT_DIR}/benchmark-diff.txt")
\`\`\`
EOF
  artifact_extra_entries=$',\n    "diff_text": "benchmark-diff.txt",\n    "diff_markdown": "DIFF.md"'
fi

write_benchmark_bundle_json "${artifact_extra_entries}"

if [ -n "${LATEST_LINK}" ]; then
  if [ -n "${old_latest_target}" ] && [ "${old_latest_target}" != "${OUT_DIR}" ] && [ -n "${PREVIOUS_LINK}" ]; then
    rm -f "${PREVIOUS_LINK}"
    ln -s "${old_latest_target}" "${PREVIOUS_LINK}"
  fi
  rm -f "${LATEST_LINK}"
  ln -s "${OUT_DIR}" "${LATEST_LINK}"
fi

echo "wrote benchmark baseline to ${OUT_DIR}"
if [ -f "${OUT_DIR}/benchmark-diff.txt" ]; then
  echo "wrote benchmark diff to ${OUT_DIR}/benchmark-diff.txt"
fi
if [ -n "${LATEST_LINK}" ]; then
  echo "updated latest benchmark link at ${LATEST_LINK}"
fi
if [ -n "${PREVIOUS_LINK}" ] && [ -L "${PREVIOUS_LINK}" ]; then
  echo "updated previous benchmark link at ${PREVIOUS_LINK}"
fi
