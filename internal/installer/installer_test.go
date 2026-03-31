package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gr/internal/project"
	"gr/internal/toolchainenv"
)

func setTestHomeDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func writeTestCommand(t *testing.T, dir, name, unixScript, windowsScript string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := unixScript
	if runtime.GOOS == "windows" {
		path += ".cmd"
		script = windowsScript
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func TestParseDependenciesDropsRAndVersionConstraints(t *testing.T) {
	got := parseDependencies(
		"R (>= 4.3), cli (>= 3.0), jsonlite",
		"glue,\n methods",
		"cli",
	)
	want := []packageRequirement{
		{Name: "cli", Constraints: []versionConstraint{{Operator: ">=", Version: "3.0"}}},
		{Name: "glue"},
		{Name: "jsonlite"},
		{Name: "methods"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDependencies() = %v, want %v", got, want)
	}
}

func TestParseDCFSupportsContinuationLines(t *testing.T) {
	data := []byte("Package: demo\nImports: cli,\n jsonlite,\n glue\nVersion: 1.0.0\n\n")
	records := parseDCF(data)
	if len(records) != 1 {
		t.Fatalf("parseDCF() len = %d, want 1", len(records))
	}
	if got := records[0]["Imports"]; got != "cli, jsonlite, glue" {
		t.Fatalf("Imports = %q", got)
	}
}

func TestFetchRepoIndexParsesPackagesGz(t *testing.T) {
	oldGOOS := installerGOOS
	t.Cleanup(func() {
		installerGOOS = oldGOOS
	})
	installerGOOS = "linux"

	packages := "Package: cli\nVersion: 3.6.5\nImports: glue\n\nPackage: jsonlite\nVersion: 1.8.9\nDepends: methods\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/src/contrib/PACKAGES.gz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(packages))
		_ = gz.Close()
		_, _ = w.Write(buf.Bytes())
	}))
	defer server.Close()

	index, err := fetchRepoIndex(server.Client(), server.URL, sourceCRAN, "4.4.3")
	if err != nil {
		t.Fatalf("fetchRepoIndex() error = %v", err)
	}

	cliCandidates := index["cli"]
	if len(cliCandidates) != 1 {
		t.Fatalf("len(cliCandidates) = %d, want 1", len(cliCandidates))
	}
	cli := cliCandidates[0]
	if cli.Version != "3.6.5" {
		t.Fatalf("cli.Version = %q", cli.Version)
	}
	if cli.TarballURL != server.URL+"/src/contrib/cli_3.6.5.tar.gz" {
		t.Fatalf("cli.TarballURL = %q", cli.TarballURL)
	}
	if !reflect.DeepEqual(cli.Dependencies, []packageRequirement{{Name: "glue"}}) {
		t.Fatalf("cli.Dependencies = %v", cli.Dependencies)
	}

	jsonliteCandidates := index["jsonlite"]
	if len(jsonliteCandidates) != 1 {
		t.Fatalf("len(jsonliteCandidates) = %d, want 1", len(jsonliteCandidates))
	}
	jsonlite := jsonliteCandidates[0]
	if !reflect.DeepEqual(jsonlite.Dependencies, []packageRequirement{{Name: "methods"}}) {
		t.Fatalf("jsonlite.Dependencies = %v", jsonlite.Dependencies)
	}
}

func TestFetchRepoIndexUsesWindowsBinaryRepositories(t *testing.T) {
	oldGOOS := installerGOOS
	t.Cleanup(func() {
		installerGOOS = oldGOOS
	})
	installerGOOS = "windows"

	packages := "Package: cli\nVersion: 3.6.5\nNeedsCompilation: yes\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bin/windows/contrib/4.4/PACKAGES.gz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(packages))
		_ = gz.Close()
		_, _ = w.Write(buf.Bytes())
	}))
	defer server.Close()

	index, err := fetchRepoIndex(server.Client(), server.URL, sourceCRAN, "4.4.3")
	if err != nil {
		t.Fatalf("fetchRepoIndex() error = %v", err)
	}
	candidates := index["cli"]
	if len(candidates) != 1 {
		t.Fatalf("len(cli candidates) = %d, want 1", len(candidates))
	}
	if candidates[0].TarballURL != server.URL+"/bin/windows/contrib/4.4/cli_3.6.5.zip" {
		t.Fatalf("TarballURL = %q", candidates[0].TarballURL)
	}
	if !candidates[0].NeedsCompilation {
		t.Fatalf("NeedsCompilation = false, want true")
	}
}

func TestReadDescriptionFromTarball(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "pkg_0.1.0.tar.gz")
	if err := writeTestTarball(tarball, map[string]string{
		"pkg/DESCRIPTION": "Package: pkg\nVersion: 0.1.0\nImports: cli, jsonlite\n",
	}); err != nil {
		t.Fatalf("writeTestTarball() error = %v", err)
	}

	desc, err := readDescriptionFromPath(tarball)
	if err != nil {
		t.Fatalf("readDescriptionFromPath() error = %v", err)
	}
	if desc.Package != "pkg" || desc.Version != "0.1.0" {
		t.Fatalf("description = %#v", desc)
	}
	if !reflect.DeepEqual(desc.Dependencies, []packageRequirement{{Name: "cli"}, {Name: "jsonlite"}}) {
		t.Fatalf("desc.Dependencies = %v", desc.Dependencies)
	}
}

func TestVersionSatisfies(t *testing.T) {
	cases := []struct {
		version    string
		constraint versionConstraint
		want       bool
	}{
		{version: "1.2.0", constraint: versionConstraint{Operator: ">=", Version: "1.1.9"}, want: true},
		{version: "1.2.0", constraint: versionConstraint{Operator: ">", Version: "1.2.0"}, want: false},
		{version: "1.2.0", constraint: versionConstraint{Operator: "<", Version: "2.0.0"}, want: true},
		{version: "1.2.0", constraint: versionConstraint{Operator: "=", Version: "1.2.0"}, want: true},
		{version: "1.2.0", constraint: versionConstraint{Operator: "<=", Version: "1.1.9"}, want: false},
	}
	for _, tc := range cases {
		if got := versionSatisfies(tc.version, tc.constraint); got != tc.want {
			t.Fatalf("versionSatisfies(%q, %#v) = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}

func TestValidatePackageRequirementsDetectsConflict(t *testing.T) {
	inst := nativeInstaller{
		requirements: map[string][]constraintRequest{
			"cli": {
				{
					RequiredBy:  "demo",
					Constraints: []versionConstraint{{Operator: ">=", Version: "4.0.0"}},
					Chain:       []string{"root", "demo"},
				},
			},
		},
	}
	err := inst.validatePackageRequirements(plannedPackage{
		Name:    "cli",
		Version: "3.6.5",
	})
	if err == nil {
		t.Fatalf("validatePackageRequirements() error = nil, want conflict")
	}
	if got := err.Error(); got != "dependency constraint conflict for cli: selected version 3.6.5 does not satisfy >= 4.0.0 required by demo (dependency path: root -> demo -> cli)" {
		t.Fatalf("validatePackageRequirements() error = %q", got)
	}
}

func TestGitHubCloneURLSupportsEnterpriseHost(t *testing.T) {
	cloneURL, host, err := githubCloneURL(project.SourceSpec{
		Package: "mypkg",
		Repo:    "owner/mypkg",
		Host:    "github.example.com/api/v3",
	})
	if err != nil {
		t.Fatalf("githubCloneURL() error = %v", err)
	}
	if cloneURL != "https://github.example.com/owner/mypkg.git" {
		t.Fatalf("cloneURL = %q", cloneURL)
	}
	if host != "github.example.com/api/v3" {
		t.Fatalf("host = %q", host)
	}
}

func TestBuildInstallCommandWrapsEnvaToolchainRun(t *testing.T) {
	dir := t.TempDir()
	setTestHomeDir(t, dir)
	binDir := filepath.Join(dir, "bin")
	envaName := "enva"
	if installerGOOS == "windows" {
		envaName += ".exe"
	}
	envaPath := filepath.Join(binDir, envaName)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	if err := os.WriteFile(envaPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(enva) error = %v", err)
	}

	prefix := filepath.Join(dir, ".local", "share", "rattler", "envs", "rs-sysdeps")
	env := toolchainenv.Apply([]string{"PATH=" + binDir}, []string{prefix}, []string{
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	})

	cmd, err := buildInstallCommand("/usr/bin/R", dir, filepath.Join(dir, "cache"), filepath.Join(dir, "lib"), env, filepath.Join(dir, "pkg.tar.gz"))
	if err != nil {
		t.Fatalf("buildInstallCommand() error = %v", err)
	}
	if cmd.Path != envaPath {
		t.Fatalf("cmd.Path = %q, want %q", cmd.Path, envaPath)
	}
	want := []string{"run", "rs-sysdeps", "--", "/usr/bin/R", "CMD", "INSTALL", "-l", filepath.Join(dir, "lib"), filepath.Join(dir, "pkg.tar.gz")}
	if !reflect.DeepEqual(cmd.Args[1:], want) {
		t.Fatalf("cmd.Args = %v, want %v", cmd.Args[1:], want)
	}
	if !slices.Contains(cmd.Env, "MAKEFLAGS=-j"+strconv.Itoa(defaultInstallJobs())) {
		t.Fatalf("cmd.Env missing default MAKEFLAGS: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "CMAKE_BUILD_PARALLEL_LEVEL="+strconv.Itoa(defaultInstallJobs())) {
		t.Fatalf("cmd.Env missing default CMAKE_BUILD_PARALLEL_LEVEL: %v", cmd.Env)
	}
}

func TestBuildInstallCommandPreservesExplicitParallelBuildEnv(t *testing.T) {
	dir := t.TempDir()
	cmd, err := buildInstallCommand("/usr/bin/R", dir, filepath.Join(dir, "cache"), filepath.Join(dir, "lib"), []string{
		"PATH=/usr/bin",
		"MAKEFLAGS=-j32",
		"CMAKE_BUILD_PARALLEL_LEVEL=32",
	}, filepath.Join(dir, "pkg.tar.gz"))
	if err != nil {
		t.Fatalf("buildInstallCommand() error = %v", err)
	}
	if !slices.Contains(cmd.Env, "MAKEFLAGS=-j32") || !slices.Contains(cmd.Env, "CMAKE_BUILD_PARALLEL_LEVEL=32") {
		t.Fatalf("cmd.Env = %v", cmd.Env)
	}
	if slices.Contains(cmd.Env, "MAKEFLAGS=-j"+strconv.Itoa(defaultInstallJobs())) {
		t.Fatalf("cmd.Env should preserve explicit MAKEFLAGS: %v", cmd.Env)
	}
}

func TestBuildInstallCommandAutoEnablesCCache(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	for _, name := range []string{"ccache", "gcc", "g++"} {
		fileName := name
		if runtime.GOOS == "windows" {
			fileName += ".exe"
		}
		if err := os.WriteFile(filepath.Join(binDir, fileName), []byte("binary"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", fileName, err)
		}
	}

	cacheRoot := filepath.Join(dir, "cache")
	cmd, err := buildInstallCommand("/usr/bin/R", dir, cacheRoot, filepath.Join(dir, "lib"), []string{
		"PATH=" + binDir,
	}, filepath.Join(dir, "pkg.tar.gz"))
	if err != nil {
		t.Fatalf("buildInstallCommand() error = %v", err)
	}
	if !slices.Contains(cmd.Env, "CC=ccache gcc") {
		t.Fatalf("cmd.Env missing CC=ccache gcc: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "CXX=ccache g++") {
		t.Fatalf("cmd.Env missing CXX=ccache g++: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "CCACHE_DIR="+filepath.Join(cacheRoot, "ccache")) {
		t.Fatalf("cmd.Env missing CCACHE_DIR: %v", cmd.Env)
	}
}

func TestBuildInstallCommandPreservesExplicitCompilerOverrides(t *testing.T) {
	dir := t.TempDir()
	cmd, err := buildInstallCommand("/usr/bin/R", dir, filepath.Join(dir, "cache"), filepath.Join(dir, "lib"), []string{
		"PATH=/usr/bin",
		"CC=clang",
		"CXX=clang++",
	}, filepath.Join(dir, "pkg.tar.gz"))
	if err != nil {
		t.Fatalf("buildInstallCommand() error = %v", err)
	}
	if !slices.Contains(cmd.Env, "CC=clang") || !slices.Contains(cmd.Env, "CXX=clang++") {
		t.Fatalf("cmd.Env = %v", cmd.Env)
	}
	if slices.Contains(cmd.Env, "CC=ccache gcc") || slices.Contains(cmd.Env, "CXX=ccache g++") {
		t.Fatalf("cmd.Env should preserve explicit compiler overrides: %v", cmd.Env)
	}
}

func TestDownloadReusesPersistentCache(t *testing.T) {
	dir := t.TempDir()
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("archive-bytes"))
	}))
	defer server.Close()

	inst := nativeInstaller{
		tempRoot:     filepath.Join(dir, "tmp"),
		downloadRoot: filepath.Join(dir, "downloads"),
		stderr:       io.Discard,
		httpClient:   server.Client(),
	}
	for _, path := range []string{inst.tempRoot, inst.downloadRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	first, err := inst.download(server.URL+"/pkg.tar.gz", "pkg_1.0.0.tar.gz")
	if err != nil {
		t.Fatalf("download(first) error = %v", err)
	}
	second, err := inst.download(server.URL+"/pkg.tar.gz", "pkg_1.0.0.tar.gz")
	if err != nil {
		t.Fatalf("download(second) error = %v", err)
	}
	if first != second {
		t.Fatalf("download paths differ: %q vs %q", first, second)
	}
	if hits != 1 {
		t.Fatalf("download server hits = %d, want 1", hits)
	}
}

func TestDescribeLocalFingerprintForFileAndMissing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "localpkg.tar.gz")
	if err := os.WriteFile(target, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	kind, fingerprint := describeLocalFingerprint(target)
	if kind != localKindFileSHA256 || fingerprint == "" {
		t.Fatalf("describeLocalFingerprint(file) = (%q, %q)", kind, fingerprint)
	}

	missingKind, missingFingerprint := describeLocalFingerprint(filepath.Join(dir, "missing.tar.gz"))
	if missingKind != localKindMissing || missingFingerprint != "" {
		t.Fatalf("describeLocalFingerprint(missing) = (%q, %q)", missingKind, missingFingerprint)
	}
}

func TestBiocVersionForR(t *testing.T) {
	cases := map[string]string{
		"4.5.1": "3.21",
		"4.4.3": "3.20",
		"4.3.2": "3.18",
		"4.2.3": "3.16",
		"4.1.9": "",
	}
	for input, want := range cases {
		if got := biocVersionForR(input); got != want {
			t.Fatalf("biocVersionForR(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBiocRepositoryURLsForR(t *testing.T) {
	if got := biocMainRepositoryURL("4.5.1"); got != "https://bioconductor.org/packages/3.21/bioc" {
		t.Fatalf("biocMainRepositoryURL() = %q", got)
	}
	if got := biocAnnotationRepositoryURL("4.5.1"); got != "https://bioconductor.org/packages/3.21/data/annotation" {
		t.Fatalf("biocAnnotationRepositoryURL() = %q", got)
	}
	if got := biocExperimentRepositoryURL("4.5.1"); got != "https://bioconductor.org/packages/3.21/data/experiment" {
		t.Fatalf("biocExperimentRepositoryURL() = %q", got)
	}
}

func TestRepositoryContribURLUsesWindowsBinaryPath(t *testing.T) {
	oldGOOS := installerGOOS
	t.Cleanup(func() {
		installerGOOS = oldGOOS
	})
	installerGOOS = "windows"

	gotURL, gotExt := repositoryContribURL("https://cloud.r-project.org", sourceCRAN, "4.4.3")
	if gotURL != "https://cloud.r-project.org/bin/windows/contrib/4.4" || gotExt != ".zip" {
		t.Fatalf("repositoryContribURL() = (%q, %q)", gotURL, gotExt)
	}
}

func TestInstallPreparedSourceWindowsNeedsCompilationFailsWithoutRtools(t *testing.T) {
	oldGOOS := installerGOOS
	oldLookPath := installerLookPath
	t.Cleanup(func() {
		installerGOOS = oldGOOS
		installerLookPath = oldLookPath
	})
	installerGOOS = "windows"
	installerLookPath = func(file string) (string, error) {
		return "", errors.New("missing " + file)
	}

	inst := nativeInstaller{
		stdout: bytes.NewBuffer(nil),
		stderr: bytes.NewBuffer(nil),
	}
	err := inst.installPreparedSource(preparedSource{
		Name:             "mypkg",
		Source:           sourceLocal,
		Location:         `C:\src\mypkg`,
		InstallPath:      `C:\src\mypkg`,
		NeedsCompilation: true,
	})
	if err == nil || !strings.Contains(err.Error(), "Rtools was not detected") {
		t.Fatalf("installPreparedSource() error = %v", err)
	}
}

func TestInstallRepoPackageWindowsSourceNeedsCompilationFailsWithoutRtools(t *testing.T) {
	oldGOOS := installerGOOS
	oldLookPath := installerLookPath
	t.Cleanup(func() {
		installerGOOS = oldGOOS
		installerLookPath = oldLookPath
	})
	installerGOOS = "windows"
	installerLookPath = func(file string) (string, error) {
		return "", errors.New("missing " + file)
	}

	inst := nativeInstaller{
		stdout: bytes.NewBuffer(nil),
		stderr: bytes.NewBuffer(nil),
	}
	err := inst.installRepoPackage(repoRecord{
		Name:             "cli",
		Version:          "3.6.5",
		Source:           sourceCRAN,
		TarballURL:       "https://example.com/src/contrib/cli_3.6.5.tar.gz",
		DepsLoaded:       true,
		NeedsCompilation: true,
	})
	if err == nil || !strings.Contains(err.Error(), "Rtools was not detected") {
		t.Fatalf("installRepoPackage() error = %v", err)
	}
}

func TestEnsureLinuxSourceBuildToolsUsesDistroAdvice(t *testing.T) {
	oldGOOS := installerGOOS
	oldLookPath := installerLookPath
	oldReadFile := installerReadFile
	t.Cleanup(func() {
		installerGOOS = oldGOOS
		installerLookPath = oldLookPath
		installerReadFile = oldReadFile
	})
	installerGOOS = "linux"
	installerLookPath = func(file string) (string, error) {
		return "", errors.New("missing " + file)
	}
	installerReadFile = func(path string) ([]byte, error) {
		if path != "/etc/os-release" {
			return nil, errors.New("unexpected path")
		}
		return []byte("ID=ubuntu\n"), nil
	}

	err := ensureLinuxSourceBuildTools("stringi", nil)
	if err == nil {
		t.Fatal("ensureLinuxSourceBuildTools() error = nil, want missing compiler guidance")
	}
	if !strings.Contains(err.Error(), "build-essential gfortran") {
		t.Fatalf("ensureLinuxSourceBuildTools() error = %v", err)
	}
	if !strings.Contains(err.Error(), "rs toolchain detect") {
		t.Fatalf("ensureLinuxSourceBuildTools() error missing rootless guidance = %v", err)
	}
	if !strings.Contains(err.Error(), "rs doctor --toolchain-only") {
		t.Fatalf("ensureLinuxSourceBuildTools() error missing toolchain-only guidance = %v", err)
	}
}

func TestEnsureLinuxSourceBuildToolsFailsWhenCompilerCannotLink(t *testing.T) {
	oldGOOS := installerGOOS
	oldReadFile := installerReadFile
	t.Cleanup(func() {
		installerGOOS = oldGOOS
		installerReadFile = oldReadFile
	})
	installerGOOS = "linux"
	installerReadFile = func(path string) ([]byte, error) {
		if path != "/etc/os-release" {
			return nil, errors.New("unexpected path")
		}
		return []byte("ID=ubuntu\n"), nil
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	successUnix := "#!/bin/sh\nexit 0\n"
	successWindows := "@echo off\r\nexit /b 0\r\n"
	failUnix := "#!/bin/sh\necho linker missing 1>&2\nexit 1\n"
	failWindows := "@echo off\r\necho linker missing 1>&2\r\nexit /b 1\r\n"
	writeTestCommand(t, binDir, "gcc", successUnix, successWindows)
	writeTestCommand(t, binDir, "gfortran", successUnix, successWindows)
	writeTestCommand(t, binDir, "make", successUnix, successWindows)
	writeTestCommand(t, binDir, "g++", failUnix, failWindows)

	err := ensureLinuxSourceBuildTools("stringi", []string{"PATH=" + binDir})
	if err == nil {
		t.Fatal("ensureLinuxSourceBuildTools() error = nil, want compiler smoke failure")
	}
	if !strings.Contains(err.Error(), "could not compile a test program") {
		t.Fatalf("ensureLinuxSourceBuildTools() error = %v", err)
	}
	if !strings.Contains(err.Error(), "linker missing") {
		t.Fatalf("ensureLinuxSourceBuildTools() error missing compiler stderr = %v", err)
	}
}

func TestWithLibraryEnvPreservesToolchainEnvironment(t *testing.T) {
	prefix := filepath.Join(string(filepath.Separator), "opt", "demo")
	pkgConfig := filepath.Join(prefix, "lib", "pkgconfig")
	pathValue := strings.Join([]string{filepath.Join(prefix, "bin"), filepath.Join(string(filepath.Separator), "usr", "bin")}, string(os.PathListSeparator))
	libPath := filepath.Join(string(filepath.Separator), "tmp", "lib")
	env := withLibraryEnv([]string{
		"PATH=" + pathValue,
		"RS_TOOLCHAIN_PREFIXES=" + prefix,
		"RS_PKG_CONFIG_PATH=" + pkgConfig,
	}, libPath)
	if !slices.Contains(env, "R_LIBS="+libPath) || !slices.Contains(env, "R_LIBS_USER="+libPath) {
		t.Fatalf("withLibraryEnv() = %v", env)
	}
	if !slices.Contains(env, "RS_TOOLCHAIN_PREFIXES="+prefix) || !slices.Contains(env, "RS_PKG_CONFIG_PATH="+pkgConfig) {
		t.Fatalf("withLibraryEnv() lost toolchain env: %v", env)
	}
}

func TestValidateBuildPrerequisitesFailsEarlyOnLinuxCompilationPackage(t *testing.T) {
	oldGOOS := installerGOOS
	oldLookPath := installerLookPath
	oldReadFile := installerReadFile
	t.Cleanup(func() {
		installerGOOS = oldGOOS
		installerLookPath = oldLookPath
		installerReadFile = oldReadFile
	})
	installerGOOS = "linux"
	installerLookPath = func(file string) (string, error) {
		return "", errors.New("missing " + file)
	}
	installerReadFile = func(path string) ([]byte, error) {
		return []byte("ID=ubuntu\n"), nil
	}

	inst := nativeInstaller{}
	err := inst.validateBuildPrerequisites(plannedPackage{
		Name: "stringi",
		Repo: &repoRecord{
			Name:             "stringi",
			Version:          "1.8.7",
			NeedsCompilation: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "package stringi requires Linux source build tools") {
		t.Fatalf("validateBuildPrerequisites() error = %v", err)
	}
}

func TestRootlessToolchainAdviceIncludesDetectedPresetRecommendation(t *testing.T) {
	dir := t.TempDir()
	setTestHomeDir(t, dir)
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

	advice := rootlessToolchainAdvice()
	if !strings.Contains(advice, "detected recommended preset on this machine: homebrew") {
		t.Fatalf("rootlessToolchainAdvice() = %q", advice)
	}
	if !strings.Contains(advice, filepath.Join(homebrewPrefix, "bin", "brew")) {
		t.Fatalf("rootlessToolchainAdvice() = %q", advice)
	}
	if !strings.Contains(advice, "rs init --toolchain-preset homebrew") {
		t.Fatalf("rootlessToolchainAdvice() = %q", advice)
	}
}

func TestAppendRepoCandidateSortsDescending(t *testing.T) {
	index := map[string][]repoRecord{}
	appendRepoCandidate(index, repoRecord{Name: "cli", Version: "3.6.5"})
	appendRepoCandidate(index, repoRecord{Name: "cli", Version: "3.6.3"})
	appendRepoCandidate(index, repoRecord{Name: "cli", Version: "3.7.0"})
	got := []string{}
	for _, record := range index["cli"] {
		got = append(got, record.Version)
	}
	want := []string{"3.7.0", "3.6.5", "3.6.3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
}

func TestSelectRepoRecordChoosesSatisfyingCandidate(t *testing.T) {
	inst := nativeInstaller{
		requirements: map[string][]constraintRequest{
			"cli": {
				{RequiredBy: "demo", Constraints: []versionConstraint{{Operator: "<", Version: "3.7.0"}}},
			},
		},
	}
	record, ok := inst.selectRepoRecord("cli", []repoRecord{
		{Name: "cli", Version: "3.7.0", DepsLoaded: true},
		{Name: "cli", Version: "3.6.5", DepsLoaded: true},
	})
	if !ok {
		t.Fatalf("selectRepoRecord() ok = false")
	}
	if record.Version != "3.6.5" {
		t.Fatalf("record.Version = %q", record.Version)
	}
}

func TestPlannedPackageMatchesInstalledRepoVersion(t *testing.T) {
	pkg := plannedPackage{
		Name:    "cli",
		Version: "3.6.5",
		Source:  sourceCRAN,
	}
	if !plannedPackageMatchesInstalled(pkg, installedPackage{
		Name:    "cli",
		Version: "3.6.5",
		Source:  sourceCRAN,
	}) {
		t.Fatalf("plannedPackageMatchesInstalled() = false, want true")
	}
	if plannedPackageMatchesInstalled(pkg, installedPackage{
		Name:    "cli",
		Version: "3.6.4",
		Source:  sourceCRAN,
	}) {
		t.Fatalf("plannedPackageMatchesInstalled() = true for version mismatch")
	}
}

func TestPlannedPackageMatchesInstalledSourceMetadata(t *testing.T) {
	prepared := preparedSource{
		Name:            "demo",
		Version:         "1.0.0",
		Source:          sourceLocal,
		Location:        "/tmp/demo.tar.gz",
		Fingerprint:     "abc123",
		FingerprintKind: localKindFileSHA256,
	}
	pkg := plannedPackage{
		Name:     "demo",
		Version:  "1.0.0",
		Source:   sourceLocal,
		Prepared: &prepared,
	}
	if !plannedPackageMatchesInstalled(pkg, installedPackage{
		Name:            "demo",
		Version:         "1.0.0",
		Source:          sourceLocal,
		Location:        "/tmp/demo.tar.gz",
		Fingerprint:     "abc123",
		FingerprintKind: localKindFileSHA256,
	}) {
		t.Fatalf("plannedPackageMatchesInstalled() = false, want true")
	}
	if plannedPackageMatchesInstalled(pkg, installedPackage{
		Name:            "demo",
		Version:         "1.0.0",
		Source:          sourceLocal,
		Location:        "/tmp/demo.tar.gz",
		Fingerprint:     "def456",
		FingerprintKind: localKindFileSHA256,
	}) {
		t.Fatalf("plannedPackageMatchesInstalled() = true for fingerprint mismatch")
	}
}

func TestLoadInstalledPackagesReadsVersionAndSourceMetadata(t *testing.T) {
	library := t.TempDir()
	metaDir := filepath.Join(library, ".rs-source-meta")
	if err := os.MkdirAll(filepath.Join(library, "demo"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(meta) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(library, "demo", "DESCRIPTION"), []byte("Package: demo\nVersion: 1.0.0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	if err := writeSourceMetadata(metaDir, "demo", preparedSource{
		Name:            "demo",
		Version:         "1.0.0",
		Source:          sourceLocal,
		Location:        "/tmp/demo.tar.gz",
		Fingerprint:     "abc123",
		FingerprintKind: localKindFileSHA256,
	}); err != nil {
		t.Fatalf("writeSourceMetadata() error = %v", err)
	}

	inst := nativeInstaller{
		req: Request{
			LibraryPath: library,
		},
		metaDir: metaDir,
	}
	if err := inst.loadInstalledPackages(); err != nil {
		t.Fatalf("loadInstalledPackages() error = %v", err)
	}
	pkg, ok := inst.installedPackages["demo"]
	if !ok {
		t.Fatalf("installedPackages[demo] missing")
	}
	if pkg.Version != "1.0.0" {
		t.Fatalf("pkg.Version = %q, want 1.0.0", pkg.Version)
	}
	if pkg.Source != sourceLocal {
		t.Fatalf("pkg.Source = %q, want %q", pkg.Source, sourceLocal)
	}
	if pkg.Fingerprint != "abc123" {
		t.Fatalf("pkg.Fingerprint = %q, want abc123", pkg.Fingerprint)
	}
}

func TestRemoveSourceMetadataIgnoresMissingFile(t *testing.T) {
	metaDir := t.TempDir()
	if err := removeSourceMetadata(metaDir, "missing"); err != nil {
		t.Fatalf("removeSourceMetadata() error = %v", err)
	}
}

func TestRemoveSourceMetadataDeletesExistingFile(t *testing.T) {
	metaDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(metaDir, "demo.tsv"), []byte("local\t\t/tmp/demo.tar.gz\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := removeSourceMetadata(metaDir, "demo"); err != nil {
		t.Fatalf("removeSourceMetadata() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(metaDir, "demo.tsv")); !os.IsNotExist(err) {
		t.Fatalf("demo.tsv still exists, err = %v", err)
	}
}

func TestParseArchiveVersions(t *testing.T) {
	body := `<a href="cli_3.6.3.tar.gz">cli_3.6.3.tar.gz</a><a href="cli_3.7.0.tar.gz">cli_3.7.0.tar.gz</a>`
	got := parseArchiveVersions("cli", body)
	want := []string{"3.7.0", "3.6.3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseArchiveVersions() = %v, want %v", got, want)
	}
}

func TestPlanBacktracksAcrossRootPackages(t *testing.T) {
	inst := nativeInstaller{
		req: Request{
			CRANDeps: []string{"pkgA", "pkgB"},
		},
		planned:   map[string]plannedPackage{},
		resolving: map[string]bool{},
		resolved:  map[string]bool{},
		cranIndex: map[string][]repoRecord{
			"pkgA": {
				{
					Name:       "pkgA",
					Version:    "2.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "cli", Constraints: []versionConstraint{{Operator: ">=", Version: "4.0.0"}}},
					},
				},
				{
					Name:       "pkgA",
					Version:    "1.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "cli", Constraints: []versionConstraint{{Operator: "<", Version: "4.0.0"}}},
					},
				},
			},
			"pkgB": {
				{
					Name:       "pkgB",
					Version:    "1.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "cli", Constraints: []versionConstraint{{Operator: "<", Version: "4.0.0"}}},
					},
				},
			},
			"cli": {
				{Name: "cli", Version: "4.1.0", Source: sourceCRAN, DepsLoaded: true},
				{Name: "cli", Version: "3.6.5", Source: sourceCRAN, DepsLoaded: true},
			},
		},
		requirements:     map[string][]constraintRequest{},
		selectedVersions: map[string]string{},
	}

	if err := inst.plan(); err != nil {
		t.Fatalf("plan() error = %v", err)
	}
	if got := inst.planned["pkgA"].Version; got != "1.0.0" {
		t.Fatalf("pkgA version = %q, want 1.0.0", got)
	}
	if got := inst.planned["cli"].Version; got != "3.6.5" {
		t.Fatalf("cli version = %q, want 3.6.5", got)
	}
	if got := inst.planned["pkgB"].Version; got != "1.0.0" {
		t.Fatalf("pkgB version = %q, want 1.0.0", got)
	}
}

func TestPlanBacktracksAcrossSiblingDependenciesInSubtree(t *testing.T) {
	inst := nativeInstaller{
		req: Request{
			CRANDeps: []string{"root"},
		},
		planned:   map[string]plannedPackage{},
		resolving: map[string]bool{},
		resolved:  map[string]bool{},
		cranIndex: map[string][]repoRecord{
			"root": {
				{
					Name:       "root",
					Version:    "1.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "pkgA"},
						{Name: "pkgB"},
					},
				},
			},
			"pkgA": {
				{
					Name:       "pkgA",
					Version:    "1.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "cli", Constraints: []versionConstraint{{Operator: ">=", Version: "3.0.0"}}},
					},
				},
			},
			"pkgB": {
				{
					Name:       "pkgB",
					Version:    "1.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "cli", Constraints: []versionConstraint{{Operator: "<", Version: "4.0.0"}}},
					},
				},
			},
			"cli": {
				{Name: "cli", Version: "4.1.0", Source: sourceCRAN, DepsLoaded: true},
				{Name: "cli", Version: "3.6.5", Source: sourceCRAN, DepsLoaded: true},
			},
		},
		requirements:     map[string][]constraintRequest{},
		selectedVersions: map[string]string{},
	}

	if err := inst.plan(); err != nil {
		t.Fatalf("plan() error = %v", err)
	}
	if got := inst.planned["root"].Version; got != "1.0.0" {
		t.Fatalf("root version = %q, want 1.0.0", got)
	}
	if got := inst.planned["pkgA"].Version; got != "1.0.0" {
		t.Fatalf("pkgA version = %q, want 1.0.0", got)
	}
	if got := inst.planned["pkgB"].Version; got != "1.0.0" {
		t.Fatalf("pkgB version = %q, want 1.0.0", got)
	}
	if got := inst.planned["cli"].Version; got != "3.6.5" {
		t.Fatalf("cli version = %q, want 3.6.5", got)
	}
}

func TestPlanReturnsConstraintConflictWhenSiblingConstraintsExhaustCandidates(t *testing.T) {
	inst := nativeInstaller{
		req: Request{
			CRANDeps: []string{"root"},
		},
		planned:   map[string]plannedPackage{},
		resolving: map[string]bool{},
		resolved:  map[string]bool{},
		cranIndex: map[string][]repoRecord{
			"root": {
				{
					Name:       "root",
					Version:    "1.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "pkgA"},
						{Name: "pkgB"},
					},
				},
			},
			"pkgA": {
				{
					Name:       "pkgA",
					Version:    "1.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "cli", Constraints: []versionConstraint{{Operator: ">=", Version: "3.0.0"}}},
					},
				},
			},
			"pkgB": {
				{
					Name:       "pkgB",
					Version:    "1.0.0",
					Source:     sourceCRAN,
					DepsLoaded: true,
					Dependencies: []packageRequirement{
						{Name: "cli", Constraints: []versionConstraint{{Operator: "<", Version: "3.0.0"}}},
					},
				},
			},
			"cli": {
				{Name: "cli", Version: "4.1.0", Source: sourceCRAN, DepsLoaded: true},
				{Name: "cli", Version: "3.6.5", Source: sourceCRAN, DepsLoaded: true},
			},
		},
		requirements:     map[string][]constraintRequest{},
		selectedVersions: map[string]string{},
	}

	err := inst.plan()
	if err == nil {
		t.Fatalf("plan() error = nil, want conflict")
	}
	var conflict ConstraintConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("plan() error = %T %v, want ConstraintConflictError", err, err)
	}
	if conflict.Package != "cli" {
		t.Fatalf("conflict.Package = %q, want cli", conflict.Package)
	}
	if conflict.Version != "3.6.5" {
		t.Fatalf("conflict.Version = %q, want 3.6.5", conflict.Version)
	}
	if conflict.RequiredBy != "pkgB" {
		t.Fatalf("conflict.RequiredBy = %q, want pkgB", conflict.RequiredBy)
	}
}

func writeTestTarball(target string, files map[string]string) error {
	file, err := os.Create(target)
	if err != nil {
		return err
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for name, content := range files {
		header := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if _, err := fmt.Fprint(tw, content); err != nil {
			return err
		}
	}
	return nil
}
