package rdeps

import (
	"reflect"
	"testing"
)

func TestFromSource(t *testing.T) {
	src := `
library(dplyr)
require("jsonlite")
requireNamespace("cli")
ggplot2::ggplot()
stats:::rnorm(1)
# library(ignored)
"# still not a comment"
`

	got := FromSource(src)
	want := []string{"cli", "dplyr", "ggplot2", "jsonlite", "stats"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromSource() = %v, want %v", got, want)
	}
}

func TestFilterInstallable(t *testing.T) {
	input := []string{"cli", "jsonlite", "methods", "stats", "utils"}

	got := FilterInstallable(input)
	want := []string{"cli", "jsonlite"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterInstallable() = %v, want %v", got, want)
	}
}

func TestSplitBiocPackages(t *testing.T) {
	cran, bioc := SplitBiocPackages([]string{"DESeq2", "jsonlite", "SummarizedExperiment"})

	if !reflect.DeepEqual(cran, []string{"jsonlite"}) {
		t.Fatalf("cran = %v, want jsonlite", cran)
	}
	if !reflect.DeepEqual(bioc, []string{"DESeq2", "SummarizedExperiment"}) {
		t.Fatalf("bioc = %v, want known bioc packages", bioc)
	}
}
