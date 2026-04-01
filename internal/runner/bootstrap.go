package runner

import (
	"fmt"
	"os"
	"path/filepath"
)

const bootstrapSource = `rs_bootstrap_context <- function() {
  rs_lib <- Sys.getenv("RS_LIB_PATH", "")
  rs_repos <- Sys.getenv("RS_REPOS", "https://cloud.r-project.org")
  rs_install <- identical(tolower(Sys.getenv("RS_INSTALL_ENABLED", "true")), "true")
  rs_backend <- tolower(Sys.getenv("RS_INSTALL_BACKEND", "auto"))
  cran_raw <- Sys.getenv("RS_CRAN_DEPS", "")
  bioc_raw <- Sys.getenv("RS_BIOC_DEPS", "")
  source_raw <- Sys.getenv("RS_SOURCE_DEPS", "")
  rs_cran <- if (nzchar(cran_raw)) strsplit(cran_raw, ",", fixed = TRUE)[[1]] else character()
  rs_bioc <- if (nzchar(bioc_raw)) strsplit(bioc_raw, ",", fixed = TRUE)[[1]] else character()
  rs_cran <- rs_cran[nzchar(rs_cran)]
  rs_bioc <- rs_bioc[nzchar(rs_bioc)]
  rs_sources <- rs_parse_sources(source_raw)
  rs_meta_dir <- if (nzchar(rs_lib)) file.path(rs_lib, ".rs-source-meta") else ""

  if (nzchar(rs_lib)) {
    dir.create(rs_lib, recursive = TRUE, showWarnings = FALSE)
    .libPaths(c(normalizePath(rs_lib, winslash = "/", mustWork = FALSE), .libPaths()))
    dir.create(rs_meta_dir, recursive = TRUE, showWarnings = FALSE)
  }

  list(
    lib = rs_lib,
    repos = rs_repos,
    install = rs_install,
    backend = rs_backend,
    cran = rs_cran,
    bioc = rs_bioc,
    sources = rs_sources,
    meta_dir = rs_meta_dir
  )
}

rs_installed_names <- function() {
  rownames(installed.packages(lib.loc = .libPaths()))
}

rs_pak_supported_sources <- function(rs_sources) {
  if (nrow(rs_sources) == 0) {
    return(TRUE)
  }
  for (i in seq_len(nrow(rs_sources))) {
    spec <- rs_sources[i, , drop = FALSE]
    if (!(spec$type[[1]] %in% c("local", "git", "github"))) {
      return(FALSE)
    }
    if (identical(spec$type[[1]], "git") && nzchar(spec$subdir[[1]])) {
      return(FALSE)
    }
    if (identical(spec$type[[1]], "github")) {
      host <- spec$host[[1]]
      if (nzchar(host) && !identical(host, "api.github.com")) {
        return(FALSE)
      }
      if (nzchar(spec$token_env[[1]])) {
        return(FALSE)
      }
    }
  }
  TRUE
}

rs_write_local_source_metadata <- function(rs_meta_dir, rs_sources) {
  if (nrow(rs_sources) == 0) {
    return(invisible(NULL))
  }
  local_sources <- rs_sources[rs_sources$type == "local", , drop = FALSE]
  if (nrow(local_sources) == 0) {
    return(invisible(NULL))
  }
  for (i in seq_len(nrow(local_sources))) {
    spec <- local_sources[i, , drop = FALSE]
    rs_write_source_metadata(
      rs_meta_dir,
      spec$package[[1]],
      spec$type[[1]],
      "",
      spec$path[[1]],
      "",
      "",
      "",
      spec$fingerprint[[1]],
      spec$fingerprint_kind[[1]]
    )
  }
  invisible(NULL)
}

rs_installed_remote_sha <- function(pkg) {
  installed <- installed.packages(
    lib.loc = .libPaths(),
    fields = c("RemoteSha")
  )
  if (!(pkg %in% rownames(installed)) || !("RemoteSha" %in% colnames(installed))) {
    return("")
  }
  sha <- installed[pkg, "RemoteSha"]
  if (is.na(sha)) {
    return("")
  }
  sha
}

rs_write_git_source_metadata <- function(rs_meta_dir, rs_sources) {
  if (nrow(rs_sources) == 0) {
    return(invisible(NULL))
  }
  git_sources <- rs_sources[rs_sources$type == "git", , drop = FALSE]
  if (nrow(git_sources) == 0) {
    return(invisible(NULL))
  }
  for (i in seq_len(nrow(git_sources))) {
    spec <- git_sources[i, , drop = FALSE]
    rs_write_source_metadata(
      rs_meta_dir,
      spec$package[[1]],
      spec$type[[1]],
      "",
      spec$url[[1]],
      spec$ref[[1]],
      rs_installed_remote_sha(spec$package[[1]]),
      "",
      spec$fingerprint[[1]],
      spec$fingerprint_kind[[1]]
    )
  }
  invisible(NULL)
}

rs_write_github_source_metadata <- function(rs_meta_dir, rs_sources) {
  if (nrow(rs_sources) == 0) {
    return(invisible(NULL))
  }
  github_sources <- rs_sources[rs_sources$type == "github", , drop = FALSE]
  if (nrow(github_sources) == 0) {
    return(invisible(NULL))
  }
  for (i in seq_len(nrow(github_sources))) {
    spec <- github_sources[i, , drop = FALSE]
    host <- spec$host[[1]]
    if (!nzchar(host)) {
      host <- "api.github.com"
    }
    rs_write_source_metadata(
      rs_meta_dir,
      spec$package[[1]],
      spec$type[[1]],
      host,
      spec$repo[[1]],
      spec$ref[[1]],
      rs_installed_remote_sha(spec$package[[1]]),
      spec$subdir[[1]],
      spec$fingerprint[[1]],
      spec$fingerprint_kind[[1]]
    )
  }
  invisible(NULL)
}

rs_install_pak <- function(ctx) {
  if (!rs_pak_supported_sources(ctx$sources)) {
    stop("pak backend does not yet support these custom source settings")
  }

  options(repos = c(CRAN = ctx$repos))
  installed <- rs_installed_names()
  missing_cran <- setdiff(ctx$cran, installed)
  missing_bioc <- setdiff(ctx$bioc, installed)
  bioc_refs <- character()
  if (length(missing_bioc) > 0) {
    bioc_refs <- paste0("bioc::", missing_bioc)
  }
  missing_sources <- ctx$sources[!(ctx$sources$package %in% installed), , drop = FALSE]
  local_refs <- character()
  git_refs <- character()
  github_refs <- character()
  if (nrow(missing_sources) > 0) {
    local_sources <- missing_sources[missing_sources$type == "local", , drop = FALSE]
    if (nrow(local_sources) > 0) {
      local_refs <- vapply(
        seq_len(nrow(local_sources)),
        function(i) sprintf("local::%s", normalizePath(local_sources$path[[i]], winslash = "/", mustWork = FALSE)),
        character(1)
      )
    }
    git_sources <- missing_sources[missing_sources$type == "git", , drop = FALSE]
    if (nrow(git_sources) > 0) {
      git_refs <- vapply(
        seq_len(nrow(git_sources)),
        function(i) {
          ref <- sprintf("%s=git::%s", git_sources$package[[i]], git_sources$url[[i]])
          if (nzchar(git_sources$ref[[i]])) {
            ref <- paste0(ref, "@", git_sources$ref[[i]])
          }
          ref
        },
        character(1)
      )
    }
    github_sources <- missing_sources[missing_sources$type == "github", , drop = FALSE]
    if (nrow(github_sources) > 0) {
      github_refs <- vapply(
        seq_len(nrow(github_sources)),
        function(i) {
          ref <- sprintf("%s=github::%s", github_sources$package[[i]], github_sources$repo[[i]])
          if (nzchar(github_sources$subdir[[i]])) {
            ref <- paste0(ref, "/", github_sources$subdir[[i]])
          }
          if (nzchar(github_sources$ref[[i]])) {
            ref <- paste0(ref, "@", github_sources$ref[[i]])
          }
          ref
        },
        character(1)
      )
    }
  }
  refs <- c(missing_cran, bioc_refs, local_refs, git_refs, github_refs)
  if (length(refs) == 0) {
    rs_write_local_source_metadata(ctx$meta_dir, ctx$sources)
    rs_write_git_source_metadata(ctx$meta_dir, ctx$sources)
    rs_write_github_source_metadata(ctx$meta_dir, ctx$sources)
    return(invisible(NULL))
  }

  if (!"pak" %in% installed) {
    message("[rs] installing pak")
    utils::install.packages("pak", lib = ctx$lib, repos = ctx$repos)
  }

  message(sprintf("[rs] installing via pak: %s", paste(refs, collapse = ", ")))
  pak::pkg_install(refs, lib = ctx$lib, ask = FALSE, upgrade = FALSE)
  rs_write_local_source_metadata(ctx$meta_dir, ctx$sources)
  rs_write_git_source_metadata(ctx$meta_dir, ctx$sources)
  rs_write_github_source_metadata(ctx$meta_dir, ctx$sources)
  invisible(NULL)
}

rs_bootstrap <- function() {
  ctx <- rs_bootstrap_context()
  if (!ctx$install) {
    return(invisible(NULL))
  }

  if (!(ctx$backend %in% c("auto", "pak", "native"))) {
    stop(sprintf("unsupported install backend %s", ctx$backend))
  }

  if (identical(ctx$backend, "auto") || identical(ctx$backend, "native")) {
    stop("auto/native backend must be executed from the Go installer")
  }
  if (identical(ctx$backend, "pak")) {
    return(rs_install_pak(ctx))
  }
  stop(sprintf("unsupported install backend %s", ctx$backend))
}

rs_parse_sources <- function(raw) {
  if (!nzchar(raw)) {
    return(data.frame(
      package = character(),
      type = character(),
      repo = character(),
      ref = character(),
      path = character(),
      subdir = character(),
      host = character(),
      token_env = character(),
      url = character(),
      fingerprint_kind = character(),
      fingerprint = character(),
      stringsAsFactors = FALSE
    ))
  }

  decode <- function(x) utils::URLdecode(x)

  lines <- strsplit(raw, "\n", fixed = TRUE)[[1]]
  rows <- lapply(lines[nzchar(lines)], function(line) {
    parts <- strsplit(line, "\t", fixed = TRUE)[[1]]
    while (length(parts) < 5) {
      parts <- c(parts, "")
    }
    while (length(parts) < 9) {
      parts <- c(parts, "")
    }
    while (length(parts) < 11) {
      parts <- c(parts, "")
    }
    list(
      package = parts[[1]],
      type = parts[[2]],
      repo = decode(parts[[3]]),
      ref = decode(parts[[4]]),
      path = decode(parts[[5]]),
      subdir = decode(parts[[6]]),
      host = decode(parts[[7]]),
      token_env = decode(parts[[8]]),
      url = decode(parts[[9]]),
      fingerprint_kind = decode(parts[[10]]),
      fingerprint = decode(parts[[11]])
    )
  })

  data.frame(
    package = vapply(rows, function(x) x$package, character(1)),
    type = vapply(rows, function(x) x$type, character(1)),
    repo = vapply(rows, function(x) x$repo, character(1)),
    ref = vapply(rows, function(x) x$ref, character(1)),
    path = vapply(rows, function(x) x$path, character(1)),
    subdir = vapply(rows, function(x) x$subdir, character(1)),
    host = vapply(rows, function(x) x$host, character(1)),
    token_env = vapply(rows, function(x) x$token_env, character(1)),
    url = vapply(rows, function(x) x$url, character(1)),
    fingerprint_kind = vapply(rows, function(x) x$fingerprint_kind, character(1)),
    fingerprint = vapply(rows, function(x) x$fingerprint, character(1)),
    stringsAsFactors = FALSE
  )
}

rs_write_source_metadata <- function(meta_dir, pkg, type, host, location, ref, commit, subdir, fingerprint, fingerprint_kind) {
  if (!nzchar(meta_dir)) {
    return(invisible(NULL))
  }
  encode <- function(x) utils::URLencode(x, reserved = TRUE)
  line <- paste(
    encode(type),
    encode(host),
    encode(location),
    encode(ref),
    encode(commit),
    encode(subdir),
    encode(fingerprint),
    encode(fingerprint_kind),
    sep = "\t"
  )
  writeLines(line, file.path(meta_dir, paste0(pkg, ".tsv")))
  invisible(NULL)
}

if (identical(tolower(Sys.getenv("RS_BOOTSTRAP_AUTORUN", "true")), "true")) {
  rs_bootstrap()
}`

func writeBootstrap(cacheRoot string) (string, error) {
	bootstrapDir := filepath.Join(cacheRoot, "bootstrap")
	if err := os.MkdirAll(bootstrapDir, 0o755); err != nil {
		return "", fmt.Errorf("create bootstrap dir: %w", err)
	}
	path := filepath.Join(bootstrapDir, "rs-profile.R")
	if data, err := os.ReadFile(path); err == nil && string(data) == bootstrapSource {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(bootstrapSource), 0o644); err != nil {
		return "", fmt.Errorf("write bootstrap profile: %w", err)
	}
	return path, nil
}
