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

func TestRunRigWithoutRigReturnsHelpfulNextStep(t *testing.T) {
	origRigLookPath := rigLookPath
	origToolLookPath := toolLookPath
	origOS := rigOS
	t.Cleanup(func() {
		rigLookPath = origRigLookPath
		toolLookPath = origToolLookPath
		rigOS = origOS
	})

	rigOS = "darwin"
	rigLookPath = func(file string) (string, error) {
		return "", fmt.Errorf("executable file not found")
	}
	toolLookPath = func(file string) (string, error) {
		if file == "brew" {
			return "/opt/homebrew/bin/brew", nil
		}
		return "", fmt.Errorf("missing")
	}
	t.Setenv(autoInstallRigEnv, "")

	err := runRig(io.Discard, io.Discard, "list")
	if err == nil {
		t.Fatalf("runRig() error = nil, want missing rig")
	}
	if !strings.Contains(err.Error(), "next step: install rig with Homebrew and rerun rs: brew tap r-lib/rig && brew install --cask rig") {
		t.Fatalf("runRig() error = %v", err)
	}
	if !strings.Contains(err.Error(), "explicit auto-install: set RS_AUTO_INSTALL_RIG=1 and retry") {
		t.Fatalf("runRig() error = %v", err)
	}
}

func TestRunRigAutoInstallsRigWhenEnabled(t *testing.T) {
	origRigLookPath := rigLookPath
	origRigCommand := rigCommand
	origToolLookPath := toolLookPath
	origToolCommand := toolCommand
	origOS := rigOS
	t.Cleanup(func() {
		rigLookPath = origRigLookPath
		rigCommand = origRigCommand
		toolLookPath = origToolLookPath
		toolCommand = origToolCommand
		rigOS = origOS
	})

	rigOS = "darwin"
	installed := false
	rigLookPath = func(file string) (string, error) {
		if file != "rig" {
			return "", fmt.Errorf("unexpected lookpath %q", file)
		}
		if installed {
			return "/opt/homebrew/bin/rig", nil
		}
		return "", fmt.Errorf("missing")
	}
	toolLookPath = func(file string) (string, error) {
		switch file {
		case "brew":
			return "/opt/homebrew/bin/brew", nil
		default:
			return "", fmt.Errorf("missing")
		}
	}

	var toolCalls []string
	toolCommand = func(name string, args ...string) *exec.Cmd {
		toolCalls = append(toolCalls, name+" "+strings.Join(args, " "))
		installed = true
		return exec.Command("sh", "-c", "exit 0")
	}

	var rigCalls []string
	rigCommand = func(name string, args ...string) *exec.Cmd {
		rigCalls = append(rigCalls, name+" "+strings.Join(args, " "))
		return exec.Command("sh", "-c", "exit 0")
	}

	t.Setenv(autoInstallRigEnv, "1")
	if err := runRig(io.Discard, io.Discard, "list"); err != nil {
		t.Fatalf("runRig() error = %v", err)
	}
	if len(toolCalls) == 0 || !strings.Contains(toolCalls[0], "brew tap r-lib/rig") {
		t.Fatalf("toolCalls = %v, want brew bootstrap", toolCalls)
	}
	if len(rigCalls) == 0 || !strings.Contains(rigCalls[0], "/opt/homebrew/bin/rig list") {
		t.Fatalf("rigCalls = %v, want rig list", rigCalls)
	}
}
