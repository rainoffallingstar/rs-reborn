library(cli)
library(jsonlite)

sales <- data.frame(
  sample = c("A", "B", "C"),
  value = c(12, 19, 23)
)

cli_h1("CRAN basic example")
cat(toJSON(sales, pretty = TRUE, auto_unbox = TRUE), "\n")
