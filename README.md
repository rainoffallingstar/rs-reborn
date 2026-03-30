# rs

`rs` is a small Go CLI that gives R scripts a `uv run`-style execution flow:

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
go build -o rs ./cmd/rs
```

Install the latest published binary into `$HOME/.cargo/bin` on macOS or Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/rainoffallingstar/rs-reborn/main/install.sh | bash
```

Optional overrides:

```bash
RS_INSTALL_TAG=2026-03-30 curl -fsSL https://raw.githubusercontent.com/rainoffallingstar/rs-reborn/main/install.sh | bash
RS_INSTALL_DIR="$HOME/bin" curl -fsSL https://raw.githubusercontent.com/rainoffallingstar/rs-reborn/main/install.sh | bash
```

Continuous integration:

- [`.github/workflows/ci.yml`](/Volumes/DataCenter_01/GitHub/gr/.github/workflows/ci.yml) runs `go test`, real-R CLI smoke coverage on Linux and macOS, native-backend end-to-end coverage for CRAN, Bioconductor, local, and GitHub installs, compatibility coverage for the `pak` backend, and native multi-R manager integration checks
- [`.github/workflows/release.yml`](/Volumes/DataCenter_01/GitHub/gr/.github/workflows/release.yml) publishes date-tagged GitHub Release binaries after successful `main` or `master` CI runs; successful rebuilds later the same day reuse that date tag and refresh the assets
- the CI helper scripts live under [`scripts/ci/`](/Volumes/DataCenter_01/GitHub/gr/scripts/ci)

Initialize a project:

```bash
./rs init
./rs init --cache-dir .rs-cache --lockfile rs.lock.json
./rs init --rscript /Library/Frameworks/R.framework/Versions/4.4-arm64/Resources/bin/Rscript
./rs init --from scripts/report.R
./rs init --from scripts/report.R --include-bundled
./rs init --from scripts/report.R --write-script-block
./rs init --from scripts/a.R --from scripts/b.R
./rs init --from-dir scripts/
./rs init --from scripts/rnaseq.R --bioc-package Biostrings
./rs init --from scripts/report.R --exclude dplyr --include cli
```

Add dependencies to `rs.toml`:

```bash
./rs add jsonlite cli
./rs add --bioc DESeq2
./rs add --script scripts/report.R readr
./rs add --source github --github-repo owner/mypkg --ref main mypkg
./rs add --source git --url file:///path/to/repo --ref main --subdir pkg gitpkg
./rs add --source local --path vendor/localpkg_0.1.0.tar.gz localpkg
```

Remove dependencies from `rs.toml`:

```bash
./rs remove cli
./rs remove --bioc DESeq2
./rs remove --script scripts/report.R readr
```

Run a script:

```bash
./rs run analysis.R
./rs run --rscript /Library/Frameworks/R.framework/Versions/4.4-arm64/Resources/bin/Rscript analysis.R
./rs run --package data.table --repo https://cloud.r-project.org analysis.R --input foo.csv
./rs run --bioc-package Biostrings analysis.R
./rs run --include cli --exclude dplyr analysis.R
./rs run --locked analysis.R
./rs run --frozen analysis.R
```

Inspect detected dependencies:

```bash
./rs scan analysis.R
./rs scan --installable analysis.R
./rs scan --json analysis.R
```

Inspect the fully resolved dependency plan without installing:

```bash
./rs list analysis.R
./rs list --json analysis.R
./rs list --include Biostrings --exclude dplyr analysis.R
```

Prune stale managed libraries from the cache:

```bash
./rs prune --dry-run
./rs prune
./rs prune scripts/report.R
```

Open an interactive R shell in the managed environment:

```bash
./rs shell analysis.R
./rs shell --locked analysis.R
./rs shell --package data.table analysis.R
```

Execute an inline R expression in the managed environment:

```bash
./rs exec -e 'cat(.libPaths()[1], "\n")' analysis.R
./rs exec --locked -e 'library(jsonlite); cat(packageVersion("jsonlite"), "\n")' analysis.R
```

Inspect cache locations and managed libraries:

```bash
./rs cache dir
./rs cache dir analysis.R
./rs cache ls
./rs cache ls --project-dir .
./rs cache ls --json analysis.R
./rs cache rm --project-dir . aaaaaaaaaaaaaaaa
./rs cache rm .rs-cache/lib/aaaaaaaaaaaaaaaa
```

Install dependencies and write or refresh the lockfile:

```bash
./rs lock analysis.R
./rs lock --bioc-package Biostrings analysis.R
```

Manage interpreter versions and selection:

```bash
./rs r list
./rs r install 4.4
./rs r install --method source 4.4
./rs r use 4.4
./rs r which
```

On macOS and Linux, `rs` now includes a native multi-R manager. `rs r list` shows both `managed` and discovered `external` interpreters, `rs r install` installs a user-local managed R, and `rs r install --method auto|binary|source` lets you control artifact selection. When `Rscript` is missing, `rs run`, `rs exec`, `rs shell`, `rs lock`, and `rs sync` print a next step by default and only auto-install R when you opt in with `RS_AUTO_INSTALL_R=1`. Set `RS_R_VERSION=4.4` if you want to pin the default bootstrap target on fresh machines. Windows binaries may still be published, but the native R manager is currently a macOS/Linux-first path; on Windows the safest v1 flow is still to point `rs` at an explicit `Rscript`.

Compatibility alias:

```bash
./rs sync analysis.R
```

Validate the current environment against the lockfile:

```bash
./rs check analysis.R
./rs check --json analysis.R
```

Inspect prerequisites and diagnose configuration issues before install/run:

```bash
./rs doctor analysis.R
./rs doctor --include Biostrings --exclude dplyr analysis.R
./rs doctor --json analysis.R
./rs doctor --strict analysis.R
./rs doctor --quiet analysis.R
./rs doctor --summary-only analysis.R
./rs doctor --verbose analysis.R
```

Use a custom cache location:

```bash
./rs run --cache-dir ./.rs-cache analysis.R
```

Use a project config:

```toml
repo = "https://cloud.r-project.org"
cache_dir = ".rs-cache"
lockfile = "rs.lock.json"
r_version = "4.4"
rscript = "tools/Rscript-4.4"
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

## Examples

The [`examples/`](/Volumes/DataCenter_01/GitHub/gr/examples) directory includes three small projects you can copy or run in place:

- [`examples/cran-basic/`](/Volumes/DataCenter_01/GitHub/gr/examples/cran-basic) shows a simple CRAN-only script with a local `rs.toml`
- [`examples/bioc-rnaseq/`](/Volumes/DataCenter_01/GitHub/gr/examples/bioc-rnaseq) shows a Bioconductor-heavy RNA-seq style workflow
- [`examples/multi-script/`](/Volumes/DataCenter_01/GitHub/gr/examples/multi-script) shows one project with per-script dependency blocks

Try them from the repository root:

```bash
./rs scan examples/cran-basic/analysis.R
./rs list examples/bioc-rnaseq/rnaseq.R
./rs doctor --json examples/multi-script/scripts/report.R
./rs shell examples/cran-basic/analysis.R
```

Each example keeps its own `cache_dir` and `lockfile` under that example directory so you can experiment without affecting another project.

## Design Doc

The fuller design write-up lives at [`docs/design.md`](/Volumes/DataCenter_01/GitHub/gr/docs/design.md). It captures the current R-only scope, command model, config merge rules, runtime bootstrap lifecycle, and the intended evolution path from prototype to a more production-ready tool.

## Roadmap

The staged delivery plan lives at [`docs/roadmap.md`](/Volumes/DataCenter_01/GitHub/gr/docs/roadmap.md). It breaks the next work into near-term, mid-term, and later milestones so implementation can stay focused without losing the larger direction.

## Architecture

### 1. CLI layer

`cmd/rs/main.go` delegates to `internal/cli`, which currently exposes:

- `rs init`
- `rs add <pkg>`
- `rs remove <pkg>`
- `rs lock <script.R>`
- `rs list <script.R>`
- `rs prune`
- `rs shell <script.R>`
- `rs exec -e ... <script.R>`
- `rs cache dir`
- `rs cache ls`
- `rs cache rm`
- `rs run <script.R>`
- `rs scan <script.R>`
- `rs sync <script.R>`
- `rs check <script.R>`
- `rs doctor <script.R>`

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

`rs init` writes a starter `rs.toml`. With `--from path/to/script.R`, it first scans the script and seeds the config with detected packages. `--from-dir path/to/scripts/` does the same for every `.R` and `.Rscript` under a directory, skipping `.git` and `.rs-cache`. By default it drops R bundled base/recommended packages such as `stats` and `utils`, so the generated config only keeps installable dependencies. It also heuristically moves a curated set of common Bioconductor packages such as `DESeq2`, `Biostrings`, and `SummarizedExperiment` into `bioc_packages`. Pass `--include-bundled` if you want the raw scan result, `--exclude <pkg>` to remove an unwanted detected dependency, `--include <pkg>` to add a missing project-level dependency, and `--bioc-package <name>` to add an explicit project-level Bioconductor dependency. With one `--from`, the detected packages are written to the root `packages` array by default; `--write-script-block` instead writes them under `[scripts."relative/path.R"]`. When you pass multiple `--from` flags, use `--from-dir`, or otherwise resolve to multiple scripts, `rs init` automatically writes one script block per scanned file.

`rs add` and `rs remove` edit that file and can target:

- project-level `packages`
- project-level `bioc_packages`
- one `[scripts."relative/path.R"]` block via `--script`
- one custom source entry via `--source github|git|local`

When you remove a package from `packages`, `rs remove` also removes the same-scope `[sources."pkg"]` or `[scripts."...".sources."pkg"]` block if one exists. Empty script sections are omitted when the file is rewritten.

When `rs add` or `rs remove` rewrites `rs.toml`, it now preserves the file preamble, comments attached to existing sections and fields, trailing inline comments, and the existing root-key, top-level source/script, root-source, and script-block ordering where possible, so routine edits are less disruptive in version control. When a package removal drops an otherwise-empty script or source section, its attached comments are carried forward to the next surviving section when possible instead of being silently discarded.
This rewrite path is still intentionally conservative rather than byte-for-byte faithful: the goal is low-diff, predictable edits for normal `init`/`add`/`remove` workflows, not a full formatting-preserving editor for every hand-crafted whitespace pattern.

### 3. Dependency detection

`internal/rdeps` performs a lightweight static scan. `rs scan --json` exposes that result in a machine-friendly shape, and `rs scan --installable` filters out bundled base/recommended packages when you only care about installable dependencies. It is enough for common scripts, but not for fully dynamic cases like:

- `library(pkg_name_from_env)`
- custom package loaders
- GitHub/Bioconductor remotes

The JSON form keeps `packages` for the full detected set and also includes `cran_packages` plus `bioc_packages` to save downstream callers from re-classifying common Bioconductor packages. `rs list --json` similarly includes `included_cran_packages`, `included_bioc_packages`, and `excluded_packages` so automation can explain how the final plan was adjusted. The same known-package split is also used by `rs list` and the runtime resolver, so direct `rs run` and `rs lock` calls treat packages like `DESeq2` as Bioconductor dependencies even without an existing `rs.toml`.

For those cases, `--package <name>` and `--bioc-package <name>` are the escape hatches today, and `[sources."pkg"]` lets you declare non-CRAN installation sources.

`rs list` shows the post-merge dependency plan after combining:

- statically detected imports from the script
- project-level `packages` and `bioc_packages`
- script-level overrides and additions
- any `--package` or `--bioc-package` flags passed on the command line

`rs prune` removes hashed library directories under the managed cache that are no longer referenced by the current project scripts. With `--dry-run`, it reports what would be removed without deleting anything.

`rs shell` resolves the same dependency plan as `rs run`, prepares the managed library, and then launches interactive `R` with that library injected into `.libPaths()`. When `rscript` is pinned in config or overridden with `--rscript`, `rs shell` first tries the sibling `R` binary from the same installation before falling back to `R` on `PATH`.

`rs exec` resolves the same dependency plan, then runs a one-off `Rscript -e` expression inside that managed environment. It is useful for quick checks, CI probes, and debugging without creating a temporary script file.

`rs cache dir` prints the cache root. `rs cache ls` lists hashed managed library directories under that cache and, when given a script or project scope, marks which ones are currently active versus stale. `rs cache rm` removes one managed library by hash or by explicit path, and only accepts directories that match the managed `<cache>/lib/<16-hex>` layout.

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

Today the native backend covers the package source types that `rs` exposes in normal use:

- CRAN packages
- Bioconductor packages
- `type = "local"` sources
- `type = "git"` sources
- standard `type = "github"` sources, including custom GitHub Enterprise hosts and `token_env` authentication

That means the default install path no longer depends on `pak` for routine `lock`, `run`, `sync`, or `check` flows. `pak` is kept as an explicit compatibility backend and CI comparison path while the Go-native planner and installer continue to mature.

By default the installer backend runs in `auto` mode, which now means `native`. You can override that behavior by setting `RS_INSTALL_BACKEND=native` or `RS_INSTALL_BACKEND=pak` before invoking `rs`. `pak` remains available as a transitional compatibility backend, but it is no longer part of the default install path.

In practice the backend modes now mean:

- `auto`: use the Go-native installer
- `native`: require the Go-native installer and fail if it cannot complete
- `pak`: use `pak` explicitly as a compatibility backend

### 5. Lock file

`rs lock`, `rs sync`, and `rs run` all write `rs.lock.json` after dependency resolution succeeds. `rs check` and `rs run --frozen` validate that file. The lockfile records:

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

`rs run --locked script.R` requires a matching lockfile, but can still install missing packages into the managed library if the lockfile inputs are valid. It never rewrites the lockfile.

`rs run --frozen script.R` is stricter and refuses to mutate dependencies at all. It validates:

- the script path, repo, and managed library path
- the current `Rscript` interpreter and R runtime metadata
- the expected dependency set derived from the script and `rs.toml`
- custom source identity drift, including local source fingerprints for `type = "local"` packages
- the actual installed package versions in the managed library
- whether the script or `rs.toml` changed after the lockfile was generated

If any of those drift, the command fails and points you back to `rs lock`.
The human-readable failure output now also adds short grouped summaries for input drift and installed-library drift so you can quickly see whether the mismatch is coming from script/config changes, runtime changes, dependency-set changes, or source differences.

`rs check --json` reports the same drift in a machine-friendly shape. Alongside the top-level `issues` array, it now also splits failures into `planning_issues`, `input_issues`, and `installed_issues`, plus installed-side buckets such as `installed_missing_packages`, `installed_version_issues`, and `installed_source_issues`. It also exposes `planning_issue_details` and `installed_issue_details` with structured fields such as `kind`, `package`, `field`, `message`, and for dependency conflicts `dependency_path`, `constraint`, `selected_version`, and `required_by`, so automation can avoid reparsing human text.

### 7. Doctor Diagnostics

`rs doctor script.R` is a preflight command for debugging environment setup before `run` or `sync`. It reports:

- whether the selected `Rscript` from `--rscript`, `rs.toml`, or `PATH` is available
- whether `git` is available when `type = "git"` sources are configured
- whether local source files and local `file://` git repositories exist
- whether required `token_env` variables are present for private GitHub installs
- which repo, lockfile path, and managed library path will be used
- whether the lockfile or managed library has not been created yet

It prints `[info]`, `[warn]`, and `[error]` lines. Missing lockfiles and missing managed libraries are warnings, because a first-time `rs sync` or `rs run` can still create them. Blocking misconfiguration such as a missing selected `Rscript`, missing local source tarball, or missing private token env returns a non-zero exit code.

`rs doctor --json` keeps the flat `warnings` and `errors` arrays, and also groups them into `setup_errors`, `source_errors`, `network_errors`, `runtime_errors`, `lock_warnings`, and `cache_warnings` so automation can distinguish prerequisites, source misconfiguration, remote-access failures, and dependency-state warnings. It also exposes `error_details` and `warning_details` with structured `category`, `kind`, `message`, and optional path/package/env fields, plus `status` and `summary` so callers can quickly judge whether the report is `ok`, `warning`, or `error` without recomputing aggregate counts. `system_hints` and `system_hint_details` remain available for packages that commonly need external libraries, SDKs, or toolchains, and now cover a broader set of native-library-heavy packages such as `stringi`, `odbc`, and `git2r` in addition to the earlier `curl`/`xml2`/geospatial families.

The doctor output now also includes explicit next-step guidance. In human-readable mode it prints `[next]` lines such as `rs lock <script>` or `rs run <script>` when the environment is merely missing a lockfile or managed library, and `rs doctor --json` exposes the same guidance under structured `next_steps` entries with `category`, `kind`, `message`, optional `command`, and a `blocking` flag so automation can distinguish hard prerequisites from optional follow-up actions.

The human-readable `rs doctor` output now also ends with a compact `[summary]` line, for example `status=warning | errors=0 | warnings=2 | hints=2 | next=4 | blocking_next=0`, so logs are easy to scan without reparsing every line above it.

If you only want that compact status line, `rs doctor --summary-only` suppresses the detailed `[info]`, `[warn]`, `[hint]`, and `[next]` lines and prints just the final summary. It still honors the normal exit behavior, including `--strict`.

If you still want warnings, hints, next steps, and the summary, but do not want the verbose environment preamble, `rs doctor --quiet` hides only the `[info]` lines.

For CI or gating scripts, `rs doctor --strict` exits non-zero unless the report status is exactly `ok`. That means warnings such as a missing lockfile or missing managed library can be treated as failures when you want a fully prepared environment before continuing. By convention, normal blocking doctor failures exit with code `1`, while `--strict` warning failures exit with code `2`.

## What to build next

If you want this to grow from prototype into a real tool, the next steps are:

1. keep tightening `rs.toml` rewrite fidelity in edge cases such as exact blank-line placement
2. add shared cache indexing keyed by R version and source metadata to avoid ABI mismatches
3. enrich `rs doctor` with system dependency hints for packages that need external libraries or compilers
4. add finer lockfile update policies such as partial refresh modes and selective package relocking
5. validate immutable GitHub commits and generic git revisions more strictly across every custom source path

## Notes

- `rs run`, `rs exec`, `rs shell`, `rs lock`, and `rs sync` bootstrap R through the native manager on macOS and Linux; by default they print next steps, and if you set `RS_AUTO_INSTALL_R=1` they install the requested target
- `rs r install <version> --method auto|binary|source` controls how managed R versions are installed; `auto` is the default and Arch Linux prefers source builds in that mode
- the supported v1 path is macOS/Linux runtime plus the macOS/Linux native R manager; Windows remains best-effort unless you pin an explicit `Rscript`
- you can still pin a project interpreter with `rscript = "..."`, override it with `--rscript`, or use the explicit `rs r ...` commands when you want full control
- package installation supports CRAN, explicitly declared Bioconductor packages, GitHub sources, and local package sources
- package installation also supports generic `git` sources with `url`, `ref`, and `subdir`
- GitHub tokens are referenced by environment variable name via `token_env`; token values are never written to the lockfile
- the tool does not yet solve system-level dependencies required by some R packages
- the current `rs.toml` parser is deliberately minimal and supports only root keys, `[sources."..."]`, `[scripts."..."]`, and `[scripts."...".sources."..."]` blocks
