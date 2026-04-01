package lockfile_test

import (
	"path/filepath"
	"testing"
	"time"

	publiclockfile "github.com/rainoffallingstar/rs-reborn/pkg/lockfile"
)

func TestPublicLockfileReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rs.lock.json")
	file := publiclockfile.File{
		Version:     1,
		GeneratedAt: time.Unix(1700000000, 0).UTC(),
		Script:      "analysis.R",
		Library:     ".rs-cache/lib/demo",
		Metadata: publiclockfile.Metadata{
			Interpreter: "/usr/bin/Rscript",
			RVersion:    "4.5.3",
		},
		Packages: []publiclockfile.Package{
			{Name: "cli", Version: "3.6.5", Source: "cran"},
		},
	}
	if err := publiclockfile.Write(path, file); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	loaded, err := publiclockfile.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if loaded.Script != "analysis.R" {
		t.Fatalf("loaded.Script = %q", loaded.Script)
	}
	if len(loaded.Packages) != 1 || loaded.Packages[0].Name != "cli" {
		t.Fatalf("loaded.Packages = %v", loaded.Packages)
	}
}
