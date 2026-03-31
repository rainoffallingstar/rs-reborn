package project

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	src := `
repo = "https://packagemanager.posit.co/cran/latest"
cache_dir = ".rs-cache"
lockfile = "state/rs.lock.json"
rscript = "bin/Rscript"
r_version = "4.4"
toolchain_prefixes = [".toolchain", "/opt/demo"]
pkg_config_path = ["pkgconfig", "/opt/demo/lib/pkgconfig"]
packages = ["dplyr", "jsonlite"]
bioc_packages = ["Biostrings"]

[sources."custompkg"]
type = "github"
host = "github.example.com/api/v3"
repo = "r-lib/cli"
ref = "v3.6.5"
subdir = "pkg"
token_env = "GITHUB_PAT"

[sources."localpkg"]
type = "local"
path = "vendor/localpkg_0.1.0.tar.gz"

[sources."gitpkg"]
type = "git"
url = "file:///tmp/example.git"
ref = "main"
subdir = "pkg"

[scripts."scripts/report.R"]
repo = "https://cran.rstudio.com"
packages = ["cli", "jsonlite"]
bioc_packages = ["DESeq2"]

[scripts."scripts/report.R".sources."custompkg"]
type = "github"
repo = "owner/reportpkg"
ref = "feature-branch"
`

	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.Defaults.Repo != "https://packagemanager.posit.co/cran/latest" {
		t.Fatalf("Defaults.Repo = %q", cfg.Defaults.Repo)
	}
	if cfg.Defaults.CacheDir != ".rs-cache" {
		t.Fatalf("Defaults.CacheDir = %q", cfg.Defaults.CacheDir)
	}
	if cfg.Defaults.Lockfile != "state/rs.lock.json" {
		t.Fatalf("Defaults.Lockfile = %q", cfg.Defaults.Lockfile)
	}
	if cfg.Defaults.Rscript != "bin/Rscript" {
		t.Fatalf("Defaults.Rscript = %q", cfg.Defaults.Rscript)
	}
	if cfg.Defaults.RVersion != "4.4" {
		t.Fatalf("Defaults.RVersion = %q", cfg.Defaults.RVersion)
	}
	if !reflect.DeepEqual(cfg.Defaults.ToolchainPrefixes, []string{".toolchain", "/opt/demo"}) {
		t.Fatalf("Defaults.ToolchainPrefixes = %v", cfg.Defaults.ToolchainPrefixes)
	}
	if !reflect.DeepEqual(cfg.Defaults.PkgConfigPath, []string{"pkgconfig", "/opt/demo/lib/pkgconfig"}) {
		t.Fatalf("Defaults.PkgConfigPath = %v", cfg.Defaults.PkgConfigPath)
	}
	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"dplyr", "jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
	if !reflect.DeepEqual(cfg.Defaults.BiocPackages, []string{"Biostrings"}) {
		t.Fatalf("Defaults.BiocPackages = %v", cfg.Defaults.BiocPackages)
	}
	if cfg.Sources["custompkg"].Type != "github" {
		t.Fatalf("Sources[custompkg].Type = %q", cfg.Sources["custompkg"].Type)
	}
	if cfg.Sources["custompkg"].Repo != "r-lib/cli" {
		t.Fatalf("Sources[custompkg].Repo = %q", cfg.Sources["custompkg"].Repo)
	}
	if cfg.Sources["custompkg"].Host != "github.example.com/api/v3" {
		t.Fatalf("Sources[custompkg].Host = %q", cfg.Sources["custompkg"].Host)
	}
	if cfg.Sources["custompkg"].Subdir != "pkg" {
		t.Fatalf("Sources[custompkg].Subdir = %q", cfg.Sources["custompkg"].Subdir)
	}
	if cfg.Sources["custompkg"].TokenEnv != "GITHUB_PAT" {
		t.Fatalf("Sources[custompkg].TokenEnv = %q", cfg.Sources["custompkg"].TokenEnv)
	}
	if cfg.Sources["localpkg"].Path != "vendor/localpkg_0.1.0.tar.gz" {
		t.Fatalf("Sources[localpkg].Path = %q", cfg.Sources["localpkg"].Path)
	}
	if cfg.Sources["gitpkg"].URL != "file:///tmp/example.git" {
		t.Fatalf("Sources[gitpkg].URL = %q", cfg.Sources["gitpkg"].URL)
	}

	scriptCfg := cfg.Scripts["scripts/report.R"]
	if scriptCfg.Repo != "https://cran.rstudio.com" {
		t.Fatalf("Scripts repo = %q", scriptCfg.Repo)
	}
	if !reflect.DeepEqual(scriptCfg.Packages, []string{"cli", "jsonlite"}) {
		t.Fatalf("Scripts packages = %v", scriptCfg.Packages)
	}
	if !reflect.DeepEqual(scriptCfg.BiocPackages, []string{"DESeq2"}) {
		t.Fatalf("Scripts bioc packages = %v", scriptCfg.BiocPackages)
	}
	if scriptCfg.Sources["custompkg"].Repo != "owner/reportpkg" {
		t.Fatalf("Scripts sources repo = %q", scriptCfg.Sources["custompkg"].Repo)
	}
	if scriptCfg.Sources["custompkg"].Ref != "feature-branch" {
		t.Fatalf("Scripts sources ref = %q", scriptCfg.Sources["custompkg"].Ref)
	}
}

func TestLoadResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rs.toml")
	content := `cache_dir = ".rs-cache"
lockfile = "locks/rs.lock.json"
rscript = "bin/Rscript"
r_version = "4.4"
toolchain_prefixes = [".toolchain", "/opt/demo"]
pkg_config_path = ["pkgconfig", "/opt/demo/lib/pkgconfig"]

[sources."localpkg"]
type = "local"
path = "vendor/localpkg_0.1.0.tar.gz"

[scripts."scripts/report.R"]
cache_dir = ".script-cache"
lockfile = "locks/report.lock.json"
rscript = "tools/Rscript"

[scripts."scripts/report.R".sources."scriptpkg"]
type = "local"
path = "vendor/scriptpkg_0.2.0.tar.gz"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Defaults.CacheDir != filepath.Join(dir, ".rs-cache") {
		t.Fatalf("Defaults.CacheDir = %q", cfg.Defaults.CacheDir)
	}
	if cfg.Defaults.Lockfile != filepath.Join(dir, "locks", "rs.lock.json") {
		t.Fatalf("Defaults.Lockfile = %q", cfg.Defaults.Lockfile)
	}
	if cfg.Defaults.Rscript != filepath.Join(dir, "bin", "Rscript") {
		t.Fatalf("Defaults.Rscript = %q", cfg.Defaults.Rscript)
	}
	if cfg.Defaults.RVersion != "4.4" {
		t.Fatalf("Defaults.RVersion = %q", cfg.Defaults.RVersion)
	}
	if !reflect.DeepEqual(cfg.Defaults.ToolchainPrefixes, []string{filepath.Join(dir, ".toolchain"), "/opt/demo"}) {
		t.Fatalf("Defaults.ToolchainPrefixes = %v", cfg.Defaults.ToolchainPrefixes)
	}
	if !reflect.DeepEqual(cfg.Defaults.PkgConfigPath, []string{filepath.Join(dir, "pkgconfig"), "/opt/demo/lib/pkgconfig"}) {
		t.Fatalf("Defaults.PkgConfigPath = %v", cfg.Defaults.PkgConfigPath)
	}
	if cfg.Sources["localpkg"].Path != filepath.Join(dir, "vendor", "localpkg_0.1.0.tar.gz") {
		t.Fatalf("Sources[localpkg].Path = %q", cfg.Sources["localpkg"].Path)
	}

	scriptCfg := cfg.Scripts["scripts/report.R"]
	if scriptCfg.CacheDir != filepath.Join(dir, ".script-cache") {
		t.Fatalf("Scripts.CacheDir = %q", scriptCfg.CacheDir)
	}
	if scriptCfg.Lockfile != filepath.Join(dir, "locks", "report.lock.json") {
		t.Fatalf("Scripts.Lockfile = %q", scriptCfg.Lockfile)
	}
	if scriptCfg.Rscript != filepath.Join(dir, "tools", "Rscript") {
		t.Fatalf("Scripts.Rscript = %q", scriptCfg.Rscript)
	}
	if scriptCfg.Sources["scriptpkg"].Path != filepath.Join(dir, "vendor", "scriptpkg_0.2.0.tar.gz") {
		t.Fatalf("Scripts.Sources[scriptpkg].Path = %q", scriptCfg.Sources["scriptpkg"].Path)
	}
}

func TestResolveForScript(t *testing.T) {
	cfg := Config{
		RootDir: "/tmp/project",
		Defaults: ScriptConfig{
			Repo:              "https://cloud.r-project.org",
			CacheDir:          "/tmp/project/.rs-cache",
			Lockfile:          "/tmp/project/rs.lock.json",
			Rscript:           "/tmp/project/bin/Rscript",
			RVersion:          "4.4",
			ToolchainPrefixes: []string{"/tmp/project/.toolchain"},
			PkgConfigPath:     []string{"/tmp/project/.toolchain/lib/pkgconfig"},
			Packages:          []string{"jsonlite"},
			BiocPackages:      []string{"Biostrings"},
		},
		Sources: map[string]SourceSpec{
			"custompkg": {
				Package:  "custompkg",
				Type:     "github",
				Host:     "api.github.com",
				Repo:     "owner/custompkg",
				Ref:      "main",
				Subdir:   "rootpkg",
				TokenEnv: "GITHUB_PAT",
			},
			"gitpkg": {
				Package: "gitpkg",
				Type:    "git",
				URL:     "file:///tmp/example.git",
				Ref:     "main",
				Subdir:  "pkg",
			},
		},
		Scripts: map[string]ScriptConfig{
			"scripts/report.R": {
				Repo:              "https://cran.rstudio.com",
				Rscript:           "/tmp/project/tools/Rscript-4.4",
				RVersion:          "4.4",
				ToolchainPrefixes: []string{"/tmp/project/scripts/.toolchain"},
				PkgConfigPath:     []string{"/tmp/project/scripts/pkgconfig"},
				Packages:          []string{"cli", "jsonlite"},
				BiocPackages:      []string{"DESeq2"},
				Sources: map[string]SourceSpec{
					"custompkg": {
						Package:  "custompkg",
						Type:     "github",
						Host:     "github.example.com/api/v3",
						Repo:     "owner/reportpkg",
						Ref:      "feature",
						Subdir:   "pkg",
						TokenEnv: "GH_ENTERPRISE_PAT",
					},
				},
			},
		},
	}

	resolved, err := cfg.ResolveForScript("/tmp/project/scripts/report.R")
	if err != nil {
		t.Fatalf("ResolveForScript() error = %v", err)
	}

	if resolved.Repo != "https://cran.rstudio.com" {
		t.Fatalf("Repo = %q", resolved.Repo)
	}
	if resolved.Rscript != "/tmp/project/tools/Rscript-4.4" {
		t.Fatalf("Rscript = %q", resolved.Rscript)
	}
	if resolved.RVersion != "4.4" {
		t.Fatalf("RVersion = %q", resolved.RVersion)
	}
	if !reflect.DeepEqual(resolved.ToolchainPrefixes, []string{"/tmp/project/.toolchain", "/tmp/project/scripts/.toolchain"}) {
		t.Fatalf("ToolchainPrefixes = %v", resolved.ToolchainPrefixes)
	}
	if !reflect.DeepEqual(resolved.PkgConfigPath, []string{"/tmp/project/.toolchain/lib/pkgconfig", "/tmp/project/scripts/pkgconfig"}) {
		t.Fatalf("PkgConfigPath = %v", resolved.PkgConfigPath)
	}
	if resolved.ScriptKey != "scripts/report.R" {
		t.Fatalf("ScriptKey = %q", resolved.ScriptKey)
	}
	if !reflect.DeepEqual(resolved.Packages, []string{"jsonlite", "cli"}) {
		t.Fatalf("Packages = %v", resolved.Packages)
	}
	if !reflect.DeepEqual(resolved.BiocPackages, []string{"Biostrings", "DESeq2"}) {
		t.Fatalf("BiocPackages = %v", resolved.BiocPackages)
	}
	if resolved.Sources["custompkg"].Repo != "owner/reportpkg" {
		t.Fatalf("Sources[custompkg].Repo = %q", resolved.Sources["custompkg"].Repo)
	}
	if resolved.Sources["custompkg"].Host != "github.example.com/api/v3" {
		t.Fatalf("Sources[custompkg].Host = %q", resolved.Sources["custompkg"].Host)
	}
	if resolved.Sources["custompkg"].Ref != "feature" {
		t.Fatalf("Sources[custompkg].Ref = %q", resolved.Sources["custompkg"].Ref)
	}
	if resolved.Sources["custompkg"].Subdir != "pkg" {
		t.Fatalf("Sources[custompkg].Subdir = %q", resolved.Sources["custompkg"].Subdir)
	}
	if resolved.Sources["custompkg"].TokenEnv != "GH_ENTERPRISE_PAT" {
		t.Fatalf("Sources[custompkg].TokenEnv = %q", resolved.Sources["custompkg"].TokenEnv)
	}
	if resolved.Sources["gitpkg"].URL != "file:///tmp/example.git" {
		t.Fatalf("Sources[gitpkg].URL = %q", resolved.Sources["gitpkg"].URL)
	}
}

func TestParseRejectsSourceWithoutTypeWithSectionName(t *testing.T) {
	src := `
[sources."custompkg"]
repo = "owner/custompkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), `line 2: [sources."custompkg"] requires type`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsConflictingRootSourceFields(t *testing.T) {
	src := `
[sources."custompkg"]
type = "github"
repo = "owner/custompkg"
url = "file:///tmp/example.git"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), `line 2: [sources."custompkg"] cannot set url when type = "github"`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsConflictingScriptSourceFields(t *testing.T) {
	src := `
[scripts."scripts/report.R".sources."localpkg"]
type = "local"
path = "vendor/localpkg.tar.gz"
subdir = "pkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), `line 2: [scripts."scripts/report.R".sources."localpkg"] cannot set subdir when type = "local"`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseUnsupportedSourceTypeSuggestsClosestMatch(t *testing.T) {
	src := `
[sources."custompkg"]
type = "githb"
repo = "owner/custompkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), `line 2: [sources."custompkg"] uses unsupported source type "githb"; supported values: github, git, local; did you mean "github"?`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsDuplicateRootKey(t *testing.T) {
	src := `
repo = "https://cloud.r-project.org"
repo = "https://cran.rstudio.com"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate key error")
	}
	if !strings.Contains(err.Error(), `line 3: duplicate key "repo" in root config`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsDuplicateScriptSection(t *testing.T) {
	src := `
[scripts."scripts/report.R"]
packages = ["jsonlite"]

[scripts."scripts/report.R"]
packages = ["cli"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate section error")
	}
	if !strings.Contains(err.Error(), `line 5: [scripts."scripts/report.R"] is declared more than once`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsDuplicateScriptKey(t *testing.T) {
	src := `
[scripts."scripts/report.R"]
packages = ["jsonlite"]
packages = ["cli"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate key error")
	}
	if !strings.Contains(err.Error(), `line 4: duplicate key "packages" in [scripts."scripts/report.R"]`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseUnsupportedSourceKeyNamesSection(t *testing.T) {
	src := `
[sources."custompkg"]
packages = ["jsonlite"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported key error")
	}
	if !strings.Contains(err.Error(), `[sources."custompkg"]: unsupported source key "packages"; supported keys: type, host, repo, url, ref, path, subdir, token_env`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseUnsupportedSourceKeySuggestsClosestMatch(t *testing.T) {
	src := `
[sources."custompkg"]
tokn_env = "GITHUB_PAT"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported key error")
	}
	if !strings.Contains(err.Error(), `[sources."custompkg"]: unsupported source key "tokn_env"; supported keys: type, host, repo, url, ref, path, subdir, token_env; did you mean "token_env"?`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseUnsupportedRootKeyListsSupportedKeys(t *testing.T) {
	src := `
url = "https://cloud.r-project.org"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported key error")
	}
	if !strings.Contains(err.Error(), `root config: unsupported key "url"; supported keys: repo, cache_dir, lockfile, rscript, r_version, toolchain_prefixes, pkg_config_path, packages, bioc_packages`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseUnsupportedScriptKeyListsSupportedKeys(t *testing.T) {
	src := `
[scripts."scripts/report.R"]
url = "https://cloud.r-project.org"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported key error")
	}
	if !strings.Contains(err.Error(), `[scripts."scripts/report.R"]: unsupported key "url"; supported keys: repo, cache_dir, lockfile, rscript, r_version, toolchain_prefixes, pkg_config_path, packages, bioc_packages`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseUnsupportedRootKeySuggestsClosestMatch(t *testing.T) {
	src := `
packags = ["jsonlite"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported key error")
	}
	if !strings.Contains(err.Error(), `root config: unsupported key "packags"; supported keys: repo, cache_dir, lockfile, rscript, r_version, toolchain_prefixes, pkg_config_path, packages, bioc_packages; did you mean "packages"?`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseUnsupportedSectionSuggestsClosestMatch(t *testing.T) {
	src := `
[script."scripts/report.R"]
packages = ["jsonlite"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported section error")
	}
	if !strings.Contains(err.Error(), `line 2: unsupported section "[script.\"scripts/report.R\"]"; supported section prefixes: scripts, sources; did you mean "[scripts.\"scripts/report.R\"]"?`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseUnsupportedScriptSubsectionSuggestsClosestMatch(t *testing.T) {
	src := `
[scripts."scripts/report.R".source."custompkg"]
type = "github"
repo = "owner/custompkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported subsection error")
	}
	if !strings.Contains(err.Error(), `line 2: invalid script section "[scripts.\"scripts/report.R\".source.\"custompkg\"]": unsupported script subsection ".source.\"custompkg\""; supported subsections: .sources.; did you mean ".sources."?`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsDuplicateRootSourceSection(t *testing.T) {
	src := `
[sources."custompkg"]
type = "github"
repo = "owner/custompkg"

[sources."custompkg"]
type = "github"
repo = "owner/otherpkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate section error")
	}
	if !strings.Contains(err.Error(), `line 6: [sources."custompkg"] is declared more than once`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsDuplicateRootSourceKey(t *testing.T) {
	src := `
[sources."custompkg"]
type = "github"
repo = "owner/custompkg"
repo = "owner/otherpkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate key error")
	}
	if !strings.Contains(err.Error(), `line 5: duplicate key "repo" in [sources."custompkg"]`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsDuplicateScriptSourceKey(t *testing.T) {
	src := `
[scripts."scripts/report.R".sources."custompkg"]
type = "github"
repo = "owner/custompkg"
repo = "owner/otherpkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate key error")
	}
	if !strings.Contains(err.Error(), `line 5: duplicate key "repo" in [scripts."scripts/report.R".sources."custompkg"]`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsDuplicateScriptSourceSection(t *testing.T) {
	src := `
[scripts."scripts/report.R".sources."custompkg"]
type = "github"
repo = "owner/custompkg"

[scripts."scripts/report.R".sources."custompkg"]
type = "github"
repo = "owner/otherpkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate section error")
	}
	if !strings.Contains(err.Error(), `line 6: [scripts."scripts/report.R".sources."custompkg"] is declared more than once`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseInvalidSourceSectionIncludesCause(t *testing.T) {
	src := `
[sources."custompkg]
type = "github"
repo = "owner/custompkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want invalid source section error")
	}
	if !strings.Contains(err.Error(), `line 2: invalid source section "[sources.\"custompkg]": invalid quoted string "\"custompkg"`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseInvalidScriptSectionIncludesCause(t *testing.T) {
	src := `
[scripts.report.R]
packages = ["jsonlite"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want invalid script section error")
	}
	if !strings.Contains(err.Error(), `line 2: invalid script section "[scripts.report.R]": expected quoted path in "report.R"`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseInvalidRootArrayValueNamesKey(t *testing.T) {
	src := `
packages = "jsonlite"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want invalid value error")
	}
	if !strings.Contains(err.Error(), `line 2: root config: invalid value for "packages": expected ["pkg"] array, got "\"jsonlite\""`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseInvalidSourceValueNamesKey(t *testing.T) {
	src := `
[sources."custompkg"]
type = "github"
repo = "owner/custompkg
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want invalid value error")
	}
	if !strings.Contains(err.Error(), `line 4: [sources."custompkg"]: invalid value for "repo": invalid quoted string "\"owner/custompkg"`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseMissingEqualsNamesRootConfig(t *testing.T) {
	src := `
packages ["jsonlite"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want missing equals error")
	}
	if !strings.Contains(err.Error(), `line 2: root config: expected key = value`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseMissingEqualsNamesScriptSourceSection(t *testing.T) {
	src := `
[scripts."scripts/report.R".sources."custompkg"]
type "github"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want missing equals error")
	}
	if !strings.Contains(err.Error(), `line 3: [scripts."scripts/report.R".sources."custompkg"]: expected key = value`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseMissingEqualsNamesScriptSection(t *testing.T) {
	src := `
[scripts."scripts/report.R"]
packages ["jsonlite"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want missing equals error")
	}
	if !strings.Contains(err.Error(), `line 3: [scripts."scripts/report.R"]: expected key = value`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseMissingEqualsNamesRootSourceSection(t *testing.T) {
	src := `
[sources."custompkg"]
type "github"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want missing equals error")
	}
	if !strings.Contains(err.Error(), `line 3: [sources."custompkg"]: expected key = value`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseSectionHeaderMissingClosingBracket(t *testing.T) {
	src := `
[scripts."scripts/report.R"
packages = ["jsonlite"]
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want invalid section header error")
	}
	if !strings.Contains(err.Error(), `line 2: invalid section header "[scripts.\"scripts/report.R\"": missing closing ]`) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsArrayStyleSectionHeaders(t *testing.T) {
	src := `
[[sources."custompkg"]]
type = "github"
repo = "owner/custompkg"
`

	_, err := Parse(src)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported array-style section error")
	}
	if !strings.Contains(err.Error(), `line 2: array-style section headers are not supported "[[sources.\"custompkg\"]]"; use "[sources.\"custompkg\"]" instead`) {
		t.Fatalf("Parse() error = %v", err)
	}
}
