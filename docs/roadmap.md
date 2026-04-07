# rs Roadmap

This roadmap turns the current `rs` prototype into a more production-ready R workflow tool without expanding scope beyond R.

The sequencing principle is:

1. harden correctness before adding broader surface area
2. make diagnostics and cache behavior trustworthy before chasing convenience features
3. add polish only after the execution model is stable

## Current baseline

As of the current design, `rs` already has:

- runtime commands: `run`, `shell`, `exec`, `lock`, `sync`
- inspection commands: `scan`, `list`, `doctor`, `check`
- project editing commands: `init`, `add`, `remove`
- interpreter commands: `r list`, `r install`, `r use`, `r which`
- cache commands: `cache dir`, `cache ls`, `cache rm`, `prune`
- support for CRAN, Bioconductor, GitHub, generic git, and local sources
- project-level and per-command `Rscript` selection
- lockfile writing and validation
- JSON output for key inspection and diagnostics commands

That is enough to be useful today. The remaining roadmap is about making it safer, more predictable, and easier to adopt.

## Milestone 1: Runtime hardening

Objective:
Make dependency reuse and lock behavior more trustworthy across real machines.

Why first:
If cache identity and lock validation are weak, every higher-level feature inherits that fragility.

Work items:

- include richer runtime metadata in the managed library cache key
- include custom source identity in the cache key
- tighten drift checks between lockfile, managed library, and current script/config state
- add more explicit error messages for `--locked` versus `--frozen`
- expand tests around mixed CRAN/Bioconductor/custom-source environments

Suggested acceptance criteria:

- changing R version or platform-relevant metadata no longer reuses an incompatible managed library
- changing GitHub/git/local source identity invalidates the prior library selection
- `check` and `run --frozen` fail with actionable reasons instead of generic mismatch messages
- test coverage includes representative cache-key drift cases

Status:

- completed: runtime metadata and custom source identity now influence managed library selection
- completed: lock validation now checks `arch` and `os` in addition to existing runtime metadata
- completed: human-readable validation failures now distinguish missing lockfile, input drift, and installed-library drift
- completed: installed-library failures now point users toward `rvx cache rm <managed-library>` and `rvx lock`
- completed: local source files and source directories now contribute stable content fingerprints to both the managed-library cache key and lockfile validation
- remaining: broaden real-world drift coverage, especially around more mixed custom-source combinations and older-library metadata edge cases

## Milestone 2: Config editing stability

Objective:
Make `rs.toml` editing safe enough for regular use in real repositories.

Why second:
Once users adopt `rs`, config churn becomes part of normal workflow. Rewriting config unreliably creates avoidable friction.

Work items:

- preserve comments, stable ordering, and low-diff rewrites when `init`, `add`, and `remove` rewrite `rs.toml`
- make script-block generation more predictable for repeated `init --from` runs
- improve validation errors for malformed config sections and conflicting source declarations
- add tests for round-tripping common hand-edited `rs.toml` files

Suggested acceptance criteria:

- hand-written comments survive normal `add` and `remove` operations
- repeated edits do not reorder unrelated sections unexpectedly
- malformed config definitions point to the exact offending section and line with actionable hints

Status:

- completed: `LoadEditable`/`Save` now preserve the top-of-file preamble, keep existing root-key/top-level source-script/root-source/script-block order where possible, and replay comments attached to existing sections and fields
- completed: comments attached to removed script/source sections are now transferred to the next surviving section when possible
- completed: delete-path comment transfer now avoids the most obvious extra blank lines when comments are moved onto surviving sections
- completed: malformed or contradictory source sections now fail with messages that name the offending `[sources."..."]` or `[scripts."...".sources."..."]` block
- completed: malformed root/script/source config entries now report section-aware line numbers, supported keys, and close-match suggestions for common typos
- completed: mixed top-level source/script layouts and delete-path comment transfers now have dedicated regressions so normal edits stay low-diff and ordering-stable
- remaining: preserve more formatting details in harder edge cases beyond the current delete-path cleanup
- remaining: keep polishing malformed-config diagnostics in rarer parser edge cases without making the format rules surprising

## Milestone 3: Better diagnostics

Objective:
Help users understand failures before and after install with less trial and error.

Why third:
The tool already has `doctor` and `check`; improving them is leverage, because every user benefits without learning new commands.

Work items:

- add stronger system dependency hints in `doctor`
- classify errors into setup, network, source, lock, and runtime buckets
- surface next-step suggestions in human output while keeping JSON machine-friendly
- add optional verbose install summaries for custom source resolution

Suggested acceptance criteria:

- common missing-system-library failures have recognizable hints
- JSON diagnostics remain structured and scriptable
- human-readable failures consistently point to the next command or fix to try

Status:

- completed: `check --json` now exposes structured input drift and installed drift fields instead of only one flat `issues` array
- completed: installed drift is now split into missing-package, version, source, and other buckets, plus structured detail objects
- completed: `doctor --json` now groups results into setup/source errors and lock/cache warnings while preserving flat arrays for compatibility
- completed: `doctor --json` now exposes structured `error_details` and `warning_details`, plus dedicated `network_errors` and `runtime_errors` buckets
- completed: human-readable validation output now includes grouped summaries for both input drift and installed drift
- completed: `doctor` now emits explicit next-step guidance in both human-readable output and structured `next_steps` JSON objects
- completed: structured `next_steps` now distinguish categories and blocking versus non-blocking follow-ups
- completed: `doctor --json` now exposes top-level `status` and aggregate `summary` counts for CI and automation consumers
- completed: `doctor --strict` now lets CI treat warnings as failures without losing the normal JSON/text report
- completed: `doctor` system hints now cover a broader set of common native-library-heavy packages such as `stringi`, `odbc`, and `git2r`
- remaining: classify more install/runtime failures beyond the current source/setup split without turning `doctor` into a full install-log parser

## Milestone 4: Reproducibility improvements

Objective:
Strengthen the lockfile from a drift signal into a more reproducible environment description.

Why fourth:
This is valuable, but it sits on top of runtime hardening and diagnostics. Better to stabilize the base first.

Work items:

- capture more exact source revision data wherever possible
- define partial refresh behavior for lock updates
- distinguish "metadata refresh" from "dependency set changed" in lock operations
- consider storing stronger fingerprints for local source artifacts

Suggested acceptance criteria:

- GitHub and git sources resolve to auditable revisions in more cases
- users can refresh lock state without accidentally broadening dependency intent
- local source changes are easier to detect and explain

Status:

- completed: local source artifacts now record stable fingerprint metadata in the lockfile and participate in lock/check drift detection even when the path does not change
- remaining: define partial refresh behavior for lock updates
- remaining: distinguish "metadata refresh" from "dependency set changed" in lock operations
- remaining: capture more exact non-local source revision data wherever possible

## Milestone 5: UX and adoption polish

Objective:
Make the tool easier to discover, demo, and integrate into CI or team workflows.

Why last:
Polish sticks better once the core semantics are unlikely to shift.

Work items:

- expand examples to cover advanced custom-source scenarios
- add CI-oriented command recipes to the docs
- document migration guidance for users coming from ad hoc scripts or `renv`
- consider a `--json` mode for more write-oriented commands where automation would benefit

Suggested acceptance criteria:

- new users can copy an example and succeed quickly
- CI usage patterns are documented and testable
- automation consumers do not need to scrape human text output

## Out of scope for now

These ideas may be useful later, but they should not displace the roadmap above:

- deeper platform coverage and hardening for the first-party cross-platform R installer
- solving OS package manager dependencies directly
- generalized multi-language execution beyond R
- a plugin ecosystem or remote execution model
- replacing mature full-environment tools in every reproducibility scenario

## Recommended implementation order

If work proceeds incrementally, the most sensible order is:

1. broaden runtime-hardening coverage across more custom-source combinations
2. richer `doctor` hints for missing system dependencies and compiler/toolchain gaps
3. tighter config rewrite fidelity in edge cases
4. stronger lockfile source metadata
5. partial lock refresh behavior
6. additional automation-oriented JSON surfaces where write commands still need them

## Definition of success

`rs` is succeeding when a user can:

- point it at an R script or small project
- understand what dependencies it inferred
- inspect the final plan before mutation
- trust that cache reuse is safe
- rerun the same workflow later with meaningful drift detection
- debug failures without reverse-engineering the tool internals
