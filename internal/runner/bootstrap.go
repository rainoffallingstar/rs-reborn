package runner

import (
	"fmt"
	"os"
	"path/filepath"
)

const bootstrapSource = `rs_bootstrap <- function() {
  rs_lib <- Sys.getenv("RS_LIB_PATH", "")
  rs_repos <- Sys.getenv("RS_REPOS", "https://cloud.r-project.org")
  rs_install <- identical(tolower(Sys.getenv("RS_INSTALL_ENABLED", "true")), "true")
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

  if (!rs_install) {
    return(invisible(NULL))
  }

  options(repos = c(CRAN = rs_repos))
  installed <- rownames(installed.packages(lib.loc = .libPaths()))

  missing_cran <- setdiff(rs_cran, installed)
  if (length(missing_cran) > 0) {
    message(sprintf("[rs] installing missing CRAN packages: %s", paste(missing_cran, collapse = ", ")))
    utils::install.packages(missing_cran, lib = rs_lib, repos = rs_repos)
    installed <- rownames(installed.packages(lib.loc = .libPaths()))
  }

  if (nrow(rs_sources) > 0) {
    missing_sources <- rs_sources[!(rs_sources$package %in% installed), , drop = FALSE]
    if (nrow(missing_sources) > 0) {
      for (i in seq_len(nrow(missing_sources))) {
        spec <- missing_sources[i, , drop = FALSE]
        if (identical(spec$type[[1]], "github")) {
          if (!"remotes" %in% installed) {
            message("[rs] installing remotes")
            utils::install.packages("remotes", lib = rs_lib, repos = rs_repos)
            installed <- rownames(installed.packages(lib.loc = .libPaths()))
          }
          target <- spec$repo[[1]]
          if (nzchar(spec$ref[[1]])) {
            message(sprintf("[rs] installing github package %s from %s@%s", spec$package[[1]], target, spec$ref[[1]]))
          } else {
            message(sprintf("[rs] installing github package %s from %s", spec$package[[1]], target))
          }
          remotes::install_github(
            target,
            ref = if (nzchar(spec$ref[[1]])) spec$ref[[1]] else NULL,
            subdir = if (nzchar(spec$subdir[[1]])) spec$subdir[[1]] else NULL,
            host = if (nzchar(spec$host[[1]])) spec$host[[1]] else "api.github.com",
            auth_token = if (nzchar(spec$token_env[[1]])) Sys.getenv(spec$token_env[[1]]) else NULL,
            lib = rs_lib,
            upgrade = "never",
            dependencies = TRUE
          )
          installed <- installed.packages(
            lib.loc = .libPaths(),
            fields = c("RemoteSha")
          )
          commit <- ""
          if (spec$package[[1]] %in% rownames(installed) && "RemoteSha" %in% colnames(installed)) {
            commit <- installed[spec$package[[1]], "RemoteSha"]
            if (is.na(commit)) {
              commit <- ""
            }
          }
          rs_write_source_metadata(
            rs_meta_dir,
            spec$package[[1]],
            spec$type[[1]],
            if (nzchar(spec$host[[1]])) spec$host[[1]] else "api.github.com",
            spec$repo[[1]],
            spec$ref[[1]],
            commit,
            spec$subdir[[1]],
            spec$fingerprint[[1]],
            spec$fingerprint_kind[[1]]
          )
        } else if (identical(spec$type[[1]], "git")) {
          clone_dir <- file.path(tempdir(), sprintf("rs-git-%s-%s", spec$package[[1]], as.integer(Sys.time())))
          status <- system2("git", c("clone", spec$url[[1]], clone_dir))
          if (!identical(status, 0L)) {
            stop(sprintf("failed to clone git source %s from %s", spec$package[[1]], spec$url[[1]]))
          }
          on.exit(unlink(clone_dir, recursive = TRUE, force = TRUE), add = TRUE)
          if (nzchar(spec$ref[[1]])) {
            status <- system2("git", c("-C", clone_dir, "checkout", spec$ref[[1]]))
            if (!identical(status, 0L)) {
              stop(sprintf("failed to checkout ref %s for git source %s", spec$ref[[1]], spec$package[[1]]))
            }
          }
          commit <- trimws(system2("git", c("-C", clone_dir, "rev-parse", "HEAD"), stdout = TRUE))
          target <- if (nzchar(spec$subdir[[1]])) file.path(clone_dir, spec$subdir[[1]]) else clone_dir
          message(sprintf("[rs] installing git package %s from %s", spec$package[[1]], spec$url[[1]]))
          status <- system2(
            file.path(R.home("bin"), "R"),
            c("CMD", "INSTALL", "-l", rs_lib, target)
          )
          if (!identical(status, 0L)) {
            stop(sprintf("failed to install git package %s from %s", spec$package[[1]], spec$url[[1]]))
          }
          rs_write_source_metadata(
            rs_meta_dir,
            spec$package[[1]],
            spec$type[[1]],
            "",
            spec$url[[1]],
            spec$ref[[1]],
            commit,
            spec$subdir[[1]],
            spec$fingerprint[[1]],
            spec$fingerprint_kind[[1]]
          )
        } else if (identical(spec$type[[1]], "local")) {
          target <- spec$path[[1]]
          message(sprintf("[rs] installing local package %s from %s", spec$package[[1]], target))
          status <- system2(
            file.path(R.home("bin"), "R"),
            c("CMD", "INSTALL", "-l", rs_lib, target)
          )
          if (!identical(status, 0L)) {
            stop(sprintf("failed to install local package %s from %s", spec$package[[1]], target))
          }
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
        } else {
          stop(sprintf("unsupported source type: %s", spec$type[[1]]))
        }
      }
      installed <- rownames(installed.packages(lib.loc = .libPaths()))
    }
  }

  missing_bioc <- setdiff(rs_bioc, installed)
  if (length(missing_bioc) > 0) {
    if (!"BiocManager" %in% installed) {
      message("[rs] installing BiocManager")
      utils::install.packages("BiocManager", lib = rs_lib, repos = rs_repos)
    }
    message(sprintf("[rs] installing missing Bioconductor packages: %s", paste(missing_bioc, collapse = ", ")))
    BiocManager::install(
      missing_bioc,
      lib = rs_lib,
      ask = FALSE,
      update = FALSE,
      site_repository = character(),
      repos = c(CRAN = rs_repos)
    )
  }

  invisible(NULL)
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
	tmpDir := filepath.Join(cacheRoot, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("create bootstrap dir: %w", err)
	}

	file, err := os.CreateTemp(tmpDir, "rs-profile-*.R")
	if err != nil {
		return "", fmt.Errorf("create bootstrap profile: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(bootstrapSource); err != nil {
		return "", fmt.Errorf("write bootstrap profile: %w", err)
	}

	return file.Name(), nil
}
