package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gr/internal/project"
	"gr/internal/rmanager"
)

func TestInitCommandFromScriptWritesRootPackages(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("library(dplyr)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from", scriptPath, dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"dplyr", "jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
	if len(cfg.Scripts) != 0 {
		t.Fatalf("Scripts = %#v, want empty", cfg.Scripts)
	}
}

func TestInitCommandFromScriptSplitsKnownBiocPackages(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("DESeq2::DESeq()\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from", scriptPath, dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
	if !reflect.DeepEqual(cfg.Defaults.BiocPackages, []string{"DESeq2"}) {
		t.Fatalf("Defaults.BiocPackages = %v", cfg.Defaults.BiocPackages)
	}
}

func TestInitCommandFromScriptFiltersBundledPackagesByDefault(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("library(stats)\nlibrary(utils)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from", scriptPath, dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
}

func TestInitCommandFromScriptCanIncludeBundledPackages(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("library(stats)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from", scriptPath, "--include-bundled", dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"jsonlite", "stats"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
}

func TestInitCommandFromScriptWritesScriptBlock(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("library(cli)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from", scriptPath, "--write-script-block", dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if len(cfg.Defaults.Packages) != 0 {
		t.Fatalf("Defaults.Packages = %v, want empty", cfg.Defaults.Packages)
	}
	scriptCfg, ok := cfg.Scripts["scripts/report.R"]
	if !ok {
		t.Fatalf("Scripts entry missing: %#v", cfg.Scripts)
	}
	if !reflect.DeepEqual(scriptCfg.Packages, []string{"cli", "jsonlite"}) {
		t.Fatalf("Scripts.Packages = %v", scriptCfg.Packages)
	}
}

func TestInitCommandFromMultipleScriptsWritesScriptBlocks(t *testing.T) {
	dir := t.TempDir()
	scriptA := filepath.Join(dir, "scripts", "a.R")
	scriptB := filepath.Join(dir, "scripts", "b.R")
	if err := os.MkdirAll(filepath.Dir(scriptA), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptA, []byte("library(stats)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.R) error = %v", err)
	}
	if err := os.WriteFile(scriptB, []byte("library(dplyr)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.R) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from", scriptA, "--from", scriptB, dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if len(cfg.Defaults.Packages) != 0 {
		t.Fatalf("Defaults.Packages = %v, want empty", cfg.Defaults.Packages)
	}
	if !reflect.DeepEqual(cfg.Scripts["scripts/a.R"].Packages, []string{"jsonlite"}) {
		t.Fatalf("Scripts[a].Packages = %v", cfg.Scripts["scripts/a.R"].Packages)
	}
	if !reflect.DeepEqual(cfg.Scripts["scripts/b.R"].Packages, []string{"dplyr"}) {
		t.Fatalf("Scripts[b].Packages = %v", cfg.Scripts["scripts/b.R"].Packages)
	}
}

func TestInitCommandFromDirWritesScriptBlocks(t *testing.T) {
	dir := t.TempDir()
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(filepath.Join(scriptsDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(scriptsDir, ".rs-cache"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.rs-cache) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "a.R"), []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.R) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "b.Rscript"), []byte("library(dplyr)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.Rscript) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, ".git", "ignored.R"), []byte("library(cli)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(ignored.R) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from-dir", scriptsDir, dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if len(cfg.Defaults.Packages) != 0 {
		t.Fatalf("Defaults.Packages = %v, want empty", cfg.Defaults.Packages)
	}
	if !reflect.DeepEqual(cfg.Scripts["scripts/a.R"].Packages, []string{"jsonlite"}) {
		t.Fatalf("Scripts[a].Packages = %v", cfg.Scripts["scripts/a.R"].Packages)
	}
	if !reflect.DeepEqual(cfg.Scripts["scripts/b.Rscript"].Packages, []string{"dplyr"}) {
		t.Fatalf("Scripts[b].Packages = %v", cfg.Scripts["scripts/b.Rscript"].Packages)
	}
	if _, ok := cfg.Scripts["scripts/.git/ignored.R"]; ok {
		t.Fatalf("ignored script unexpectedly included: %#v", cfg.Scripts)
	}
}

func TestInitCommandBiocPackageAddsProjectDefault(t *testing.T) {
	dir := t.TempDir()

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--bioc-package", "Biostrings", dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.BiocPackages, []string{"Biostrings"}) {
		t.Fatalf("Defaults.BiocPackages = %v", cfg.Defaults.BiocPackages)
	}
}

func TestRUseCommandRejectsUnsupportedNativeSelector(t *testing.T) {
	dir := t.TempDir()

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	_, err := runWithCapturedStdout(t, func() error {
		return rUseCommand([]string{"--project-dir", dir, "oldrel"})
	})
	if err == nil {
		t.Fatal("rUseCommand() error = nil, want unsupported selector")
	}
	if !strings.Contains(err.Error(), `native R manager does not yet support selector "oldrel"`) {
		t.Fatalf("rUseCommand() error = %v", err)
	}

	cfg, loadErr := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if loadErr != nil {
		t.Fatalf("LoadEditable() error = %v", loadErr)
	}
	if cfg.Defaults.RVersion != "" || cfg.Defaults.Rscript != "" {
		t.Fatalf("config unexpectedly changed after rejected selector: %#v", cfg.Defaults)
	}
	if validateErr := rmanager.ValidateVersionSelector("oldrel"); validateErr == nil {
		t.Fatal("ValidateVersionSelector(oldrel) error = nil, want unsupported selector")
	}
}

func TestInitCommandExcludeRemovesDetectedPackage(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("library(dplyr)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from", scriptPath, "--exclude", "dplyr", dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
}

func TestInitCommandIncludeAddsProjectDefaultPackages(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--from", scriptPath, "--include", "cli", "--include", "Biostrings", dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"cli", "jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
	if !reflect.DeepEqual(cfg.Defaults.BiocPackages, []string{"Biostrings"}) {
		t.Fatalf("Defaults.BiocPackages = %v", cfg.Defaults.BiocPackages)
	}
}

func TestInitCommandWritesRscript(t *testing.T) {
	dir := t.TempDir()

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--rscript", "tools/Rscript-4.4", dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if cfg.Defaults.Rscript != "tools/Rscript-4.4" {
		t.Fatalf("Defaults.Rscript = %q", cfg.Defaults.Rscript)
	}
}

func TestInitCommandWritesRVersion(t *testing.T) {
	dir := t.TempDir()

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--r-version", "4.4", dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if cfg.Defaults.RVersion != "4.4" {
		t.Fatalf("Defaults.RVersion = %q", cfg.Defaults.RVersion)
	}
}

func TestRUseCommandWritesResolvedRscript(t *testing.T) {
	dir := t.TempDir()
	rscriptPath := filepath.Join(dir, "tools", "Rscript-4.4")
	if err := os.MkdirAll(filepath.Dir(rscriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(rscriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(Rscript) error = %v", err)
	}
	if err := project.Save(filepath.Join(dir, project.ConfigFileName), project.NewDefaultConfig(project.InitOptions{})); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return rUseCommand([]string{"--project-dir", dir, rscriptPath})
	}); err != nil {
		t.Fatalf("rUseCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if cfg.Defaults.Rscript != rscriptPath {
		t.Fatalf("Defaults.Rscript = %q, want %q", cfg.Defaults.Rscript, rscriptPath)
	}
	if cfg.Defaults.RVersion != "" {
		t.Fatalf("Defaults.RVersion = %q, want empty", cfg.Defaults.RVersion)
	}
}

func TestRUseCommandWritesRVersion(t *testing.T) {
	dir := t.TempDir()
	if err := project.Save(filepath.Join(dir, project.ConfigFileName), project.NewDefaultConfig(project.InitOptions{})); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return rUseCommand([]string{"--project-dir", dir, "4.4"})
	}); err != nil {
		t.Fatalf("rUseCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if cfg.Defaults.RVersion != "4.4" {
		t.Fatalf("Defaults.RVersion = %q, want 4.4", cfg.Defaults.RVersion)
	}
	if cfg.Defaults.Rscript != "" {
		t.Fatalf("Defaults.Rscript = %q, want empty", cfg.Defaults.Rscript)
	}
}

func TestRWhichCommandPrintsConfiguredRscript(t *testing.T) {
	dir := t.TempDir()
	rscriptPath := filepath.Join(dir, "tools", "Rscript-4.4")
	if err := os.MkdirAll(filepath.Dir(rscriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(rscriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(Rscript) error = %v", err)
	}

	cfg := project.NewDefaultConfig(project.InitOptions{})
	cfg.Defaults.Rscript = rscriptPath
	if err := project.Save(filepath.Join(dir, project.ConfigFileName), cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	output, err := runWithCapturedStdout(t, func() error {
		return rWhichCommand([]string{dir})
	})
	if err != nil {
		t.Fatalf("rWhichCommand() error = %v", err)
	}
	if strings.TrimSpace(output) != rscriptPath {
		t.Fatalf("rWhichCommand() output = %q, want %q", output, rscriptPath)
	}
}

func TestScanCommandJSON(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	if err := os.WriteFile(scriptPath, []byte("library(stats)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	output, err := runWithCapturedStdout(t, func() error {
		return scanCommand([]string{"--json", scriptPath})
	})
	if err != nil {
		t.Fatalf("scanCommand() error = %v", err)
	}

	var report struct {
		Script          string   `json:"script"`
		Packages        []string `json:"packages"`
		CRANPackages    []string `json:"cran_packages"`
		BiocPackages    []string `json:"bioc_packages"`
		InstallableOnly bool     `json:"installable_only"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, output)
	}
	if report.Script != scriptPath {
		t.Fatalf("report.Script = %q, want %q", report.Script, scriptPath)
	}
	if !reflect.DeepEqual(report.Packages, []string{"jsonlite", "stats"}) {
		t.Fatalf("report.Packages = %v", report.Packages)
	}
	if !reflect.DeepEqual(report.CRANPackages, []string{"jsonlite", "stats"}) {
		t.Fatalf("report.CRANPackages = %v", report.CRANPackages)
	}
	if len(report.BiocPackages) != 0 {
		t.Fatalf("report.BiocPackages = %v, want empty", report.BiocPackages)
	}
	if report.InstallableOnly {
		t.Fatalf("report.InstallableOnly = true, want false")
	}
}

func TestScanCommandJSONSplitsBiocPackages(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	if err := os.WriteFile(scriptPath, []byte("DESeq2::DESeq()\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	output, err := runWithCapturedStdout(t, func() error {
		return scanCommand([]string{"--json", "--installable", scriptPath})
	})
	if err != nil {
		t.Fatalf("scanCommand() error = %v", err)
	}

	var report struct {
		Packages        []string `json:"packages"`
		CRANPackages    []string `json:"cran_packages"`
		BiocPackages    []string `json:"bioc_packages"`
		InstallableOnly bool     `json:"installable_only"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, output)
	}
	if !reflect.DeepEqual(report.Packages, []string{"DESeq2", "jsonlite"}) {
		t.Fatalf("report.Packages = %v", report.Packages)
	}
	if !reflect.DeepEqual(report.CRANPackages, []string{"jsonlite"}) {
		t.Fatalf("report.CRANPackages = %v", report.CRANPackages)
	}
	if !reflect.DeepEqual(report.BiocPackages, []string{"DESeq2"}) {
		t.Fatalf("report.BiocPackages = %v", report.BiocPackages)
	}
	if !report.InstallableOnly {
		t.Fatalf("report.InstallableOnly = false, want true")
	}
}

func TestScanCommandInstallableText(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	if err := os.WriteFile(scriptPath, []byte("library(stats)\njsonlite::fromJSON('{}')\nlibrary(utils)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	output, err := runWithCapturedStdout(t, func() error {
		return scanCommand([]string{"--installable", scriptPath})
	})
	if err != nil {
		t.Fatalf("scanCommand() error = %v", err)
	}

	if strings.TrimSpace(output) != "jsonlite" {
		t.Fatalf("scan output = %q, want only jsonlite", output)
	}
}

func TestAddCommandResolvesRelativeScriptAgainstProjectDir(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("print('report')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	cfg := project.NewDefaultConfig(project.InitOptions{})
	if err := project.Save(filepath.Join(dir, project.ConfigFileName), cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return addCommand([]string{"--project-dir", dir, "--script", "scripts/report.R", "jsonlite"})
	}); err != nil {
		t.Fatalf("addCommand() error = %v", err)
	}

	updated, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	scriptCfg, ok := updated.Scripts["scripts/report.R"]
	if !ok {
		t.Fatalf("Scripts entry missing: %#v", updated.Scripts)
	}
	if !reflect.DeepEqual(scriptCfg.Packages, []string{"jsonlite"}) {
		t.Fatalf("Scripts.Packages = %v", scriptCfg.Packages)
	}
}

func TestRemoveCommandResolvesRelativeScriptAgainstProjectDir(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "scripts", "report.R")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("print('report')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	cfg, err := project.NewConfigFromScript(project.InitOptions{}, dir, scriptPath, true)
	if err != nil {
		t.Fatalf("NewConfigFromScript() error = %v", err)
	}
	cfg.Scripts["scripts/report.R"] = project.ScriptConfig{
		Packages: []string{"jsonlite"},
	}
	if err := project.Save(filepath.Join(dir, project.ConfigFileName), cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return removeCommand([]string{"--project-dir", dir, "--script", "scripts/report.R", "jsonlite"})
	}); err != nil {
		t.Fatalf("removeCommand() error = %v", err)
	}

	updated, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if _, ok := updated.Scripts["scripts/report.R"]; ok {
		t.Fatalf("script entry still present: %#v", updated.Scripts)
	}
}

func runWithCapturedStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = original
	}()

	runErr := fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()
	return string(out), runErr
}
