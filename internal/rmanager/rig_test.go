package rmanager

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestResolveVersionOrPathUsesHighestInstalledVersionForNamedRelease(t *testing.T) {
	origGlob := rigGlob
	origStat := rigStat
	origHome := rigHomeDir
	t.Cleanup(func() {
		rigGlob = origGlob
		rigStat = origStat
		rigHomeDir = origHome
	})

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "4.4.3", "bin", "Rscript")
	newPath := filepath.Join(dir, "4.5.2", "bin", "Rscript")
	for _, path := range []string{oldPath, newPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	rigGlob = func(pattern string) ([]string, error) {
		return []string{oldPath, newPath}, nil
	}
	rigStat = os.Stat
	rigHomeDir = func() (string, error) {
		return dir, nil
	}

	got, err := ResolveVersionOrPath("release")
	if err != nil {
		t.Fatalf("ResolveVersionOrPath() error = %v", err)
	}
	if got != newPath {
		t.Fatalf("ResolveVersionOrPath() = %q, want %q", got, newPath)
	}
}

func TestEnsureInstalledRscriptInstallsDefaultReleaseWhenRscriptMissing(t *testing.T) {
	origCommand := rigCommand
	origLookPath := rigLookPath
	origGlob := rigGlob
	origStat := rigStat
	origHome := rigHomeDir
	t.Cleanup(func() {
		rigCommand = origCommand
		rigLookPath = origLookPath
		rigGlob = origGlob
		rigStat = origStat
		rigHomeDir = origHome
	})

	dir := t.TempDir()
	installed := filepath.Join(dir, "4.5.3", "bin", "Rscript")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var called []string
	rigLookPath = func(file string) (string, error) {
		if file != "rig" {
			return "", fmt.Errorf("unexpected lookpath %q", file)
		}
		return "/usr/local/bin/rig", nil
	}
	rigCommand = func(name string, args ...string) *exec.Cmd {
		called = append(called, append([]string{name}, args...)...)
		return exec.Command("sh", "-c", "exit 0")
	}
	rigGlob = func(pattern string) ([]string, error) {
		return []string{installed}, nil
	}
	rigStat = os.Stat
	rigHomeDir = func() (string, error) {
		return dir, nil
	}

	var stderr bytes.Buffer
	got, err := EnsureInstalledRscript("Rscript", io.Discard, &stderr)
	if err != nil {
		t.Fatalf("EnsureInstalledRscript() error = %v", err)
	}
	if got != installed {
		t.Fatalf("EnsureInstalledRscript() = %q, want %q", got, installed)
	}
	if len(called) < 3 || called[1] != "add" || called[2] != "release" {
		t.Fatalf("rigCommand args = %v, want add release", called)
	}
	if !strings.Contains(stderr.String(), "installing R release via rig") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestLooksLikeVersionSpec(t *testing.T) {
	for _, spec := range []string{"4.4", "4.5.3", "release", "oldrel", "devel", "4.4-arm64"} {
		if !LooksLikeVersionSpec(spec) {
			t.Fatalf("LooksLikeVersionSpec(%q) = false, want true", spec)
		}
	}
	for _, spec := range []string{"Rscript", "custom-rscript", "/opt/R/bin/Rscript"} {
		if LooksLikeVersionSpec(spec) {
			t.Fatalf("LooksLikeVersionSpec(%q) = true, want false", spec)
		}
	}
}
