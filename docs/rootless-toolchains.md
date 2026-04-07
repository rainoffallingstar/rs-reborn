# Rootless Toolchains

`rvx` now supports user-local toolchain prefixes for two different jobs:

- building a managed R from source with `rvx r install <version> --method source`
- installing source-based R packages through `rvx run`, `rvx lock`, `rvx sync`, or the native package installer

What `rvx` does:

- reads `toolchain_prefixes` and `pkg_config_path` from `rs.toml`
- reads `RS_TOOLCHAIN_PREFIXES` and `RS_PKG_CONFIG_PATH` from the environment
- expands each toolchain prefix into `bin`, `include`, and `lib`
- injects `PATH`, `CPPFLAGS`, `LDFLAGS`, and `PKG_CONFIG_PATH` automatically
- validates configured paths in `rvx doctor`

What `rvx` does not do yet:

- by default it does not silently install Homebrew, `enva`, Conda, micromamba, mamba, Spack, or system libraries for you
- it only materializes a user-local prefix when you explicitly opt in with `--bootstrap-toolchain`
- it does not install arbitrary system libraries for you; it only bootstraps the manager-specific user-local prefix and packages that command requests

That means the workflow is:

1. create a user-local prefix with your preferred package manager
2. install the required compilers, headers, or `pkg-config` there
3. point `rvx` at that prefix with `rs.toml` or environment variables

If you want `rvx` to create the prefix for you, use a command that accepts `--bootstrap-toolchain`, for example:

```bash
rvx run --bootstrap-toolchain analysis.R
rvx lock --bootstrap-toolchain analysis.R
rvx check --bootstrap-toolchain analysis.R
rvx doctor --toolchain-only --bootstrap-toolchain
rvx r install 4.5.3 --method source --bootstrap-toolchain
```

## Project Config

For project-managed package installs, prefer keeping the configuration in `rs.toml`:

```toml
toolchain_prefixes = [
  "/home/you/.local",
  "/home/you/micromamba/envs/rs-sysdeps",
]
pkg_config_path = [
  "/home/you/.local/lib/pkgconfig",
  "/home/you/.local/share/pkgconfig",
]
```

This is the best default when you want `rvx run`, `rvx lock`, `rvx sync`, and `rvx doctor` to use the same rootless toolchain setup consistently.

If you want a starter template instead of writing these paths by hand, `rvx init` can seed one for you:

```bash
rvx init --toolchain-preset auto
rvx init --toolchain-preset enva
rvx init --toolchain-preset micromamba
rvx init --toolchain-preset homebrew
rvx init --toolchain-preset spack
```

The current preset meanings are:

- `enva`: `~/.local/share/rattler/envs/rs-sysdeps`
- `micromamba`: `~/micromamba/envs/rs-sysdeps`
- `mamba`: `~/.local/share/mamba/envs/rs-sysdeps`
- `conda`: `~/.conda/envs/rs-sysdeps`
- `homebrew`: `~/homebrew`
- `spack`: `~/spack/views/rs-sysdeps`

`auto` reuses the same top recommendation that `rvx toolchain detect` would print for the current machine. The named presets are starter templates, not auto-detected installs. If your real prefix differs, add explicit `--toolchain-prefix` or `--pkg-config-path` values, or edit `rs.toml` after initialization.

If you want to preview the template without writing `rs.toml`, use:

```bash
rvx toolchain template enva
rvx toolchain template micromamba
rvx toolchain template homebrew --format env
rvx toolchain template spack --format toml
rvx toolchain template conda --check
rvx toolchain detect
rvx toolchain bootstrap auto
```

Supported output formats:

- `toml`: prints `toolchain_prefixes` and `pkg_config_path` lines you can paste into `rs.toml`
- `env`: prints `export RS_TOOLCHAIN_PREFIXES=...` and `export RS_PKG_CONFIG_PATH=...` for ad hoc shell sessions

Add `--check` if you want `rvx` to verify whether the preset paths already exist on the current machine. This is especially useful on shared clusters, where the intended Homebrew, micromamba, or Spack view may or may not have been provisioned yet.

If you are not sure which common rootless layout already exists, run `rvx toolchain detect`. It scans the current home directory for the built-in preset roots and reports which presets look complete or partial on this machine, along with the matching `rvx toolchain template <preset> --check` follow-up.

`rvx toolchain detect` now also prints a suggested `rvx init --toolchain-preset ...` command for each detected candidate, so moving from discovery to a project-local `rs.toml` is just a copy-paste step.

It also prints a preset-specific setup hint so you can go from "which layout should I use?" to "what command should I run next?" without leaving `rvx`.

If you want that collapsed into one short action plan, use `rvx toolchain bootstrap <preset|auto>`. It prints the setup command, the matching template/init follow-up, and the `rvx doctor --toolchain-only` validation step together.

If you want `rvx` to inspect one script first and fold the inferred system-library packages into the same rootless environment setup, use:

```bash
rvx toolchain plan analysis.R
rvx toolchain init analysis.R
rvx toolchain init --phase base analysis.R
```

`rvx toolchain plan` resolves the script dependency set first, then maps the resulting system-hint categories onto a preset-specific package list. `rvx toolchain init` executes that plan. `--phase base` installs only the shared compiler/toolchain floor; `--phase full` adds the inferred system-library packages in the same initialization run.

## Quick Rootless Recipes

These are starting points, not guaranteed universal one-liners. The exact libraries you need still depend on which R packages you install.

### enva

If you already use `enva`, this is now the preferred rootless bootstrap path. `rvx` treats it as the first-class conda-style toolchain manager, and `auto` bootstrap now stays on `enva` instead of falling back to micromamba/mamba/conda:

```bash
rvx toolchain bootstrap enva
rvx init --toolchain-preset enva
rvx doctor --toolchain-only
```

`rvx toolchain bootstrap enva` generates and runs a small temporary `conda-forge` YAML that creates an `rs-sysdeps` environment under `~/.local/share/rattler/envs/rs-sysdeps` with compilers, binutils, `cmake`, `libiconv`, and a compatible Linux sysroot, then `rvx` wires that prefix into native builds and runtime library lookup.

### micromamba

If micromamba, mamba, or Conda is already allowed on your machine, these remain supported compatibility bootstrap paths when selected explicitly:

```bash
micromamba create -y -p "$HOME/micromamba/envs/rs-sysdeps" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake libiconv
rvx init --toolchain-preset micromamba
rvx doctor --toolchain-only
```

After that, add more user-local libraries to the same environment when specific R packages need them.

### Homebrew In Home

If you already have a Homebrew prefix in your home directory, install toolchain pieces there and point `rvx` at it:

```bash
"$HOME/homebrew/bin/brew" install pkg-config gcc cmake libiconv
rvx init --toolchain-preset homebrew
rvx doctor --toolchain-only
```

This works best when your team or cluster already standardized on a shared "Homebrew in home" convention.

### Spack

If your cluster already uses Spack, populate a dedicated view that exposes the compilers and metadata tools you need:

```bash
spack view symlink "$HOME/spack/views/rs-sysdeps" pkgconf gcc cmake libiconv
rvx init --toolchain-preset spack
rvx doctor --toolchain-only
```

Spack layouts are often site-specific. Treat this as a skeleton and adjust it to match your lab or cluster policy.

## Ad Hoc Environment Variables

For one-off source builds, especially `rvx r install --method source`, you can export the variables directly:

```bash
export RS_TOOLCHAIN_PREFIXES="$HOME/.local:$HOME/.local/share/rattler/envs/rs-sysdeps"
export RS_PKG_CONFIG_PATH="$HOME/.local/lib/pkgconfig:$HOME/.local/share/pkgconfig"

rvx r install 4.4.3 --method source
rvx run analysis.R
```

Use this mode when you do not have a project yet, or when you want to test a candidate toolchain before writing it into `rs.toml`.

## Homebrew In Home

On macOS or Linux, one practical rootless option is a user-local Homebrew install. Once Homebrew itself is installed in your home directory, wire it into `rs` like this:

```toml
toolchain_prefixes = [
  "/home/you/homebrew",
]
pkg_config_path = [
  "/home/you/homebrew/lib/pkgconfig",
  "/home/you/homebrew/share/pkgconfig",
]
```

or:

```bash
export RS_TOOLCHAIN_PREFIXES="$HOME/homebrew"
export RS_PKG_CONFIG_PATH="$HOME/homebrew/lib/pkgconfig:$HOME/homebrew/share/pkgconfig"
```

This is a good fit when you want one stable personal prefix that provides `pkg-config`, headers, and common build libraries.

## Conda-Family Prefixes

If you already use `enva`, micromamba, mamba, or Conda, create a dedicated build-dependency prefix instead of relying on the runtime R inside a larger data-science environment. `enva` is the preferred bootstrap path when available, but the same pattern works for the rest of the conda-family tools:

```toml
toolchain_prefixes = [
  "/home/you/.local/share/rattler/envs/rs-sysdeps",
]
pkg_config_path = [
  "/home/you/.local/share/rattler/envs/rs-sysdeps/lib/pkgconfig",
  "/home/you/.local/share/rattler/envs/rs-sysdeps/share/pkgconfig",
]
```

or:

```bash
export RS_TOOLCHAIN_PREFIXES="$HOME/.local/share/rattler/envs/rs-sysdeps"
export RS_PKG_CONFIG_PATH="$HOME/.local/share/rattler/envs/rs-sysdeps/lib/pkgconfig:$HOME/.local/share/rattler/envs/rs-sysdeps/share/pkgconfig"
```

If your site standardizes on micromamba, mamba, or Conda instead, substitute that manager's `rs-sysdeps` prefix. This is usually the easiest rootless option on clusters where conda-family user-local environments are accepted but system package installs are unavailable.

## Spack

If your cluster or lab already uses Spack, point `rs` at the installed prefix that contains the actual headers and binaries:

```toml
toolchain_prefixes = [
  "/path/to/spack/opt/spack/linux-.../pkg-config-0.29.2-abcdef",
  "/path/to/spack/opt/spack/linux-.../xz-5.6.3-ghijkl",
  "/path/to/spack/opt/spack/linux-.../readline-8.2-mnopqr",
]
pkg_config_path = [
  "/path/to/spack/opt/spack/linux-.../pkg-config-0.29.2-abcdef/lib/pkgconfig",
]
```

With Spack, you often need multiple prefixes because each package may be installed into its own hash-qualified directory. `rs` supports multiple entries and preserves their order.

## Choosing Between Them

- prefer `rs.toml` when the toolchain is project-specific and should be reproducible
- prefer environment variables when testing or when no project exists yet
- prefer a dedicated build prefix over reusing a large general-purpose Conda R runtime
- if both a managed `rs` R and an external Conda R exist, prefer the managed `rs` R for source installs

## How To Check It

Use `rvx doctor` before a heavy source build:

```bash
rvx doctor analysis.R
rvx doctor --json analysis.R
rvx doctor --toolchain-only
rvx doctor --toolchain-only path/to/project
```

`doctor` now verifies:

- each configured `toolchain_prefixes` entry exists
- each configured `pkg_config_path` entry exists
- the configured entries are directories
- `pkg-config` is available on the effective `PATH` when rootless pkg-config paths are configured

If these checks fail, `doctor` returns structured setup errors and next steps instead of waiting for a long compile log to fail later.

If you only want to validate rootless toolchain configuration, without scanning one script or checking lockfile state, use `rvx doctor --toolchain-only`. It inspects project-level `toolchain_prefixes` / `pkg_config_path` when an `rs.toml` is present, and otherwise falls back to `RS_TOOLCHAIN_PREFIXES` / `RS_PKG_CONFIG_PATH`.

The doctor JSON also exposes the toolchain contribution preview directly:

- `toolchain_path`
- `toolchain_cppflags`
- `toolchain_ldflags`
- `toolchain_pkg_config_path`

These fields show what `rs` itself adds for rootless builds, without dumping the host machine's entire pre-existing environment.

## Rootless Limitation

If a package needs headers or shared libraries that are not present in your user-local prefixes, `rs` cannot fix that automatically yet. You still need to install those dependencies into a prefix you control, then point `rs` at that prefix.
