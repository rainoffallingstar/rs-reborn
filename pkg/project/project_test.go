package project_test

import (
	"path/filepath"
	"testing"

	publicproject "github.com/rainoffallingstar/rs-reborn/pkg/project"
)

func TestPublicProjectConfigRoundTrip(t *testing.T) {
	cfg := publicproject.NewDefaultConfig(publicproject.InitOptions{
		Packages: []string{"cli"},
	})
	if cfg.Defaults.Repo != publicproject.DefaultRepo {
		t.Fatalf("cfg.Defaults.Repo = %q, want %q", cfg.Defaults.Repo, publicproject.DefaultRepo)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, publicproject.ConfigFileName)
	if err := publicproject.Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := publicproject.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Defaults.Packages) != 1 || loaded.Defaults.Packages[0] != "cli" {
		t.Fatalf("loaded.Defaults.Packages = %v", loaded.Defaults.Packages)
	}
}
