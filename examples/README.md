# Examples

These examples are small, self-contained `rvx` projects.

- `cran-basic/`: CRAN-only script with a tiny `rs.toml`
- `bioc-rnaseq/`: Bioconductor example using `DESeq2`
- `multi-script/`: one project with script-specific dependency blocks

From the repository root, a good tour is:

```bash
./rvx scan examples/cran-basic/analysis.R
./rvx list examples/bioc-rnaseq/rnaseq.R
./rvx doctor examples/multi-script/scripts/report.R
./rvx run examples/cran-basic/analysis.R
```

Each example writes its cache and lockfile inside its own directory.
