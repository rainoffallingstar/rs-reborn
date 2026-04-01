package rdeps_test

import (
	"reflect"
	"testing"

	publicrdeps "github.com/rainoffallingstar/rs-reborn/pkg/rdeps"
)

func TestPublicSplitBiocPackages(t *testing.T) {
	cran, bioc := publicrdeps.SplitBiocPackages([]string{"jsonlite", "Biostrings"})
	if !reflect.DeepEqual(cran, []string{"jsonlite"}) {
		t.Fatalf("cran = %v", cran)
	}
	if !reflect.DeepEqual(bioc, []string{"Biostrings"}) {
		t.Fatalf("bioc = %v", bioc)
	}
}
