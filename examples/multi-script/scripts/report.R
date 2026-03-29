library(glue)
library(jsonlite)

metrics <- list(
  title = "weekly-report",
  rows = 3,
  status = "ready"
)

cat(glue("{metrics$title}: {toJSON(metrics, auto_unbox = TRUE)}\n"))
