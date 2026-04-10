# Validation

Use the smallest convincing validation path first, then widen only if the change crosses package boundaries or touches user-facing CLI flows.

## Fast Path

- Build the primary binary:
  `go build -o rvx ./cmd/rvx`
- Run package-local tests for the touched area:
  `go test ./internal/<area> ./pkg/<area>`
- Run the full Go test sweep when the change spans multiple areas or exported APIs:
  `go test ./...`

## Recommended Mapping

- Config parsing, config rewriting, init/add/remove behavior:
  `go test ./internal/project ./pkg/project`
- Dependency detection or Bioconductor/bundled-package logic:
  `go test ./internal/rdeps ./pkg/rdeps`
- Runtime resolution, install planning, lock/check/doctor/cache behavior:
  `go test ./internal/runner ./pkg/runner`
- Managed R list/install/use/which behavior:
  `go test ./internal/rmanager ./pkg/rmanager`
- Toolchain detect/bootstrap/template logic:
  `go test ./internal/toolchainenv ./pkg/toolchain`
- Lockfile shape or serialization:
  run the relevant `internal/runner` or `pkg/lockfile` tests, depending on where the behavior lives.

## E2E Scripts

- Broad CLI smoke coverage:
  `bash scripts/ci/e2e-smoke.sh`
- Toolchain CLI coverage:
  `bash scripts/ci/e2e-toolchain-cli.sh`

Use the broader scripts under `scripts/ci/` only when the touched area clearly overlaps them, for example native installer compatibility, release install behavior, or R manager workflows.

## Important Notes

- `scripts/ci/e2e-smoke.sh` builds a temporary `rvx` binary, uses temp directories, expects local `Rscript`, and exercises real lock/run/check flows.
- `scripts/ci/e2e-toolchain-cli.sh` builds a temporary binary and simulates toolchain layouts inside a temp `HOME`; it is the best fit for toolchain output or preset logic changes.
- Some E2E scripts rely on external tooling or networked package installs; prefer the narrowest script that matches the changed subsystem.

## Decision Guide

- Pure parser or formatting fix: package-local `go test` is usually enough.
- CLI flag or output-shape change: run package-local tests, then the matching E2E script if the behavior is user-visible.
- Runtime/install/check/lock change: run targeted tests first and strongly consider `bash scripts/ci/e2e-smoke.sh`.
- Toolchain guidance or preset change: run targeted tests first and then `bash scripts/ci/e2e-toolchain-cli.sh`.
