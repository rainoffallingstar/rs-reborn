package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rainoffallingstar/rs-reborn/internal/project"
	"github.com/rainoffallingstar/rs-reborn/internal/rmanager"
	"github.com/rainoffallingstar/rs-reborn/internal/runner"
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

func TestDoctorCommandAllowsToolchainOnlyWithoutScript(t *testing.T) {
	oldDoctor := cliDoctor
	t.Cleanup(func() {
		cliDoctor = oldDoctor
	})

	dir := t.TempDir()
	called := false
	cliDoctor = func(opts runner.DoctorOptions) error {
		called = true
		if !opts.ToolchainOnly {
			t.Fatalf("opts.ToolchainOnly = false, want true")
		}
		if opts.ProjectDir != dir {
			t.Fatalf("opts.ProjectDir = %q, want %q", opts.ProjectDir, dir)
		}
		if opts.ScriptPath != "" {
			t.Fatalf("opts.ScriptPath = %q, want empty", opts.ScriptPath)
		}
		return nil
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return doctorCommand([]string{"--toolchain-only", dir})
	}); err != nil {
		t.Fatalf("doctorCommand() error = %v", err)
	}
	if !called {
		t.Fatal("doctorCommand() did not call runner")
	}
}

func TestDoctorCommandRejectsMissingScriptWithoutToolchainOnly(t *testing.T) {
	_, err := runWithCapturedStdout(t, func() error {
		return doctorCommand(nil)
	})
	if err == nil {
		t.Fatal("doctorCommand() error = nil, want usage error")
	}
	if !strings.Contains(err.Error(), "usage: rs doctor [flags] path/to/script.R") {
		t.Fatalf("doctorCommand() error = %v", err)
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

func TestInitCommandWritesToolchainConfig(t *testing.T) {
	dir := t.TempDir()

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{
			"--toolchain-prefix", ".toolchain",
			"--toolchain-prefix", "/opt/demo",
			"--pkg-config-path", "pkgconfig",
			dir,
		})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.Defaults.ToolchainPrefixes, []string{".toolchain", "/opt/demo"}) {
		t.Fatalf("Defaults.ToolchainPrefixes = %v", cfg.Defaults.ToolchainPrefixes)
	}
	if !reflect.DeepEqual(cfg.Defaults.PkgConfigPath, []string{"pkgconfig"}) {
		t.Fatalf("Defaults.PkgConfigPath = %v", cfg.Defaults.PkgConfigPath)
	}
}

func TestInitCommandWritesToolchainPresetConfig(t *testing.T) {
	dir := t.TempDir()
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return "/demo-home", nil
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--toolchain-preset", "micromamba", dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	prefix := filepath.Join("/demo-home", "micromamba", "envs", "rs-sysdeps")
	if !reflect.DeepEqual(cfg.Defaults.ToolchainPrefixes, []string{prefix}) {
		t.Fatalf("Defaults.ToolchainPrefixes = %v", cfg.Defaults.ToolchainPrefixes)
	}
	if !reflect.DeepEqual(cfg.Defaults.PkgConfigPath, []string{
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	}) {
		t.Fatalf("Defaults.PkgConfigPath = %v", cfg.Defaults.PkgConfigPath)
	}
}

func TestInitCommandAutoDetectsRecommendedToolchainPreset(t *testing.T) {
	dir := t.TempDir()
	homebrewPrefix := filepath.Join(dir, "homebrew")
	for _, path := range []string{
		homebrewPrefix,
		filepath.Join(homebrewPrefix, "lib", "pkgconfig"),
		filepath.Join(homebrewPrefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	projectDir := filepath.Join(dir, "project")
	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--toolchain-preset", "auto", projectDir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(projectDir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.Defaults.ToolchainPrefixes, []string{homebrewPrefix}) {
		t.Fatalf("Defaults.ToolchainPrefixes = %v", cfg.Defaults.ToolchainPrefixes)
	}
	if !reflect.DeepEqual(cfg.Defaults.PkgConfigPath, []string{
		filepath.Join(homebrewPrefix, "lib", "pkgconfig"),
		filepath.Join(homebrewPrefix, "share", "pkgconfig"),
	}) {
		t.Fatalf("Defaults.PkgConfigPath = %v", cfg.Defaults.PkgConfigPath)
	}
}

func TestInitCommandMergesToolchainPresetWithExplicitFlags(t *testing.T) {
	dir := t.TempDir()
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return "/demo-home", nil
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{
			"--toolchain-preset", "homebrew",
			"--toolchain-prefix", ".toolchain",
			"--pkg-config-path", "pkgconfig",
			dir,
		})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	cfg, err := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	prefix := filepath.Join("/demo-home", "homebrew")
	if !reflect.DeepEqual(cfg.Defaults.ToolchainPrefixes, []string{prefix, ".toolchain"}) {
		t.Fatalf("Defaults.ToolchainPrefixes = %v", cfg.Defaults.ToolchainPrefixes)
	}
	if !reflect.DeepEqual(cfg.Defaults.PkgConfigPath, []string{
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
		"pkgconfig",
	}) {
		t.Fatalf("Defaults.PkgConfigPath = %v", cfg.Defaults.PkgConfigPath)
	}
}

func TestInitCommandRejectsAutoToolchainPresetWhenNothingDetected(t *testing.T) {
	dir := t.TempDir()
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	_, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--toolchain-preset", "auto", filepath.Join(dir, "project")})
	})
	if err == nil {
		t.Fatal("initCommand() error = nil, want auto-detect failure")
	}
	if !strings.Contains(err.Error(), "could not auto-detect a common rootless toolchain preset on this machine") {
		t.Fatalf("initCommand() error = %v", err)
	}
	if !strings.Contains(err.Error(), "rs toolchain detect") {
		t.Fatalf("initCommand() error = %v", err)
	}
}

func TestInitCommandRejectsUnknownToolchainPreset(t *testing.T) {
	dir := t.TempDir()

	_, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{"--toolchain-preset", "unknown", dir})
	})
	if err == nil {
		t.Fatal("initCommand() error = nil, want preset validation error")
	}
	if !strings.Contains(err.Error(), `unsupported --toolchain-preset "unknown"`) {
		t.Fatalf("initCommand() error = %v", err)
	}
}

func TestToolchainTemplateCommandPrintsTOML(t *testing.T) {
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return "/demo-home", nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainTemplateCommand([]string{"micromamba"})
	})
	if err != nil {
		t.Fatalf("toolchainTemplateCommand() error = %v", err)
	}
	prefix := filepath.Join("/demo-home", "micromamba", "envs", "rs-sysdeps")
	if !strings.Contains(output, `toolchain_prefixes = [`+strconv.Quote(prefix)+`]`) {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
	wantPkg := `pkg_config_path = [` + strconv.Quote(filepath.Join(prefix, "lib", "pkgconfig")) + `, ` + strconv.Quote(filepath.Join(prefix, "share", "pkgconfig")) + `]`
	if !strings.Contains(output, wantPkg) {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
}

func TestToolchainTemplateCommandPrintsEnvaPreset(t *testing.T) {
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return "/demo-home", nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainTemplateCommand([]string{"enva"})
	})
	if err != nil {
		t.Fatalf("toolchainTemplateCommand() error = %v", err)
	}
	prefix := filepath.Join("/demo-home", ".local", "share", "rattler", "envs", "rs-sysdeps")
	if !strings.Contains(output, `toolchain_prefixes = [`+strconv.Quote(prefix)+`]`) {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
	wantPkg := `pkg_config_path = [` + strconv.Quote(filepath.Join(prefix, "lib", "pkgconfig")) + `, ` + strconv.Quote(filepath.Join(prefix, "share", "pkgconfig")) + `]`
	if !strings.Contains(output, wantPkg) {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
}

func TestToolchainTemplateCommandPrintsEnv(t *testing.T) {
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return "/demo-home", nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainTemplateCommand([]string{"--format", "env", "homebrew"})
	})
	if err != nil {
		t.Fatalf("toolchainTemplateCommand() error = %v", err)
	}
	prefix := filepath.Join("/demo-home", "homebrew")
	if !strings.Contains(output, `export RS_TOOLCHAIN_PREFIXES='`+prefix+`'`) {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
	wantPkg := strings.Join([]string{
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	}, string(os.PathListSeparator))
	if !strings.Contains(output, `export RS_PKG_CONFIG_PATH='`+wantPkg+`'`) {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
}

func TestToolchainTemplateCommandRejectsUnknownFormat(t *testing.T) {
	_, err := runWithCapturedStdout(t, func() error {
		return toolchainTemplateCommand([]string{"--format", "json", "homebrew"})
	})
	if err == nil {
		t.Fatal("toolchainTemplateCommand() error = nil, want format validation error")
	}
	if !strings.Contains(err.Error(), `unsupported --format "json"`) {
		t.Fatalf("toolchainTemplateCommand() error = %v", err)
	}
}

func TestToolchainTemplateCommandCheckPassesWhenPathsExist(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "homebrew")
	for _, path := range []string{
		prefix,
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	oldHome := cliUserHomeDir
	oldStat := cliStat
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
		cliStat = oldStat
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}
	cliStat = os.Stat

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainTemplateCommand([]string{"homebrew", "--check"})
	})
	if err != nil {
		t.Fatalf("toolchainTemplateCommand() error = %v", err)
	}
	if !strings.Contains(output, "[ok] all preset toolchain paths exist on this machine") {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
}

func TestToolchainTemplateCommandCheckFailsWhenPathsMissing(t *testing.T) {
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return "/demo-home", nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainTemplateCommand([]string{"micromamba", "--check"})
	})
	if err == nil {
		t.Fatal("toolchainTemplateCommand() error = nil, want missing-path failure")
	}
	prefix := filepath.Join("/demo-home", "micromamba", "envs", "rs-sysdeps")
	if !strings.Contains(output, "[check] toolchain prefix missing: "+prefix) {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
	if !strings.Contains(output, "[summary] preset paths are missing on this machine") {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
}

func TestToolchainDetectCommandPrintsDetectedCandidates(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "homebrew")
	for _, path := range []string{
		prefix,
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainDetectCommand(nil)
	})
	if err != nil {
		t.Fatalf("toolchainDetectCommand() error = %v", err)
	}
	if !strings.Contains(output, "[detect] homebrew (complete, recommended)") {
		t.Fatalf("toolchainDetectCommand() output = %q", output)
	}
	if !strings.Contains(output, "[next] preview template: rs toolchain template homebrew --check") {
		t.Fatalf("toolchainDetectCommand() output = %q", output)
	}
	if !strings.Contains(output, `[next] prepare user-local prefix: "`+filepath.Join(prefix, "bin", "brew")+`" install pkg-config gcc`) {
		t.Fatalf("toolchainDetectCommand() output = %q", output)
	}
	if !strings.Contains(output, "[next] initialize project defaults: rs init --toolchain-preset homebrew") {
		t.Fatalf("toolchainDetectCommand() output = %q", output)
	}
}

func TestToolchainDetectCommandJSONOutput(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "micromamba", "envs", "rs-sysdeps")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", prefix, err)
	}
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainDetectCommand([]string{"--json"})
	})
	if err != nil {
		t.Fatalf("toolchainDetectCommand() error = %v", err)
	}
	var report toolchainDetectReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, output)
	}
	if len(report.Candidates) != 1 {
		t.Fatalf("report.Candidates = %v", report.Candidates)
	}
	if report.Candidates[0].Preset != "micromamba" {
		t.Fatalf("report.Candidates[0].Preset = %q", report.Candidates[0].Preset)
	}
	if !report.Candidates[0].Recommended {
		t.Fatalf("report.Candidates[0].Recommended = false, want true")
	}
	if report.Candidates[0].SuggestedInitCommand != "rs init --toolchain-preset micromamba" {
		t.Fatalf("report.Candidates[0].SuggestedInitCommand = %q", report.Candidates[0].SuggestedInitCommand)
	}
	if !strings.Contains(report.Candidates[0].SuggestedSetupCommand, `micromamba create -y -p "`) {
		t.Fatalf("report.Candidates[0].SuggestedSetupCommand = %q", report.Candidates[0].SuggestedSetupCommand)
	}
	if !strings.Contains(report.Candidates[0].SuggestedSetupNote, "dedicated build-tools environment") {
		t.Fatalf("report.Candidates[0].SuggestedSetupNote = %q", report.Candidates[0].SuggestedSetupNote)
	}
	if report.Candidates[0].Complete {
		t.Fatalf("report.Candidates[0].Complete = true, want false")
	}
	if !reflect.DeepEqual(report.Candidates[0].ExistingPrefixes, []string{prefix}) {
		t.Fatalf("report.Candidates[0].ExistingPrefixes = %v", report.Candidates[0].ExistingPrefixes)
	}
}

func TestToolchainDetectCommandPrefersEnvaOverMicromambaWhenBothExist(t *testing.T) {
	dir := t.TempDir()
	envaPrefix := filepath.Join(dir, ".local", "share", "rattler", "envs", "rs-sysdeps")
	micromambaPrefix := filepath.Join(dir, "micromamba", "envs", "rs-sysdeps")
	for _, path := range []string{
		envaPrefix,
		filepath.Join(envaPrefix, "lib", "pkgconfig"),
		filepath.Join(envaPrefix, "share", "pkgconfig"),
		micromambaPrefix,
		filepath.Join(micromambaPrefix, "lib", "pkgconfig"),
		filepath.Join(micromambaPrefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainDetectCommand([]string{"--json"})
	})
	if err != nil {
		t.Fatalf("toolchainDetectCommand() error = %v", err)
	}
	var report toolchainDetectReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, output)
	}
	if len(report.Candidates) < 2 {
		t.Fatalf("report.Candidates = %v, want at least 2", report.Candidates)
	}
	if report.Candidates[0].Preset != "enva" {
		t.Fatalf("report.Candidates[0].Preset = %q, want enva", report.Candidates[0].Preset)
	}
	if !report.Candidates[0].Recommended {
		t.Fatalf("report.Candidates[0].Recommended = false, want true")
	}
	if report.Candidates[1].Preset != "micromamba" {
		t.Fatalf("report.Candidates[1].Preset = %q, want micromamba", report.Candidates[1].Preset)
	}
}

func TestToolchainDetectCommandReportsNoCandidates(t *testing.T) {
	dir := t.TempDir()
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainDetectCommand(nil)
	})
	if err != nil {
		t.Fatalf("toolchainDetectCommand() error = %v", err)
	}
	if !strings.Contains(output, "no common rootless toolchain presets detected on this machine") {
		t.Fatalf("toolchainDetectCommand() output = %q", output)
	}
	if !strings.Contains(output, "rs toolchain template micromamba") {
		t.Fatalf("toolchainDetectCommand() output = %q", output)
	}
}

func TestToolchainTemplateCommandSupportsAutoPreset(t *testing.T) {
	dir := t.TempDir()
	homebrewPrefix := filepath.Join(dir, "homebrew")
	for _, path := range []string{
		homebrewPrefix,
		filepath.Join(homebrewPrefix, "lib", "pkgconfig"),
		filepath.Join(homebrewPrefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainTemplateCommand([]string{"auto"})
	})
	if err != nil {
		t.Fatalf("toolchainTemplateCommand() error = %v", err)
	}
	if !strings.Contains(output, `toolchain_prefixes = ["`) {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
	if !strings.Contains(output, "homebrew") || !strings.Contains(output, "pkg_config_path") {
		t.Fatalf("toolchainTemplateCommand() output = %q", output)
	}
}

func TestToolchainBootstrapCommandPrintsBootstrapPlan(t *testing.T) {
	dir := t.TempDir()
	homebrewPrefix := filepath.Join(dir, "homebrew")
	for _, path := range []string{
		homebrewPrefix,
		filepath.Join(homebrewPrefix, "lib", "pkgconfig"),
		filepath.Join(homebrewPrefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainBootstrapCommand([]string{"auto"})
	})
	if err != nil {
		t.Fatalf("toolchainBootstrapCommand() error = %v", err)
	}
	if !strings.Contains(output, "[bootstrap] preset: homebrew (detected complete layout, recommended)") {
		t.Fatalf("toolchainBootstrapCommand() output = %q", output)
	}
	if !strings.Contains(output, `[bootstrap] setup command: "`+filepath.Join(homebrewPrefix, "bin", "brew")+`" install pkg-config gcc`) {
		t.Fatalf("toolchainBootstrapCommand() output = %q", output)
	}
	if !strings.Contains(output, "[next] initialize project defaults: rs init --toolchain-preset homebrew") {
		t.Fatalf("toolchainBootstrapCommand() output = %q", output)
	}
	if !strings.Contains(output, "[next] validate toolchain configuration: rs doctor --toolchain-only") {
		t.Fatalf("toolchainBootstrapCommand() output = %q", output)
	}
}

func TestToolchainBootstrapCommandJSONOutput(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "micromamba", "envs", "rs-sysdeps")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", prefix, err)
	}

	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	output, err := runWithCapturedStdout(t, func() error {
		return toolchainBootstrapCommand([]string{"micromamba", "--json"})
	})
	if err != nil {
		t.Fatalf("toolchainBootstrapCommand() error = %v", err)
	}
	var report toolchainBootstrapReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, output)
	}
	if report.Candidate.Preset != "micromamba" {
		t.Fatalf("report.Candidate.Preset = %q", report.Candidate.Preset)
	}
	if report.InitCommand != "rs init --toolchain-preset micromamba" {
		t.Fatalf("report.InitCommand = %q", report.InitCommand)
	}
	if report.TemplateCheckCommand != "rs toolchain template micromamba --check" {
		t.Fatalf("report.TemplateCheckCommand = %q", report.TemplateCheckCommand)
	}
	if !strings.Contains(report.Candidate.SuggestedSetupCommand, `micromamba create -y -p "`) {
		t.Fatalf("report.Candidate.SuggestedSetupCommand = %q", report.Candidate.SuggestedSetupCommand)
	}
}

func TestToolchainBootstrapCommandPrintsEnvaPlan(t *testing.T) {
	output, err := runWithCapturedStdout(t, func() error {
		return toolchainBootstrapCommand([]string{"enva"})
	})
	if err != nil {
		t.Fatalf("toolchainBootstrapCommand() error = %v", err)
	}
	if !strings.Contains(output, "[bootstrap] preset: enva") {
		t.Fatalf("toolchainBootstrapCommand() output = %q", output)
	}
	if !strings.Contains(output, "enva") || !strings.Contains(output, "create --yaml") {
		t.Fatalf("toolchainBootstrapCommand() output = %q", output)
	}
	if !strings.Contains(output, "[next] initialize project defaults: rs init --toolchain-preset enva") {
		t.Fatalf("toolchainBootstrapCommand() output = %q", output)
	}
}

func TestRInstallCommandPassesBootstrapToolchainFlag(t *testing.T) {
	oldInstall := cliInstallRWithOptions
	t.Cleanup(func() {
		cliInstallRWithOptions = oldInstall
	})

	called := false
	cliInstallRWithOptions = func(opts rmanager.InstallOptions) error {
		called = true
		if opts.Version != "4.4.3" {
			t.Fatalf("opts.Version = %q", opts.Version)
		}
		if opts.Method != rmanager.InstallMethodSource {
			t.Fatalf("opts.Method = %q", opts.Method)
		}
		if !opts.BootstrapToolchain {
			t.Fatalf("opts.BootstrapToolchain = false, want true")
		}
		return nil
	}

	if _, err := runWithCapturedStdout(t, func() error {
		return rInstallCommand([]string{"--method", "source", "--bootstrap-toolchain", "4.4.3"})
	}); err != nil {
		t.Fatalf("rInstallCommand() error = %v", err)
	}
	if !called {
		t.Fatal("rInstallCommand() did not call installer")
	}
}

func TestToolchainDetectCommandSortsAndMarksRecommendedCandidate(t *testing.T) {
	dir := t.TempDir()
	homebrewPrefix := filepath.Join(dir, "homebrew")
	micromambaPrefix := filepath.Join(dir, "micromamba", "envs", "rs-sysdeps")
	for _, path := range []string{
		homebrewPrefix,
		filepath.Join(homebrewPrefix, "lib", "pkgconfig"),
		filepath.Join(homebrewPrefix, "share", "pkgconfig"),
		micromambaPrefix,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	oldHome := cliUserHomeDir
	t.Cleanup(func() {
		cliUserHomeDir = oldHome
	})
	cliUserHomeDir = func() (string, error) {
		return dir, nil
	}

	candidates, err := detectToolchainCandidates()
	if err != nil {
		t.Fatalf("detectToolchainCandidates() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2 (%v)", len(candidates), candidates)
	}
	if !candidates[0].Recommended {
		t.Fatalf("candidates[0].Recommended = false, want true")
	}
	if candidates[1].Recommended {
		t.Fatalf("candidates[1].Recommended = true, want false")
	}
	if candidates[0].Preset != "homebrew" {
		t.Fatalf("candidates[0].Preset = %q, want homebrew because complete should outrank partial", candidates[0].Preset)
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

func TestRUseCommandRejectsUnresolvableVersionWithoutMutatingConfig(t *testing.T) {
	dir := t.TempDir()

	if _, err := runWithCapturedStdout(t, func() error {
		return initCommand([]string{dir})
	}); err != nil {
		t.Fatalf("initCommand() error = %v", err)
	}

	oldValidate := cliValidateVersionSelector
	oldResolvePath := cliResolveVersionOrPath
	oldResolveVersion := cliResolveVersionSelector
	t.Cleanup(func() {
		cliValidateVersionSelector = oldValidate
		cliResolveVersionOrPath = oldResolvePath
		cliResolveVersionSelector = oldResolveVersion
	})
	cliValidateVersionSelector = func(spec string) error { return nil }
	cliResolveVersionOrPath = func(spec string) (string, error) {
		return "", fmt.Errorf("could not find an installed Rscript for version %q", spec)
	}
	cliResolveVersionSelector = func(spec string) (string, error) {
		return "", fmt.Errorf("could not resolve R version selector %q", spec)
	}

	_, err := runWithCapturedStdout(t, func() error {
		return rUseCommand([]string{"--project-dir", dir, "5.3.2"})
	})
	if err == nil {
		t.Fatal("rUseCommand() error = nil, want unresolved version failure")
	}
	if !strings.Contains(err.Error(), `could not resolve R version selector "5.3.2"`) {
		t.Fatalf("rUseCommand() error = %v", err)
	}

	cfg, loadErr := project.LoadEditable(filepath.Join(dir, project.ConfigFileName))
	if loadErr != nil {
		t.Fatalf("LoadEditable() error = %v", loadErr)
	}
	if cfg.Defaults.RVersion != "" || cfg.Defaults.Rscript != "" {
		t.Fatalf("config unexpectedly changed after rejected version: %#v", cfg.Defaults)
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

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	runErr := fn()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out, runErr
}
