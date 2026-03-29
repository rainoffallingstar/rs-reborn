# `rs` v1 Draft Release Notes

`rs` is a small Go CLI for running R scripts with `uv run`-style dependency bootstrap.

This first release focuses on making the core execution model trustworthy:

- detect R dependencies from a script
- merge them with `rs.toml`
- install missing packages into an isolated managed library
- write and validate a lockfile
- report drift and setup problems with actionable diagnostics

## Highlights

### R-first execution workflow

`rs` now supports the core workflow around one script or a small repository:

- `rs run`
- `rs shell`
- `rs exec`
- `rs lock` / `rs sync`
- `rs check`
- `rs doctor`
- `rs scan`
- `rs list`

### Custom source support

Dependency resolution supports:

- CRAN
- Bioconductor
- GitHub sources
- generic git sources
- local package tarballs and local source directories

### Stronger reproducibility checks

The lockfile and managed-library cache now track more than package names:

- runtime metadata influences cache reuse
- GitHub/git/local source identity influences cache reuse
- local sources now carry stable content fingerprints
- `check`, `run --locked`, and `run --frozen` detect more real drift cases with clearer diagnostics

### Better diagnostics

`rs doctor` and `rs check` now provide:

- clearer setup/source/runtime buckets
- structured JSON details for automation
- actionable next-step suggestions
- stricter validation with `--strict`

### Project-level interpreter selection

This release adds basic multi-R support without expanding the tool beyond R:

- pin an interpreter in `rs.toml` with `rscript = "..."`
- override it per invocation with `--rscript`
- inspect or update project selection with `rs r which` and `rs r use`

### Thin `rig` integration

For environments that already use [`rig`](https://github.com/r-lib/rig), `rs` now exposes:

- `rs r list`
- `rs r install <version>`
- `rs r use <version>`
- `rs r which`

This is intentionally a thin integration layer, not a full first-party R installer.

## Scope Boundaries

What v1 is:

- an R-only launcher with isolated package bootstrap
- a lightweight alternative for script-oriented workflows
- a tool with lock/check/doctor visibility built in

What v1 is not:

- a full replacement for every `renv` workflow
- a solver for OS-level system dependencies
- a complete formatting-preserving `rs.toml` editor
- a cross-platform R installer with deep lifecycle management

## Known Deferrals

The following are deliberately deferred until after v1:

- partial lock refresh behavior
- metadata-only refresh semantics
- advanced CI recipes
- `renv` migration guidance
- write-command JSON output
- richer `rs.toml` rewrite fidelity beyond low-diff stability

## Recommended Announcement Framing

If you want a short public summary, this version is best described as:

> A small R-only CLI that makes `Rscript` feel closer to `uv run`, with managed libraries, lock/check/doctor visibility, custom-source support, and explicit interpreter selection.
