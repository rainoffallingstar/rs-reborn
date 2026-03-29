package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gr/internal/installer"
	"gr/internal/lockfile"
	"gr/internal/project"
)

func writeFakeRscript(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "Rscript")
	script := `#!/bin/sh
if [ "$1" = "-e" ]; then
	cat <<'EOF'
version	4.4.1
platform	x86_64-pc-linux-gnu
arch	x86_64
os	linux-gnu
pkg_type	source
EOF
	exit 0
fi
echo "unexpected fake Rscript args: $*" >&2
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(fake Rscript) error = %v", err)
	}
	return path
}

func TestMergeDeps(t *testing.T) {
	got := mergeDeps([]string{"jsonlite", "dplyr"}, []string{"cli", "dplyr"})
	want := []string{"cli", "dplyr", "jsonlite"}

	if len(got) != len(want) {
		t.Fatalf("mergeDeps length = %d, want %d (%v)", len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeDeps[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestInstallBackendDefaultsToAuto(t *testing.T) {
	t.Setenv("RS_INSTALL_BACKEND", "")

	if got := installBackend(); got != "auto" {
		t.Fatalf("installBackend() = %q, want auto", got)
	}
}

func TestInstallBackendUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("RS_INSTALL_BACKEND", "legacy")

	if got := installBackend(); got != "legacy" {
		t.Fatalf("installBackend() = %q, want legacy", got)
	}
}

func TestBootstrapSourceIncludesInstallerBackends(t *testing.T) {
	for _, want := range []string{
		"RS_INSTALL_BACKEND",
		"rs_install_pak",
		"pak::pkg_install",
		"native backend must be executed from the Go installer",
		"local::",
		"git::",
		"github::",
	} {
		if !strings.Contains(bootstrapSource, want) {
			t.Fatalf("bootstrapSource missing %q", want)
		}
	}
}

func TestEnsureInstalledUsesNativeBackend(t *testing.T) {
	oldNative := nativeInstall
	oldBootstrap := bootstrapInstall
	t.Cleanup(func() {
		nativeInstall = oldNative
		bootstrapInstall = oldBootstrap
	})

	calledNative := false
	nativeInstall = func(req installer.Request) error {
		calledNative = true
		if req.Runtime.RVersion != "4.4.1" {
			t.Fatalf("native runtime version = %q, want 4.4.1", req.Runtime.RVersion)
		}
		return nil
	}
	bootstrapInstall = func(env ResolvedEnvironment, backend string) error {
		t.Fatalf("bootstrapInstall called unexpectedly with backend %q", backend)
		return nil
	}

	t.Setenv("RS_INSTALL_BACKEND", "native")
	err := EnsureInstalled(ResolvedEnvironment{
		ScriptPath:  filepath.Join(t.TempDir(), "analysis.R"),
		LibraryPath: t.TempDir(),
		Interpreter: "fake-Rscript",
		Runtime: RuntimeMetadata{
			Interpreter: "fake-Rscript",
			RVersion:    "4.4.1",
		},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("EnsureInstalled() error = %v", err)
	}
	if !calledNative {
		t.Fatalf("nativeInstall was not called")
	}
}

func TestEnsureInstalledAutoFallsBackToLegacy(t *testing.T) {
	oldNative := nativeInstall
	oldBootstrap := bootstrapInstall
	t.Cleanup(func() {
		nativeInstall = oldNative
		bootstrapInstall = oldBootstrap
	})

	var stderr bytes.Buffer
	nativeInstall = func(req installer.Request) error {
		return errors.New("native exploded")
	}
	bootstrapCalled := ""
	bootstrapInstall = func(env ResolvedEnvironment, backend string) error {
		bootstrapCalled = backend
		return nil
	}

	t.Setenv("RS_INSTALL_BACKEND", "auto")
	err := EnsureInstalled(ResolvedEnvironment{
		ScriptPath:  filepath.Join(t.TempDir(), "analysis.R"),
		LibraryPath: t.TempDir(),
		Interpreter: "fake-Rscript",
		Runtime: RuntimeMetadata{
			Interpreter: "fake-Rscript",
			RVersion:    "4.4.1",
		},
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("EnsureInstalled() error = %v", err)
	}
	if bootstrapCalled != "legacy" {
		t.Fatalf("bootstrapInstall backend = %q, want legacy", bootstrapCalled)
	}
	if !strings.Contains(stderr.String(), "native backend unavailable or failed; falling back to legacy: native exploded") {
		t.Fatalf("fallback stderr = %q", stderr.String())
	}
}

func TestDoctorJSONOutputIncludesDependencyConflictDetails(t *testing.T) {
	oldValidate := nativeValidatePlan
	t.Cleanup(func() {
		nativeValidatePlan = oldValidate
	})
	nativeValidatePlan = func(req installer.Request) error {
		return installer.ConstraintConflictError{
			Package:     "cli",
			Version:     "3.6.5",
			RequiredBy:  "demo",
			Operator:    ">=",
			Requirement: "4.0.0",
			Chain:       []string{"root", "demo"},
		}
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	t.Setenv("RS_INSTALL_BACKEND", "native")
	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		JSON:        true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Doctor() error = nil, want blocking dependency conflict")
	}

	var report DoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if !slices.Contains(report.Errors, "dependency constraint conflict for cli: selected version 3.6.5 does not satisfy >= 4.0.0 required by demo (dependency path: root -> demo -> cli)") {
		t.Fatalf("report.Errors = %v", report.Errors)
	}
	found := false
	for _, detail := range report.ErrorDetails {
		if detail.Kind == "dependency_conflict" && detail.Package == "cli" {
			if !reflect.DeepEqual(detail.DependencyPath, []string{"root", "demo", "cli"}) {
				t.Fatalf("detail.DependencyPath = %v", detail.DependencyPath)
			}
			if detail.Constraint != ">= 4.0.0" {
				t.Fatalf("detail.Constraint = %q", detail.Constraint)
			}
			if detail.Selected != "3.6.5" {
				t.Fatalf("detail.Selected = %q", detail.Selected)
			}
			if detail.RequiredBy != "demo" {
				t.Fatalf("detail.RequiredBy = %q", detail.RequiredBy)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("report.ErrorDetails = %v, want dependency conflict detail", report.ErrorDetails)
	}
}

func TestParseDependencyConflictIssue(t *testing.T) {
	detail, ok := parseDependencyConflictIssue("dependency constraint conflict for cli: selected version 3.6.5 does not satisfy >= 4.0.0 required by demo (dependency path: root -> demo -> cli)")
	if !ok {
		t.Fatalf("parseDependencyConflictIssue() ok = false")
	}
	if detail.Package != "cli" {
		t.Fatalf("detail.Package = %q", detail.Package)
	}
	if detail.SelectedVersion != "3.6.5" {
		t.Fatalf("detail.SelectedVersion = %q", detail.SelectedVersion)
	}
	if detail.Constraint != ">= 4.0.0" {
		t.Fatalf("detail.Constraint = %q", detail.Constraint)
	}
	if detail.RequiredBy != "demo" {
		t.Fatalf("detail.RequiredBy = %q", detail.RequiredBy)
	}
	if !reflect.DeepEqual(detail.DependencyPath, []string{"root", "demo", "cli"}) {
		t.Fatalf("detail.DependencyPath = %v", detail.DependencyPath)
	}
}

func TestDefaultLockfilePath(t *testing.T) {
	got := defaultLockfilePath("/tmp/project", "/tmp/project/scripts/a.R")
	want := "/tmp/project/rs.lock.json"
	if got != want {
		t.Fatalf("defaultLockfilePath() = %q, want %q", got, want)
	}
}

func TestPredictedLibraryPathIncludesRuntimeMetadata(t *testing.T) {
	cacheRoot := "/tmp/rs-cache"
	scriptPath := "/tmp/project/script.R"
	cranDeps := []string{"cli", "jsonlite"}
	biocDeps := []string{"Biostrings"}
	runtimeA := RuntimeMetadata{
		Interpreter: "/usr/local/bin/Rscript",
		RVersion:    "4.5.0",
		Platform:    "aarch64-apple-darwin25.0.0",
		Arch:        "aarch64",
		OS:          "darwin25.0.0",
		PackageType: "source",
	}
	runtimeB := RuntimeMetadata{
		Interpreter: "/usr/local/bin/Rscript",
		RVersion:    "4.5.1",
		Platform:    "aarch64-apple-darwin25.0.0",
		Arch:        "aarch64",
		OS:          "darwin25.0.0",
		PackageType: "binary",
	}

	gotA := predictedLibraryPath(cacheRoot, scriptPath, cranDeps, biocDeps, nil, "https://cloud.r-project.org", runtimeA)
	gotB := predictedLibraryPath(cacheRoot, scriptPath, cranDeps, biocDeps, nil, "https://cloud.r-project.org", runtimeB)

	if gotA == gotB {
		t.Fatalf("predictedLibraryPath() should change when runtime metadata changes, got %q", gotA)
	}
}

func TestPredictedLibraryPathIgnoresTokenEnvButTracksSourceIdentity(t *testing.T) {
	cacheRoot := "/tmp/rs-cache"
	scriptPath := "/tmp/project/script.R"
	runtime := RuntimeMetadata{
		Interpreter: "/usr/local/bin/Rscript",
		RVersion:    "4.5.0",
		Platform:    "aarch64-apple-darwin25.0.0",
		Arch:        "aarch64",
		OS:          "darwin25.0.0",
		PackageType: "source",
	}
	base := map[string]project.SourceSpec{
		"mypkg": {
			Package:  "mypkg",
			Type:     "github",
			Host:     "github.example.com/api/v3",
			Repo:     "owner/mypkg",
			Ref:      "main",
			Subdir:   "pkg",
			TokenEnv: "TOKEN_ONE",
		},
	}
	tokenOnlyChanged := map[string]project.SourceSpec{
		"mypkg": {
			Package:  "mypkg",
			Type:     "github",
			Host:     "github.example.com/api/v3",
			Repo:     "owner/mypkg",
			Ref:      "main",
			Subdir:   "pkg",
			TokenEnv: "TOKEN_TWO",
		},
	}
	refChanged := map[string]project.SourceSpec{
		"mypkg": {
			Package:  "mypkg",
			Type:     "github",
			Host:     "github.example.com/api/v3",
			Repo:     "owner/mypkg",
			Ref:      "release",
			Subdir:   "pkg",
			TokenEnv: "TOKEN_ONE",
		},
	}

	gotBase := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, base, "https://cloud.r-project.org", runtime)
	gotTokenOnlyChanged := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, tokenOnlyChanged, "https://cloud.r-project.org", runtime)
	gotRefChanged := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, refChanged, "https://cloud.r-project.org", runtime)

	if gotBase != gotTokenOnlyChanged {
		t.Fatalf("predictedLibraryPath() should ignore token env changes: %q vs %q", gotBase, gotTokenOnlyChanged)
	}
	if gotBase == gotRefChanged {
		t.Fatalf("predictedLibraryPath() should change when source identity changes, got %q", gotBase)
	}
}

func TestPredictedLibraryPathTracksGitHubHostAndSubdirChanges(t *testing.T) {
	cacheRoot := "/tmp/rs-cache"
	scriptPath := "/tmp/project/script.R"
	runtime := RuntimeMetadata{
		Interpreter: "/usr/local/bin/Rscript",
		RVersion:    "4.5.0",
		Platform:    "aarch64-apple-darwin25.0.0",
		Arch:        "aarch64",
		OS:          "darwin25.0.0",
		PackageType: "source",
	}
	base := map[string]project.SourceSpec{
		"mypkg": {
			Package: "mypkg",
			Type:    "github",
			Host:    "github.example.com/api/v3",
			Repo:    "owner/mypkg",
			Ref:     "main",
			Subdir:  "pkg",
		},
	}
	hostChanged := map[string]project.SourceSpec{
		"mypkg": {
			Package: "mypkg",
			Type:    "github",
			Host:    "github.enterprise.local/api/v3",
			Repo:    "owner/mypkg",
			Ref:     "main",
			Subdir:  "pkg",
		},
	}
	subdirChanged := map[string]project.SourceSpec{
		"mypkg": {
			Package: "mypkg",
			Type:    "github",
			Host:    "github.example.com/api/v3",
			Repo:    "owner/mypkg",
			Ref:     "main",
			Subdir:  "pkg/sub",
		},
	}

	gotBase := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, base, "https://cloud.r-project.org", runtime)
	gotHostChanged := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, hostChanged, "https://cloud.r-project.org", runtime)
	gotSubdirChanged := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, subdirChanged, "https://cloud.r-project.org", runtime)

	if gotBase == gotHostChanged {
		t.Fatalf("predictedLibraryPath() should change when github host changes, got %q", gotBase)
	}
	if gotBase == gotSubdirChanged {
		t.Fatalf("predictedLibraryPath() should change when github subdir changes, got %q", gotBase)
	}
}

func TestDescribeLocalSourceFingerprintFileAndDir(t *testing.T) {
	dir := t.TempDir()

	filePath := filepath.Join(dir, "pkg.tar.gz")
	if err := os.WriteFile(filePath, []byte("pkg-v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}

	kind, fingerprint := describeLocalSourceFingerprint(filePath)
	if kind != localSourceFingerprintKindFile || fingerprint == "" {
		t.Fatalf("describeLocalSourceFingerprint(file) = (%q, %q), want file fingerprint", kind, fingerprint)
	}

	pkgDir := filepath.Join(dir, "pkgdir")
	if err := os.MkdirAll(filepath.Join(pkgDir, "R"), 0o755); err != nil {
		t.Fatalf("MkdirAll(pkgDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "DESCRIPTION"), []byte("Package: demo\nVersion: 0.1.0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "R", "hello.R"), []byte("hello <- function() 'hi'\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(hello.R) error = %v", err)
	}

	kind, fingerprint = describeLocalSourceFingerprint(pkgDir)
	if kind != localSourceFingerprintKindDir || fingerprint == "" {
		t.Fatalf("describeLocalSourceFingerprint(dir) = (%q, %q), want dir fingerprint", kind, fingerprint)
	}
}

func TestFingerprintDirectoryTreeChangesAcrossEdits(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgdir")
	if err := os.MkdirAll(filepath.Join(pkgDir, "R"), 0o755); err != nil {
		t.Fatalf("MkdirAll(pkgDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "DESCRIPTION"), []byte("Package: demo\nVersion: 0.1.0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "R", "hello.R"), []byte("hello <- function() 'hi'\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(hello.R) error = %v", err)
	}

	first, err := fingerprintDirectoryTree(pkgDir)
	if err != nil {
		t.Fatalf("fingerprintDirectoryTree(first) error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(pkgDir, "R", "hello.R"), []byte("hello <- function() 'hello'\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(hello.R v2) error = %v", err)
	}
	second, err := fingerprintDirectoryTree(pkgDir)
	if err != nil {
		t.Fatalf("fingerprintDirectoryTree(second) error = %v", err)
	}
	if first == second {
		t.Fatalf("fingerprintDirectoryTree() should change when file content changes, got %q", first)
	}

	if err := os.WriteFile(filepath.Join(pkgDir, "NAMESPACE"), []byte("export(hello)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(NAMESPACE) error = %v", err)
	}
	third, err := fingerprintDirectoryTree(pkgDir)
	if err != nil {
		t.Fatalf("fingerprintDirectoryTree(third) error = %v", err)
	}
	if second == third {
		t.Fatalf("fingerprintDirectoryTree() should change when a file is added, got %q", second)
	}

	if err := os.Rename(filepath.Join(pkgDir, "R", "hello.R"), filepath.Join(pkgDir, "R", "renamed.R")); err != nil {
		t.Fatalf("Rename(hello.R) error = %v", err)
	}
	fourth, err := fingerprintDirectoryTree(pkgDir)
	if err != nil {
		t.Fatalf("fingerprintDirectoryTree(fourth) error = %v", err)
	}
	if third == fourth {
		t.Fatalf("fingerprintDirectoryTree() should change when a file is renamed, got %q", third)
	}
}

func TestPredictedLibraryPathTracksGitAndLocalSourceIdentity(t *testing.T) {
	dir := t.TempDir()
	cacheRoot := filepath.Join(dir, "cache")
	scriptPath := filepath.Join(dir, "script.R")
	localPath := filepath.Join(dir, "localpkg.tar.gz")
	if err := os.WriteFile(localPath, []byte("local-pkg-v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(localPath) error = %v", err)
	}

	runtime := RuntimeMetadata{
		Interpreter: "/usr/local/bin/Rscript",
		RVersion:    "4.5.0",
		Platform:    "aarch64-apple-darwin25.0.0",
		Arch:        "aarch64",
		OS:          "darwin25.0.0",
		PackageType: "source",
	}
	base := map[string]project.SourceSpec{
		"gitpkg": {
			Package: "gitpkg",
			Type:    "git",
			URL:     "file:///tmp/repo",
			Ref:     "main",
			Subdir:  "pkg",
		},
		"localpkg": {
			Package: "localpkg",
			Type:    "local",
			Path:    localPath,
		},
	}
	gitChanged := map[string]project.SourceSpec{
		"gitpkg": {
			Package: "gitpkg",
			Type:    "git",
			URL:     "file:///tmp/repo",
			Ref:     "release",
			Subdir:  "pkg",
		},
		"localpkg": base["localpkg"],
	}

	gotBase := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, base, "https://cloud.r-project.org", runtime)
	gotGitChanged := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, gitChanged, "https://cloud.r-project.org", runtime)
	if gotBase == gotGitChanged {
		t.Fatalf("predictedLibraryPath() should change when git source identity changes, got %q", gotBase)
	}

	if err := os.WriteFile(localPath, []byte("local-pkg-v2"), 0o644); err != nil {
		t.Fatalf("WriteFile(localPath v2) error = %v", err)
	}
	gotLocalChanged := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, base, "https://cloud.r-project.org", runtime)
	if gotBase == gotLocalChanged {
		t.Fatalf("predictedLibraryPath() should change when local source fingerprint changes, got %q", gotBase)
	}
}

func TestPredictedLibraryPathTracksGitURLAndSubdirChanges(t *testing.T) {
	dir := t.TempDir()
	cacheRoot := filepath.Join(dir, "cache")
	scriptPath := filepath.Join(dir, "script.R")
	runtime := RuntimeMetadata{
		Interpreter: "/usr/local/bin/Rscript",
		RVersion:    "4.5.0",
		Platform:    "aarch64-apple-darwin25.0.0",
		Arch:        "aarch64",
		OS:          "darwin25.0.0",
		PackageType: "source",
	}
	base := map[string]project.SourceSpec{
		"gitpkg": {
			Package: "gitpkg",
			Type:    "git",
			URL:     "file:///tmp/repo",
			Ref:     "main",
			Subdir:  "pkg",
		},
	}
	urlChanged := map[string]project.SourceSpec{
		"gitpkg": {
			Package: "gitpkg",
			Type:    "git",
			URL:     "file:///tmp/repo-two",
			Ref:     "main",
			Subdir:  "pkg",
		},
	}
	subdirChanged := map[string]project.SourceSpec{
		"gitpkg": {
			Package: "gitpkg",
			Type:    "git",
			URL:     "file:///tmp/repo",
			Ref:     "main",
			Subdir:  "pkg/sub",
		},
	}

	gotBase := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, base, "https://cloud.r-project.org", runtime)
	gotURLChanged := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, urlChanged, "https://cloud.r-project.org", runtime)
	gotSubdirChanged := predictedLibraryPath(cacheRoot, scriptPath, nil, nil, subdirChanged, "https://cloud.r-project.org", runtime)

	if gotBase == gotURLChanged {
		t.Fatalf("predictedLibraryPath() should change when git url changes, got %q", gotBase)
	}
	if gotBase == gotSubdirChanged {
		t.Fatalf("predictedLibraryPath() should change when git subdir changes, got %q", gotBase)
	}
}

func TestValidationErrorIncludesModeContextAndHint(t *testing.T) {
	err := ValidationError{
		Mode:         ValidationModeFrozen,
		Kind:         ValidationKindInstalled,
		ScriptPath:   "/tmp/project/script.R",
		LockfilePath: "/tmp/project/rs.lock.json",
		LibraryPath:  "/tmp/project/.rs-cache/lib/abcdef0123456789",
		Issues:       []string{"package not installed in managed library: cli"},
	}

	got := err.Error()
	for _, want := range []string{
		"frozen mode validation failed: /tmp/project/rs.lock.json",
		"the managed library does not match the frozen dependency set",
		"package not installed in managed library: cli",
		"summary: missing packages = cli",
		"hint: run `rs cache rm /tmp/project/.rs-cache/lib/abcdef0123456789`",
		"run `rs lock /tmp/project/script.R`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ValidationError.Error() missing %q in:\n%s", want, got)
		}
	}
}

func TestInstalledSummaryLines(t *testing.T) {
	got := installedSummaryLines([]string{
		"package not installed in managed library: cli",
		"version mismatch for jsonlite: lockfile has 1.8.0, installed is 1.8.1",
		"source ref mismatch for mypkg: lockfile has main, installed is release",
		"source fingerprint mismatch for localpkg: lockfile has abc123, installed is def456",
		"priority mismatch for stats: lockfile has base, installed is <none>",
	})

	for _, want := range []string{
		"summary: missing packages = cli",
		"summary: version mismatches = jsonlite",
		"summary: source mismatches = localpkg(source_fingerprint), mypkg(source_ref)",
		"summary: other installed mismatch = priority mismatch for stats: lockfile has base, installed is <none>",
	} {
		if !reflect.DeepEqual(true, slices.Contains(got, want)) {
			t.Fatalf("installedSummaryLines() missing %q in %v", want, got)
		}
	}
}

func TestInputSummaryLines(t *testing.T) {
	got := inputSummaryLines([]string{
		"script mismatch: lockfile has /tmp/a.R, current script is /tmp/b.R",
		"project config changed after lockfile was generated at 2026-03-27T00:00:00Z",
		"repository mismatch: lockfile has https://a, current repo is https://b",
		"R version mismatch: lockfile has 4.5.0, current runtime is 4.5.1",
		"missing package in lockfile: cli",
		"lockfile contains unexpected package: jsonlite",
		"source ref mismatch for mypkg: lockfile has main, config requires release",
		"source fingerprint mismatch for localpkg: lockfile has abc123, config requires def456",
		"unsupported lockfile version 2",
	})

	for _, want := range []string{
		"summary: script/config drift = project config timestamp, script path",
		"summary: runtime drift = R version, repository",
		"summary: dependency set drift = cli, jsonlite",
		"summary: source config drift = localpkg(source_fingerprint), mypkg(source_ref)",
		"summary: other input mismatch = unsupported lockfile version 2",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("inputSummaryLines() missing %q in %v", want, got)
		}
	}
}

func TestValidationErrorIncludesInputSummaryAndHint(t *testing.T) {
	err := ValidationError{
		Mode:         ValidationModeLocked,
		Kind:         ValidationKindInputs,
		ScriptPath:   "/tmp/project/script.R",
		LockfilePath: "/tmp/project/rs.lock.json",
		Issues: []string{
			"repository mismatch: lockfile has https://a, current repo is https://b",
			"missing package in lockfile: cli",
		},
	}

	got := err.Error()
	for _, want := range []string{
		"locked mode validation failed: /tmp/project/rs.lock.json",
		"the current script, config, or runtime no longer matches the lockfile inputs required by --locked",
		"summary: runtime drift = repository",
		"summary: dependency set drift = cli",
		"hint: run `rs lock /tmp/project/script.R`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ValidationError.Error() missing %q in:\n%s", want, got)
		}
	}
}

func TestValidationIssuesPrefersStructuredIssues(t *testing.T) {
	err := ValidationError{
		Mode:         ValidationModeCheck,
		Kind:         ValidationKindInputs,
		ScriptPath:   "/tmp/project/script.R",
		LockfilePath: "/tmp/project/rs.lock.json",
		Issues:       []string{"repository mismatch: lockfile has a, current repo is b"},
	}

	got := validationIssues(err)
	if !reflect.DeepEqual(got, []string{"repository mismatch: lockfile has a, current repo is b"}) {
		t.Fatalf("validationIssues() = %v", got)
	}
}

func TestValidationIssueBreakdownSplitsKinds(t *testing.T) {
	inputErr := ValidationError{
		Mode:         ValidationModeCheck,
		Kind:         ValidationKindInputs,
		ScriptPath:   "/tmp/project/script.R",
		LockfilePath: "/tmp/project/rs.lock.json",
		Issues:       []string{"repository mismatch: lockfile has a, current repo is b"},
	}
	installedErr := ValidationError{
		Mode:         ValidationModeCheck,
		Kind:         ValidationKindInstalled,
		ScriptPath:   "/tmp/project/script.R",
		LockfilePath: "/tmp/project/rs.lock.json",
		LibraryPath:  "/tmp/project/.rs-cache/lib/abcdef0123456789",
		Issues:       []string{"version mismatch for cli: lockfile has 1.0.0, installed is 1.1.0"},
	}

	issues, inputIssues, installedIssues := validationIssueBreakdown(inputErr)
	if !reflect.DeepEqual(issues, inputIssues) || len(installedIssues) != 0 {
		t.Fatalf("validationIssueBreakdown(input) = issues=%v input=%v installed=%v", issues, inputIssues, installedIssues)
	}

	issues, inputIssues, installedIssues = validationIssueBreakdown(installedErr)
	if !reflect.DeepEqual(issues, installedIssues) || len(inputIssues) != 0 {
		t.Fatalf("validationIssueBreakdown(installed) = issues=%v input=%v installed=%v", issues, inputIssues, installedIssues)
	}
}

func TestCategorizeInstalledIssues(t *testing.T) {
	missing, version, source, other := categorizeInstalledIssues([]string{
		"package not installed in managed library: cli",
		"version mismatch for jsonlite: lockfile has 1.8.0, installed is 1.8.1",
		"source ref mismatch for mypkg: lockfile has main, installed is release",
		"priority mismatch for stats: lockfile has base, installed is <none>",
	})

	if !reflect.DeepEqual(missing, []string{"cli"}) {
		t.Fatalf("missing = %v", missing)
	}
	if !reflect.DeepEqual(version, []string{"version mismatch for jsonlite: lockfile has 1.8.0, installed is 1.8.1"}) {
		t.Fatalf("version = %v", version)
	}
	if !reflect.DeepEqual(source, []string{"source ref mismatch for mypkg: lockfile has main, installed is release"}) {
		t.Fatalf("source = %v", source)
	}
	if !reflect.DeepEqual(other, []string{"priority mismatch for stats: lockfile has base, installed is <none>"}) {
		t.Fatalf("other = %v", other)
	}
}

func TestBuildInstalledIssueDetails(t *testing.T) {
	got := buildInstalledIssueDetails([]string{
		"package not installed in managed library: cli",
		"version mismatch for jsonlite: lockfile has 1.8.0, installed is 1.8.1",
		"source ref mismatch for mypkg: lockfile has main, installed is release",
		"source fingerprint mismatch for localpkg: lockfile has abc123, installed is def456",
		"priority mismatch for stats: lockfile has base, installed is <none>",
		"unexpected installed issue",
	})

	want := []InstalledIssueDetail{
		{Kind: "missing_package", Package: "cli", Message: "package not installed in managed library: cli"},
		{Kind: "version_mismatch", Package: "jsonlite", Field: "version", Message: "version mismatch for jsonlite: lockfile has 1.8.0, installed is 1.8.1"},
		{Kind: "source_mismatch", Package: "mypkg", Field: "source_ref", Message: "source ref mismatch for mypkg: lockfile has main, installed is release"},
		{Kind: "source_mismatch", Package: "localpkg", Field: "source_fingerprint", Message: "source fingerprint mismatch for localpkg: lockfile has abc123, installed is def456"},
		{Kind: "priority_mismatch", Package: "stats", Field: "priority", Message: "priority mismatch for stats: lockfile has base, installed is <none>"},
		{Kind: "other", Message: "unexpected installed issue"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildInstalledIssueDetails() = %#v, want %#v", got, want)
	}
}

func TestCategorizeDoctorIssues(t *testing.T) {
	setup, source, network, runtime, other := categorizeDoctorErrors([]string{
		"Rscript is not available on PATH",
		"source \"privatepkg\" requires environment variable GH_ENTERPRISE_PAT, but it is not set",
		"unexpected doctor failure",
	})
	if !reflect.DeepEqual(setup, []string{"Rscript is not available on PATH"}) {
		t.Fatalf("setup = %v", setup)
	}
	if len(source) != 0 {
		t.Fatalf("source = %v", source)
	}
	if !reflect.DeepEqual(network, []string{"source \"privatepkg\" requires environment variable GH_ENTERPRISE_PAT, but it is not set"}) {
		t.Fatalf("network = %v", network)
	}
	if len(runtime) != 0 {
		t.Fatalf("runtime = %v", runtime)
	}
	if !reflect.DeepEqual(other, []string{"unexpected doctor failure"}) {
		t.Fatalf("other = %v", other)
	}

	lockWarnings, cacheWarnings, otherWarnings := categorizeDoctorWarnings([]string{
		"lockfile not found: /tmp/project/rs.lock.json",
		"managed library directory does not exist yet: /tmp/project/.rs-cache/lib/abc",
		"unexpected warning",
	})
	if !reflect.DeepEqual(lockWarnings, []string{"lockfile not found: /tmp/project/rs.lock.json"}) {
		t.Fatalf("lockWarnings = %v", lockWarnings)
	}
	if !reflect.DeepEqual(cacheWarnings, []string{"managed library directory does not exist yet: /tmp/project/.rs-cache/lib/abc"}) {
		t.Fatalf("cacheWarnings = %v", cacheWarnings)
	}
	if !reflect.DeepEqual(otherWarnings, []string{"unexpected warning"}) {
		t.Fatalf("otherWarnings = %v", otherWarnings)
	}
}

func TestCollectSystemDependencyHints(t *testing.T) {
	details := collectSystemDependencyHintDetails(
		[]string{"curl", "odbc", "stringi", "xml2", "textshaping"},
		[]string{"terra"},
		map[string]project.SourceSpec{
			"git2r": {Package: "git2r", Type: "local"},
			"rJava": {Package: "rJava", Type: "local"},
		},
	)
	hints := renderSystemHints(details)

	joined := strings.Join(hints, "\n")
	for _, want := range []string{
		"packages curl, git2r commonly need libcurl and OpenSSL development headers",
		"package odbc commonly need database client development libraries",
		"package stringi commonly needs ICU development libraries",
		"package xml2 commonly need libxml2 development headers",
		"package terra commonly need geospatial system libraries",
		"package rJava needs a working Java/JDK toolchain",
		"package textshaping commonly need font and text rendering libraries",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("collectSystemDependencyHints() missing %q in:\n%s", want, joined)
		}
	}

	categories := []string{}
	for _, detail := range details {
		categories = append(categories, detail.Category)
	}
	for _, want := range []string{"network", "icu", "xml", "geospatial", "java", "database", "fonts"} {
		if !slices.Contains(categories, want) {
			t.Fatalf("collectSystemDependencyHintDetails() missing category %q in %v", want, categories)
		}
	}
}

func TestCompareInstalledPackagesDetectsLocalSourceFingerprintMismatch(t *testing.T) {
	locked := []lockfile.Package{
		{
			Name:                  "localpkg",
			Version:               "0.1.0",
			Source:                "local",
			SourceLocation:        "/tmp/localpkg.tar.gz",
			SourceFingerprintKind: localSourceFingerprintKindFile,
			SourceFingerprint:     "abc123",
		},
	}
	actual := []lockfile.Package{
		{
			Name:                  "localpkg",
			Version:               "0.1.0",
			Source:                "local",
			SourceLocation:        "/tmp/localpkg.tar.gz",
			SourceFingerprintKind: localSourceFingerprintKindFile,
			SourceFingerprint:     "def456",
		},
	}

	issues := compareInstalledPackages(locked, actual)
	if len(issues) != 1 || !strings.Contains(issues[0], "source fingerprint mismatch for localpkg") {
		t.Fatalf("compareInstalledPackages() = %v, want fingerprint mismatch", issues)
	}
}

func TestCollectValidationIssuesMismatch(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.R")
	cfgPath := filepath.Join(dir, "rs.toml")
	if err := os.WriteFile(scriptPath, []byte("library(stats)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("packages = [\"cli\"]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	env := ResolvedEnvironment{
		ScriptPath:  scriptPath,
		Repo:        "https://cloud.r-project.org",
		LibraryPath: filepath.Join(dir, ".rs-cache", "lib", "abc"),
		Interpreter: "/usr/bin/Rscript",
		CRANDeps:    []string{"cli", "stats"},
		ProjectConfig: project.Config{
			Path: cfgPath,
		},
	}

	file := lockfile.File{
		Version:     1,
		GeneratedAt: time.Now().UTC().Add(-time.Hour),
		Script:      scriptPath,
		Repo:        "https://cran.rstudio.com",
		Library:     filepath.Join(dir, ".rs-cache", "lib", "old"),
		Metadata: lockfile.Metadata{
			Interpreter: "/opt/homebrew/bin/Rscript",
			RVersion:    "4.4.0",
			Platform:    "x86_64-apple-darwin",
			Arch:        "x86_64",
			OS:          "darwin24.0.0",
			PackageType: "binary",
		},
		Packages: []lockfile.Package{
			{Name: "cli", Version: "3.6.4", Source: "cran"},
		},
	}

	runtime := RuntimeMetadata{
		Interpreter: "/usr/bin/Rscript",
		RVersion:    "4.5.2",
		Platform:    "aarch64-apple-darwin25.0.0",
		Arch:        "aarch64",
		OS:          "darwin25.0.0",
		PackageType: "source",
	}

	actual := []lockfile.Package{
		{Name: "cli", Version: "3.6.5", Source: "cran"},
	}

	issues := collectValidationIssues(env, file, runtime, actual)
	joined := strings.Join(issues, "\n")

	for _, want := range []string{
		"repository mismatch",
		"library mismatch",
		"interpreter mismatch",
		"R version mismatch",
		"platform mismatch",
		"arch mismatch",
		"os mismatch",
		"package type mismatch",
		"script changed after lockfile",
		"project config changed after lockfile",
		"missing package in lockfile: stats",
		"version mismatch for cli",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected issue containing %q, got:\n%s", want, joined)
		}
	}
}

func TestCompareInstalledPackagesMatches(t *testing.T) {
	locked := []lockfile.Package{
		{Name: "cli", Version: "3.6.5", Source: "cran"},
		{Name: "stats", Version: "4.5.2", Source: "base", Priority: "base"},
	}
	actual := []lockfile.Package{
		{Name: "cli", Version: "3.6.5", Source: "cran"},
		{Name: "stats", Version: "4.5.2", Source: "base", Priority: "base"},
	}

	issues := compareInstalledPackages(locked, actual)
	if len(issues) != 0 {
		t.Fatalf("compareInstalledPackages() issues = %v, want none", issues)
	}
}

func TestCollectInputValidationIssuesIgnoresInstalledState(t *testing.T) {
	env := ResolvedEnvironment{
		ScriptPath:  "/tmp/project/script.R",
		Repo:        "https://cloud.r-project.org",
		LibraryPath: "/tmp/project/.rs-cache/lib/abc",
		Interpreter: "/usr/bin/Rscript",
		CRANDeps:    []string{"cli"},
	}
	file := lockfile.File{
		Version:     1,
		GeneratedAt: time.Now().UTC().Add(time.Hour),
		Script:      "/tmp/project/script.R",
		Repo:        "https://cloud.r-project.org",
		Library:     "/tmp/project/.rs-cache/lib/abc",
		Metadata: lockfile.Metadata{
			Interpreter: "/usr/bin/Rscript",
			RVersion:    "4.5.2",
			Platform:    "aarch64-apple-darwin25.0.0",
			PackageType: "source",
		},
		Packages: []lockfile.Package{
			{Name: "cli", Version: "3.6.5", Source: "cran"},
		},
	}
	runtime := RuntimeMetadata{
		Interpreter: "/usr/bin/Rscript",
		RVersion:    "4.5.2",
		Platform:    "aarch64-apple-darwin25.0.0",
		PackageType: "source",
	}

	issues := collectInputValidationIssues(env, file, runtime)
	if len(issues) != 0 {
		t.Fatalf("collectInputValidationIssues() issues = %v, want none", issues)
	}
}

func TestCompareLockedSources(t *testing.T) {
	env := ResolvedEnvironment{
		SourceDeps: map[string]project.SourceSpec{
			"custompkg": {
				Package: "custompkg",
				Type:    "github",
				Host:    "github.example.com/api/v3",
				Repo:    "owner/custompkg",
				Ref:     "main",
				Subdir:  "pkg",
			},
		},
	}
	locked := []lockfile.Package{
		{
			Name:           "custompkg",
			Version:        "0.1.0",
			Source:         "github",
			SourceHost:     "api.github.com",
			SourceLocation: "owner/custompkg",
			SourceRef:      "v1.0.0",
			SourceSubdir:   "rootpkg",
		},
	}

	issues := compareLockedSources(env, locked)
	if len(issues) != 3 {
		t.Fatalf("compareLockedSources() = %v, want three issues", issues)
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "source host mismatch") || !strings.Contains(joined, "source ref mismatch") || !strings.Contains(joined, "source subdir mismatch") {
		t.Fatalf("compareLockedSources() = %v, want host/ref/subdir mismatch", issues)
	}
}

func TestCompareLockedSourcesGitLocationRefAndSubdir(t *testing.T) {
	env := ResolvedEnvironment{
		SourceDeps: map[string]project.SourceSpec{
			"gitpkg": {
				Package: "gitpkg",
				Type:    "git",
				URL:     "file:///tmp/repo",
				Ref:     "main",
				Subdir:  "pkg",
			},
		},
	}
	locked := []lockfile.Package{
		{
			Name:           "gitpkg",
			Version:        "0.1.0",
			Source:         "git",
			SourceLocation: "file:///tmp/other-repo",
			SourceRef:      "release",
			SourceSubdir:   "pkg/sub",
		},
	}

	issues := compareLockedSources(env, locked)
	if len(issues) != 3 {
		t.Fatalf("compareLockedSources() = %v, want three git issues", issues)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"source location mismatch", "source ref mismatch", "source subdir mismatch"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("compareLockedSources() missing %q in %v", want, issues)
		}
	}
}

func TestCompareLockedSourcesDetectsLocalFingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "localpkg.tar.gz")
	if err := os.WriteFile(localPath, []byte("local-pkg-v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(localPath) error = %v", err)
	}

	env := ResolvedEnvironment{
		SourceDeps: map[string]project.SourceSpec{
			"localpkg": {
				Package: "localpkg",
				Type:    "local",
				Path:    localPath,
			},
		},
	}
	locked := []lockfile.Package{
		{
			Name:                  "localpkg",
			Version:               "0.1.0",
			Source:                "local",
			SourceLocation:        localPath,
			SourceFingerprintKind: localSourceFingerprintKindFile,
			SourceFingerprint:     "oldfingerprint",
		},
	}

	issues := compareLockedSources(env, locked)
	if len(issues) != 1 || !strings.Contains(issues[0], "source fingerprint mismatch for localpkg") {
		t.Fatalf("compareLockedSources() = %v, want local fingerprint mismatch", issues)
	}
}

func TestCompareLockedSourcesDetectsLocalFingerprintKindMismatch(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "localpkg")
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(localPath) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localPath, "DESCRIPTION"), []byte("Package: demo\nVersion: 0.1.0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}

	env := ResolvedEnvironment{
		SourceDeps: map[string]project.SourceSpec{
			"localpkg": {
				Package: "localpkg",
				Type:    "local",
				Path:    localPath,
			},
		},
	}
	locked := []lockfile.Package{
		{
			Name:                  "localpkg",
			Version:               "0.1.0",
			Source:                "local",
			SourceLocation:        localPath,
			SourceFingerprintKind: localSourceFingerprintKindFile,
		},
	}

	issues := compareLockedSources(env, locked)
	if len(issues) != 1 || !strings.Contains(issues[0], "source fingerprint kind mismatch for localpkg") {
		t.Fatalf("compareLockedSources() = %v, want local fingerprint kind mismatch", issues)
	}
}

func TestEnrichLockedPackagesAddsLocalSourceFingerprint(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "localpkg.tar.gz")
	if err := os.WriteFile(localPath, []byte("local-pkg-v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(localPath) error = %v", err)
	}

	env := ResolvedEnvironment{
		SourceDeps: map[string]project.SourceSpec{
			"localpkg": {
				Package: "localpkg",
				Type:    "local",
				Path:    localPath,
			},
		},
	}
	pkgs := []lockfile.Package{{Name: "localpkg", Version: "0.1.0"}}

	enrichLockedPackages(env, pkgs)
	if pkgs[0].Source != "local" || pkgs[0].SourceLocation != localPath {
		t.Fatalf("enrichLockedPackages() source fields = %#v", pkgs[0])
	}
	if pkgs[0].SourceFingerprintKind != localSourceFingerprintKindFile || pkgs[0].SourceFingerprint == "" {
		t.Fatalf("enrichLockedPackages() fingerprint fields = %#v", pkgs[0])
	}
}

func TestReadInstalledSourceMetadataIncludesFingerprintFields(t *testing.T) {
	dir := t.TempDir()
	metaDir := filepath.Join(dir, ".rs-source-meta")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(metaDir) error = %v", err)
	}
	line := strings.Join([]string{"local", "", "/tmp/localpkg.tar.gz", "", "", "", "abc123", localSourceFingerprintKindFile}, "\t")
	if err := os.WriteFile(filepath.Join(metaDir, "localpkg.tsv"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(metadata) error = %v", err)
	}

	meta, err := readInstalledSourceMetadata(dir)
	if err != nil {
		t.Fatalf("readInstalledSourceMetadata() error = %v", err)
	}
	got := meta["localpkg"]
	if got.Source != "local" || got.SourceLocation != "/tmp/localpkg.tar.gz" || got.SourceFingerprint != "abc123" || got.SourceFingerprintKind != localSourceFingerprintKindFile {
		t.Fatalf("readInstalledSourceMetadata() = %#v", got)
	}
}

func TestValidateSourceDeps(t *testing.T) {
	err := validateSourceDeps(map[string]project.SourceSpec{
		"broken": {
			Package: "broken",
			Type:    "github",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing repo") {
		t.Fatalf("validateSourceDeps() error = %v, want missing repo", err)
	}
}

func TestValidateSourceDepsMissingTokenEnv(t *testing.T) {
	t.Setenv("GH_ENTERPRISE_PAT", "")
	err := validateSourceDeps(map[string]project.SourceSpec{
		"privatepkg": {
			Package:  "privatepkg",
			Type:     "github",
			Repo:     "owner/privatepkg",
			TokenEnv: "GH_ENTERPRISE_PAT",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "GH_ENTERPRISE_PAT") {
		t.Fatalf("validateSourceDeps() error = %v, want missing token env", err)
	}
}

func TestCollectSourceDefinitionIssues(t *testing.T) {
	t.Setenv("GH_ENTERPRISE_PAT", "")
	issues := collectSourceDefinitionIssues(map[string]project.SourceSpec{
		"privatepkg": {
			Package:  "privatepkg",
			Type:     "github",
			Repo:     "owner/privatepkg",
			TokenEnv: "GH_ENTERPRISE_PAT",
		},
		"brokenlocal": {
			Package: "brokenlocal",
			Type:    "local",
		},
	})
	joined := strings.Join(issues, "\n")
	for _, want := range []string{
		"GH_ENTERPRISE_PAT",
		"brokenlocal",
		"missing path",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("collectSourceDefinitionIssues() missing %q in %q", want, joined)
		}
	}
}

func TestCollectSourceAvailabilityIssues(t *testing.T) {
	dir := t.TempDir()
	localPkg := filepath.Join(dir, "pkg.tar.gz")
	if err := os.WriteFile(localPkg, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("WriteFile(localPkg) error = %v", err)
	}

	localRepo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(localRepo, 0o755); err != nil {
		t.Fatalf("MkdirAll(localRepo) error = %v", err)
	}

	issues := collectSourceAvailabilityIssues(map[string]project.SourceSpec{
		"existinglocal": {
			Package: "existinglocal",
			Type:    "local",
			Path:    localPkg,
		},
		"missinglocal": {
			Package: "missinglocal",
			Type:    "local",
			Path:    filepath.Join(dir, "missing.tar.gz"),
		},
		"existinggit": {
			Package: "existinggit",
			Type:    "git",
			URL:     "file://" + localRepo,
		},
		"missinggit": {
			Package: "missinggit",
			Type:    "git",
			URL:     "file://" + filepath.Join(dir, "missing-repo"),
		},
		"remotegit": {
			Package: "remotegit",
			Type:    "git",
			URL:     "https://example.com/repo.git",
		},
	})

	joined := strings.Join(issues, "\n")
	if strings.Contains(joined, "existinglocal") || strings.Contains(joined, "existinggit") || strings.Contains(joined, "remotegit") {
		t.Fatalf("collectSourceAvailabilityIssues() reported unexpected issue: %q", joined)
	}
	for _, want := range []string{
		"missinglocal",
		"missinggit",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("collectSourceAvailabilityIssues() missing %q in %q", want, joined)
		}
	}
}

func TestLocalGitPath(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "empty", raw: "", want: "", ok: false},
		{name: "file url", raw: "file:///tmp/repo", want: "/tmp/repo", ok: true},
		{name: "plain path", raw: "/tmp/repo", want: "/tmp/repo", ok: true},
		{name: "ssh remote", raw: "git@github.com:owner/repo.git", want: "", ok: false},
		{name: "https remote", raw: "https://github.com/owner/repo.git", want: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := localGitPath(tt.raw)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("localGitPath(%q) = (%q, %t), want (%q, %t)", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestBuildListReport(t *testing.T) {
	plan := dependencyPlan{
		ScriptPath:   "/tmp/project/scripts/report.R",
		ProjectPath:  "/tmp/project/rs.toml",
		ScriptKey:    "scripts/report.R",
		Repo:         "https://cloud.r-project.org",
		CacheRoot:    "/tmp/project/.rs-cache",
		LockfilePath: "/tmp/project/rs.lock.json",
		LibraryPath:  "/tmp/project/.rs-cache/lib/abc",
		DetectedDeps: []string{"jsonlite", "mypkg"},
		CRANDeps:     []string{"jsonlite"},
		BiocDeps:     []string{"DESeq2"},
		SourceDeps: map[string]project.SourceSpec{
			"mypkg": {
				Package:  "mypkg",
				Type:     "github",
				Repo:     "owner/mypkg",
				Ref:      "main",
				TokenEnv: "GITHUB_PAT",
			},
		},
	}

	report := buildListReport(plan, ListOptions{})
	if report.Script != plan.ScriptPath || report.Lockfile != plan.LockfilePath || report.ManagedLibrary != plan.LibraryPath {
		t.Fatalf("buildListReport() basic fields mismatch: %#v", report)
	}
	if len(report.CustomSources) != 1 || report.CustomSources[0].Package != "mypkg" || report.CustomSources[0].Repo != "owner/mypkg" {
		t.Fatalf("buildListReport() sources = %#v", report.CustomSources)
	}
}

func TestListJSONOutput(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	configPath := filepath.Join(dir, "rs.toml")
	if err := os.WriteFile(scriptPath, []byte("library(jsonlite)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte("packages = [\"cli\"]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	var stdout bytes.Buffer
	err := List(ListOptions{
		ScriptPath: scriptPath,
		JSON:       true,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	var report ListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if report.Script != scriptPath {
		t.Fatalf("report.Script = %q", report.Script)
	}
	if !strings.Contains(strings.Join(report.CRANDeps, ","), "cli") || !strings.Contains(strings.Join(report.DetectedDeps, ","), "jsonlite") {
		t.Fatalf("report deps = %#v", report)
	}
}

func TestListJSONOutputIncludesAppliedAdjustments(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	if err := os.WriteFile(scriptPath, []byte("library(dplyr)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := List(ListOptions{
		ScriptPath:      scriptPath,
		ExtraDeps:       []string{"cli"},
		ExtraBiocDeps:   []string{"Biostrings"},
		IncludeDeps:     []string{"cli"},
		IncludeBiocDeps: []string{"Biostrings"},
		ExcludeDeps:     []string{"dplyr"},
		JSON:            true,
		Stdout:          &stdout,
		Stderr:          &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	var report ListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if !reflect.DeepEqual(report.IncludedCRAN, []string{"cli"}) {
		t.Fatalf("report.IncludedCRAN = %v", report.IncludedCRAN)
	}
	if !reflect.DeepEqual(report.IncludedBioc, []string{"Biostrings"}) {
		t.Fatalf("report.IncludedBioc = %v", report.IncludedBioc)
	}
	if !reflect.DeepEqual(report.ExcludedDeps, []string{"dplyr"}) {
		t.Fatalf("report.ExcludedDeps = %v", report.ExcludedDeps)
	}
}

func TestDoctorPrintsAppliedAdjustments(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("library(dplyr)\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:      scriptPath,
		ExtraDeps:       []string{"cli"},
		ExtraBiocDeps:   []string{"Biostrings"},
		IncludeDeps:     []string{"cli"},
		IncludeBiocDeps: []string{"Biostrings"},
		ExcludeDeps:     []string{"dplyr"},
		RscriptPath:     rscriptPath,
		Stdout:          &stdout,
		Stderr:          &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "[info] included packages: CRAN=cli | Bioconductor=Biostrings") {
		t.Fatalf("Doctor() output missing included packages:\n%s", out)
	}
	if !strings.Contains(out, "[info] excluded packages: dplyr") {
		t.Fatalf("Doctor() output missing excluded packages:\n%s", out)
	}
}

func TestDoctorPrintsSystemHints(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("xml2::read_xml('<a/>')\ncurl::curl()\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "[hint] package curl commonly need libcurl and OpenSSL development headers") {
		t.Fatalf("Doctor() output missing curl hint:\n%s", out)
	}
	if !strings.Contains(out, "[hint] package xml2 commonly need libxml2 development headers") {
		t.Fatalf("Doctor() output missing xml2 hint:\n%s", out)
	}
	if !strings.Contains(out, "[next] create a lockfile and install the resolved dependencies: rs lock "+scriptPath) {
		t.Fatalf("Doctor() output missing lock next step:\n%s", out)
	}
	if !strings.Contains(out, "[next] materialize the managed library for this script: rs run "+scriptPath) {
		t.Fatalf("Doctor() output missing install next step:\n%s", out)
	}
	if !strings.Contains(out, "[summary] status=warning | errors=0 | warnings=2 | hints=2 | next=4 | blocking_next=0") {
		t.Fatalf("Doctor() output missing summary line:\n%s", out)
	}
}

func TestDoctorSummaryOnly(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		SummaryOnly: true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	out := strings.TrimSpace(stdout.String())
	want := "[summary] status=warning | errors=0 | warnings=2 | hints=0 | next=2 | blocking_next=0"
	if out != want {
		t.Fatalf("Doctor() output = %q, want %q", out, want)
	}
}

func TestDoctorSummaryOnlyStrictFails(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		Strict:      true,
		SummaryOnly: true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Doctor() error = nil, want strict summary-only failure")
	}
	var doctorErr DoctorError
	if !errors.As(err, &doctorErr) {
		t.Fatalf("Doctor() error = %T, want DoctorError", err)
	}
	if doctorErr.Code != 2 {
		t.Fatalf("doctorErr.Code = %d, want 2", doctorErr.Code)
	}
	out := strings.TrimSpace(stdout.String())
	want := "[summary] status=warning | errors=0 | warnings=2 | hints=0 | next=2 | blocking_next=0"
	if out != want {
		t.Fatalf("Doctor() output = %q, want %q", out, want)
	}
}

func TestDoctorQuietHidesInfoLines(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("xml2::read_xml('<a/>')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		Quiet:       true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	out := stdout.String()
	if strings.Contains(out, "[info]") {
		t.Fatalf("Doctor() output unexpectedly contains info lines:\n%s", out)
	}
	if !strings.Contains(out, "[warn] lockfile not found: ") {
		t.Fatalf("Doctor() output missing warning lines:\n%s", out)
	}
	if !strings.Contains(out, "[hint] package xml2 commonly need libxml2 development headers") {
		t.Fatalf("Doctor() output missing hint lines:\n%s", out)
	}
	if !strings.Contains(out, "[summary] status=warning") {
		t.Fatalf("Doctor() output missing summary line:\n%s", out)
	}
}

func TestDoctorJSONOutput(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("DESeq2::DESeq()\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:      scriptPath,
		ExtraDeps:       []string{"cli"},
		ExtraBiocDeps:   []string{"Biostrings"},
		IncludeDeps:     []string{"cli"},
		IncludeBiocDeps: []string{"Biostrings"},
		ExcludeDeps:     []string{"DESeq2"},
		RscriptPath:     rscriptPath,
		JSON:            true,
		Stdout:          &stdout,
		Stderr:          &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}

	var report DoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if !reflect.DeepEqual(report.CRANDeps, []string{"cli", "jsonlite"}) {
		t.Fatalf("report.CRANDeps = %v", report.CRANDeps)
	}
	if !reflect.DeepEqual(report.BiocDeps, []string{"Biostrings"}) {
		t.Fatalf("report.BiocDeps = %v", report.BiocDeps)
	}
	if !reflect.DeepEqual(report.ExcludedDeps, []string{"DESeq2"}) {
		t.Fatalf("report.ExcludedDeps = %v", report.ExcludedDeps)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("report.Warnings = %v, want at least lock/library warnings", report.Warnings)
	}
	if report.Status != "warning" {
		t.Fatalf("report.Status = %q, want warning", report.Status)
	}
	if report.Summary.WarningCount != len(report.Warnings) || report.Summary.ErrorCount != 0 {
		t.Fatalf("report.Summary = %#v, want warning summary", report.Summary)
	}
	if len(report.LockWarnings) == 0 {
		t.Fatalf("report.LockWarnings = %v, want lock warning", report.LockWarnings)
	}
	if len(report.CacheWarnings) == 0 {
		t.Fatalf("report.CacheWarnings = %v, want cache warning", report.CacheWarnings)
	}
	if len(report.SetupErrors) != 0 || len(report.SourceErrors) != 0 || len(report.NetworkErrors) != 0 || len(report.RuntimeErrors) != 0 || len(report.OtherErrors) != 0 {
		t.Fatalf("unexpected doctor error categories: setup=%v source=%v network=%v runtime=%v other=%v", report.SetupErrors, report.SourceErrors, report.NetworkErrors, report.RuntimeErrors, report.OtherErrors)
	}
	if len(report.WarningDetails) < 2 {
		t.Fatalf("report.WarningDetails = %v, want structured warning details", report.WarningDetails)
	}
	if len(report.SystemHints) != 0 {
		t.Fatalf("report.SystemHints = %v, want none for DESeq2/jsonlite/Biostrings test", report.SystemHints)
	}
	if len(report.SystemHintDetails) != 0 {
		t.Fatalf("report.SystemHintDetails = %v, want none for DESeq2/jsonlite/Biostrings test", report.SystemHintDetails)
	}
	if len(report.NextSteps) == 0 {
		t.Fatalf("report.NextSteps = %v, want actionable follow-ups", report.NextSteps)
	}
	foundLock := false
	foundRun := false
	for _, step := range report.NextSteps {
		if step.Category == "lock" && step.Kind == "create_lockfile" && step.Command == "rs lock "+scriptPath && !step.Blocking {
			foundLock = true
		}
		if step.Category == "cache" && step.Kind == "materialize_library" && step.Command == "rs run "+scriptPath && !step.Blocking {
			foundRun = true
		}
	}
	if !foundLock || !foundRun {
		t.Fatalf("report.NextSteps = %v, want lock and install commands", report.NextSteps)
	}
	if report.Summary.NextStepCount != len(report.NextSteps) || report.Summary.BlockingNextStepCount != 0 {
		t.Fatalf("report.Summary = %#v, want non-blocking next step counts", report.Summary)
	}
	kinds := []string{}
	for _, detail := range report.WarningDetails {
		kinds = append(kinds, detail.Kind)
	}
	for _, want := range []string{"missing_lockfile", "missing_managed_library"} {
		if !slices.Contains(kinds, want) {
			t.Fatalf("report.WarningDetails missing kind %q in %v", want, report.WarningDetails)
		}
	}
}

func TestDoctorStrictFailsOnWarnings(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		Strict:      true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Doctor() error = nil, want strict-mode warning failure")
	}
	var doctorErr DoctorError
	if !errors.As(err, &doctorErr) {
		t.Fatalf("Doctor() error = %T, want DoctorError", err)
	}
	if doctorErr.Code != 2 {
		t.Fatalf("doctorErr.Code = %d, want 2", doctorErr.Code)
	}
	out := stdout.String()
	if !strings.Contains(out, "[summary] status=warning") {
		t.Fatalf("Doctor() output missing warning summary:\n%s", out)
	}
	if !strings.Contains(doctorErr.Error(), "strict mode requires doctor status=ok, got warning") {
		t.Fatalf("DoctorError = %v", doctorErr)
	}
}

func TestDoctorStrictJSONStillPrintsReport(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		JSON:        true,
		Strict:      true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Doctor() error = nil, want strict-mode warning failure")
	}

	var report DoctorReport
	if unmarshalErr := json.Unmarshal(stdout.Bytes(), &report); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", unmarshalErr, stdout.String())
	}
	if report.Status != "warning" {
		t.Fatalf("report.Status = %q, want warning", report.Status)
	}
}

func TestDoctorJSONOutputIncludesSystemHints(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("xml2::read_xml('<a/>')\ncurl::curl()\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		JSON:        true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}

	var report DoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	joined := strings.Join(report.SystemHints, "\n")
	for _, want := range []string{
		"package curl commonly need libcurl and OpenSSL development headers",
		"package xml2 commonly need libxml2 development headers",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("report.SystemHints missing %q in:\n%s", want, joined)
		}
	}
	if len(report.SystemHintDetails) == 0 {
		t.Fatalf("report.SystemHintDetails = %v, want structured hints", report.SystemHintDetails)
	}
	categories := []string{}
	for _, detail := range report.SystemHintDetails {
		categories = append(categories, detail.Category)
	}
	for _, want := range []string{"network", "xml"} {
		if !slices.Contains(categories, want) {
			t.Fatalf("report.SystemHintDetails missing category %q in %v", want, categories)
		}
	}
	systemStepKinds := []string{}
	for _, step := range report.NextSteps {
		if step.Category == "system_dependency" && step.Kind == "install_system_dependency" {
			systemStepKinds = append(systemStepKinds, step.Message)
		}
	}
	if len(systemStepKinds) == 0 {
		t.Fatalf("report.NextSteps = %v, want system dependency follow-up", report.NextSteps)
	}
	if report.Status != "warning" || report.Summary.SystemHintCount != len(report.SystemHintDetails) {
		t.Fatalf("report.Status/report.Summary = %q / %#v, want warning with system hint count", report.Status, report.Summary)
	}
}

func TestDoctorJSONOutputClassifiesNetworkAndSourceErrors(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	configPath := filepath.Join(dir, project.ConfigFileName)
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("privpkg::do_work()\ngitpkg::do_work()\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}
	config := strings.Join([]string{
		"packages = [\"privpkg\", \"gitpkg\"]",
		"",
		"[sources.\"privpkg\"]",
		"type = \"github\"",
		"repo = \"owner/privatepkg\"",
		"token_env = \"RS_TEST_GH_TOKEN\"",
		"",
		"[sources.\"gitpkg\"]",
		"type = \"git\"",
		"url = \"file:///tmp/rs-missing-git-source\"",
		"",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	t.Setenv("RS_TEST_GH_TOKEN", "")

	var stdout bytes.Buffer
	err := Doctor(DoctorOptions{
		ScriptPath:  scriptPath,
		RscriptPath: rscriptPath,
		JSON:        true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Doctor() error = nil, want categorized doctor failure")
	}
	var doctorErr DoctorError
	if !errors.As(err, &doctorErr) {
		t.Fatalf("Doctor() error = %T, want DoctorError", err)
	}
	if doctorErr.Code != 1 {
		t.Fatalf("doctorErr.Code = %d, want 1", doctorErr.Code)
	}

	var report DoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if len(report.NetworkErrors) == 0 {
		t.Fatalf("report.NetworkErrors = %v, want missing token env error", report.NetworkErrors)
	}
	if report.Status != "error" {
		t.Fatalf("report.Status = %q, want error", report.Status)
	}
	if len(report.SourceErrors) == 0 {
		t.Fatalf("report.SourceErrors = %v, want missing git source error", report.SourceErrors)
	}
	if len(report.ErrorDetails) < 2 {
		t.Fatalf("report.ErrorDetails = %v, want structured doctor errors", report.ErrorDetails)
	}

	foundToken := false
	foundGitPath := false
	foundBlockingFollowup := false
	for _, detail := range report.ErrorDetails {
		if detail.Kind == "missing_token_env" && detail.Category == "network" && detail.Package == "privpkg" && detail.EnvVar == "RS_TEST_GH_TOKEN" {
			foundToken = true
		}
		if detail.Kind == "missing_git_source" && detail.Category == "source" && detail.Package == "gitpkg" && detail.Path == "/tmp/rs-missing-git-source" {
			foundGitPath = true
		}
	}
	for _, step := range report.NextSteps {
		if step.Category == "network" && step.Kind == "set_env_var" && step.Blocking {
			foundBlockingFollowup = true
		}
	}
	if !foundToken || !foundGitPath || !foundBlockingFollowup {
		t.Fatalf("report.ErrorDetails/report.NextSteps = %v / %v, want structured blocking follow-up", report.ErrorDetails, report.NextSteps)
	}
	if report.Summary.ErrorCount != len(report.Errors) || report.Summary.BlockingNextStepCount == 0 || report.Summary.NetworkErrorCount == 0 || report.Summary.SourceErrorCount == 0 {
		t.Fatalf("report.Summary = %#v, want error and blocking counts", report.Summary)
	}
}

func TestBuildDoctorNextStepsHealthyEnvironment(t *testing.T) {
	plan := dependencyPlan{
		ScriptPath: "/tmp/project/report.R",
	}

	steps := buildDoctorNextSteps(plan, nil, false, nil, nil, nil)
	if len(steps) != 1 {
		t.Fatalf("len(steps) = %d, want 1 (%v)", len(steps), steps)
	}
	if steps[0].Category != "run" || steps[0].Kind != "run_script" {
		t.Fatalf("steps[0] = %#v, want run/run_script", steps[0])
	}
	if steps[0].Command != "rs run /tmp/project/report.R" {
		t.Fatalf("steps[0].Command = %q", steps[0].Command)
	}
	if steps[0].Blocking {
		t.Fatalf("steps[0].Blocking = true, want false")
	}
}

func TestDoctorStatusOKWhenClean(t *testing.T) {
	report := buildDoctorReport(
		dependencyPlan{ScriptPath: "/tmp/project/report.R"},
		DoctorOptions{},
		"/opt/homebrew/bin/Rscript",
		nil,
		"",
		false,
		nil,
		nil,
		nil,
		nil,
		buildDoctorNextSteps(dependencyPlan{ScriptPath: "/tmp/project/report.R"}, nil, false, nil, nil, nil),
	)
	if report.Status != "ok" {
		t.Fatalf("report.Status = %q, want ok", report.Status)
	}
	if report.Summary.ErrorCount != 0 || report.Summary.WarningCount != 0 || report.Summary.NextStepCount != 1 {
		t.Fatalf("report.Summary = %#v", report.Summary)
	}
}

func TestDoctorStrictErrorMessage(t *testing.T) {
	err := doctorStrictError(DoctorReport{
		Status: "warning",
		Summary: DoctorSummary{
			WarningCount:          2,
			SystemHintCount:       1,
			BlockingNextStepCount: 1,
		},
	})
	if !strings.Contains(err.Error(), "strict mode requires doctor status=ok, got warning") {
		t.Fatalf("doctorStrictError() = %v", err)
	}
	if !strings.Contains(err.Error(), "warnings: 2") {
		t.Fatalf("doctorStrictError() = %v", err)
	}
	if err.Code != 2 {
		t.Fatalf("doctorStrictError().Code = %d, want 2", err.Code)
	}
}

func TestFormatDoctorSummary(t *testing.T) {
	line := formatDoctorSummary(DoctorReport{
		Status: "error",
		Summary: DoctorSummary{
			ErrorCount:            2,
			WarningCount:          1,
			SystemHintCount:       3,
			NextStepCount:         4,
			BlockingNextStepCount: 2,
		},
	})
	want := "status=error | errors=2 | warnings=1 | hints=3 | next=4 | blocking_next=2"
	if line != want {
		t.Fatalf("formatDoctorSummary() = %q, want %q", line, want)
	}
}

func TestPrintAppliedAdjustments(t *testing.T) {
	var out bytes.Buffer
	printAppliedAdjustments(&out, "[rs] ", []string{"cli"}, []string{"Biostrings"}, []string{"dplyr"})

	got := out.String()
	if !strings.Contains(got, "[rs] included packages: CRAN=cli | Bioconductor=Biostrings") {
		t.Fatalf("printAppliedAdjustments() missing include line:\n%s", got)
	}
	if !strings.Contains(got, "[rs] excluded packages: dplyr") {
		t.Fatalf("printAppliedAdjustments() missing exclude line:\n%s", got)
	}
}

func TestCheckJSONOutputOnFailure(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	cacheDir := filepath.Join(dir, "cache")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := Check(CheckOptions{
		ScriptPath:      scriptPath,
		ExtraDeps:       []string{"cli"},
		ExtraBiocDeps:   []string{"Biostrings"},
		IncludeDeps:     []string{"cli"},
		IncludeBiocDeps: []string{"Biostrings"},
		ExcludeDeps:     []string{"jsonlite"},
		CacheDir:        cacheDir,
		RscriptPath:     rscriptPath,
		JSON:            true,
		Stdout:          &stdout,
		Stderr:          &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Check() error = nil, want lockfile failure")
	}

	var report CheckReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if report.Valid {
		t.Fatalf("report.Valid = true, want false")
	}
	if !reflect.DeepEqual(report.IncludedCRAN, []string{"cli"}) {
		t.Fatalf("report.IncludedCRAN = %v", report.IncludedCRAN)
	}
	if !reflect.DeepEqual(report.IncludedBioc, []string{"Biostrings"}) {
		t.Fatalf("report.IncludedBioc = %v", report.IncludedBioc)
	}
	if !reflect.DeepEqual(report.ExcludedDeps, []string{"jsonlite"}) {
		t.Fatalf("report.ExcludedDeps = %v", report.ExcludedDeps)
	}
	if len(report.Issues) == 0 {
		t.Fatalf("report.Issues = %v, want lockfile-related issue", report.Issues)
	}
	if len(report.InputIssues) == 0 {
		t.Fatalf("report.InputIssues = %v, want input-side lockfile issue", report.InputIssues)
	}
	if len(report.InstalledIssues) != 0 {
		t.Fatalf("report.InstalledIssues = %v, want none for missing lockfile", report.InstalledIssues)
	}
	if len(report.InstalledMissingPackages) != 0 || len(report.InstalledVersionIssues) != 0 || len(report.InstalledSourceIssues) != 0 || len(report.InstalledOtherIssues) != 0 {
		t.Fatalf("installed issue categories should be empty: missing=%v version=%v source=%v other=%v", report.InstalledMissingPackages, report.InstalledVersionIssues, report.InstalledSourceIssues, report.InstalledOtherIssues)
	}
	if len(report.InstalledIssueDetails) != 0 {
		t.Fatalf("report.InstalledIssueDetails = %v, want none for missing lockfile", report.InstalledIssueDetails)
	}
	if len(report.PlanningIssues) != 0 {
		t.Fatalf("report.PlanningIssues = %v, want none for missing lockfile", report.PlanningIssues)
	}
	if len(report.PlanningIssueDetails) != 0 {
		t.Fatalf("report.PlanningIssueDetails = %v, want none for missing lockfile", report.PlanningIssueDetails)
	}
}

func TestCheckJSONOutputIncludesPlanningConflictDetails(t *testing.T) {
	oldValidate := nativeValidatePlan
	t.Cleanup(func() {
		nativeValidatePlan = oldValidate
	})
	nativeValidatePlan = func(req installer.Request) error {
		return installer.ConstraintConflictError{
			Package:     "cli",
			Version:     "3.6.5",
			RequiredBy:  "demo",
			Operator:    ">=",
			Requirement: "4.0.0",
			Chain:       []string{"root", "demo"},
		}
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	cacheDir := filepath.Join(dir, "cache")
	rscriptPath := writeFakeRscript(t, dir)
	if err := os.WriteFile(scriptPath, []byte("jsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	t.Setenv("RS_INSTALL_BACKEND", "native")
	var stdout bytes.Buffer
	err := Check(CheckOptions{
		ScriptPath:  scriptPath,
		CacheDir:    cacheDir,
		RscriptPath: rscriptPath,
		JSON:        true,
		Stdout:      &stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Check() error = nil, want planning conflict")
	}

	var report CheckReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if report.Valid {
		t.Fatalf("report.Valid = true, want false")
	}
	if !slices.Contains(report.PlanningIssues, "dependency constraint conflict for cli: selected version 3.6.5 does not satisfy >= 4.0.0 required by demo (dependency path: root -> demo -> cli)") {
		t.Fatalf("report.PlanningIssues = %v", report.PlanningIssues)
	}
	if len(report.InputIssues) != 0 {
		t.Fatalf("report.InputIssues = %v, want none", report.InputIssues)
	}
	if len(report.InstalledIssues) != 0 {
		t.Fatalf("report.InstalledIssues = %v, want none", report.InstalledIssues)
	}
	found := false
	for _, detail := range report.PlanningIssueDetails {
		if detail.Kind == "dependency_conflict" && detail.Package == "cli" {
			if !reflect.DeepEqual(detail.DependencyPath, []string{"root", "demo", "cli"}) {
				t.Fatalf("detail.DependencyPath = %v", detail.DependencyPath)
			}
			if detail.Constraint != ">= 4.0.0" {
				t.Fatalf("detail.Constraint = %q", detail.Constraint)
			}
			if detail.Selected != "3.6.5" {
				t.Fatalf("detail.Selected = %q", detail.Selected)
			}
			if detail.RequiredBy != "demo" {
				t.Fatalf("detail.RequiredBy = %q", detail.RequiredBy)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("report.PlanningIssueDetails = %v, want dependency conflict detail", report.PlanningIssueDetails)
	}
}

func TestResolveDependencyPlanAutoSplitsKnownBioc(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	if err := os.WriteFile(scriptPath, []byte("DESeq2::DESeq()\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	plan, err := resolveDependencyPlan(scriptPath, nil, nil, nil, "", "", "")
	if err != nil {
		t.Fatalf("resolveDependencyPlan() error = %v", err)
	}

	if !reflect.DeepEqual(plan.DetectedDeps, []string{"DESeq2", "jsonlite"}) {
		t.Fatalf("plan.DetectedDeps = %v", plan.DetectedDeps)
	}
	if !reflect.DeepEqual(plan.CRANDeps, []string{"jsonlite"}) {
		t.Fatalf("plan.CRANDeps = %v", plan.CRANDeps)
	}
	if !reflect.DeepEqual(plan.BiocDeps, []string{"DESeq2"}) {
		t.Fatalf("plan.BiocDeps = %v", plan.BiocDeps)
	}
}

func TestResolveDependencyPlanIncludeAndExclude(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	if err := os.WriteFile(scriptPath, []byte("library(jsonlite)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	plan, err := resolveDependencyPlan(scriptPath, []string{"cli", "Biostrings"}, nil, []string{"jsonlite"}, "", "", "")
	if err != nil {
		t.Fatalf("resolveDependencyPlan() error = %v", err)
	}

	if !reflect.DeepEqual(plan.CRANDeps, []string{"cli"}) {
		t.Fatalf("plan.CRANDeps = %v", plan.CRANDeps)
	}
	if !reflect.DeepEqual(plan.BiocDeps, []string{"Biostrings"}) {
		t.Fatalf("plan.BiocDeps = %v", plan.BiocDeps)
	}
}

func TestListJSONOutputAutoSplitsKnownBioc(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "report.R")
	if err := os.WriteFile(scriptPath, []byte("DESeq2::DESeq()\njsonlite::fromJSON('{}')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	var stdout bytes.Buffer
	err := List(ListOptions{
		ScriptPath: scriptPath,
		JSON:       true,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	var report ListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if !reflect.DeepEqual(report.CRANDeps, []string{"jsonlite"}) {
		t.Fatalf("report.CRANDeps = %v", report.CRANDeps)
	}
	if !reflect.DeepEqual(report.BiocDeps, []string{"DESeq2"}) {
		t.Fatalf("report.BiocDeps = %v", report.BiocDeps)
	}
}

func TestBuildListReportEmptySlices(t *testing.T) {
	report := buildListReport(dependencyPlan{
		ScriptPath:   "/tmp/project/report.R",
		LockfilePath: "/tmp/project/rs.lock.json",
		LibraryPath:  "/tmp/project/.rs-cache/lib/abc",
		CacheRoot:    "/tmp/project/.rs-cache",
	}, ListOptions{})

	if report.DetectedDeps == nil || report.CRANDeps == nil || report.BiocDeps == nil || report.IncludedCRAN == nil || report.IncludedBioc == nil || report.ExcludedDeps == nil || report.CustomSources == nil {
		t.Fatalf("buildListReport() should return non-nil empty slices: %#v", report)
	}
}

func TestCollectProjectScriptPathsSkipsCacheDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("MkdirAll(scripts) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".rs-cache"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "a.R"), []byte("library(jsonlite)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.R) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".rs-cache", "ignored.R"), []byte("library(cli)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(ignored.R) error = %v", err)
	}

	paths, err := collectProjectScriptPaths(dir)
	if err != nil {
		t.Fatalf("collectProjectScriptPaths() error = %v", err)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "scripts/a.R") {
		t.Fatalf("collectProjectScriptPaths() = %v", paths)
	}
}

func TestPruneCacheRootDryRun(t *testing.T) {
	cacheRoot := t.TempDir()
	libRoot := filepath.Join(cacheRoot, "lib")
	if err := os.MkdirAll(libRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(libRoot) error = %v", err)
	}
	keep := filepath.Join(libRoot, "1111111111111111")
	old := filepath.Join(libRoot, "2222222222222222")
	misc := filepath.Join(libRoot, "not-managed")
	for _, path := range []string{keep, old, misc} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	summary, err := pruneCacheRoot(cacheRoot, map[string]struct{}{keep: {}}, true)
	if err != nil {
		t.Fatalf("pruneCacheRoot() error = %v", err)
	}
	if len(summary.Kept) != 1 || len(summary.Removed) != 1 || summary.Removed[0] != old {
		t.Fatalf("pruneCacheRoot() summary = %#v", summary)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("old library should still exist in dry-run: %v", err)
	}
	if _, err := os.Stat(misc); err != nil {
		t.Fatalf("misc dir should still exist: %v", err)
	}
}

func TestPruneProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("MkdirAll(scripts) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rs.toml"), []byte("cache_dir = \".rs-cache\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(rs.toml) error = %v", err)
	}
	scriptA := filepath.Join(dir, "scripts", "a.R")
	scriptB := filepath.Join(dir, "scripts", "b.R")
	if err := os.WriteFile(scriptA, []byte("library(jsonlite)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.R) error = %v", err)
	}
	if err := os.WriteFile(scriptB, []byte("library(cli)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.R) error = %v", err)
	}

	planA, err := resolveDependencyPlan(scriptA, nil, nil, nil, "", "", "")
	if err != nil {
		t.Fatalf("resolveDependencyPlan(a) error = %v", err)
	}
	planB, err := resolveDependencyPlan(scriptB, nil, nil, nil, "", "", "")
	if err != nil {
		t.Fatalf("resolveDependencyPlan(b) error = %v", err)
	}
	oldLib := filepath.Join(planA.CacheRoot, "lib", "ffffffffffffffff")
	for _, path := range []string{planA.LibraryPath, planB.LibraryPath, oldLib} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	var stdout bytes.Buffer
	err = Prune(PruneOptions{
		ProjectDir: dir,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if _, err := os.Stat(oldLib); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old lib should be removed, stat err = %v", err)
	}
	for _, keep := range []string{planA.LibraryPath, planB.LibraryPath} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("keep lib missing %s: %v", keep, err)
		}
	}
	if !strings.Contains(stdout.String(), "[remove]") {
		t.Fatalf("Prune() output = %q, want removal line", stdout.String())
	}
}

func TestCacheDirDefault(t *testing.T) {
	var stdout bytes.Buffer
	if err := CacheDir(CacheDirOptions{Stdout: &stdout}); err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got == "" {
		t.Fatalf("CacheDir() output is empty")
	}
	if !strings.Contains(got, "rs") {
		t.Fatalf("CacheDir() output = %q, want rs cache path", got)
	}
}

func TestCacheListProjectJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("MkdirAll(scripts) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rs.toml"), []byte("cache_dir = \".rs-cache\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(rs.toml) error = %v", err)
	}
	scriptPath := filepath.Join(dir, "scripts", "a.R")
	if err := os.WriteFile(scriptPath, []byte("library(jsonlite)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.R) error = %v", err)
	}

	plan, err := resolveDependencyPlan(scriptPath, nil, nil, nil, "", "", "")
	if err != nil {
		t.Fatalf("resolveDependencyPlan() error = %v", err)
	}
	activeLib := plan.LibraryPath
	staleLib := filepath.Join(plan.CacheRoot, "lib", "aaaaaaaaaaaaaaaa")
	for _, path := range []string{activeLib, staleLib} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	var stdout bytes.Buffer
	err = CacheList(CacheListOptions{
		ProjectDir: dir,
		JSON:       true,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("CacheList() error = %v", err)
	}

	var report CacheListReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput=%s", err, stdout.String())
	}
	if report.CacheRoot != plan.CacheRoot {
		t.Fatalf("report.CacheRoot = %q, want %q", report.CacheRoot, plan.CacheRoot)
	}
	if len(report.Libraries) != 2 {
		t.Fatalf("report.Libraries = %#v", report.Libraries)
	}

	activeFound := false
	staleFound := false
	for _, lib := range report.Libraries {
		if lib.Path == activeLib && lib.Active {
			activeFound = true
		}
		if lib.Path == staleLib && !lib.Active {
			staleFound = true
		}
	}
	if !activeFound || !staleFound {
		t.Fatalf("report.Libraries active/stale mismatch: %#v", report.Libraries)
	}
}

func TestCacheRemoveByHash(t *testing.T) {
	cacheRoot := t.TempDir()
	target := filepath.Join(cacheRoot, "lib", "aaaaaaaaaaaaaaaa")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll(target) error = %v", err)
	}

	var stdout bytes.Buffer
	err := CacheRemove(CacheRemoveOptions{
		Target:   "aaaaaaaaaaaaaaaa",
		CacheDir: cacheRoot,
		Stdout:   &stdout,
		Stderr:   &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("CacheRemove() error = %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target should be removed, stat err = %v", err)
	}
	if !strings.Contains(stdout.String(), "[remove]") {
		t.Fatalf("CacheRemove() output = %q, want removal line", stdout.String())
	}
}

func TestCacheRemoveByPathDryRun(t *testing.T) {
	cacheRoot := t.TempDir()
	target := filepath.Join(cacheRoot, "lib", "bbbbbbbbbbbbbbbb")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll(target) error = %v", err)
	}

	var stdout bytes.Buffer
	err := CacheRemove(CacheRemoveOptions{
		Target: target,
		DryRun: true,
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("CacheRemove() error = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target should still exist after dry-run: %v", err)
	}
	if !strings.Contains(stdout.String(), "[dry-run]") {
		t.Fatalf("CacheRemove() output = %q, want dry-run line", stdout.String())
	}
}

func TestCacheRemoveRejectsNonManagedPath(t *testing.T) {
	cacheRoot := t.TempDir()
	target := filepath.Join(cacheRoot, "tmp", "not-managed")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll(target) error = %v", err)
	}

	err := CacheRemove(CacheRemoveOptions{
		Target: target,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "managed library path") {
		t.Fatalf("CacheRemove() error = %v, want managed library path validation", err)
	}
}
