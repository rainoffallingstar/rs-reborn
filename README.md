# rvx

`rvx` is a small Go CLI that gives R scripts a `uv run`-style execution flow:

1. scan a script for `library()`, `require()`, `requireNamespace()`, `pkg::fn`, and `pkg:::fn`
2. merge detected packages with project-level dependencies declared in `rs.toml`
3. create a managed library directory under the local rs cache
4. install missing packages into that isolated library
5. optionally write a lock file with resolved package versions
6. execute the target script with `Rscript`

## Why this shape

`uv run` works because it wraps the interpreter launch with a dependency bootstrap. R can use the same idea if we treat package resolution as a pre-flight step instead of a manual setup step.

This prototype keeps the design intentionally small:

- the CLI is implemented in Go and has no third-party dependencies
- package detection is static and fast
- the runtime injects a temporary `R_PROFILE_USER` file before `Rscript` starts
- installed packages live in a cache-managed library instead of polluting the default user library
- project configuration is read from a lightweight top-level `rs.toml`
- lock state is written to `rs.lock.json`

## Usage

Build the CLI:

```bash
go build -o rvx ./cmd/rvx
```

`rs` remains available as a compatibility alias, but `rvx` is now the primary binary name to avoid conflicts with the traditional Unix/BSD `rs` command.

Use `rvx` as a Go library:

```go
import (
    "github.com/rainoffallingstar/rs-reborn/pkg/project"
    "github.com/rainoffallingstar/rs-reborn/pkg/rdeps"
    "github.com/rainoffallingstar/rs-reborn/pkg/rmanager"
    "github.com/rainoffallingstar/rs-reborn/pkg/runner"
)

cfg, _ := project.Load("rs.toml")
deps, _ := rdeps.FromFile("analysis.R")
rscript, _ := rmanager.ResolveVersionOrPath("4.5")
_ = runner.Run(runner.RunOptions{
    ScriptPath:  "analysis.R",
    RscriptPath: rscript,
})

_ = cfg
_ = deps
```

Public SDK packages currently live under:

- `github.com/rainoffallingstar/rs-reborn/pkg/project`
- `github.com/rainoffallingstar/rs-reborn/pkg/rdeps`
- `github.com/rainoffallingstar/rs-reborn/pkg/rmanager`
- `github.com/rainoffallingstar/rs-reborn/pkg/lockfile`
- `github.com/rainoffallingstar/rs-reborn/pkg/runner`

Install the latest published binary into `$HOME/.cargo/bin` on macOS or Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/rainoffallingstar/rs-reborn/main/install.sh | bash
```

Optional overrides:

```bash
RS_INSTALL_TAG=2026-03-30 curl -fsSL https://raw.githubusercontent.com/rainoffallingstar/rs-reborn/main/install.sh | bash
RS_INSTALL_DIR="$HOME/bin" curl -fsSL https://raw.githubusercontent.com/rainoffallingstar/rs-reborn/main/install.sh | bash
```

The install scripts fetch the matching `SHA256SUMS` asset from the release and verify the downloaded archive before extraction.

After install, use `rvx version` to confirm the release tag, commit, and build date baked into the binary.

Install the latest published Windows binary into `%USERPROFILE%\.cargo\bin` with PowerShell:

```powershell
irm https://raw.githubusercontent.com/rainoffallingstar/rs-reborn/main/install.ps1 | iex
$env:RS_INSTALL_TAG="2026-03-30"; irm https://raw.githubusercontent.com/rainoffallingstar/rs-reborn/main/install.ps1 | iex
```

Continuous integration:

- [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs `go test`, real-R CLI smoke coverage on Linux, macOS, and Windows x64, native-backend end-to-end coverage for CRAN, Bioconductor, local, and GitHub installs, compatibility coverage for the `pak` backend, and native multi-R manager integration checks
- [`.github/workflows/release.yml`](.github/workflows/release.yml) publishes date-tagged GitHub Release binaries after successful `main` or `master` CI runs; successful rebuilds later the same day reuse that date tag and refresh the assets
- the CI helper scripts live under [`scripts/ci/`](scripts/ci)

Performance benchmarking:

- installer microbenchmarks: [`scripts/bench/installer-bench.sh`](scripts/bench/installer-bench.sh)
- runner/logging microbenchmarks: [`scripts/bench/runner-bench.sh`](scripts/bench/runner-bench.sh)
- combined local baseline capture: [`scripts/bench/capture-baseline.sh`](scripts/bench/capture-baseline.sh), which also refreshes a latest-link alias and emits `benchmark.json`
- baseline diff helper: [`scripts/bench/diff-baseline.sh`](scripts/bench/diff-baseline.sh), which compares explicit bundles or the default latest/previous captures
- repeated baseline capture also emits a compact `DIFF.md`/`benchmark-diff.txt` pair when a previous capture is available
- benchmark notes and current local baseline: [`docs/benchmarks.md`](docs/benchmarks.md)
- optional CI benchmark job: manually trigger `CI` with `run_benchmarks=true` to upload a run-tagged benchmark artifact

Initialize a project:

```bash
./rvx init
./rvx init --cache-dir .rs-cache --lockfile rs.lock.json
./rvx init --rscript /Library/Frameworks/R.framework/Versions/4.4-arm64/Resources/bin/Rscript
./rvx init --from scripts/report.R
./rvx init --from scripts/report.R --include-bundled
./rvx init --from scripts/report.R --write-script-block
./rvx init --from scripts/a.R --from scripts/b.R
./rvx init --from-dir scripts/
./rvx init --from scripts/rnaseq.R --bioc-package Biostrings
./rvx init --from scripts/report.R --exclude dplyr --include cli
```

Add dependencies to `rs.toml`:

```bash
./rvx add jsonlite cli
./rvx add --bioc DESeq2
./rvx add --script scripts/report.R readr
./rvx add --source github --github-repo owner/mypkg --ref main mypkg
./rvx add --source git --url file:///path/to/repo --ref main --subdir pkg gitpkg
./rvx add --source local --path vendor/localpkg_0.1.0.tar.gz localpkg
```

Remove dependencies from `rs.toml`:

```bash
./rvx remove cli
./rvx remove --bioc DESeq2
./rvx remove --script scripts/report.R readr
```

Run a script:

```bash
./rvx run analysis.R
./rvx run --rscript /Library/Frameworks/R.framework/Versions/4.4-arm64/Resources/bin/Rscript analysis.R
./rvx run --package data.table --repo https://cloud.r-project.org analysis.R --input foo.csv
./rvx run --bioc-package Biostrings analysis.R
./rvx run --include cli --exclude dplyr analysis.R
./rvx run --locked analysis.R
./rvx run --frozen analysis.R
```

Inspect detected dependencies:

```bash
./rvx scan analysis.R
./rvx scan --installable analysis.R
./rvx scan --json analysis.R
```

Inspect the fully resolved dependency plan without installing:

```bash
./rvx list analysis.R
./rvx list --json analysis.R
./rvx list --include Biostrings --exclude dplyr analysis.R
```

Prune stale managed libraries from the cache:

```bash
./rvx prune --dry-run
./rvx prune
./rvx prune scripts/report.R
```

Open an interactive R shell in the managed environment:

```bash
./rvx shell analysis.R
./rvx shell --locked analysis.R
./rvx shell --package data.table analysis.R
```

Execute an inline R expression in the managed environment:

```bash
./rvx exec -e 'cat(.libPaths()[1], "\n")' analysis.R
./rvx exec --locked -e 'library(jsonlite); cat(packageVersion("jsonlite"), "\n")' analysis.R
```

Inspect cache locations and managed libraries:

```bash
./rvx cache dir
./rvx cache dir analysis.R
./rvx cache ls
./rvx cache ls --project-dir .
./rvx cache ls --json analysis.R
./rvx cache rm --project-dir . aaaaaaaaaaaaaaaa
./rvx cache rm .rs-cache/lib/aaaaaaaaaaaaaaaa
./rvx cache rm dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
```

Install dependencies and write or refresh the lockfile:

```bash
./rvx lock analysis.R
./rvx lock --bioc-package Biostrings analysis.R
```

Manage interpreter versions and selection:

```bash
./rvx r list
./rvx r install 4.4
./rvx r install --method source 4.4
./rvx r use 4.4
./rvx r which
```

`rvx` now includes a native multi-R manager on macOS, Linux, and Windows x64. `rvx r list` shows both `managed` and discovered `external` interpreters, `rvx r install` installs a user-local managed R, and `rvx r install --method auto|binary|source` lets you control artifact selection where supported. On Windows, native R installs are binary-only in this release, `rvx shell` prefers `Rterm.exe`, and source-based package installs from `local`, `git`, or `github` still require Rtools. When `Rscript` is missing, `rvx run`, `rvx exec`, `rvx shell`, `rvx lock`, and `rvx sync` print a next step by default and only auto-install R when you opt in with `RS_AUTO_INSTALL_R=1`. Set `RS_R_VERSION=4.4` if you want to pin the default bootstrap target on fresh machines.

Compatibility alias:

```bash
./rvx sync analysis.R
```

Validate the current environment against the lockfile:

```bash
./rvx check analysis.R
./rvx check --json analysis.R
```

Inspect prerequisites and diagnose configuration issues before install/run:

```bash
./rvx doctor analysis.R
./rvx doctor --include Biostrings --exclude dplyr analysis.R
./rvx doctor --json analysis.R
./rvx doctor --strict analysis.R
./rvx doctor --quiet analysis.R
./rvx doctor --summary-only analysis.R
./rvx doctor --verbose analysis.R
```

Use a custom cache location:

```bash
./rvx run --cache-dir ./.rs-cache analysis.R
```

Use a project config:

```toml
repo = "https://cloud.r-project.org"
cache_dir = ".rs-cache"
lockfile = "rs.lock.json"
r_version = "4.4"
rscript = "tools/Rscript-4.4"
toolchain_prefixes = [".toolchain", "/opt/demo"]
pkg_config_path = [".toolchain/lib/pkgconfig"]
packages = ["jsonlite", "cli"]
bioc_packages = ["Biostrings"]

[sources."mypkg"]
type = "github"
host = "github.example.com/api/v3"
repo = "owner/mypkg"
ref = "main"
subdir = "pkg"
token_env = "GH_ENTERPRISE_PAT"

[sources."localpkg"]
type = "local"
path = "vendor/localpkg_0.1.0.tar.gz"

[sources."gitpkg"]
type = "git"
url = "file:///path/to/repo"
ref = "main"
subdir = "pkg"

[scripts."scripts/report.R"]
repo = "https://cran.rstudio.com"
packages = ["ggplot2", "readr", "mypkg", "localpkg", "gitpkg"]
bioc_packages = ["DESeq2"]

[scripts."scripts/report.R".sources."mypkg"]
type = "github"
repo = "owner/report-specific-pkg"
ref = "feature-branch"
```

The root block defines project defaults. A `[scripts."relative/path.R"]` block can override or extend settings for one script, including `rscript` when one entrypoint must pin a specific interpreter path and `r_version` when one entrypoint should target a specific R line such as `4.4`. When both are set, `rs` expects the selected interpreter to match `r_version`. A `[sources."packageName"]` block declares a project-wide non-CRAN installation source, and `[scripts."relative/path.R".sources."packageName"]` overrides that source for one script. `rs.toml` is validated when it is loaded, so malformed sections, contradictory source fields, repeated keys, and common typos now fail early with the offending section and line, plus supported-key or close-match hints when the fix is obvious.

For rootless or user-local source builds, you can keep toolchain hints in `rs.toml`:

```toml
toolchain_prefixes = [
  "/home/you/.local",
  "/home/you/.local/share/rattler/envs/rs-sysdeps",
]
pkg_config_path = [
  "/home/you/.local/lib/pkgconfig",
  "/home/you/.local/share/pkgconfig",
]
```

or provide them ad hoc through environment variables:

```bash
export RS_TOOLCHAIN_PREFIXES="$HOME/.local:$HOME/.local/share/rattler/envs/rs-sysdeps"
export RS_PKG_CONFIG_PATH="$HOME/.local/lib/pkgconfig:$HOME/.local/share/pkgconfig"

./rvx r install 4.4.3 --method source
./rvx run analysis.R
```

`rvx` expands each `toolchain_prefixes` entry into the usual `bin`, `include`, and `lib` locations and injects the resulting `PATH`, `CPPFLAGS`, `LDFLAGS`, `LIBRARY_PATH`, runtime library path, and `PKG_CONFIG_PATH` automatically for native R builds and source package installs.

The detailed rootless cookbook lives at [`docs/rootless-toolchains.md`](docs/rootless-toolchains.md). It includes copy-paste examples for `enva`, Homebrew-in-home, compatibility conda-family layouts, and Spack, and explains the current product boundary clearly: `rvx` auto-detects and auto-uses an existing user-local prefix by default, and with `--bootstrap-toolchain` it can invoke a supported external manager to create one for you.

For faster bootstrapping, `rvx init` also supports `--toolchain-preset auto|enva|micromamba|mamba|conda|homebrew|spack`, which seeds `toolchain_prefixes` and `pkg_config_path` with a common user-local template. `auto` reuses the top recommendation from `rvx toolchain detect`, so if one of the built-in layouts already exists under your home directory you can wire it into a new project in one step. You can still append explicit `--toolchain-prefix` or `--pkg-config-path` values on the same command.

`rvx toolchain detect` now also prints preset-specific setup hints such as an `enva` bootstrap command, a conda-family environment creation command, or the matching Homebrew/Spack follow-up, so rootless users can move from discovery to a concrete user-local prefix more directly. The longer cookbook lives in [docs/rootless-toolchains.md](docs/rootless-toolchains.md).

When no explicit `toolchain_prefixes` / `pkg_config_path` config or `RS_TOOLCHAIN_PREFIXES` / `RS_PKG_CONFIG_PATH` environment is present, native source-build paths and runtime package-install environments now also auto-detect a recommended existing rootless prefix and use it automatically. Explicit config still wins.

If you want `rvx` to first parse one script, then turn the detected package set into a rootless system-dependency plan, use:

```bash
rvx toolchain plan analysis.R
rvx toolchain plan --preset enva --phase base analysis.R
rvx toolchain init --preset enva analysis.R
rvx toolchain init --preset enva --phase base analysis.R
```

`plan` reports the base build-tool packages plus any extra system-library packages inferred from the resolved dependency set. `init` executes that plan through the selected preset, so you can either bootstrap everything in one shot with `--phase full` or seed just the compiler/toolchain floor first with `--phase base`.

If no suitable prefix exists yet and you want `rvx` to create one for you, use a command that accepts `--bootstrap-toolchain`. `auto` bootstrap now only attempts first-class managers such as `enva`, plus already-detected Homebrew and Spack layouts. Compatibility conda-family presets remain available when selected explicitly:

```bash
rvx run --bootstrap-toolchain analysis.R
rvx lock --bootstrap-toolchain analysis.R
rvx check --bootstrap-toolchain analysis.R
rvx doctor --toolchain-only --bootstrap-toolchain
rvx r install 4.5.3 --method source --bootstrap-toolchain
```

If you want to inspect a preset before writing anything, use:

```bash
rvx toolchain template enva
rvx toolchain template mamba
rvx toolchain template conda --check
rvx toolchain template homebrew --format env
rvx toolchain template spack --check
rvx toolchain detect
rvx toolchain bootstrap auto
```

## Examples

The [`examples/`](examples) directory includes three small projects you can copy or run in place:

- [`examples/cran-basic/`](examples/cran-basic) shows a simple CRAN-only script with a local `rs.toml`
- [`examples/bioc-rnaseq/`](examples/bioc-rnaseq) shows a Bioconductor-heavy RNA-seq style workflow
- [`examples/multi-script/`](examples/multi-script) shows one project with per-script dependency blocks

Try them from the repository root:

```bash
./rvx scan examples/cran-basic/analysis.R
./rvx list examples/bioc-rnaseq/rnaseq.R
./rvx doctor --json examples/multi-script/scripts/report.R
./rvx shell examples/cran-basic/analysis.R
```

Each example keeps its own `cache_dir` and `lockfile` under that example directory so you can experiment without affecting another project.

## Design Doc

The fuller design write-up lives at [`docs/design.md`](docs/design.md). It captures the current R-only scope, command model, config merge rules, runtime bootstrap lifecycle, and the intended evolution path from prototype to a more production-ready tool.

## Roadmap

The staged delivery plan lives at [`docs/roadmap.md`](docs/roadmap.md). It breaks the next work into near-term, mid-term, and later milestones so implementation can stay focused without losing the larger direction.

## Architecture

### 1. CLI layer

`cmd/rvx/main.go` delegates to `internal/cli`, and `cmd/rs/main.go` remains a compatibility alias. The CLI currently exposes:

- `rvx init`
- `rvx add <pkg>`
- `rvx remove <pkg>`
- `rvx lock <script.R>`
- `rvx list <script.R>`
- `rvx prune`
- `rvx shell <script.R>`
- `rvx exec -e ... <script.R>`
- `rvx cache dir`
- `rvx cache ls`
- `rvx cache rm`
- `rvx run <script.R>`
- `rvx scan <script.R>`
- `rvx sync <script.R>`
- `rvx check <script.R>`
- `rvx doctor <script.R>`

### 2. Project config

`internal/project` walks upward from the script directory and loads the first `rs.toml` it finds. That file can currently declare:

- `repo`
- `cache_dir`
- `lockfile`
- `rscript`
- `r_version`
- `packages`
- `bioc_packages`
- `[sources."packageName"]`
- `[scripts."relative/path.R"]`
- `[scripts."relative/path.R".sources."packageName"]`

Script blocks are matched against the script path relative to the directory that contains `rs.toml`. Script-specific packages are merged with the project defaults, while scalar settings like `repo`, `cache_dir`, `lockfile`, `rscript`, and `r_version` override the default value for that one script. Source blocks are keyed by package name, and script-local source blocks override project-wide ones.

`rvx init` writes a starter `rs.toml`. With `--from path/to/script.R`, it first scans the script and seeds the config with detected packages. `--from-dir path/to/scripts/` does the same for every `.R` and `.Rscript` under a directory, skipping `.git` and `.rs-cache`. By default it drops R bundled base/recommended packages such as `stats` and `utils`, so the generated config only keeps installable dependencies. It also heuristically moves a curated set of common Bioconductor packages such as `DESeq2`, `Biostrings`, and `SummarizedExperiment` into `bioc_packages`. Pass `--include-bundled` if you want the raw scan result, `--exclude <pkg>` to remove an unwanted detected dependency, `--include <pkg>` to add a missing project-level dependency, and `--bioc-package <name>` to add an explicit project-level Bioconductor dependency. With one `--from`, the detected packages are written to the root `packages` array by default; `--write-script-block` instead writes them under `[scripts."relative/path.R"]`. When you pass multiple `--from` flags, use `--from-dir`, or otherwise resolve to multiple scripts, `rvx init` automatically writes one script block per scanned file.

`rvx add` and `rvx remove` edit that file and can target:

- project-level `packages`
- project-level `bioc_packages`
- one `[scripts."relative/path.R"]` block via `--script`
- one custom source entry via `--source github|git|local`

When you remove a package from `packages`, `rvx remove` also removes the same-scope `[sources."pkg"]` or `[scripts."...".sources."pkg"]` block if one exists. Empty script sections are omitted when the file is rewritten.

When `rvx add` or `rvx remove` rewrites `rs.toml`, it now preserves the file preamble, comments attached to existing sections and fields, trailing inline comments, and the existing root-key, top-level source/script, root-source, and script-block ordering where possible, so routine edits are less disruptive in version control. When a package removal drops an otherwise-empty script or source section, its attached comments are carried forward to the next surviving section when possible instead of being silently discarded.
This rewrite path is still intentionally conservative rather than byte-for-byte faithful: the goal is low-diff, predictable edits for normal `init`/`add`/`remove` workflows, not a full formatting-preserving editor for every hand-crafted whitespace pattern.

### 3. Dependency detection

`internal/rdeps` performs a lightweight static scan. `rvx scan --json` exposes that result in a machine-friendly shape, and `rvx scan --installable` filters out bundled base/recommended packages when you only care about installable dependencies. It is enough for common scripts, but not for fully dynamic cases like:

- `library(pkg_name_from_env)`
- custom package loaders
- GitHub/Bioconductor remotes

The JSON form keeps `packages` for the full detected set and also includes `cran_packages` plus `bioc_packages` to save downstream callers from re-classifying common Bioconductor packages. `rvx list --json` similarly includes `included_cran_packages`, `included_bioc_packages`, and `excluded_packages` so automation can explain how the final plan was adjusted. The same known-package split is also used by `rvx list` and the runtime resolver, so direct `rvx run` and `rvx lock` calls treat packages like `DESeq2` as Bioconductor dependencies even without an existing `rs.toml`.

For those cases, `--package <name>` and `--bioc-package <name>` are the escape hatches today, and `[sources."pkg"]` lets you declare non-CRAN installation sources.

`rvx list` shows the post-merge dependency plan after combining:

- statically detected imports from the script
- project-level `packages` and `bioc_packages`
- script-level overrides and additions
- any `--package` or `--bioc-package` flags passed on the command line

`rvx prune` removes hashed library directories under the managed cache that are no longer referenced by the current project scripts. It also prunes the shared per-user package store under the global rvx cache root based on `last_used_at`, so stale cross-project package snapshots eventually age out. With `--dry-run`, it reports what would be removed without deleting anything.

`rvx shell` resolves the same dependency plan as `rvx run`, prepares the managed library, and then launches interactive `R` with that library injected into `.libPaths()`. When `rscript` is pinned in config or overridden with `--rscript`, `rvx shell` first tries the sibling `R` binary from the same installation before falling back to `R` on `PATH`.

`rvx exec` resolves the same dependency plan, then runs a one-off `Rscript -e` expression inside that managed environment. It is useful for quick checks, CI probes, and debugging without creating a temporary script file.

`rvx cache dir` prints the project managed-cache root. `rvx cache ls` lists hashed managed library directories under that cache and, when given a script or project scope, marks which ones are currently active versus stale. It also reports the shared per-user package store root and its entries, which are used to seed project-local managed libraries across different repositories and local `.rs-cache` directories. That shared package store is a best-effort cache layer: if it cannot be updated, `rvx` keeps the project-local managed library as the runtime truth and only surfaces a warning. `rvx cache rm` removes one managed library by hash or path, and it can also remove one shared package-store entry by its 64-character hash or absolute path.

### 4. Runtime bootstrap

`internal/runner` computes a cache key from:

- absolute script path
- resolved CRAN package list
- resolved Bioconductor package list
- selected CRAN mirror
- custom source identity, including GitHub/git location metadata and a content fingerprint for `type = "local"` sources
- the inspected R runtime metadata (`Rscript` path, R version, platform, architecture, OS, and package type)

It then writes a temporary `R_PROFILE_USER` file that:

- prepends the managed library to `.libPaths()`
- keeps the runtime library wiring in one place before `Rscript` starts

Because the bootstrap happens before `Rscript script.R` starts, the script still sees normal `commandArgs()` behavior.

Package installation is now orchestrated primarily from Go. The `native` backend resolves CRAN and Bioconductor indexes, clones or checks out `git` and GitHub sources, computes local-source fingerprints, writes `.rs-source-meta`, and then shells out to `R CMD INSTALL` for the final install step.

Today the native backend covers the package source types that `rvx` exposes in normal use:

- CRAN packages
- Bioconductor packages
- `type = "local"` sources
- `type = "git"` sources
- standard `type = "github"` sources, including custom GitHub Enterprise hosts and `token_env` authentication

That means the default install path no longer depends on `pak` for routine `lock`, `run`, `sync`, or `check` flows. `pak` is kept as an explicit compatibility backend and CI comparison path while the Go-native planner and installer continue to mature.

By default the installer backend runs in `auto` mode, which now means `native`. You can override that behavior by setting `RS_INSTALL_BACKEND=native` or `RS_INSTALL_BACKEND=pak` before invoking `rvx`. `pak` remains available as a transitional compatibility backend, but it is no longer part of the default install path.

In practice the backend modes now mean:

- `auto`: use the Go-native installer
- `native`: require the Go-native installer and fail if it cannot complete
- `pak`: use `pak` explicitly as a compatibility backend

### 5. Lock file

`rvx lock`, `rvx sync`, and `rvx run` all write `rs.lock.json` after dependency resolution succeeds. `rvx check` and `rvx run --frozen` validate that file. The lockfile records:

- the script path
- the CRAN mirror
- the managed library path
- the `Rscript` interpreter path
- the R version, platform, architecture, OS, and package type
- each resolved package with version, source, and custom source host/location/ref/subdir when applicable
- for GitHub and git installs, the lockfile records the resolved commit when that information is available
- for `type = "local"` sources, the lockfile also records a stable source fingerprint plus fingerprint kind so local tarball or directory changes can be detected even when the path stays the same

That still is not a full reproducibility story, but it is now enough to detect many "works on my machine" differences between environments.

### 6. Locked And Frozen Execution

`rvx run --locked script.R` requires a matching lockfile, but can still install missing packages into the managed library if the lockfile inputs are valid. It never rewrites the lockfile.

`rvx run --frozen script.R` is stricter and refuses to mutate dependencies at all. It validates:

- the script path, repo, and managed library path
- the current `Rscript` interpreter and R runtime metadata
- the expected dependency set derived from the script and `rs.toml`
- custom source identity drift, including local source fingerprints for `type = "local"` packages
- the actual installed package versions in the managed library
- whether the script or `rs.toml` changed after the lockfile was generated

If any of those drift, the command fails and points you back to `rvx lock`.
The human-readable failure output now also adds short grouped summaries for input drift and installed-library drift so you can quickly see whether the mismatch is coming from script/config changes, runtime changes, dependency-set changes, or source differences.

`rvx check --json` reports the same drift in a machine-friendly shape. Alongside the top-level `issues` array, it now also splits failures into `planning_issues`, `input_issues`, and `installed_issues`, plus installed-side buckets such as `installed_missing_packages`, `installed_version_issues`, and `installed_source_issues`. It also exposes `planning_issue_details` and `installed_issue_details` with structured fields such as `kind`, `package`, `field`, `message`, and for dependency conflicts `dependency_path`, `constraint`, `selected_version`, and `required_by`, so automation can avoid reparsing human text.

### 7. Doctor Diagnostics

`rvx doctor script.R` is a preflight command for debugging environment setup before `run` or `sync`. It reports:

- whether the selected `Rscript` from `--rscript`, `rs.toml`, or `PATH` is available
- whether `git` is available when `type = "git"` sources are configured
- whether local source files and local `file://` git repositories exist
- whether required `token_env` variables are present for private GitHub installs
- which repo, lockfile path, and managed library path will be used
- whether the lockfile or managed library has not been created yet

It prints `[info]`, `[warn]`, and `[error]` lines. Missing lockfiles and missing managed libraries are warnings, because a first-time `rvx sync` or `rvx run` can still create them. Blocking misconfiguration such as a missing selected `Rscript`, missing local source tarball, missing private token env, or broken `toolchain_prefixes` / `pkg_config_path` entries returns a non-zero exit code. When a rootless toolchain is configured, `doctor` also warns if `pkg-config` itself is still missing from the effective `PATH`.

`rvx doctor --json` keeps the flat `warnings` and `errors` arrays, and also groups them into `setup_errors`, `source_errors`, `network_errors`, `runtime_errors`, `lock_warnings`, and `cache_warnings` so automation can distinguish prerequisites, source misconfiguration, remote-access failures, and dependency-state warnings. It also exposes `error_details` and `warning_details` with structured `category`, `kind`, `message`, and optional path/package/env fields, plus `status` and `summary` so callers can quickly judge whether the report is `ok`, `warning`, or `error` without recomputing aggregate counts. The report now also includes `toolchain_prefixes` and `pkg_config_path`, so rootless and user-local build setups can be inspected directly from automation. `system_hints` and `system_hint_details` remain available for packages that commonly need external libraries, SDKs, or toolchains, and now cover a broader set of native-library-heavy packages such as `stringi`, `odbc`, and `git2r` in addition to the earlier `curl`/`xml2`/geospatial families.

For rootless-only preflight, `rvx doctor --toolchain-only [path/to/script.R|path/to/project]` skips dependency scanning and lockfile checks, then validates just the effective toolchain prefixes and pkg-config paths. It uses project config when an `rs.toml` is present, or falls back to `RS_TOOLCHAIN_PREFIXES` / `RS_PKG_CONFIG_PATH` when you are validating an ad hoc user-local build environment.

The doctor JSON report now also includes `toolchain_path`, `toolchain_cppflags`, `toolchain_ldflags`, and `toolchain_pkg_config_path`, which show the rootless build contribution that `rvx` would inject on top of your existing environment.

The doctor output now also includes explicit next-step guidance. In human-readable mode it prints `[next]` lines such as `rvx lock <script>` or `rvx run <script>` when the environment is merely missing a lockfile or managed library, and `rvx doctor --json` exposes the same guidance under structured `next_steps` entries with `category`, `kind`, `message`, optional `command`, optional `note`, optional `preset`, and a `blocking` flag so automation can distinguish hard prerequisites from optional follow-up actions.

For rootless/source-build issues, those next steps now also point at `rvx toolchain detect`, `rvx toolchain template ...`, and `rvx doctor --toolchain-only ...` so the recovery flow stays inside `rvx` instead of requiring guesswork.

The human-readable `rvx doctor` output now also ends with a compact `[summary]` line, for example `status=warning | errors=0 | warnings=2 | hints=2 | next=4 | blocking_next=0`, so logs are easy to scan without reparsing every line above it.

If you only want that compact status line, `rvx doctor --summary-only` suppresses the detailed `[info]`, `[warn]`, `[hint]`, and `[next]` lines and prints just the final summary. It still honors the normal exit behavior, including `--strict`.

If you still want warnings, hints, next steps, and the summary, but do not want the verbose environment preamble, `rvx doctor --quiet` hides only the `[info]` lines. Those `[info]` lines now also show the effective `toolchain prefixes` and `pkg-config path`, which makes rootless source-build debugging much easier.

For CI or gating scripts, `rvx doctor --strict` exits non-zero unless the report status is exactly `ok`. That means warnings such as a missing lockfile or missing managed library can be treated as failures when you want a fully prepared environment before continuing. By convention, normal blocking doctor failures exit with code `1`, while `--strict` warning failures exit with code `2`.

## What to build next

If you want this to grow from prototype into a real tool, the next steps are:

1. keep tightening `rs.toml` rewrite fidelity in edge cases such as exact blank-line placement
2. broaden mixed-source drift coverage and immutable custom-source verification
3. enrich `rvx doctor` with more system dependency hints for packages that need external libraries or compilers
4. add finer lockfile update policies such as partial refresh modes and selective package relocking
5. keep tightening cache and release observability so package reuse and published artifacts stay easy to audit

## Notes

- `rvx run`, `rvx exec`, `rvx shell`, `rvx lock`, and `rvx sync` bootstrap R through the native manager on macOS, Linux, and Windows; by default they print next steps, and if you set `RS_AUTO_INSTALL_R=1` they install the requested target
- `rvx r install <version> --method auto|binary|source` controls how managed R versions are installed; `auto` is the default, Arch Linux prefers source builds in that mode, and Windows currently supports `auto|binary`
- Windows x64 is now a supported path for runtime commands and the native R manager; Windows ARM64 binaries still ship as secondary artifacts with lighter validation depth
- Windows CRAN/Bioconductor installs are binary-first, while `local`, `git`, and `github` package installs remain source-based and may require Rtools
- you can still pin a project interpreter with `rscript = "..."`, override it with `--rscript`, or use the explicit `rvx r ...` commands when you want full control
- package installation supports CRAN, explicitly declared Bioconductor packages, GitHub sources, and local package sources
- package installation also supports generic `git` sources with `url`, `ref`, and `subdir`
- GitHub tokens are referenced by environment variable name via `token_env`; token values are never written to the lockfile
- the tool does not yet solve system-level dependencies required by some R packages
- the current `rs.toml` parser is deliberately minimal and supports only root keys, `[sources."..."]`, `[scripts."..."]`, and `[scripts."...".sources."..."]` blocks
