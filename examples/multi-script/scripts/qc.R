library(cli)
library(stringr)

labels <- c("sample-a", "sample-b", "sample-c")
normalized <- str_replace_all(labels, "-", "_")

cli_h1("QC labels")
cat(paste(normalized, collapse = "\n"), "\n")
