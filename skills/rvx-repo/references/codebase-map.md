# Codebase Map

Use this reference to jump to the right files quickly.

## Product Surface

- `README.md`: user-facing CLI workflows, examples, install commands, and feature inventory.
- `docs/design.md`: architecture, command lifecycle, config model, cache/lock strategy, and diagnostics goals.
- `examples/README.md`: the smallest realistic tour of bundled example projects.

## Entrypoints

- `cmd/rvx/main.go`: primary binary entrypoint.
- `cmd/rs/main.go`: compatibility alias entrypoint.
- `internal/cli/cli.go`: top-level command parsing, subcommand dispatch, flags, and most printed output shapes.

## Core Packages

- `internal/project/`: `rs.toml` discovery, parse/render/edit flows, config inheritance, path resolution, and config-writing behavior.
- `internal/rdeps/`: static scanning of R source for `library()`, `require()`, `requireNamespace()`, `pkg::fn`, and `pkg:::fn`; bundled-package filtering; Bioconductor classification.
- `internal/runner/`: environment resolution, managed library hashing/selection, install orchestration, lock/check/doctor flows, cache commands, and execution.
- `internal/installer/`: native package install implementation used by the runner.
- `internal/rmanager/`: managed R installation discovery, version selectors, install methods, and project interpreter helpers.
- `internal/toolchainenv/`: rootless system dependency/toolchain detection, templates, package-plan generation, and bootstrap guidance.
- `internal/lockfile/`: lockfile read/write helpers used by runtime validation.
- `internal/eventstream/` and `internal/progresscmd/`: runtime progress/reporting helpers.
- `internal/brand/`: product naming used by CLI output.

## Public SDK Surface

- `pkg/project/`, `pkg/rdeps/`, `pkg/runner/`, `pkg/rmanager/`, `pkg/lockfile/`, `pkg/toolchain/`: thin exported wrappers or type aliases over the corresponding `internal/*` packages.
- When public API behavior changes, inspect the matching `pkg/*` package even if the real logic lives under `internal/*`.

## Task Routing

- New flag, subcommand, or CLI output regression: start at `internal/cli/cli.go`, then follow calls into the relevant `internal/*` package.
- `rs.toml` parse/edit bug, path handling, or low-diff rewrite issue: start in `internal/project/`.
- Wrong detected package set, bundled package handling, or Bioconductor classification: start in `internal/rdeps/`.
- `run`, `exec`, `shell`, `lock`, `sync`, `check`, `doctor`, or cache bug: start in `internal/runner/`.
- Managed R install/list/use/which issue: start in `internal/rmanager/`.
- Toolchain detect/bootstrap/template issue: start in `internal/toolchainenv/`.
- Release or installer packaging issue: inspect `install.sh`, `install.ps1`, and the relevant `scripts/ci/e2e-release-install*` scripts.
- Example-specific regression: inspect the corresponding files under `examples/`.

## CI And Scripts

- `scripts/ci/e2e-smoke.sh`: broad CLI smoke coverage for scan/list/doctor/init/lock/check/exec/shell/run/cache/prune/r-which/r-use.
- `scripts/ci/e2e-toolchain-cli.sh`: CLI coverage for toolchain detect/bootstrap/template/init flows.
- `scripts/ci/`: additional end-to-end and environment-specific coverage for native backend, pak compatibility, release install, R manager, and toolchain scenarios.
- `scripts/bench/`: microbenchmark and baseline-capture helpers.

## Useful Heuristics

- If a change affects user-facing behavior and exported Go APIs, update both `internal/*` and `pkg/*`.
- If a change affects package planning, think through `scan`, `list`, `run`, `lock`, `check`, and `doctor`, not only one command.
- If a change affects interpreter or toolchain selection, check both config-layer behavior and the corresponding CLI guidance text.
