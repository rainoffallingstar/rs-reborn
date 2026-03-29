package project

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRenderRoundTrip(t *testing.T) {
	cfg := Config{
		Defaults: ScriptConfig{
			Repo:         DefaultRepo,
			CacheDir:     ".rs-cache",
			Lockfile:     "rs.lock.json",
			Packages:     []string{"cli", "jsonlite"},
			BiocPackages: []string{"DESeq2"},
		},
		Sources: map[string]SourceSpec{
			"mypkg": {
				Package:  "mypkg",
				Type:     "github",
				Repo:     "owner/mypkg",
				Ref:      "main",
				Subdir:   "pkg",
				TokenEnv: "GITHUB_PAT",
			},
		},
		Scripts: map[string]ScriptConfig{
			"scripts/report.R": {
				Packages: []string{"ggplot2"},
				Sources: map[string]SourceSpec{
					"localpkg": {
						Package: "localpkg",
						Type:    "local",
						Path:    "vendor/localpkg_0.1.0.tar.gz",
					},
				},
			},
		},
	}

	rendered := Render(cfg)
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse(Render(cfg)) error = %v\nrendered:\n%s", err, rendered)
	}

	if !reflect.DeepEqual(parsed.Defaults, cfg.Defaults) {
		t.Fatalf("Defaults mismatch:\n got=%#v\nwant=%#v", parsed.Defaults, cfg.Defaults)
	}
	if !reflect.DeepEqual(parsed.Sources, cfg.Sources) {
		t.Fatalf("Sources mismatch:\n got=%#v\nwant=%#v", parsed.Sources, cfg.Sources)
	}
	if !reflect.DeepEqual(parsed.Scripts, cfg.Scripts) {
		t.Fatalf("Scripts mismatch:\n got=%#v\nwant=%#v", parsed.Scripts, cfg.Scripts)
	}
}

func TestLoadEditablePreservesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := "cache_dir = \".rs-cache\"\nlockfile = \"locks/rs.lock.json\"\nrscript = \"tools/Rscript\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}

	if cfg.Defaults.CacheDir != ".rs-cache" {
		t.Fatalf("CacheDir = %q, want relative path", cfg.Defaults.CacheDir)
	}
	if cfg.Defaults.Lockfile != "locks/rs.lock.json" {
		t.Fatalf("Lockfile = %q, want relative path", cfg.Defaults.Lockfile)
	}
	if cfg.Defaults.Rscript != "tools/Rscript" {
		t.Fatalf("Rscript = %q, want relative path", cfg.Defaults.Rscript)
	}
}

func TestRenderPreservesRscriptOrderAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"repo = \"https://cloud.r-project.org\"",
		"# interpreter note",
		"rscript = \"tools/Rscript-4.4\" # pinned R",
		"packages = [\"jsonlite\"]",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if got != content+"\n" {
		t.Fatalf("Save() changed rscript formatting unexpectedly:\n--- want ---\n%s\n--- got ---\n%s", content+"\n", got)
	}
}

func TestSavePreservesPreambleAndExistingOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# team note: keep this file readable",
		"# generated once, then hand-edited",
		"",
		"lockfile = \"rs.lock.json\" # canonical lock path",
		"repo = \"https://cran.rstudio.com\" # preferred mirror",
		"cache_dir = \".rs-cache\"",
		"",
		"# keep local sources grouped here",
		"[sources.\"zpkg\"]",
		"path = \"vendor/zpkg.tar.gz\" # local tarball",
		"# zpkg type note",
		"type = \"local\"",
		"",
		"[sources.\"apkg\"]",
		"path = \"vendor/apkg.tar.gz\"",
		"type = \"local\"",
		"",
		"# primary reporting script",
		"[scripts.\"scripts/b.R\"]",
		"repo = \"https://cran.r-project.org\" # script-specific repo",
		"packages = [\"jsonlite\"]",
		"",
		"[scripts.\"scripts/a.R\"]",
		"packages = [\"cli\"]",
		"",
		"# footer note",
		"# keep this file sorted by intent, not alphabetically",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	cfg.RootDir = dir
	if err := AddPackage(&cfg, AddPackageOptions{Package: "glue"}); err != nil {
		t.Fatalf("AddPackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)

	if !strings.HasPrefix(got, "# team note: keep this file readable\n# generated once, then hand-edited\n\n") {
		t.Fatalf("Save() did not preserve preamble:\n%s", got)
	}
	if !strings.Contains(got, "lockfile = \"rs.lock.json\" # canonical lock path") {
		t.Fatalf("Save() did not preserve root trailing comment:\n%s", got)
	}
	if !strings.Contains(got, "repo = \"https://cran.rstudio.com\" # preferred mirror") {
		t.Fatalf("Save() did not preserve root inline comment:\n%s", got)
	}
	if !strings.Contains(got, "# keep local sources grouped here\n[sources.\"zpkg\"]") {
		t.Fatalf("Save() did not preserve section leading comment:\n%s", got)
	}
	if !strings.Contains(got, "path = \"vendor/zpkg.tar.gz\" # local tarball") {
		t.Fatalf("Save() did not preserve source trailing comment:\n%s", got)
	}
	if !strings.Contains(got, "# zpkg type note\ntype = \"local\"") {
		t.Fatalf("Save() did not preserve field leading comment:\n%s", got)
	}
	if strings.Index(got, "lockfile = ") > strings.Index(got, "repo = ") {
		t.Fatalf("root key order changed unexpectedly:\n%s", got)
	}
	if strings.Index(got, "[sources.\"zpkg\"]") > strings.Index(got, "[sources.\"apkg\"]") {
		t.Fatalf("root source order changed unexpectedly:\n%s", got)
	}
	if strings.Index(got, "path = \"vendor/zpkg.tar.gz\"") > strings.Index(got, "type = \"local\"") {
		t.Fatalf("root source field order changed unexpectedly:\n%s", got)
	}
	if strings.Index(got, "[scripts.\"scripts/b.R\"]") > strings.Index(got, "[scripts.\"scripts/a.R\"]") {
		t.Fatalf("script order changed unexpectedly:\n%s", got)
	}
	if !strings.Contains(got, "# primary reporting script\n[scripts.\"scripts/b.R\"]") {
		t.Fatalf("Save() did not preserve script leading comment:\n%s", got)
	}
	scriptB := got[strings.Index(got, "[scripts.\"scripts/b.R\"]"):]
	if !strings.Contains(scriptB, "repo = \"https://cran.r-project.org\" # script-specific repo") {
		t.Fatalf("Save() did not preserve script trailing comment:\n%s", got)
	}
	if strings.Index(scriptB, "repo = \"https://cran.r-project.org\"") > strings.Index(scriptB, "packages = [\"jsonlite\"]") {
		t.Fatalf("script field order changed unexpectedly:\n%s", got)
	}
	if !strings.Contains(got, "packages = [\"glue\"]") {
		t.Fatalf("Save() output missing new package:\n%s", got)
	}
	if !strings.HasSuffix(got, "# footer note\n# keep this file sorted by intent, not alphabetically\n") {
		t.Fatalf("Save() did not preserve file footer comments:\n%s", got)
	}
}

func TestLoadEditableSaveNoOpPreservesHandEditedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# project note",
		"",
		"repo = \"https://cloud.r-project.org\" # preferred default",
		"packages = [\"jsonlite\"]",
		"",
		"# root source note",
		"[sources.\"localpkg\"] # local source trailing",
		"path = \"vendor/localpkg.tar.gz\" # local source path",
		"type = \"local\"",
		"",
		"# reporting script",
		"[scripts.\"scripts/report.R\"] # script trailing",
		"# script package note",
		"packages = [\"cli\", \"localpkg\"] # script package trailing",
		"",
		"# script source note",
		"[scripts.\"scripts/report.R\".sources.\"localpkg\"] # script source trailing",
		"# script source path note",
		"path = \"vendor/report-localpkg.tar.gz\" # script source path trailing",
		"type = \"local\"",
		"",
		"# footer note",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if got != content {
		t.Fatalf("Save() changed hand-edited file unexpectedly:\n--- want ---\n%s--- got ---\n%s", content, got)
	}
}

func TestLoadEditableSaveNoOpPreservesLeadingScriptComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# first script note",
		"[scripts.\"scripts/report.R\"] # script trailing",
		"# package note",
		"packages = [\"jsonlite\"] # package trailing",
		"",
		"# script source note",
		"[scripts.\"scripts/report.R\".sources.\"localpkg\"] # source trailing",
		"path = \"vendor/localpkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
		"# footer note",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if got != content {
		t.Fatalf("Save() changed leading-script file unexpectedly:\n--- want ---\n%s--- got ---\n%s", content, got)
	}
}

func TestLoadEditableSaveNoOpPreservesLeadingRootSourceComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# root source note",
		"[sources.\"localpkg\"] # source trailing",
		"# path note",
		"path = \"vendor/localpkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
		"# footer note",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if got != content {
		t.Fatalf("Save() changed leading-root-source file unexpectedly:\n--- want ---\n%s--- got ---\n%s", content, got)
	}
}

func TestLoadEditableSaveNoOpPreservesRootSourceThenScriptLayout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# root source note",
		"[sources.\"localpkg\"] # source trailing",
		"path = \"vendor/localpkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
		"# script note",
		"[scripts.\"scripts/report.R\"] # script trailing",
		"packages = [\"localpkg\", \"jsonlite\"] # packages trailing",
		"",
		"# script source note",
		"[scripts.\"scripts/report.R\".sources.\"localpkg\"]",
		"path = \"vendor/report-localpkg.tar.gz\"",
		"type = \"local\"",
		"",
		"# footer note",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if got != content {
		t.Fatalf("Save() changed source-then-script file unexpectedly:\n--- want ---\n%s--- got ---\n%s", content, got)
	}
}

func TestLoadEditableSaveNoOpPreservesScriptThenRootSourceLayout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# script note",
		"[scripts.\"scripts/report.R\"] # script trailing",
		"packages = [\"localpkg\", \"jsonlite\"] # packages trailing",
		"",
		"# script source note",
		"[scripts.\"scripts/report.R\".sources.\"localpkg\"]",
		"path = \"vendor/report-localpkg.tar.gz\"",
		"type = \"local\"",
		"",
		"# root source note",
		"[sources.\"localpkg\"] # source trailing",
		"path = \"vendor/localpkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
		"# footer note",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if got != content {
		t.Fatalf("Save() changed script-then-source file unexpectedly:\n--- want ---\n%s--- got ---\n%s", content, got)
	}
}

func TestAddPackageProjectSourcePreservesTopLevelMixedOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# script note",
		"[scripts.\"scripts/report.R\"] # script trailing",
		"packages = [\"localpkg\", \"jsonlite\"] # packages trailing",
		"",
		"# root source note",
		"[sources.\"localpkg\"] # source trailing",
		"path = \"vendor/localpkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := AddPackage(&cfg, AddPackageOptions{
		Package: "apkg",
		Source: &SourceSpec{
			Type: "local",
			Path: "vendor/apkg.tar.gz",
		},
	}); err != nil {
		t.Fatalf("AddPackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if strings.Index(got, "[scripts.\"scripts/report.R\"]") > strings.Index(got, "[sources.\"localpkg\"]") {
		t.Fatalf("script block moved after root source unexpectedly:\n%s", got)
	}
	if strings.Index(got, "[sources.\"localpkg\"]") > strings.Index(got, "[sources.\"apkg\"]") {
		t.Fatalf("new root source was not appended after existing root source:\n%s", got)
	}
	if !strings.Contains(got, "packages = [\"apkg\"]") {
		t.Fatalf("new root package missing from output:\n%s", got)
	}
}

func TestAddPackageRootKeyPreservesTopLevelMixedOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# root source note",
		"[sources.\"localpkg\"] # source trailing",
		"path = \"vendor/localpkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
		"# script note",
		"[scripts.\"scripts/report.R\"] # script trailing",
		"packages = [\"localpkg\"] # packages trailing",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := AddPackage(&cfg, AddPackageOptions{Package: "jsonlite"}); err != nil {
		t.Fatalf("AddPackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if !strings.Contains(got, "packages = [\"jsonlite\"]") {
		t.Fatalf("new root package missing from output:\n%s", got)
	}
	if strings.Index(got, "packages = [\"jsonlite\"]") > strings.Index(got, "[sources.\"localpkg\"]") {
		t.Fatalf("new root key did not render before top-level sections:\n%s", got)
	}
	if strings.Index(got, "[sources.\"localpkg\"]") > strings.Index(got, "[scripts.\"scripts/report.R\"]") {
		t.Fatalf("root source moved after script unexpectedly:\n%s", got)
	}
}

func TestRemovePackageRootSourcePreservesTopLevelMixedOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# script note",
		"[scripts.\"scripts/report.R\"] # script trailing",
		"packages = [\"jsonlite\"] # packages trailing",
		"",
		"# removable source note",
		"[sources.\"apkg\"] # source trailing",
		"path = \"vendor/apkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
		"# stable source note",
		"[sources.\"bpkg\"]",
		"path = \"vendor/bpkg.tar.gz\"",
		"type = \"local\"",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := RemovePackage(&cfg, RemovePackageOptions{Package: "apkg"}); err != nil {
		t.Fatalf("RemovePackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if strings.Contains(got, `[sources."apkg"]`) {
		t.Fatalf("removed root source still present:\n%s", got)
	}
	if strings.Index(got, `[scripts."scripts/report.R"]`) > strings.Index(got, `[sources."bpkg"]`) {
		t.Fatalf("remaining root source moved before script unexpectedly:\n%s", got)
	}
}

func TestAddPackageRootAndScript(t *testing.T) {
	cfg := NewDefaultConfig(InitOptions{})
	cfg.RootDir = "/tmp/project"

	if err := AddPackage(&cfg, AddPackageOptions{Package: "jsonlite"}); err != nil {
		t.Fatalf("AddPackage(root) error = %v", err)
	}
	if err := AddPackage(&cfg, AddPackageOptions{
		ScriptPath: "/tmp/project/scripts/report.R",
		Package:    "DESeq2",
		Bioc:       true,
	}); err != nil {
		t.Fatalf("AddPackage(script bioc) error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
	scriptCfg := cfg.Scripts["scripts/report.R"]
	if !reflect.DeepEqual(scriptCfg.BiocPackages, []string{"DESeq2"}) {
		t.Fatalf("Scripts.BiocPackages = %v", scriptCfg.BiocPackages)
	}
}

func TestAddPackageSource(t *testing.T) {
	cfg := NewDefaultConfig(InitOptions{})
	cfg.RootDir = "/tmp/project"

	err := AddPackage(&cfg, AddPackageOptions{
		ScriptPath: "/tmp/project/scripts/report.R",
		Package:    "mypkg",
		Source: &SourceSpec{
			Type:     "github",
			Repo:     "owner/mypkg",
			Ref:      "main",
			TokenEnv: "GITHUB_PAT",
		},
	})
	if err != nil {
		t.Fatalf("AddPackage(source) error = %v", err)
	}

	scriptCfg := cfg.Scripts["scripts/report.R"]
	if !reflect.DeepEqual(scriptCfg.Packages, []string{"mypkg"}) {
		t.Fatalf("Scripts.Packages = %v", scriptCfg.Packages)
	}
	spec := scriptCfg.Sources["mypkg"]
	if spec.Type != "github" || spec.Repo != "owner/mypkg" || spec.Ref != "main" || spec.TokenEnv != "GITHUB_PAT" {
		t.Fatalf("Scripts.Sources[mypkg] = %#v", spec)
	}
}

func TestNewConfigFromScriptWritesRootPackages(t *testing.T) {
	cfg, err := NewConfigFromScript(InitOptions{
		Repo:     DefaultRepo,
		CacheDir: ".rs-cache",
		Lockfile: "rs.lock.json",
		Packages: []string{"cli", "jsonlite"},
	}, "/tmp/project", "/tmp/project/scripts/report.R", false)
	if err != nil {
		t.Fatalf("NewConfigFromScript(root) error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"cli", "jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
	if len(cfg.Scripts) != 0 {
		t.Fatalf("Scripts = %#v, want empty", cfg.Scripts)
	}
}

func TestNewConfigFromScriptWritesScriptBlock(t *testing.T) {
	cfg, err := NewConfigFromScript(InitOptions{
		Packages: []string{"cli", "jsonlite"},
	}, "/tmp/project", "/tmp/project/scripts/report.R", true)
	if err != nil {
		t.Fatalf("NewConfigFromScript(script block) error = %v", err)
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

func TestNewConfigFromScriptsWritesScriptBlocks(t *testing.T) {
	cfg, err := NewConfigFromScripts(InitOptions{}, "/tmp/project", map[string]ScriptConfig{
		"/tmp/project/scripts/a.R": {
			Packages: []string{"cli", "jsonlite"},
		},
		"/tmp/project/scripts/b.R": {
			Packages:     []string{"dplyr"},
			BiocPackages: []string{"DESeq2"},
		},
	}, false)
	if err != nil {
		t.Fatalf("NewConfigFromScripts() error = %v", err)
	}

	if len(cfg.Defaults.Packages) != 0 {
		t.Fatalf("Defaults.Packages = %v, want empty", cfg.Defaults.Packages)
	}
	if !reflect.DeepEqual(cfg.Scripts["scripts/a.R"].Packages, []string{"cli", "jsonlite"}) {
		t.Fatalf("Scripts[a].Packages = %v", cfg.Scripts["scripts/a.R"].Packages)
	}
	if !reflect.DeepEqual(cfg.Scripts["scripts/b.R"].Packages, []string{"dplyr"}) {
		t.Fatalf("Scripts[b].Packages = %v", cfg.Scripts["scripts/b.R"].Packages)
	}
	if !reflect.DeepEqual(cfg.Scripts["scripts/b.R"].BiocPackages, []string{"DESeq2"}) {
		t.Fatalf("Scripts[b].BiocPackages = %v", cfg.Scripts["scripts/b.R"].BiocPackages)
	}
}

func TestAddPackageProjectSource(t *testing.T) {
	cfg := NewDefaultConfig(InitOptions{})

	err := AddPackage(&cfg, AddPackageOptions{
		Package: "mypkg",
		Source: &SourceSpec{
			Type: "local",
			Path: "vendor/mypkg.tar.gz",
		},
	})
	if err != nil {
		t.Fatalf("AddPackage(project source) error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"mypkg"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
	spec := cfg.Sources["mypkg"]
	if spec.Type != "local" || spec.Path != "vendor/mypkg.tar.gz" {
		t.Fatalf("Sources[mypkg] = %#v", spec)
	}
}

func TestAddPackageRejectsInvalidSource(t *testing.T) {
	cfg := NewDefaultConfig(InitOptions{})
	err := AddPackage(&cfg, AddPackageOptions{
		Package: "mypkg",
		Source: &SourceSpec{
			Type: "github",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires repo") {
		t.Fatalf("AddPackage() error = %v, want repo validation", err)
	}
}

func TestRemovePackageRootAndSource(t *testing.T) {
	cfg := Config{
		Defaults: ScriptConfig{
			Packages:     []string{"jsonlite", "mypkg"},
			BiocPackages: []string{"DESeq2"},
		},
		Sources: map[string]SourceSpec{
			"mypkg": {Package: "mypkg", Type: "local", Path: "vendor/mypkg.tar.gz"},
		},
	}

	if err := RemovePackage(&cfg, RemovePackageOptions{Package: "mypkg"}); err != nil {
		t.Fatalf("RemovePackage(root source) error = %v", err)
	}
	if err := RemovePackage(&cfg, RemovePackageOptions{Package: "DESeq2", Bioc: true}); err != nil {
		t.Fatalf("RemovePackage(root bioc) error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Packages, []string{"jsonlite"}) {
		t.Fatalf("Defaults.Packages = %v", cfg.Defaults.Packages)
	}
	if len(cfg.Defaults.BiocPackages) != 0 {
		t.Fatalf("Defaults.BiocPackages = %v", cfg.Defaults.BiocPackages)
	}
	if _, ok := cfg.Sources["mypkg"]; ok {
		t.Fatalf("Sources[mypkg] still exists")
	}
}

func TestRemovePackageDeletesEmptyScriptBlock(t *testing.T) {
	cfg := Config{
		Path:    "/tmp/project/rs.toml",
		RootDir: "/tmp/project",
		Scripts: map[string]ScriptConfig{
			"scripts/report.R": {
				Packages: []string{"localpkg"},
				Sources: map[string]SourceSpec{
					"localpkg": {Package: "localpkg", Type: "local", Path: "vendor/localpkg.tar.gz"},
				},
			},
		},
	}

	err := RemovePackage(&cfg, RemovePackageOptions{
		ScriptPath: "/tmp/project/scripts/report.R",
		Package:    "localpkg",
	})
	if err != nil {
		t.Fatalf("RemovePackage(script) error = %v", err)
	}

	if _, ok := cfg.Scripts["scripts/report.R"]; ok {
		t.Fatalf("Scripts[report] still exists: %#v", cfg.Scripts["scripts/report.R"])
	}
	rendered := Render(cfg)
	if strings.Contains(rendered, "[scripts.") {
		t.Fatalf("Render() still contains script block:\n%s", rendered)
	}
}

func TestRemovePackageTransfersDeletedScriptComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# first script note",
		"[scripts.\"scripts/a.R\"] # first script trailing",
		"# package note",
		"packages = [\"localpkg\"] # package trailing",
		"",
		"# second script note",
		"[scripts.\"scripts/b.R\"]",
		"packages = [\"jsonlite\"]",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := RemovePackage(&cfg, RemovePackageOptions{
		ScriptPath: filepath.Join(dir, "scripts", "a.R"),
		Package:    "localpkg",
	}); err != nil {
		t.Fatalf("RemovePackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if strings.Contains(got, "[scripts.\"scripts/a.R\"]") {
		t.Fatalf("deleted script block still present:\n%s", got)
	}
	want := strings.Join([]string{
		"# first script note",
		"# first script trailing",
		"# package note",
		"# package trailing",
		"# second script note",
		"[scripts.\"scripts/b.R\"]",
		"packages = [\"jsonlite\"]",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("deleted script comments were not transferred cleanly:\n%s", got)
	}
}

func TestRemovePackageTransfersDeletedScriptCommentsToNextTopLevelRootSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# removed script note",
		"[scripts.\"scripts/a.R\"] # removed script trailing",
		"# package note",
		"packages = [\"localpkg\"] # package trailing",
		"",
		"# source note",
		"[sources.\"bpkg\"]",
		"path = \"vendor/bpkg.tar.gz\"",
		"type = \"local\"",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := RemovePackage(&cfg, RemovePackageOptions{
		ScriptPath: filepath.Join(dir, "scripts", "a.R"),
		Package:    "localpkg",
	}); err != nil {
		t.Fatalf("RemovePackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if strings.Contains(got, "[scripts.\"scripts/a.R\"]") {
		t.Fatalf("deleted script block still present:\n%s", got)
	}
	want := strings.Join([]string{
		"# removed script note",
		"# removed script trailing",
		"# package note",
		"# package trailing",
		"# source note",
		"[sources.\"bpkg\"]",
		"path = \"vendor/bpkg.tar.gz\"",
		"type = \"local\"",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("deleted script comments were not transferred to next top-level root source cleanly:\n%s", got)
	}
}

func TestRemovePackageTransfersDeletedRootSourceComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"packages = [\"apkg\", \"bpkg\"]",
		"",
		"# first source note",
		"[sources.\"apkg\"] # first source trailing",
		"# source field note",
		"path = \"vendor/apkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
		"[sources.\"bpkg\"]",
		"path = \"vendor/bpkg.tar.gz\"",
		"type = \"local\"",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := RemovePackage(&cfg, RemovePackageOptions{Package: "apkg"}); err != nil {
		t.Fatalf("RemovePackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if strings.Contains(got, "[sources.\"apkg\"]") {
		t.Fatalf("deleted source block still present:\n%s", got)
	}
	want := strings.Join([]string{
		"packages = [\"bpkg\"]",
		"",
		"# first source note",
		"# first source trailing",
		"# source field note",
		"# path trailing",
		"[sources.\"bpkg\"]",
		"path = \"vendor/bpkg.tar.gz\"",
		"type = \"local\"",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("deleted source comments were not transferred cleanly:\n%s", got)
	}
}

func TestRemovePackageTransfersDeletedRootSourceCommentsToNextTopLevelScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"# first script note",
		"[scripts.\"scripts/a.R\"]",
		"packages = [\"jsonlite\"]",
		"",
		"# removed source note",
		"[sources.\"apkg\"] # removed source trailing",
		"# source field note",
		"path = \"vendor/apkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
		"# second script note",
		"[scripts.\"scripts/b.R\"]",
		"packages = [\"cli\"]",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := RemovePackage(&cfg, RemovePackageOptions{Package: "apkg"}); err != nil {
		t.Fatalf("RemovePackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	if strings.Contains(got, "[sources.\"apkg\"]") {
		t.Fatalf("deleted source block still present:\n%s", got)
	}
	want := strings.Join([]string{
		"# first script note",
		"[scripts.\"scripts/a.R\"]",
		"packages = [\"jsonlite\"]",
		"",
		"# removed source note",
		"# removed source trailing",
		"# source field note",
		"# path trailing",
		"# second script note",
		"[scripts.\"scripts/b.R\"]",
		"packages = [\"cli\"]",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("deleted root source comments were not transferred to next top-level script cleanly:\n%s", got)
	}
}

func TestRemovePackageLastTopLevelRootSourceTransfersCommentsToEpilogueWithoutExtraBlankLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := strings.Join([]string{
		"packages = [\"jsonlite\"]",
		"",
		"# last source note",
		"[sources.\"apkg\"] # last source trailing",
		"# source field note",
		"path = \"vendor/apkg.tar.gz\" # path trailing",
		"type = \"local\"",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadEditable(path)
	if err != nil {
		t.Fatalf("LoadEditable() error = %v", err)
	}
	if err := RemovePackage(&cfg, RemovePackageOptions{Package: "apkg"}); err != nil {
		t.Fatalf("RemovePackage() error = %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(gotBytes)
	want := strings.Join([]string{
		"packages = [\"jsonlite\"]",
		"# last source note",
		"# last source trailing",
		"# source field note",
		"# path trailing",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("removing last top-level source produced unexpected epilogue layout:\n%s", got)
	}
}

func TestRemovePackageMissing(t *testing.T) {
	cfg := NewDefaultConfig(InitOptions{})
	err := RemovePackage(&cfg, RemovePackageOptions{Package: "missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("RemovePackage() error = %v, want not found", err)
	}
}
