package rmanager

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveVersionOrPathFindsInstalledVersionFromGlob(t *testing.T) {
	origGlob := rigGlob
	origStat := rigStat
	origHome := rigHomeDir
	t.Cleanup(func() {
		rigGlob = origGlob
		rigStat = origStat
		rigHomeDir = origHome
	})

	dir := t.TempDir()
	match := filepath.Join(dir, "Rscript")
	if err := os.WriteFile(match, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	rigGlob = func(pattern string) ([]string, error) {
		return []string{match}, nil
	}
	rigStat = os.Stat
	rigHomeDir = func() (string, error) {
		return dir, nil
	}

	got, err := ResolveVersionOrPath("4.4")
	if err != nil {
		t.Fatalf("ResolveVersionOrPath() error = %v", err)
	}
	if got != match {
		t.Fatalf("ResolveVersionOrPath() = %q, want %q", got, match)
	}
}

func TestResolveVersionOrPathAcceptsExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Rscript-custom")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := ResolveVersionOrPath(path)
	if err != nil {
		t.Fatalf("ResolveVersionOrPath() error = %v", err)
	}
	if got != path {
		t.Fatalf("ResolveVersionOrPath() = %q, want %q", got, path)
	}
}
