# `rs` v1 Release Checklist

This checklist is for the first public release of `rs` as an R-only dependency bootstrap CLI.

## Release Goal

Ship a minimal but trustworthy v1:

- per-script R dependency bootstrap works
- lock/check/frozen drift behavior is trustworthy
- local/custom source identity is validated
- project-level interpreter selection is usable
- multi-R support is available through explicit `rscript` selection and thin `rig` integration

## Must Pass Before Release

### 1. Core test suite

- [ ] `go test ./...` passes on the release branch
- [ ] no skipped or quarantined tests hide known v1 regressions
- [ ] `.github/workflows/ci.yml` is green across all jobs

Suggested command:

```bash
GOCACHE=/tmp/go-build GOMODCACHE=/tmp/gomodcache go test ./...
```

### 2. CLI smoke coverage

Verify at least one end-to-end project fixture for each of these:

- [ ] `rs init`
- [ ] `rs run`
- [ ] `rs lock`
- [ ] `rs check`
- [ ] `rs doctor`
- [ ] `rs shell`
- [ ] `rs exec`

### 3. Lock lifecycle

For at least one script with a checked-in `rs.toml`:

- [ ] `lock -> check -> run --locked -> run --frozen` passes when inputs are unchanged
- [ ] modifying script/config inputs causes actionable drift failure
- [ ] modifying a local source file or directory causes actionable drift failure

### 4. Source matrix smoke test

At least one fixture should exercise:

- [ ] CRAN
- [ ] Bioconductor
- [ ] GitHub source
- [ ] generic git source
- [ ] local tarball or local source directory

### 5. Interpreter selection smoke test

At least one machine with multiple R installations should verify:

- [ ] `rscript = "..."` in `rs.toml` is respected
- [ ] `--rscript` overrides config
- [ ] `rs shell` chooses matching `R` when possible
- [ ] `rs check` and `rs doctor` report the selected interpreter path

### 6. `rig` integration smoke test

Run on a machine where `rig` is actually installed:

- [ ] `rs r list`
- [ ] `rs r install <version>`
- [ ] `rs r use <version>`
- [ ] `rs r which`

Notes:

- v1 only promises thin `rig` integration, not a first-party R installer
- if Windows is not fully validated, document that explicitly instead of implying parity

### 7. Docs alignment

- [ ] `README.md` matches actual CLI flags and current behavior
- [ ] `docs/design.md` no longer contradicts implemented multi-R support
- [ ] `docs/roadmap.md` reflects current baseline and remaining deferrals

### 8. Support statement

Decide and publish the v1 support boundary:

- [ ] supported OS list
- [ ] whether Windows is supported, experimental, or deferred
- [ ] whether `rig` is required for R installation management or just recommended
- [ ] which commands are stable for automation today

## Recommended v1 Support Statement

Unless additional validation changes this, the safest public statement is:

- runtime commands are supported on macOS and Linux
- explicit interpreter selection via `rscript` and `--rscript` is supported
- `rs r ...` is a thin `rig` integration layer
- Windows is best-effort until validated in CI and smoke-tested with multiple R installs

## Release Artifacts

- [ ] tagged source release
- [ ] release notes
- [ ] binaries for supported platforms, or clear source-build instructions if binaries are not shipped yet

## CI Mapping

The current GitHub Actions workflow is split into these release-facing jobs:

- `go-test`: repository unit and package tests on Linux and macOS
- `cli-smoke`: real-R command coverage for `scan`, `list`, `doctor`, `lock`, `check`, `exec`, `shell`, `run`, cache commands, and interpreter selection
- `local-source-drift`: end-to-end verification that local source fingerprint drift breaks `check`, `--locked`, and `--frozen` as expected
- `rig-integration`: Linux smoke coverage for `rs r list/install/use/which`

## Blockers That Should Delay v1

Do not cut a final release if any of these are still true:

- [ ] `rig` integration has not been validated once on a real machine
- [ ] `run --locked` or `run --frozen` still has known correctness gaps
- [ ] local source fingerprint drift is known to miss real changes
- [ ] README examples diverge from shipped flags or command names

## Okay To Defer After v1

These are explicitly not blockers for the first release:

- partial lock refresh semantics
- metadata-only lock refresh
- advanced CI recipes
- `renv` migration guide
- write-command `--json`
- full formatting-preserving `rs.toml` rewriting
- first-party R installer beyond `rig`
