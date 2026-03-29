# Examples

These examples are small, self-contained `rs` projects.

- `cran-basic/`: CRAN-only script with a tiny `rs.toml`
- `bioc-rnaseq/`: Bioconductor example using `DESeq2`
- `multi-script/`: one project with script-specific dependency blocks

From the repository root, a good tour is:

```bash
./rs scan examples/cran-basic/analysis.R
./rs list examples/bioc-rnaseq/rnaseq.R
./rs doctor examples/multi-script/scripts/report.R
./rs run examples/cran-basic/analysis.R
```

Each example writes its cache and lockfile inside its own directory.
