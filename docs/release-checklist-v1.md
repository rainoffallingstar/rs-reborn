# `rvx` v1 Release Checklist

This checklist is for the first public release of `rvx` as an R-only dependency bootstrap CLI.

## Release Goal

Ship a minimal but trustworthy v1:

- per-script R dependency bootstrap works
- lock/check/frozen drift behavior is trustworthy
- local/custom source identity is validated
- project-level interpreter selection is usable
- multi-R support is available through explicit `rscript` selection and the native R manager

## Current Readiness Snapshot

These are the facts already established in the repo before the final release cut:

- local verification currently includes `go test ./...`
- local verification currently includes release-artifact install smoke through `install.sh`
- CI covers Linux and macOS `go test`, CLI smoke, bootstrap guidance, and release-install smoke
- CI also covers Linux end-to-end paths for local-source drift, doctor failures, git sources, native package installation, `pak`, and the native R manager

The remaining release work is mostly about final branch green status, support-boundary sign-off, and publishing from the intended release revision.

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

- [ ] `rvx init`
- [ ] `rvx run`
- [ ] `rvx lock`
- [ ] `rvx check`
- [ ] `rvx doctor`
- [ ] `rvx shell`
- [ ] `rvx exec`

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
- [ ] `rvx shell` chooses matching `R` when possible
- [ ] `rvx check` and `rvx doctor` report the selected interpreter path

### 6. Native R manager smoke test

Run on a machine where the native manager can install or discover multiple R versions:

- [ ] `rvx r list`
- [ ] `rvx r install <version>`
- [ ] `rvx r use <version>`
- [ ] `rvx r which`

Notes:

- v1 now ships a first-party R manager on macOS, Linux, and Windows x64
- keep the Windows x64 / Windows ARM64 validation split explicit in public wording

### 7. Docs alignment

- [ ] `README.md` matches actual CLI flags and current behavior
- [ ] `docs/design.md` no longer contradicts implemented multi-R support
- [ ] `docs/roadmap.md` reflects current baseline and remaining deferrals
- [ ] rootless/toolchain docs match actual `--bootstrap-toolchain` behavior and current preset list

### 8. Support statement

Decide and publish the v1 support boundary:

- [ ] supported OS list
- [ ] whether Windows is supported, experimental, or deferred
- [ ] whether the Windows x64 / Windows ARM64 split is described clearly enough
- [ ] which commands are stable for automation today
- [ ] whether rootless/source-build guidance is described clearly enough for user-local environments

## Recommended v1 Support Statement

Unless additional validation changes this, the safest public statement is:

- runtime commands are supported on macOS, Linux, and Windows x64
- explicit interpreter selection via `rscript` and `--rscript` is supported
- `rvx r ...` is a first-party native R manager on macOS, Linux, and Windows x64
- rootless source-build flows support detected user-local prefixes by default, plus explicit `--bootstrap-toolchain` opt-in for manager-driven prefix creation
- conda-style `auto` bootstrap stays on `enva`; micromamba/mamba/conda remain explicit compatibility presets only
- Windows ARM64 remains a shipped secondary artifact with lighter validation depth
- stable automation-oriented commands today are `scan`, `list`, `doctor`, `check`, `lock`, `sync`, `run`, `exec`, `shell`, and `rvx r list|install|use|which` on macOS, Linux, and Windows x64

## Release Artifacts

- [ ] tagged source release
- [ ] release notes
- [ ] binaries for supported platforms, or clear source-build instructions if binaries are not shipped yet

## CI Mapping

The current GitHub Actions workflow is split into these release-facing jobs:

- `go-test`: repository unit and package tests on Linux, macOS, and Windows x64
- `cli-smoke`: real-R command coverage for `scan`, `list`, `doctor`, `lock`, `check`, `exec`, `shell`, `run`, cache commands, and interpreter selection on Linux, macOS, and Windows x64
- `r-bootstrap-guidance`: missing-R guidance and `RS_AUTO_INSTALL_R` bootstrap messaging on Linux, macOS, and Windows x64
- `toolchain-doctor`: end-to-end verification of rootless toolchain-only validation and diagnostics on Linux and macOS
- `toolchain-cli`: end-to-end verification of toolchain detect/template/bootstrap CLI behavior on Linux and macOS
- `toolchain-enva`: end-to-end verification that `enva` is preferred over micromamba when actively bootstrapping a new rootless toolchain prefix
- `toolchain-enva-runtime`: end-to-end verification that `rvx run --bootstrap-toolchain` injects the bootstrapped `enva` prefix into the real runtime environment
- `release-install-smoke`: build a host release artifact, install it through `install.sh` or `install.ps1`, and verify the installed binary starts on Linux, macOS, and Windows x64
- `local-source-drift`: end-to-end verification that local source fingerprint drift breaks `check`, `--locked`, and `--frozen` as expected
- `doctor-failures`: end-to-end verification of blocking doctor failure output
- `git-source`: end-to-end verification of generic git sources
- `cache-rebuild`: end-to-end verification of managed-library rebuild behavior
- `multi-script-project`: end-to-end verification of project-level multi-script behavior
- `native-backend`: end-to-end verification that `RS_INSTALL_BACKEND=auto` stays on the native path, including Windows binary-first package installs
- `native-cran-archive`: end-to-end verification of CRAN archive resolution on the native installer
- `native-github`: end-to-end verification of standard GitHub sources on the native installer
- `native-bioc`: end-to-end verification of Bioconductor installs on the native installer
- `pak-backend`: explicit compatibility coverage for the `pak` backend on Linux and Windows x64
- `native-r-manager`: macOS, Linux, and Windows x64 smoke coverage for `rvx r list/install/use/which`
- `native-r-manager-source`: Ubuntu end-to-end verification of explicit `rvx r install --method source`

## Final Human Sign-Off Before Tagging

Even with the automated coverage above, the last release pass should still confirm:

- [ ] the intended release branch commit is pushed and CI is green on GitHub, not just locally
- [ ] the published support statement is acceptable for v1, especially the Windows x64 / Windows ARM64 split
- [ ] the native R manager has been judged sufficiently validated for the promised scope, or the public wording has been reduced accordingly
- [ ] the release workflow is triggered from the intended revision and the date tag outcome is checked once after publish

## Blockers That Should Delay v1

Do not cut a final release if any of these are still true:

- [ ] native R manager install and selection has not been validated once on a real machine
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
- deeper Windows support for the native R manager
