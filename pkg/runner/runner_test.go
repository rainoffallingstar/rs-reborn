package runner_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	publicrunner "github.com/rainoffallingstar/rs-reborn/pkg/runner"
)

func TestPublicScanScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "analysis.R")
	src := "library(jsonlite)\nlibrary(Biostrings)\n"
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	deps, err := publicrunner.ScanScript(script)
	if err != nil {
		t.Fatalf("ScanScript() error = %v", err)
	}
	slices.Sort(deps)
	want := []string{"Biostrings", "jsonlite"}
	if !slices.Equal(deps, want) {
		t.Fatalf("deps = %v, want %v", deps, want)
	}
}
