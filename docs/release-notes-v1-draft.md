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

### Rootless toolchain bootstrap

This release also makes rootless and user-local source-build flows much more practical:

- `rs` can auto-detect and auto-use an existing user-local toolchain prefix when no explicit toolchain config is present
- `rs init --toolchain-preset auto|enva|micromamba|mamba|conda|homebrew|spack` can seed common rootless layouts directly into `rs.toml`
- `rs toolchain detect`, `rs toolchain template`, and `rs doctor --toolchain-only` now provide a complete discover/preview/validate loop
- commands such as `rs run`, `rs lock`, `rs check`, `rs doctor`, and `rs r install --method source` now accept `--bootstrap-toolchain` to explicitly create a user-local toolchain prefix through a supported external manager when needed

For active bootstrap of a new conda-style build-tools prefix, the current priority is:

- `enva`
- `micromamba`
- `mamba`
- `conda`

Already-detected Homebrew and Spack layouts remain supported and are still recommended by `auto` when they already exist on the machine.

### Project-level interpreter selection

This release adds basic multi-R support without expanding the tool beyond R:

- pin an interpreter in `rs.toml` with `rscript = "..."`
- override it per invocation with `--rscript`
- inspect or update project selection with `rs r which` and `rs r use`

### Native multi-R management

On macOS, Linux, and Windows x64, `rs` now exposes:

- `rs r list`
- `rs r install <version>`
- `rs r use <version>`
- `rs r which`

This is now a first-party native R manager with user-local installs, source-build fallback where supported, and project-level interpreter selection.

## Support Boundary

This release is intended to be described conservatively:

- runtime commands are supported on macOS, Linux, and Windows x64
- `rs r ...` is the supported native-manager path on macOS, Linux, and Windows x64
- Windows ARM64 binaries are still published as secondary artifacts with lighter validation depth

## Scope Boundaries

What v1 is:

- an R-only launcher with isolated package bootstrap
- a lightweight alternative for script-oriented workflows
- a tool with lock/check/doctor visibility built in
- a user-local multi-R manager across macOS, Linux, and Windows x64
- a tool that can validate and bootstrap rootless toolchain prefixes for source builds

What v1 is not:

- a full replacement for every `renv` workflow
- a solver for OS-level system dependencies
- a complete formatting-preserving `rs.toml` editor
- a full system-package manager or automatic Rtools installer
- a general-purpose sysadmin tool that silently installs arbitrary system libraries without user opt-in

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
