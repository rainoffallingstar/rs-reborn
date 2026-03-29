library(DESeq2)
library(SummarizedExperiment)
library(jsonlite)

counts <- matrix(
  c(
    120, 135, 95,
    310, 305, 330,
    42, 58, 51,
    500, 480, 515
  ),
  nrow = 4,
  byrow = TRUE,
  dimnames = list(
    c("geneA", "geneB", "geneC", "geneD"),
    c("sample1", "sample2", "sample3")
  )
)

coldata <- DataFrame(condition = c("ctrl", "ctrl", "treated"))
dds <- DESeqDataSetFromMatrix(countData = counts, colData = coldata, design = ~condition)

summary <- list(
  genes = nrow(dds),
  samples = ncol(dds),
  design = as.character(design(dds))
)

cat(toJSON(summary, auto_unbox = TRUE, pretty = TRUE), "\n")
