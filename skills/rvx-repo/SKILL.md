---
name: rvx-repo
description: Use when Codex is working in the rs-reborn or rvx repository: implementing or reviewing Go code, debugging the rvx CLI, changing rs.toml or rs.lock.json behavior, adjusting dependency detection, runner or installer flows, R version management, toolchain bootstrapping, examples, install scripts, or CI coverage. This skill helps map repo tasks to the right files, docs, and validation commands.
---

# rvx Repo

Use this skill for repository work, not for generic R advice. The repository builds `rvx`, a Go CLI that gives R scripts and interactive R sessions a `uv run`-style workflow: detect dependencies, merge them with `rs.toml`, materialize a managed library, install anything missing, then run, lock, check, or diagnose inside that environment.

## Start With The Right Slice

- For command dispatch, flags, help text, and JSON/text CLI output, read `internal/cli/cli.go` first, then `cmd/rvx/main.go` and `cmd/rs/main.go`.
- For `rs.toml` discovery, parsing, editing, path resolution, and rewrite stability, read `internal/project/`.
- For static package detection, Bioconductor classification, and bundled-package filtering, read `internal/rdeps/`.
- For `run`, `exec`, `shell`, `lock`, `sync`, `check`, `doctor`, cache management, and environment resolution, read `internal/runner/`.
- For managed R installs, version selectors, and `rvx r ...`, read `internal/rmanager/`.
- For rootless toolchain detection/bootstrap and related CLI output, read `internal/toolchainenv/`.
- For public library/API changes, keep `pkg/*` aligned with the touched `internal/*` package because `pkg/*` is mostly the exported wrapper surface.
- For product intent and realistic usage, skim `README.md`, `docs/design.md`, and `examples/README.md` before making assumptions.

## Working Pattern

1. Classify the task before editing.
   Decide whether it is mainly CLI surface, config/editing, dependency detection, runtime/install flow, R management, or toolchain work.
2. Read the narrowest relevant slice.
   Avoid loading the whole repository when one package and one CI script are enough.
3. Preserve the product shape.
   Favor small, explicit workflows and actionable diagnostics over magic behavior.
4. Keep wrappers in sync.
   When an exported type or function changes under `internal/*`, check whether the matching `pkg/*` alias/wrapper also needs an update.
5. Validate at the smallest useful level first.
   Start with the touched package, then widen to repo-wide or E2E coverage only when the change crosses package boundaries or affects CLI behavior.

## Repo-Specific Rules

- Build the main binary with `go build -o rvx ./cmd/rvx`.
- Treat `rvx` as the primary product name. `rs` exists as a compatibility alias, so avoid adding behavior that only works through the legacy name.
- Keep dependency detection intentionally static and fast. Prefer clear escape hatches such as explicit includes/excludes over heavy dynamic inference.
- Preserve low-diff config editing behavior in `internal/project`; comments, ordering, and section-local edits are part of the product value.
- Keep `doctor`, `check`, `toolchain`, and install failures actionable. Prefer next-step guidance over opaque raw errors.
- When changing cache identity, runtime metadata, or lock validation, reason about them together so install reuse, lock correctness, and cross-runtime safety stay aligned.

## Validation Ladder

- Config/parser/editing only: run the relevant `internal/project` and `pkg/project` tests.
- Dependency scanning only: run the relevant `internal/rdeps` and `pkg/rdeps` tests.
- Runtime, lock, check, doctor, or cache behavior: run the relevant `internal/runner` and `pkg/runner` tests.
- Managed R selection or installs: run the relevant `internal/rmanager` and `pkg/rmanager` tests.
- Toolchain detection/bootstrap/template work: run the relevant `internal/toolchainenv` and `pkg/toolchain` tests.
- Broad Go surface changes: run `go test ./...`.
- CLI flow changes: prefer the existing E2E scripts in `scripts/ci/` instead of inventing ad hoc smoke checks.

## Reference Map

Read [references/codebase-map.md](references/codebase-map.md) when you need a quick module map or task-to-file routing.

Read [references/validation.md](references/validation.md) when choosing the smallest convincing verification path.
