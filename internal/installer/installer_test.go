package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainoffallingstar/rs-reborn/internal/brand"
	"github.com/rainoffallingstar/rs-reborn/internal/eventstream"
	"github.com/rainoffallingstar/rs-reborn/internal/progresscmd"
	"github.com/rainoffallingstar/rs-reborn/internal/project"
	"github.com/rainoffallingstar/rs-reborn/internal/toolchainenv"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type timeoutReadCloser struct {
	read func(p []byte) (int, error)
}

func (r timeoutReadCloser) Read(p []byte) (int, error) {
	return r.read(p)
}

func (r timeoutReadCloser) Close() error { return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

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

func testEnvironmentWithPath(path string) []string {
	if runtime.GOOS != "windows" {
		return []string{"PATH=" + path}
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), "PATH=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "PATH="+path)
	return env
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

func TestInstallerFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{d: 8 * time.Second, want: "8s"},
		{d: 72 * time.Second, want: "1m12s"},
		{d: 2*time.Hour + 5*time.Minute, want: "2h05m"},
	}
	for _, tc := range cases {
		if got := installerFormatElapsed(tc.d); got != tc.want {
			t.Fatalf("installerFormatElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
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

func TestReadDescriptionFromPathReusesSidecarCache(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "pkg_0.1.0.tar.gz")
	if err := writeTestTarball(tarball, map[string]string{
		"pkg/DESCRIPTION": "Package: pkg\nVersion: 0.1.0\nImports: cli\n",
	}); err != nil {
		t.Fatalf("writeTestTarball() error = %v", err)
	}

	desc, err := readDescriptionFromPath(tarball)
	if err != nil {
		t.Fatalf("readDescriptionFromPath(first) error = %v", err)
	}
	if desc.Package != "pkg" || desc.Version != "0.1.0" {
		t.Fatalf("description = %#v", desc)
	}
	sidecar := descriptionSidecarPath(tarball)
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("Stat(%q) error = %v", sidecar, err)
	}

	original := installerReadDescriptionFile
	t.Cleanup(func() {
		installerReadDescriptionFile = original
	})
	installerReadDescriptionFile = func(string) ([]byte, error) {
		t.Fatalf("installerReadDescriptionFile should not be called when sidecar cache is warm")
		return nil, nil
	}

	desc, err = readDescriptionFromPath(tarball)
	if err != nil {
		t.Fatalf("readDescriptionFromPath(second) error = %v", err)
	}
	if desc.Package != "pkg" || desc.Version != "0.1.0" {
		t.Fatalf("cached description = %#v", desc)
	}
}

func TestReadDescriptionFromPathIgnoresStaleSidecarCache(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "pkg_0.1.0.tar.gz")
	if err := writeTestTarball(tarball, map[string]string{
		"pkg/DESCRIPTION": "Package: pkg\nVersion: 0.1.0\nImports: cli\n",
	}); err != nil {
		t.Fatalf("writeTestTarball() error = %v", err)
	}

	sidecar := descriptionSidecarPath(tarball)
	if err := os.WriteFile(sidecar, []byte(`{"Package":"stale","Version":"9.9.9"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", sidecar, err)
	}
	staleTime := time.Now().Add(-time.Minute)
	if err := os.Chtimes(sidecar, staleTime, staleTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", sidecar, err)
	}
	freshTime := time.Now()
	if err := os.Chtimes(tarball, freshTime, freshTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", tarball, err)
	}

	called := false
	original := installerReadDescriptionFile
	t.Cleanup(func() {
		installerReadDescriptionFile = original
	})
	installerReadDescriptionFile = func(target string) ([]byte, error) {
		called = true
		return original(target)
	}

	desc, err := readDescriptionFromPath(tarball)
	if err != nil {
		t.Fatalf("readDescriptionFromPath() error = %v", err)
	}
	if !called {
		t.Fatalf("installerReadDescriptionFile was not called for stale sidecar")
	}
	if desc.Package != "pkg" || desc.Version != "0.1.0" {
		t.Fatalf("description = %#v", desc)
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

	cmd, err := buildInstallCommand("/usr/bin/R", dir, filepath.Join(dir, "cache"), filepath.Join(dir, "lib"), env, "", filepath.Join(dir, "pkg.tar.gz"))
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

func TestBuildInstallCommandTargetsWrapsMultiplePackages(t *testing.T) {
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

	cmd, err := buildInstallCommandTargets(
		"/usr/bin/R",
		dir,
		filepath.Join(dir, "cache"),
		filepath.Join(dir, "lib"),
		env,
		"",
		filepath.Join(dir, "pkg-a.tar.gz"),
		filepath.Join(dir, "pkg-b.tar.gz"),
	)
	if err != nil {
		t.Fatalf("buildInstallCommandTargets() error = %v", err)
	}
	if cmd.Path != envaPath {
		t.Fatalf("cmd.Path = %q, want %q", cmd.Path, envaPath)
	}
	want := []string{
		"run", "rs-sysdeps", "--",
		"/usr/bin/R", "CMD", "INSTALL", "-l", filepath.Join(dir, "lib"),
		filepath.Join(dir, "pkg-a.tar.gz"),
		filepath.Join(dir, "pkg-b.tar.gz"),
	}
	if !reflect.DeepEqual(cmd.Args[1:], want) {
		t.Fatalf("cmd.Args = %v, want %v", cmd.Args[1:], want)
	}
}

func TestBuildInstallCommandWithJobsOverridesParallelism(t *testing.T) {
	dir := t.TempDir()
	cmd, err := buildInstallCommandWithJobs(
		"/usr/bin/R",
		dir,
		filepath.Join(dir, "cache"),
		filepath.Join(dir, "lib"),
		[]string{"PATH=/usr/bin"},
		"",
		3,
		filepath.Join(dir, "pkg.tar.gz"),
	)
	if err != nil {
		t.Fatalf("buildInstallCommandWithJobs() error = %v", err)
	}
	if !slices.Contains(cmd.Env, "MAKEFLAGS=-j3") {
		t.Fatalf("cmd.Env missing MAKEFLAGS=-j3: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "CMAKE_BUILD_PARALLEL_LEVEL=3") {
		t.Fatalf("cmd.Env missing CMAKE_BUILD_PARALLEL_LEVEL=3: %v", cmd.Env)
	}
}

func TestBuildInstallCommandPreservesExplicitParallelBuildEnv(t *testing.T) {
	dir := t.TempDir()
	cmd, err := buildInstallCommand("/usr/bin/R", dir, filepath.Join(dir, "cache"), filepath.Join(dir, "lib"), []string{
		"PATH=/usr/bin",
		"MAKEFLAGS=-j32",
		"CMAKE_BUILD_PARALLEL_LEVEL=32",
	}, "", filepath.Join(dir, "pkg.tar.gz"))
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
	}, "", filepath.Join(dir, "pkg.tar.gz"))
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
	}, "", filepath.Join(dir, "pkg.tar.gz"))
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

func TestBuildInstallCommandPrefersCondaTargetCompilers(t *testing.T) {
	oldGOOS := installerGOOS
	t.Cleanup(func() {
		installerGOOS = oldGOOS
	})
	installerGOOS = "linux"

	dir := t.TempDir()
	setTestHomeDir(t, dir)
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	micromambaName := "micromamba"
	if runtime.GOOS == "windows" {
		micromambaName += ".exe"
	}
	if err := os.WriteFile(filepath.Join(binDir, micromambaName), []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(micromamba) error = %v", err)
	}

	prefix := filepath.Join(dir, "micromamba", "envs", "rs-sysdeps")
	prefixBin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(prefixBin, 0o755); err != nil {
		t.Fatalf("MkdirAll(prefixBin) error = %v", err)
	}
	for _, name := range []string{
		"ccache",
		"x86_64-conda-linux-gnu-gcc",
		"x86_64-conda-linux-gnu-c++",
		"x86_64-conda-linux-gnu-gfortran",
	} {
		fileName := name
		if runtime.GOOS == "windows" {
			fileName += ".exe"
		}
		if err := os.WriteFile(filepath.Join(prefixBin, fileName), []byte("binary"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", fileName, err)
		}
	}

	env := toolchainenv.Apply([]string{"PATH=" + binDir}, []string{prefix}, []string{
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	})
	cmd, err := buildInstallCommand("/usr/bin/R", dir, filepath.Join(dir, "cache"), filepath.Join(dir, "lib"), env, "", filepath.Join(dir, "pkg.tar.gz"))
	if err != nil {
		t.Fatalf("buildInstallCommand() error = %v", err)
	}
	if !slices.Contains(cmd.Env, "CC=ccache x86_64-conda-linux-gnu-gcc") {
		t.Fatalf("cmd.Env missing target CC: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "CXX=ccache x86_64-conda-linux-gnu-c++") {
		t.Fatalf("cmd.Env missing target CXX: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "FC=ccache x86_64-conda-linux-gnu-gfortran") {
		t.Fatalf("cmd.Env missing target FC: %v", cmd.Env)
	}
}

func TestBuildInstallCommandAddsPackageSpecificEncodingFixups(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "prefix")
	libDir := filepath.Join(prefix, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(libDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libiconv.so"), []byte("binary"), 0o644); err != nil {
		t.Fatalf("WriteFile(libiconv) error = %v", err)
	}

	env := toolchainenv.Apply([]string{"PATH=/usr/bin"}, []string{prefix}, []string{
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	})
	cmd, err := buildInstallCommand("/usr/bin/R", dir, filepath.Join(dir, "cache"), filepath.Join(dir, "lib"), env, "haven", filepath.Join(dir, "pkg.tar.gz"))
	if err != nil {
		t.Fatalf("buildInstallCommand() error = %v", err)
	}
	if !slices.Contains(cmd.Env, "LIBS=-liconv") {
		t.Fatalf("cmd.Env missing LIBS=-liconv: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "SAN_LIBS=-L"+libDir+" -liconv") {
		t.Fatalf("cmd.Env missing SAN_LIBS: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "LIBRARY_PATH="+libDir) {
		t.Fatalf("cmd.Env missing LIBRARY_PATH: %v", cmd.Env)
	}
	if runtimeLibraryEnv := runtimeLibraryEnvForInstallerTest(); runtimeLibraryEnv != "" && !slices.Contains(cmd.Env, runtimeLibraryEnv+"="+libDir) {
		t.Fatalf("cmd.Env missing %s=%s: %v", runtimeLibraryEnv, libDir, cmd.Env)
	}
}

func TestCanBatchInstallRepoPackagesRejectsSpecialFixupPackagesOnly(t *testing.T) {
	inst := nativeInstaller{
		planned: map[string]plannedPackage{
			"cli": {
				Name:   "cli",
				Source: sourceCRAN,
				Repo: &repoRecord{
					Name:             "cli",
					Source:           sourceCRAN,
					NeedsCompilation: false,
				},
			},
			"digest": {
				Name:   "digest",
				Source: sourceCRAN,
				Repo: &repoRecord{
					Name:             "digest",
					Source:           sourceCRAN,
					NeedsCompilation: true,
				},
			},
			"haven": {
				Name:   "haven",
				Source: sourceCRAN,
				Repo: &repoRecord{
					Name:             "haven",
					Source:           sourceCRAN,
					NeedsCompilation: false,
				},
			},
		},
		installedPackages: map[string]installedPackage{},
	}
	if inst.canBatchInstallRepoPackages([]string{"cli"}) {
		t.Fatalf("single package should not trigger batch install")
	}
	if !inst.canBatchInstallRepoPackages([]string{"cli", "digest"}) {
		t.Fatalf("ordinary compiled package should still allow batch install")
	}
	if inst.canBatchInstallRepoPackages([]string{"cli", "haven"}) {
		t.Fatalf("package requiring native fixups should prevent batch install")
	}
}

func TestSplitBatchInstallableRepoPackagesKeepsBatchableSubset(t *testing.T) {
	inst := nativeInstaller{
		planned: map[string]plannedPackage{
			"cli": {
				Name:   "cli",
				Source: sourceCRAN,
				Repo:   &repoRecord{Name: "cli", Source: sourceCRAN},
			},
			"digest": {
				Name:   "digest",
				Source: sourceCRAN,
				Repo:   &repoRecord{Name: "digest", Source: sourceCRAN, NeedsCompilation: true},
			},
			"haven": {
				Name:   "haven",
				Source: sourceCRAN,
				Repo:   &repoRecord{Name: "haven", Source: sourceCRAN},
			},
			"glue": {
				Name:   "glue",
				Source: sourceCRAN,
				Repo:   &repoRecord{Name: "glue", Source: sourceCRAN},
			},
		},
		installedPackages: map[string]installedPackage{},
	}

	batchable, remainder := inst.splitBatchInstallableRepoPackages([]string{"cli", "haven", "digest", "glue"})
	if !reflect.DeepEqual(batchable, []string{"cli", "digest", "glue"}) {
		t.Fatalf("batchable = %v", batchable)
	}
	if !reflect.DeepEqual(remainder, []string{"haven"}) {
		t.Fatalf("remainder = %v", remainder)
	}
}

func TestInstallCompiledPackageBatchBatchesOrdinaryCompiledPackages(t *testing.T) {
	original := installerEnsureBuildTools
	t.Cleanup(func() {
		installerEnsureBuildTools = original
	})
	installerEnsureBuildTools = func(pkg string, env []string) error {
		return nil
	}

	dir := t.TempDir()
	archive := testTarGzBytes(t, map[string]string{
		"pkg/DESCRIPTION": "Package: pkg\nVersion: 1.0.0\nNeedsCompilation: yes\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	rLog := filepath.Join(dir, "r-args.txt")
	rBinary := writeTestCommand(
		t,
		dir,
		"R",
		fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", rLog),
		fmt.Sprintf("@echo off\r\n> %q echo %%*\r\n", rLog),
	)

	inst := nativeInstaller{
		tempRoot:       filepath.Join(dir, "tmp"),
		downloadRoot:   filepath.Join(dir, "downloads"),
		metaDir:        filepath.Join(dir, "meta"),
		stdout:         io.Discard,
		stderr:         io.Discard,
		httpClient:     server.Client(),
		rBinary:        rBinary,
		prefetchedRepo: map[string]string{},
		req: Request{
			WorkDir:     dir,
			CacheRoot:   filepath.Join(dir, "cache"),
			LibraryPath: filepath.Join(dir, "lib"),
			Environment: []string{"PATH=" + dir},
		},
		planned: map[string]plannedPackage{
			"digest": {
				Name:    "digest",
				Version: "1.0.0",
				Source:  sourceCRAN,
				Repo: &repoRecord{
					Name:             "digest",
					Version:          "1.0.0",
					Source:           sourceCRAN,
					TarballURL:       server.URL + "/digest_1.0.0.tar.gz",
					NeedsCompilation: true,
					DepsLoaded:       true,
				},
			},
			"fs": {
				Name:    "fs",
				Version: "1.0.0",
				Source:  sourceCRAN,
				Repo: &repoRecord{
					Name:             "fs",
					Version:          "1.0.0",
					Source:           sourceCRAN,
					TarballURL:       server.URL + "/fs_1.0.0.tar.gz",
					NeedsCompilation: true,
					DepsLoaded:       true,
				},
			},
		},
		installedPackages: map[string]installedPackage{},
	}
	for _, path := range []string{inst.tempRoot, inst.downloadRoot, inst.metaDir, inst.req.LibraryPath, inst.req.CacheRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	installed, err := inst.installCompiledPackageBatch([]string{"digest", "fs"})
	if err != nil {
		t.Fatalf("installCompiledPackageBatch() error = %v", err)
	}
	if !reflect.DeepEqual(installed, []string{"digest", "fs"}) {
		t.Fatalf("installCompiledPackageBatch() installed = %v", installed)
	}

	data, err := os.ReadFile(rLog)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", rLog, err)
	}
	args := string(data)
	if !strings.Contains(args, "digest_1.0.0.tar.gz") || !strings.Contains(args, "fs_1.0.0.tar.gz") {
		t.Fatalf("R install args = %q, want both compiled tarballs", args)
	}
}

func TestInstallRepoPackageBatchesSplitsLargeBatchIntoMultipleInvocations(t *testing.T) {
	original := installerEnsureBuildTools
	originalRunProgress := installerRunProgressCommand
	t.Cleanup(func() {
		installerEnsureBuildTools = original
		installerRunProgressCommand = originalRunProgress
	})
	installerEnsureBuildTools = func(pkg string, env []string) error {
		return nil
	}
	var (
		invocationMu sync.Mutex
		invocations  [][]string
		activeCalls  int32
		maxActive    int32
	)
	installerRunProgressCommand = func(cmd *exec.Cmd, label string, progress io.Writer, errors io.Writer, opts progresscmd.RunOptions) error {
		current := atomic.AddInt32(&activeCalls, 1)
		for {
			seen := atomic.LoadInt32(&maxActive)
			if current <= seen || atomic.CompareAndSwapInt32(&maxActive, seen, current) {
				break
			}
		}
		invocationMu.Lock()
		invocations = append(invocations, append([]string(nil), cmd.Args...))
		invocationMu.Unlock()
		libraryPath := ""
		targets := []string{}
		for idx := 0; idx < len(cmd.Args); idx++ {
			if cmd.Args[idx] == "-l" && idx+1 < len(cmd.Args) {
				libraryPath = cmd.Args[idx+1]
				idx++
				continue
			}
			if strings.HasSuffix(cmd.Args[idx], ".tar.gz") {
				targets = append(targets, cmd.Args[idx])
			}
		}
		if libraryPath == "" {
			return fmt.Errorf("test hook missing -l library path: %v", cmd.Args)
		}
		for _, target := range targets {
			base := filepath.Base(target)
			name := strings.TrimSuffix(base, ".tar.gz")
			if cut := strings.LastIndex(name, "_"); cut >= 0 {
				name = name[:cut]
			}
			pkgDir := filepath.Join(libraryPath, name)
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(pkgDir, "DESCRIPTION"), []byte(fmt.Sprintf("Package: %s\nVersion: 1.0.0\n", name)), 0o644); err != nil {
				return err
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&activeCalls, -1)
		return nil
	}

	dir := t.TempDir()
	archive := testTarGzBytes(t, map[string]string{
		"pkg/DESCRIPTION": "Package: pkg\nVersion: 1.0.0\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	inst := nativeInstaller{
		tempRoot:       filepath.Join(dir, "tmp"),
		downloadRoot:   filepath.Join(dir, "downloads"),
		metaDir:        filepath.Join(dir, "meta"),
		stdout:         io.Discard,
		stderr:         io.Discard,
		httpClient:     server.Client(),
		rBinary:        "R",
		prefetchedRepo: map[string]string{},
		req: Request{
			WorkDir:     dir,
			CacheRoot:   filepath.Join(dir, "cache"),
			LibraryPath: filepath.Join(dir, "lib"),
			Environment: testEnvironmentWithPath(dir),
		},
		planned:           map[string]plannedPackage{},
		installedPackages: map[string]installedPackage{},
	}
	for _, path := range []string{inst.tempRoot, inst.downloadRoot, inst.metaDir, inst.req.LibraryPath, inst.req.CacheRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}
	for _, name := range []string{"a", "b", "c", "d"} {
		inst.planned[name] = plannedPackage{
			Name:    name,
			Version: "1.0.0",
			Source:  sourceCRAN,
			Repo: &repoRecord{
				Name:       name,
				Version:    "1.0.0",
				Source:     sourceCRAN,
				TarballURL: server.URL + "/" + name + "_1.0.0.tar.gz",
				DepsLoaded: true,
			},
		}
	}

	installed, err := inst.installRepoPackageBatches([]string{"a", "b", "c", "d"}, 2)
	if err != nil {
		t.Fatalf("installRepoPackageBatches() error = %v", err)
	}
	if !reflect.DeepEqual(installed, []string{"a", "b", "c", "d"}) {
		t.Fatalf("installRepoPackageBatches() installed = %v", installed)
	}
	invocationMu.Lock()
	defer invocationMu.Unlock()
	if len(invocations) != 2 {
		t.Fatalf("batch invocation count = %d, want 2; args = %v", len(invocations), invocations)
	}
	for _, args := range invocations {
		if len(args) < 2 {
			t.Fatalf("batch invocation args too short: %v", args)
		}
	}
	if got := atomic.LoadInt32(&maxActive); got < 2 {
		t.Fatalf("max concurrent batch invocations = %d, want at least 2", got)
	}
}

func TestPrepareInstallTargetPatchesEncodingMakevarsInTarball(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "prefix")
	libDir := filepath.Join(prefix, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(libDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libiconv.so"), []byte("binary"), 0o644); err != nil {
		t.Fatalf("WriteFile(libiconv) error = %v", err)
	}

	tarball := filepath.Join(dir, "haven_2.5.5.tar.gz")
	if err := writeTestTarball(tarball, map[string]string{
		"haven/DESCRIPTION":     "Package: haven\nVersion: 2.5.5\n",
		"haven/src/Makevars.in": "PKG_LIBS = @libs@\n",
	}); err != nil {
		t.Fatalf("writeTestTarball() error = %v", err)
	}

	inst := nativeInstaller{
		tempRoot: dir,
		req: Request{
			Environment: toolchainenv.Apply([]string{"PATH=/usr/bin"}, []string{prefix}, []string{
				filepath.Join(prefix, "lib", "pkgconfig"),
				filepath.Join(prefix, "share", "pkgconfig"),
			}),
		},
	}
	target, err := inst.prepareInstallTarget("haven", tarball)
	if err != nil {
		t.Fatalf("prepareInstallTarget() error = %v", err)
	}
	if target == tarball {
		t.Fatalf("prepareInstallTarget() returned original tarball, want unpacked source dir")
	}

	data, err := os.ReadFile(filepath.Join(target, "src", "Makevars.in"))
	if err != nil {
		t.Fatalf("ReadFile(Makevars.in) error = %v", err)
	}
	if got := string(data); got != "PKG_LIBS = -L"+libDir+" -liconv @libs@\n" {
		t.Fatalf("patched Makevars.in = %q", got)
	}
}

func TestPrepareInstallTargetAllowsEncodingTarballWithoutMakevars(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "prefix")
	libDir := filepath.Join(prefix, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(libDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libiconv.so"), []byte("binary"), 0o644); err != nil {
		t.Fatalf("WriteFile(libiconv) error = %v", err)
	}

	tarball := filepath.Join(dir, "readr_2.2.0.tar.gz")
	if err := writeTestTarball(tarball, map[string]string{
		"readr/DESCRIPTION": "Package: readr\nVersion: 2.2.0\n",
		"readr/src/init.c":  "void R_init_readr(void) {}\n",
	}); err != nil {
		t.Fatalf("writeTestTarball() error = %v", err)
	}

	inst := nativeInstaller{
		tempRoot: dir,
		req: Request{
			Environment: toolchainenv.Apply([]string{"PATH=/usr/bin"}, []string{prefix}, []string{
				filepath.Join(prefix, "lib", "pkgconfig"),
				filepath.Join(prefix, "share", "pkgconfig"),
			}),
		},
	}
	target, err := inst.prepareInstallTarget("readr", tarball)
	if err != nil {
		t.Fatalf("prepareInstallTarget() error = %v", err)
	}
	if target == tarball {
		t.Fatalf("prepareInstallTarget() returned original tarball, want unpacked source dir")
	}
	if _, err := os.Stat(filepath.Join(target, "src", "Makevars")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(Makevars) error = %v, want not exists", err)
	}
	if _, err := os.Stat(filepath.Join(target, "src", "Makevars.in")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(Makevars.in) error = %v, want not exists", err)
	}
}

func TestSeedPlannedPackagesFromCacheReusesMatchingSiblingLibrary(t *testing.T) {
	cacheRoot := t.TempDir()
	libraryRoot := filepath.Join(cacheRoot, "lib")
	sourceLib := filepath.Join(libraryRoot, "aaaaaaaaaaaaaaaa")
	targetLib := filepath.Join(libraryRoot, "bbbbbbbbbbbbbbbb")
	for _, path := range []string{
		filepath.Join(sourceLib, "cli"),
		filepath.Join(sourceLib, ".rs-source-meta"),
		targetLib,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourceLib, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.5\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceLib, "cli", "NAMESPACE"), []byte("exportPattern(\"^[[:alpha:]]+\")\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(NAMESPACE) error = %v", err)
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: targetLib,
		},
		metaDir:           filepath.Join(targetLib, ".rs-source-meta"),
		stderr:            &bytes.Buffer{},
		planned:           map[string]plannedPackage{"cli": {Name: "cli", Version: "3.6.5", Source: sourceCRAN}},
		installedPackages: map[string]installedPackage{},
	}
	if err := inst.seedPlannedPackagesFromCache(); err != nil {
		t.Fatalf("seedPlannedPackagesFromCache() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(targetLib, "cli", "DESCRIPTION"))
	if err != nil {
		t.Fatalf("ReadFile(copied DESCRIPTION) error = %v", err)
	}
	if !strings.Contains(string(data), "Version: 3.6.5") {
		t.Fatalf("copied DESCRIPTION = %q", string(data))
	}
	if got := inst.installedPackages["cli"]; got.Version != "3.6.5" || got.Source != sourceCRAN {
		t.Fatalf("installedPackages[cli] = %#v", got)
	}

	sourceInfo, err := os.Stat(filepath.Join(sourceLib, "cli", "DESCRIPTION"))
	if err != nil {
		t.Fatalf("Stat(source DESCRIPTION) error = %v", err)
	}
	targetInfo, err := os.Stat(filepath.Join(targetLib, "cli", "DESCRIPTION"))
	if err != nil {
		t.Fatalf("Stat(target DESCRIPTION) error = %v", err)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatalf("expected reused package files to be hard-linked")
	}
	if log := inst.stderr.(*bytes.Buffer).String(); log != "" {
		t.Fatalf("cache reuse log = %q, want silent single-package default reuse", log)
	}
}

func TestSeedPlannedPackagesFromCacheSkipsMismatchedSiblingPackage(t *testing.T) {
	cacheRoot := t.TempDir()
	libraryRoot := filepath.Join(cacheRoot, "lib")
	sourceLib := filepath.Join(libraryRoot, "aaaaaaaaaaaaaaaa")
	targetLib := filepath.Join(libraryRoot, "bbbbbbbbbbbbbbbb")
	if err := os.MkdirAll(filepath.Join(sourceLib, "cli"), 0o755); err != nil {
		t.Fatalf("MkdirAll(source cli) error = %v", err)
	}
	if err := os.MkdirAll(targetLib, 0o755); err != nil {
		t.Fatalf("MkdirAll(target) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceLib, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.4\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: targetLib,
		},
		metaDir:           filepath.Join(targetLib, ".rs-source-meta"),
		stderr:            io.Discard,
		planned:           map[string]plannedPackage{"cli": {Name: "cli", Version: "3.6.5", Source: sourceCRAN}},
		installedPackages: map[string]installedPackage{},
	}
	if err := inst.seedPlannedPackagesFromCache(); err != nil {
		t.Fatalf("seedPlannedPackagesFromCache() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetLib, "cli")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target cli package unexpectedly copied, stat err = %v", err)
	}
}

func TestSeedPlannedPackagesFromCacheReportsSingleReuseWhenVerbose(t *testing.T) {
	cacheRoot := t.TempDir()
	libraryRoot := filepath.Join(cacheRoot, "lib")
	sourceLib := filepath.Join(libraryRoot, "aaaaaaaaaaaaaaaa")
	targetLib := filepath.Join(libraryRoot, "bbbbbbbbbbbbbbbb")
	for _, path := range []string{
		filepath.Join(sourceLib, "cli"),
		filepath.Join(sourceLib, ".rs-source-meta"),
		targetLib,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourceLib, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.5\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceLib, "cli", "NAMESPACE"), []byte("exportPattern(\"^[[:alpha:]]+\")\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(NAMESPACE) error = %v", err)
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: targetLib,
			Verbose:     true,
		},
		metaDir:           filepath.Join(targetLib, ".rs-source-meta"),
		stderr:            &bytes.Buffer{},
		planned:           map[string]plannedPackage{"cli": {Name: "cli", Version: "3.6.5", Source: sourceCRAN}},
		installedPackages: map[string]installedPackage{},
	}
	if err := inst.seedPlannedPackagesFromCache(); err != nil {
		t.Fatalf("seedPlannedPackagesFromCache() error = %v", err)
	}
	if log := inst.stderr.(*bytes.Buffer).String(); !strings.Contains(log, "reused 1 cached package from 1 library snapshot") {
		t.Fatalf("cache reuse verbose log = %q", log)
	}
}

func TestSeedPlannedPackagesFromStoreReusesMatchingStoredPackage(t *testing.T) {
	cacheRoot := t.TempDir()
	runtime := Runtime{
		Interpreter:     "/opt/demo/R/4.4.3/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.4.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	}
	pkg := plannedPackage{Name: "cli", Version: "3.6.5", Source: sourceCRAN}
	storeLib := packageStorePathForPlanned(cacheRoot, pkg, runtime)
	targetLib := filepath.Join(cacheRoot, "lib", "bbbbbbbbbbbbbbbb")
	for _, path := range []string{
		filepath.Join(storeLib, "cli"),
		targetLib,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(storeLib, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.5\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeLib, "cli", "NAMESPACE"), []byte("exportPattern(\"^[[:alpha:]]+\")\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(NAMESPACE) error = %v", err)
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: targetLib,
			Runtime:     runtime,
		},
		metaDir:           filepath.Join(targetLib, ".rs-source-meta"),
		stderr:            &bytes.Buffer{},
		planned:           map[string]plannedPackage{"cli": pkg},
		installedPackages: map[string]installedPackage{},
	}
	if err := inst.seedPlannedPackagesFromStore(); err != nil {
		t.Fatalf("seedPlannedPackagesFromStore() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(targetLib, "cli", "DESCRIPTION"))
	if err != nil {
		t.Fatalf("ReadFile(copied DESCRIPTION) error = %v", err)
	}
	if !strings.Contains(string(data), "Version: 3.6.5") {
		t.Fatalf("copied DESCRIPTION = %q", string(data))
	}
	if got := inst.installedPackages["cli"]; got.Version != "3.6.5" || got.Source != sourceCRAN {
		t.Fatalf("installedPackages[cli] = %#v", got)
	}

	sourceInfo, err := os.Stat(filepath.Join(storeLib, "cli", "DESCRIPTION"))
	if err != nil {
		t.Fatalf("Stat(source DESCRIPTION) error = %v", err)
	}
	targetInfo, err := os.Stat(filepath.Join(targetLib, "cli", "DESCRIPTION"))
	if err != nil {
		t.Fatalf("Stat(target DESCRIPTION) error = %v", err)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatalf("expected stored package files to be hard-linked")
	}
	if log := inst.stderr.(*bytes.Buffer).String(); log != "" {
		t.Fatalf("store reuse log = %q, want silent single-package default reuse", log)
	}
}

func TestSeedPlannedPackagesFromStoreReportsSingleReuseWhenVerbose(t *testing.T) {
	cacheRoot := t.TempDir()
	runtime := Runtime{
		Interpreter:     "/opt/demo/R/4.4.3/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.4.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	}
	pkg := plannedPackage{Name: "cli", Version: "3.6.5", Source: sourceCRAN}
	storeLib := packageStorePathForPlanned(cacheRoot, pkg, runtime)
	targetLib := filepath.Join(cacheRoot, "lib", "bbbbbbbbbbbbbbbb")
	for _, path := range []string{
		filepath.Join(storeLib, "cli"),
		targetLib,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(storeLib, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.5\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeLib, "cli", "NAMESPACE"), []byte("exportPattern(\"^[[:alpha:]]+\")\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(NAMESPACE) error = %v", err)
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: targetLib,
			Runtime:     runtime,
			Verbose:     true,
		},
		metaDir:           filepath.Join(targetLib, ".rs-source-meta"),
		stderr:            &bytes.Buffer{},
		planned:           map[string]plannedPackage{"cli": pkg},
		installedPackages: map[string]installedPackage{},
	}
	if err := inst.seedPlannedPackagesFromStore(); err != nil {
		t.Fatalf("seedPlannedPackagesFromStore() error = %v", err)
	}
	if log := inst.stderr.(*bytes.Buffer).String(); !strings.Contains(log, "reused 1 stored package") {
		t.Fatalf("store reuse verbose log = %q", log)
	}
}

func TestSeedPlannedPackagesFromStoreReusesMultipleStoredPackages(t *testing.T) {
	cacheRoot := t.TempDir()
	runtime := Runtime{
		Interpreter:     "/opt/demo/R/4.4.3/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.4.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	}
	targetLib := filepath.Join(cacheRoot, "lib", "bbbbbbbbbbbbbbbb")
	if err := os.MkdirAll(targetLib, 0o755); err != nil {
		t.Fatalf("MkdirAll(target) error = %v", err)
	}

	planned := map[string]plannedPackage{
		"cli":  {Name: "cli", Version: "3.6.5", Source: sourceCRAN},
		"glue": {Name: "glue", Version: "1.8.0", Source: sourceCRAN},
	}
	order := []string{"cli", "glue"}
	for _, name := range order {
		pkg := planned[name]
		storeLib := packageStorePathForPlanned(cacheRoot, pkg, runtime)
		if err := os.MkdirAll(filepath.Join(storeLib, name), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s store) error = %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(storeLib, name, "DESCRIPTION"), []byte(fmt.Sprintf("Package: %s\nVersion: %s\nRepository: CRAN\n", name, pkg.Version)), 0o644); err != nil {
			t.Fatalf("WriteFile(%s DESCRIPTION) error = %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(storeLib, name, "NAMESPACE"), []byte("exportPattern(\"^[[:alpha:]]+\")\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s NAMESPACE) error = %v", name, err)
		}
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: targetLib,
			Runtime:     runtime,
		},
		metaDir:           filepath.Join(targetLib, ".rs-source-meta"),
		stderr:            &bytes.Buffer{},
		planned:           planned,
		order:             order,
		installedPackages: map[string]installedPackage{},
	}
	if err := inst.seedPlannedPackagesFromStore(); err != nil {
		t.Fatalf("seedPlannedPackagesFromStore() error = %v", err)
	}
	for _, name := range order {
		if got := inst.installedPackages[name]; got.Version != planned[name].Version {
			t.Fatalf("installedPackages[%s] = %#v", name, got)
		}
		if _, err := os.Stat(filepath.Join(targetLib, name, "DESCRIPTION")); err != nil {
			t.Fatalf("Stat(%s DESCRIPTION) error = %v", name, err)
		}
	}
	if log := inst.stderr.(*bytes.Buffer).String(); !strings.Contains(log, "reused 2 stored packages") || strings.Contains(log, "reusing stored cli") || strings.Contains(log, "reusing stored glue") {
		t.Fatalf("store reuse log = %q", log)
	}
}

func TestLoadInstalledPackageFromLibraryReadsSourceMetadata(t *testing.T) {
	library := t.TempDir()
	if err := os.MkdirAll(filepath.Join(library, "cli"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cli) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(library, ".rs-source-meta"), 0o755); err != nil {
		t.Fatalf("MkdirAll(meta) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(library, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.5\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	metaLine := "github\tgithub\towner/repo\tmain\tdeadbeef\tpkg\tfingerprint\tfile_sha256\n"
	if err := os.WriteFile(filepath.Join(library, ".rs-source-meta", "cli.tsv"), []byte(metaLine), 0o644); err != nil {
		t.Fatalf("WriteFile(cli.tsv) error = %v", err)
	}

	pkg, ok, err := loadInstalledPackageFromLibrary(library, "cli")
	if err != nil {
		t.Fatalf("loadInstalledPackageFromLibrary() error = %v", err)
	}
	if !ok {
		t.Fatalf("loadInstalledPackageFromLibrary() ok = false, want true")
	}
	if pkg.Name != "cli" || pkg.Version != "3.6.5" || pkg.Source != sourceGitHub {
		t.Fatalf("pkg = %#v", pkg)
	}
	if pkg.Location != "owner/repo" || pkg.Commit != "deadbeef" || pkg.FingerprintKind != localKindFileSHA256 {
		t.Fatalf("pkg metadata = %#v", pkg)
	}
}

func TestFindReusablePackagesInLibraryUsesPointLookupsForSmallRemainder(t *testing.T) {
	library := t.TempDir()
	for _, path := range []string{
		filepath.Join(library, "cli"),
		filepath.Join(library, ".rs-source-meta"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(library, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.5\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}

	got, err := findReusablePackagesInLibrary(library, map[string]plannedPackage{
		"cli":   {Name: "cli", Version: "3.6.5", Source: sourceCRAN},
		"glue":  {Name: "glue", Version: "1.8.0", Source: sourceCRAN},
		"rlang": {Name: "rlang", Version: "1.1.7", Source: sourceCRAN},
	})
	if err != nil {
		t.Fatalf("findReusablePackagesInLibrary() error = %v", err)
	}
	if len(got) != 1 || got["cli"].Version != "3.6.5" {
		t.Fatalf("findReusablePackagesInLibrary() = %#v, want only cli", got)
	}
}

func TestFindReusablePackagesInLibraryIgnoresBrokenUnrelatedPackageDirs(t *testing.T) {
	library := t.TempDir()
	for _, path := range []string{
		filepath.Join(library, "cli"),
		filepath.Join(library, "broken"),
		filepath.Join(library, ".rs-source-meta"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(library, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.5\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(cli DESCRIPTION) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(library, "broken", "DESCRIPTION"), 0o755); err != nil {
		t.Fatalf("MkdirAll(broken DESCRIPTION dir) error = %v", err)
	}

	got, err := findReusablePackagesInLibrary(library, map[string]plannedPackage{
		"cli": {Name: "cli", Version: "3.6.5", Source: sourceCRAN},
	})
	if err != nil {
		t.Fatalf("findReusablePackagesInLibrary() error = %v", err)
	}
	if len(got) != 1 || got["cli"].Version != "3.6.5" {
		t.Fatalf("findReusablePackagesInLibrary() = %#v, want only cli", got)
	}
}

func TestFindReusablePackagesInLibraryIgnoresBrokenUnrelatedMetadataForSmallCandidateSet(t *testing.T) {
	library := t.TempDir()
	for _, path := range []string{
		filepath.Join(library, "cli"),
		filepath.Join(library, ".rs-source-meta"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(library, "cli", "DESCRIPTION"), []byte("Package: cli\nVersion: 3.6.5\nRepository: CRAN\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(cli DESCRIPTION) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(library, ".rs-source-meta", "broken.tsv"), []byte("%zz"), 0o644); err != nil {
		t.Fatalf("WriteFile(broken.tsv) error = %v", err)
	}

	got, err := findReusablePackagesInLibrary(library, map[string]plannedPackage{
		"cli": {Name: "cli", Version: "3.6.5", Source: sourceCRAN},
	})
	if err != nil {
		t.Fatalf("findReusablePackagesInLibrary() error = %v", err)
	}
	if len(got) != 1 || got["cli"].Version != "3.6.5" {
		t.Fatalf("findReusablePackagesInLibrary() = %#v, want only cli", got)
	}
}

func TestParallelWorkerLimitBoundsToCPUAndItemCount(t *testing.T) {
	original := runtime.GOMAXPROCS(0)
	runtime.GOMAXPROCS(6)
	t.Cleanup(func() {
		runtime.GOMAXPROCS(original)
	})

	if got := parallelWorkerLimit(0); got != 0 {
		t.Fatalf("parallelWorkerLimit(0) = %d, want 0", got)
	}
	if got := parallelWorkerLimit(1); got != 1 {
		t.Fatalf("parallelWorkerLimit(1) = %d, want 1", got)
	}
	if got := parallelWorkerLimit(3); got != 3 {
		t.Fatalf("parallelWorkerLimit(3) = %d, want 3", got)
	}
	if got := parallelWorkerLimit(99); got != 6 {
		t.Fatalf("parallelWorkerLimit(99) = %d, want 6", got)
	}

	runtime.GOMAXPROCS(32)
	if got := parallelWorkerLimit(99); got != 8 {
		t.Fatalf("parallelWorkerLimit(99) with high GOMAXPROCS = %d, want 8", got)
	}
}

func TestCompiledBatchWorkerLimitCapsAtTwo(t *testing.T) {
	original := runtime.GOMAXPROCS(0)
	runtime.GOMAXPROCS(16)
	t.Cleanup(func() {
		runtime.GOMAXPROCS(original)
	})

	if got := compiledBatchWorkerLimit(0); got != 0 {
		t.Fatalf("compiledBatchWorkerLimit(0) = %d, want 0", got)
	}
	if got := compiledBatchWorkerLimit(1); got != 1 {
		t.Fatalf("compiledBatchWorkerLimit(1) = %d, want 1", got)
	}
	if got := compiledBatchWorkerLimit(8); got != 2 {
		t.Fatalf("compiledBatchWorkerLimit(8) = %d, want 2", got)
	}
}

func TestSplitRepoBatchChunksPreservesOrderAndMinBatchSize(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f"}
	got := splitRepoBatchChunks(names, 3)
	want := [][]string{
		{"a", "d"},
		{"b", "e"},
		{"c", "f"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitRepoBatchChunks() = %v, want %v", got, want)
	}

	got = splitRepoBatchChunks([]string{"a", "b", "c"}, 3)
	want = [][]string{{"a", "b", "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitRepoBatchChunks() small batch = %v, want %v", got, want)
	}
}

func TestShouldLogPackageInstallSummaryOnlyForSlowPackages(t *testing.T) {
	if shouldLogPackageInstallSummary(44 * time.Second) {
		t.Fatal("shouldLogPackageInstallSummary(44s) = true, want false")
	}
	if !shouldLogPackageInstallSummary(45 * time.Second) {
		t.Fatal("shouldLogPackageInstallSummary(45s) = false, want true")
	}
}

func TestShouldLogDependencyLayerSummary(t *testing.T) {
	if shouldLogDependencyLayerSummary(5*time.Second, 0, 0, false) {
		t.Fatal("shouldLogDependencyLayerSummary() = true for empty layer, want false")
	}
	if shouldLogDependencyLayerSummary(29*time.Second, 0, 3, false) {
		t.Fatal("shouldLogDependencyLayerSummary() = true for fast pure layer, want false")
	}
	if !shouldLogDependencyLayerSummary(30*time.Second, 0, 3, false) {
		t.Fatal("shouldLogDependencyLayerSummary() = false for slow pure layer, want true")
	}
	if !shouldLogDependencyLayerSummary(5*time.Second, 2, 3, false) {
		t.Fatal("shouldLogDependencyLayerSummary() = false for compiled layer, want true")
	}
	if !shouldLogDependencyLayerSummary(5*time.Second, 0, 3, true) {
		t.Fatal("shouldLogDependencyLayerSummary() = false for verbose pure layer, want true")
	}
}

func TestFormatDependencyLayerSummary(t *testing.T) {
	if got, ok := formatDependencyLayerSummary(1, 4, 10*time.Second, 0, 3, false); ok || got != "" {
		t.Fatalf("formatDependencyLayerSummary() = (%q, %v), want empty suppressed summary", got, ok)
	}
	if got, ok := formatDependencyLayerSummary(2, 4, 30*time.Second, 0, 3, false); !ok || got != "dependency layer 2/4 completed in 30s (3 installed)" {
		t.Fatalf("formatDependencyLayerSummary() = (%q, %v)", got, ok)
	}
	if got, ok := formatDependencyLayerSummary(3, 4, 12*time.Second, 2, 5, false); !ok || got != "dependency layer 3/4 completed in 12s (5 installed, 2 compiled)" {
		t.Fatalf("formatDependencyLayerSummary() compiled = (%q, %v)", got, ok)
	}
	if got, ok := formatDependencyLayerSummary(1, 4, 10*time.Second, 0, 3, true); !ok || got != "dependency layer 1/4 completed in 10s (3 installed)" {
		t.Fatalf("formatDependencyLayerSummary() verbose = (%q, %v)", got, ok)
	}
}

func TestFormatDependencyLayerPlan(t *testing.T) {
	if got := formatDependencyLayerPlan(1, 4, 3, 0); got != "dependency layer 1/4: 3 package(s)" {
		t.Fatalf("formatDependencyLayerPlan() pure = %q", got)
	}
	if got := formatDependencyLayerPlan(2, 4, 0, 2); got != "dependency layer 2/4: 2 compiled package(s)" {
		t.Fatalf("formatDependencyLayerPlan() compiled = %q", got)
	}
	if got := formatDependencyLayerPlan(3, 4, 5, 2); got != "dependency layer 3/4: 7 package(s) (5 pure, 2 compiled)" {
		t.Fatalf("formatDependencyLayerPlan() mixed = %q", got)
	}
}

func TestShouldStageDependencyLayerPlan(t *testing.T) {
	if shouldStageDependencyLayerPlan(1, 0, false) {
		t.Fatal("shouldStageDependencyLayerPlan(single pure) = true, want false")
	}
	if !shouldStageDependencyLayerPlan(0, 1, false) {
		t.Fatal("shouldStageDependencyLayerPlan(compiled) = false, want true")
	}
	if !shouldStageDependencyLayerPlan(3, 0, false) {
		t.Fatal("shouldStageDependencyLayerPlan(large pure) = false, want true")
	}
	if !shouldStageDependencyLayerPlan(1, 0, true) {
		t.Fatal("shouldStageDependencyLayerPlan(verbose) = false, want true")
	}
}

func TestFormatParallelInstallSummary(t *testing.T) {
	if got := formatParallelInstallSummary(1, 1, "workers", []string{"cli"}); got != "" {
		t.Fatalf("formatParallelInstallSummary() single = %q, want empty", got)
	}
	got := formatParallelInstallSummary(8, 4, "batches", []string{"a", "b", "c", "d", "e", "f", "g", "h"})
	want := "installing 8 package(s) across 4 parallel batches: a, b, c, d, e, f, +2 more"
	if got != want {
		t.Fatalf("formatParallelInstallSummary() = %q, want %q", got, want)
	}
}

func TestShouldLogParallelInstallSummary(t *testing.T) {
	if shouldLogParallelInstallSummary(8, 4, false) {
		t.Fatal("shouldLogParallelInstallSummary(small default) = true, want false")
	}
	if !shouldLogParallelInstallSummary(12, 4, false) {
		t.Fatal("shouldLogParallelInstallSummary(large default) = false, want true")
	}
	if !shouldLogParallelInstallSummary(4, 2, true) {
		t.Fatal("shouldLogParallelInstallSummary(verbose) = false, want true")
	}
}

func TestFormatSlowInstallSummary(t *testing.T) {
	if got := formatSlowInstallSummary(nil, 4); got != "" {
		t.Fatalf("formatSlowInstallSummary(nil) = %q, want empty", got)
	}
	got := formatSlowInstallSummary([]installTiming{
		{label: "sass", duration: 3*time.Minute + 30*time.Second},
		{label: "ragg", duration: 94 * time.Second},
		{label: "stringi", duration: 53 * time.Second},
		{label: "ggplot2", duration: 60 * time.Second},
		{label: "batch[cli, glue, +2 more]", duration: 48 * time.Second},
	}, 4)
	want := "slow installs: sass 3m30s, ragg 1m34s, ggplot2 1m00s, stringi 53s, +1 more"
	if got != want {
		t.Fatalf("formatSlowInstallSummary() = %q, want %q", got, want)
	}
}

func TestShouldStageReuseSummary(t *testing.T) {
	if shouldStageReuseSummary(1, false) {
		t.Fatal("shouldStageReuseSummary(single default) = true, want false")
	}
	if !shouldStageReuseSummary(2, false) {
		t.Fatal("shouldStageReuseSummary(multi default) = false, want true")
	}
	if !shouldStageReuseSummary(1, true) {
		t.Fatal("shouldStageReuseSummary(verbose) = false, want true")
	}
}

func TestFormatNativeInstallSummary(t *testing.T) {
	got := formatNativeInstallSummary(12*time.Second, installSummaryStats{
		installedCount:       8,
		compiledInstallCount: 3,
		reusedCount:          2,
	})
	want := "native package install completed in 12s (8 installed, 3 compiled, 2 reused)"
	if got != want {
		t.Fatalf("formatNativeInstallSummary() = %q, want %q", got, want)
	}

	got = formatNativeInstallSummary(3*time.Second, installSummaryStats{})
	want = "native package install completed in 3s (no package changes)"
	if got != want {
		t.Fatalf("formatNativeInstallSummary() empty = %q, want %q", got, want)
	}
}

func TestCountInstalledPlannedPackages(t *testing.T) {
	inst := &nativeInstaller{
		order: []string{"a", "b", "c"},
		planned: map[string]plannedPackage{
			"a": {Name: "a", Version: "1.0.0", Source: sourceCRAN},
			"b": {Name: "b", Version: "1.0.0", Source: sourceCRAN},
			"c": {Name: "c", Version: "1.0.0", Source: sourceCRAN},
		},
		installedPackages: map[string]installedPackage{
			"a": {Name: "a", Version: "1.0.0", Source: sourceCRAN},
			"c": {Name: "c", Version: "1.0.0", Source: sourceCRAN},
		},
	}
	if got := countInstalledPlannedPackages(inst); got != 2 {
		t.Fatalf("countInstalledPlannedPackages() = %d, want 2", got)
	}
}

func TestInstallJobsPerPackageSplitsCompileBudgetAcrossWorkers(t *testing.T) {
	if got := installJobsPerPackage(1); got != defaultInstallJobs() {
		t.Fatalf("installJobsPerPackage(1) = %d, want %d", got, defaultInstallJobs())
	}
	wantTwoWorkers := defaultInstallJobs() / 2
	if wantTwoWorkers < 1 {
		wantTwoWorkers = 1
	}
	if got := installJobsPerPackage(2); got != wantTwoWorkers {
		t.Fatalf("installJobsPerPackage(2) = %d", got)
	}
	if got := installJobsPerPackage(defaultInstallJobs() * 2); got != 1 {
		t.Fatalf("installJobsPerPackage(oversubscribed) = %d, want 1", got)
	}
}

func TestWaitAllSyncPlannedPackagesToStoreReturnsFirstError(t *testing.T) {
	first := make(chan error, 1)
	second := make(chan error, 1)
	first <- errors.New("first")
	second <- errors.New("second")

	err := waitAllSyncPlannedPackagesToStore([]<-chan error{nil, first, second})
	if err == nil || err.Error() != "first" {
		t.Fatalf("waitAllSyncPlannedPackagesToStore() error = %v, want first", err)
	}
}

func TestSyncPlannedPackageToStoreMaterializesStoreEntry(t *testing.T) {
	cacheRoot := t.TempDir()
	libraryPath := filepath.Join(cacheRoot, "lib", "aaaaaaaaaaaaaaaa")
	metaDir := filepath.Join(libraryPath, ".rs-source-meta")
	runtime := Runtime{
		Interpreter:     "/opt/demo/R/4.4.3/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.4.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	}
	prepared := preparedSource{
		Name:            "demo",
		Version:         "0.1.0",
		Source:          sourceGitHub,
		Host:            "github.com",
		Location:        "owner/demo",
		Ref:             "main",
		Commit:          "abc123",
		Fingerprint:     "feedbeef",
		FingerprintKind: localKindDirSHA256,
	}
	pkg := plannedPackage{
		Name:     "demo",
		Version:  "0.1.0",
		Source:   sourceGitHub,
		Prepared: &prepared,
	}
	for _, path := range []string{
		filepath.Join(libraryPath, "demo"),
		metaDir,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(libraryPath, "demo", "DESCRIPTION"), []byte("Package: demo\nVersion: 0.1.0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(DESCRIPTION) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(libraryPath, "demo", "NAMESPACE"), []byte("exportPattern(\"^[[:alpha:]]+\")\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(NAMESPACE) error = %v", err)
	}
	if err := writeSourceMetadata(metaDir, "demo", prepared); err != nil {
		t.Fatalf("writeSourceMetadata() error = %v", err)
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: libraryPath,
			Runtime:     runtime,
		},
		planned: map[string]plannedPackage{"demo": pkg},
		metaDir: metaDir,
	}
	if err := inst.syncPlannedPackageToStore("demo"); err != nil {
		t.Fatalf("syncPlannedPackageToStore() error = %v", err)
	}

	storeLib := packageStorePathForPlanned(cacheRoot, pkg, runtime)
	installed, err := loadInstalledPackagesFromLibrary(storeLib)
	if err != nil {
		t.Fatalf("loadInstalledPackagesFromLibrary(store) error = %v", err)
	}
	got, ok := installed["demo"]
	if !ok {
		t.Fatalf("stored package metadata missing for demo")
	}
	if got.Source != sourceGitHub || got.Location != "owner/demo" || got.Commit != "abc123" || got.Fingerprint != "feedbeef" {
		t.Fatalf("stored installed package = %#v", got)
	}

	sourceInfo, err := os.Stat(filepath.Join(libraryPath, "demo", "DESCRIPTION"))
	if err != nil {
		t.Fatalf("Stat(source DESCRIPTION) error = %v", err)
	}
	targetInfo, err := os.Stat(filepath.Join(storeLib, "demo", "DESCRIPTION"))
	if err != nil {
		t.Fatalf("Stat(store DESCRIPTION) error = %v", err)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatalf("expected package store files to be hard-linked")
	}
	stateData, err := os.ReadFile(filepath.Join(storeLib, PackageStoreStateFile))
	if err != nil {
		t.Fatalf("ReadFile(package store state) error = %v", err)
	}
	var state PackageStoreState
	if err := json.Unmarshal(stateData, &state); err != nil {
		t.Fatalf("json.Unmarshal(package store state) error = %v", err)
	}
	if state.Package != "demo" || state.RuntimeIdentity == "" || state.LastUsedAt == "" || state.UpdatedAt == "" {
		t.Fatalf("package store state = %#v", state)
	}
}

func TestSyncPlannedPackageToStoreSkipsRewriteForMatchingStoreEntry(t *testing.T) {
	cacheRoot := t.TempDir()
	libraryPath := filepath.Join(cacheRoot, "lib", "current")
	metaDir := filepath.Join(libraryPath, ".rs-source-meta")
	runtime := Runtime{
		Interpreter:     "/opt/demo/R/4.4.3/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.4.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	}
	prepared := preparedSource{
		Name:            "demo",
		Version:         "0.1.0",
		Source:          sourceGitHub,
		Host:            "github.com",
		Location:        "owner/demo",
		Ref:             "main",
		Commit:          "abc123",
		Fingerprint:     "feedbeef",
		FingerprintKind: localKindDirSHA256,
	}
	pkg := plannedPackage{
		Name:     "demo",
		Version:  "0.1.0",
		Source:   sourceGitHub,
		Prepared: &prepared,
	}
	storeLib := packageStorePathForPlanned(cacheRoot, pkg, runtime)
	for _, path := range []string{
		filepath.Join(libraryPath, "demo"),
		metaDir,
		filepath.Join(storeLib, "demo"),
		filepath.Join(storeLib, ".rs-source-meta"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	currentDesc := filepath.Join(libraryPath, "demo", "DESCRIPTION")
	storeDesc := filepath.Join(storeLib, "demo", "DESCRIPTION")
	for _, path := range []string{currentDesc, storeDesc} {
		if err := os.WriteFile(path, []byte("Package: demo\nVersion: 0.1.0\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(libraryPath, "demo", "NAMESPACE"),
		filepath.Join(storeLib, "demo", "NAMESPACE"),
	} {
		if err := os.WriteFile(path, []byte("exportPattern(\"^[[:alpha:]]+\")\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	if err := writeSourceMetadata(metaDir, "demo", prepared); err != nil {
		t.Fatalf("writeSourceMetadata(current) error = %v", err)
	}
	if err := writeSourceMetadata(filepath.Join(storeLib, ".rs-source-meta"), "demo", prepared); err != nil {
		t.Fatalf("writeSourceMetadata(store) error = %v", err)
	}
	originalTime := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	if err := writePackageStoreState(storeLib, pkg, runtime, PackageStoreState{
		UpdatedAt:  originalTime,
		LastUsedAt: originalTime,
	}); err != nil {
		t.Fatalf("writePackageStoreState() error = %v", err)
	}
	beforeInfo, err := os.Stat(storeDesc)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", storeDesc, err)
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: libraryPath,
			Runtime:     runtime,
		},
		planned: map[string]plannedPackage{"demo": pkg},
		metaDir: metaDir,
	}
	if err := inst.syncPlannedPackageToStore("demo"); err != nil {
		t.Fatalf("syncPlannedPackageToStore() error = %v", err)
	}

	afterInfo, err := os.Stat(storeDesc)
	if err != nil {
		t.Fatalf("Stat(%q) after sync error = %v", storeDesc, err)
	}
	if !os.SameFile(beforeInfo, afterInfo) {
		t.Fatalf("expected matching store entry to be reused without rewriting package files")
	}
	state, err := readPackageStoreState(storeLib)
	if err != nil {
		t.Fatalf("readPackageStoreState() error = %v", err)
	}
	if state.UpdatedAt != originalTime {
		t.Fatalf("state.UpdatedAt = %q, want preserved %q", state.UpdatedAt, originalTime)
	}
	if state.LastUsedAt == "" || state.LastUsedAt == originalTime {
		t.Fatalf("state.LastUsedAt = %q, want refreshed timestamp", state.LastUsedAt)
	}
}

func TestLoadInstalledPackageFromLibraryUsesPackageStoreStateFastPath(t *testing.T) {
	library := t.TempDir()
	pkg := plannedPackage{
		Name:    "demo",
		Version: "0.1.0",
		Source:  sourceGitHub,
		Prepared: &preparedSource{
			Name:            "demo",
			Version:         "0.1.0",
			Source:          sourceGitHub,
			Host:            "github.com",
			Location:        "owner/demo",
			Ref:             "main",
			Commit:          "abc123",
			Subdir:          "pkg",
			Fingerprint:     "feedbeef",
			FingerprintKind: localKindDirSHA256,
		},
	}
	runtime := Runtime{
		Interpreter:     "/opt/demo/R/4.4.3/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.4.3",
	}
	if err := writePackageStoreState(library, pkg, runtime, PackageStoreState{
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("writePackageStoreState() error = %v", err)
	}

	got, ok, err := loadInstalledPackageFromLibrary(library, "demo")
	if err != nil {
		t.Fatalf("loadInstalledPackageFromLibrary() error = %v", err)
	}
	if !ok {
		t.Fatalf("loadInstalledPackageFromLibrary() ok = false, want true")
	}
	if got.Source != sourceGitHub || got.Location != "owner/demo" || got.Commit != "abc123" || got.Subdir != "pkg" || got.FingerprintKind != localKindDirSHA256 {
		t.Fatalf("got = %#v", got)
	}
}

func TestRecordPlannedPackagesInstalledSyncsBatchToStore(t *testing.T) {
	cacheRoot := t.TempDir()
	libraryPath := filepath.Join(cacheRoot, "lib", "current")
	metaDir := filepath.Join(libraryPath, ".rs-source-meta")
	runtime := Runtime{
		Interpreter:     "/opt/demo/R/4.4.3/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.4.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	}
	for _, name := range []string{"cli", "glue"} {
		for _, path := range []string{
			filepath.Join(libraryPath, name),
			metaDir,
		} {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
		}
		if err := os.WriteFile(filepath.Join(libraryPath, name, "DESCRIPTION"), []byte(fmt.Sprintf("Package: %s\nVersion: 1.0.0\nRepository: CRAN\n", name)), 0o644); err != nil {
			t.Fatalf("WriteFile(%s DESCRIPTION) error = %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(libraryPath, name, "NAMESPACE"), []byte("exportPattern(\"^[[:alpha:]]+\")\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s NAMESPACE) error = %v", name, err)
		}
	}

	inst := nativeInstaller{
		req: Request{
			CacheRoot:   cacheRoot,
			LibraryPath: libraryPath,
			Runtime:     runtime,
		},
		planned: map[string]plannedPackage{
			"cli":  {Name: "cli", Version: "1.0.0", Source: sourceCRAN},
			"glue": {Name: "glue", Version: "1.0.0", Source: sourceCRAN},
		},
		metaDir:           metaDir,
		installedPackages: map[string]installedPackage{},
	}
	if err := inst.recordPlannedPackagesInstalled([]string{"cli", "glue"}); err != nil {
		t.Fatalf("recordPlannedPackagesInstalled() error = %v", err)
	}
	for _, name := range []string{"cli", "glue"} {
		if inst.installedPackages[name].Version != "1.0.0" {
			t.Fatalf("installedPackages[%s] = %#v", name, inst.installedPackages[name])
		}
		storeLib := packageStorePathForPlanned(cacheRoot, inst.planned[name], runtime)
		if _, err := os.Stat(filepath.Join(storeLib, name, "DESCRIPTION")); err != nil {
			t.Fatalf("Stat(store %s DESCRIPTION) error = %v", name, err)
		}
	}
}

func TestPackageStorePathForPlannedIncludesRuntimeIdentity(t *testing.T) {
	cacheRoot := t.TempDir()
	pkg := plannedPackage{Name: "cli", Version: "3.6.5", Source: sourceCRAN}
	first := packageStorePathForPlanned(cacheRoot, pkg, Runtime{
		Interpreter:     "/opt/demo/R/4.4.3/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.4.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	})
	second := packageStorePathForPlanned(cacheRoot, pkg, Runtime{
		Interpreter:     "/opt/other/R/4.4.3/bin/Rscript",
		InterpreterKind: "external-conda",
		RVersion:        "4.4.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	})
	if first == second {
		t.Fatalf("packageStorePathForPlanned() should vary across runtime identities")
	}
}

func TestPackageStorePathForPlannedNormalizesManagedInterpreterIdentity(t *testing.T) {
	cacheRoot := t.TempDir()
	pkg := plannedPackage{Name: "cli", Version: "3.6.5", Source: sourceCRAN}
	first := packageStorePathForPlanned(cacheRoot, pkg, Runtime{
		Interpreter:     "/opt/rs-a/versions/4.5.3-linux-amd64/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.5.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	})
	second := packageStorePathForPlanned(cacheRoot, pkg, Runtime{
		Interpreter:     "/other/root/versions/4.5.3-linux-amd64/bin/Rscript",
		InterpreterKind: "managed",
		RVersion:        "4.5.3",
		Platform:        "x86_64-pc-linux-gnu",
		Arch:            "x86_64",
		OS:              "linux-gnu",
		PackageType:     "source",
	})
	if first != second {
		t.Fatalf("packageStorePathForPlanned() should ignore equivalent managed interpreter paths: %q vs %q", first, second)
	}
}

func runtimeLibraryEnvForInstallerTest() string {
	switch runtime.GOOS {
	case "linux":
		return "LD_LIBRARY_PATH"
	case "darwin":
		return "DYLD_FALLBACK_LIBRARY_PATH"
	default:
		return ""
	}
}

func TestInstallPlanLayersPreservesDependencyLayers(t *testing.T) {
	planned := map[string]plannedPackage{
		"jsonlite": {Name: "jsonlite"},
		"glue":     {Name: "glue"},
		"cli": {
			Name: "cli",
			Deps: []packageRequirement{{Name: "glue"}},
		},
		"pillar": {
			Name: "pillar",
			Deps: []packageRequirement{{Name: "cli"}, {Name: "jsonlite"}},
		},
	}

	got := installPlanLayers(planned, []string{"jsonlite", "glue", "cli", "pillar"})
	want := [][]string{
		{"jsonlite", "glue"},
		{"cli"},
		{"pillar"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installPlanLayers() = %v, want %v", got, want)
	}
}

func TestInstallPlanLayersIgnoresDependenciesOutsidePlan(t *testing.T) {
	planned := map[string]plannedPackage{
		"cli": {
			Name: "cli",
			Deps: []packageRequirement{{Name: "methods"}},
		},
		"jsonlite": {Name: "jsonlite"},
	}

	got := installPlanLayers(planned, []string{"cli", "jsonlite"})
	want := [][]string{
		{"cli", "jsonlite"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installPlanLayers() = %v, want %v", got, want)
	}
}

func TestCanParallelInstallPurePackagesDisablesWindows(t *testing.T) {
	oldGOOS := installerGOOS
	t.Cleanup(func() {
		installerGOOS = oldGOOS
	})
	installerGOOS = "windows"

	inst := nativeInstaller{stderr: io.Discard}
	if inst.canParallelInstallPurePackages() {
		t.Fatal("canParallelInstallPurePackages() = true, want false on windows")
	}
}

func TestDownloadReusesPersistentCache(t *testing.T) {
	dir := t.TempDir()
	var hits int
	archive := testTarGzBytes(t, map[string]string{
		"pkg/DESCRIPTION": "Package: pkg\nVersion: 1.0.0\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(archive)
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

func TestDownloadRemovesCorruptCachedTarballAndRedownloads(t *testing.T) {
	dir := t.TempDir()
	var hits int
	archive := testTarGzBytes(t, map[string]string{
		"pkg/DESCRIPTION": "Package: pkg\nVersion: 1.0.0\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(archive)
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

	target := filepath.Join(inst.downloadRoot, downloadCacheName(server.URL+"/pkg.tar.gz", "pkg_1.0.0.tar.gz"))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(target dir) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("corrupt"), 0o644); err != nil {
		t.Fatalf("WriteFile(corrupt cache) error = %v", err)
	}

	got, err := inst.download(server.URL+"/pkg.tar.gz", "pkg_1.0.0.tar.gz")
	if err != nil {
		t.Fatalf("download() error = %v", err)
	}
	if got != target {
		t.Fatalf("download() = %q, want %q", got, target)
	}
	if hits != 1 {
		t.Fatalf("download server hits = %d, want 1", hits)
	}
	if err := validateCachedDownload(target); err != nil {
		t.Fatalf("validateCachedDownload(downloaded file) error = %v", err)
	}
}

func TestInstallRepoPackageReusesPrefetchedDownload(t *testing.T) {
	dir := t.TempDir()
	var hits int
	archive := testTarGzBytes(t, map[string]string{
		"cli/DESCRIPTION": "Package: cli\nVersion: 3.6.5\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	rLog := filepath.Join(dir, "r-args.txt")
	rBinary := writeTestCommand(
		t,
		dir,
		"R",
		fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", rLog),
		fmt.Sprintf("@echo off\r\n> %q echo %%*\r\n", rLog),
	)

	inst := nativeInstaller{
		tempRoot:       filepath.Join(dir, "tmp"),
		downloadRoot:   filepath.Join(dir, "downloads"),
		metaDir:        filepath.Join(dir, "meta"),
		stdout:         io.Discard,
		stderr:         io.Discard,
		httpClient:     server.Client(),
		rBinary:        rBinary,
		prefetchedRepo: map[string]string{},
		req: Request{
			WorkDir:     dir,
			CacheRoot:   filepath.Join(dir, "cache"),
			LibraryPath: filepath.Join(dir, "lib"),
			Environment: []string{"PATH=" + dir},
		},
	}
	for _, path := range []string{inst.tempRoot, inst.downloadRoot, inst.metaDir, inst.req.LibraryPath, inst.req.CacheRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	record := repoRecord{
		Name:       "cli",
		Version:    "3.6.5",
		Source:     sourceCRAN,
		TarballURL: server.URL + "/cli_3.6.5.tar.gz",
		DepsLoaded: true,
	}
	inst.planned = map[string]plannedPackage{
		"cli": {
			Name:    "cli",
			Version: "3.6.5",
			Source:  sourceCRAN,
			Repo:    &record,
		},
	}
	inst.order = []string{"cli"}
	inst.installedPackages = map[string]installedPackage{}

	if err := inst.prefetchPlannedPackages(); err != nil {
		t.Fatalf("prefetchPlannedPackages() error = %v", err)
	}
	if hits != 1 {
		t.Fatalf("prefetch hits = %d, want 1", hits)
	}
	prefetched := inst.prefetchedRepo[record.TarballURL]
	if strings.TrimSpace(prefetched) == "" {
		t.Fatalf("prefetchedRepo missing %s", record.TarballURL)
	}

	if err := inst.installRepoPackage(record); err != nil {
		t.Fatalf("installRepoPackage() error = %v", err)
	}
	if hits != 1 {
		t.Fatalf("installRepoPackage() should reuse prefetched file, hits = %d, want 1", hits)
	}
	data, err := os.ReadFile(rLog)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", rLog, err)
	}
	if !strings.Contains(string(data), prefetched) {
		t.Fatalf("R install args = %q, want prefetched target %q", string(data), prefetched)
	}
}

func TestInstallRepoPackageReadsDescriptionFromDownloadedArtifact(t *testing.T) {
	dir := t.TempDir()
	var packageHits int
	archive := testTarGzBytes(t, map[string]string{
		"cli/DESCRIPTION": "Package: cli\nVersion: 3.6.5\nNeedsCompilation: yes\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		packageHits++
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	rLog := filepath.Join(dir, "r-args.txt")
	rBinary := writeTestCommand(
		t,
		dir,
		"R",
		fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", rLog),
		fmt.Sprintf("@echo off\r\n> %q echo %%*\r\n", rLog),
	)
	successUnix := "#!/bin/sh\nexit 0\n"
	successWindows := "@echo off\r\nexit /b 0\r\n"
	requiredTools := []string{"make", "gcc"}
	if runtime.GOOS != "windows" {
		requiredTools = append(requiredTools, "g++", "gfortran")
	}
	for _, tool := range requiredTools {
		writeTestCommand(t, dir, tool, successUnix, successWindows)
	}

	inst := nativeInstaller{
		tempRoot:       filepath.Join(dir, "tmp"),
		downloadRoot:   filepath.Join(dir, "downloads"),
		metaDir:        filepath.Join(dir, "meta"),
		stdout:         io.Discard,
		stderr:         io.Discard,
		httpClient:     server.Client(),
		rBinary:        rBinary,
		prefetchedRepo: map[string]string{},
		req: Request{
			WorkDir:     dir,
			CacheRoot:   filepath.Join(dir, "cache"),
			LibraryPath: filepath.Join(dir, "lib"),
			Environment: []string{"PATH=" + dir},
		},
	}
	for _, path := range []string{inst.tempRoot, inst.downloadRoot, inst.metaDir, inst.req.LibraryPath, inst.req.CacheRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	record := repoRecord{
		Name:       "cli",
		Version:    "3.6.5",
		Source:     sourceCRAN,
		TarballURL: server.URL + "/cli_3.6.5.tar.gz",
		DepsLoaded: false,
	}
	if err := inst.installRepoPackage(record); err != nil {
		t.Fatalf("installRepoPackage() error = %v", err)
	}
	if packageHits != 1 {
		t.Fatalf("package HTTP hits = %d, want 1 download-only hit", packageHits)
	}
	if _, err := os.Stat(rLog); err != nil {
		t.Fatalf("expected R install command to run: %v", err)
	}
}

func TestPrefetchPlannedPackagesLeavesRepoMetadataLazyButCached(t *testing.T) {
	dir := t.TempDir()
	archive := testTarGzBytes(t, map[string]string{
		"pkg/DESCRIPTION": "Package: pkg\nVersion: 1.0.0\nImports: cli\nNeedsCompilation: yes\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	record := repoRecord{
		Name:       "pkg",
		Version:    "1.0.0",
		Source:     sourceCRAN,
		TarballURL: server.URL + "/pkg_1.0.0.tar.gz",
		DepsLoaded: false,
	}
	inst := nativeInstaller{
		tempRoot:       filepath.Join(dir, "tmp"),
		downloadRoot:   filepath.Join(dir, "downloads"),
		stderr:         io.Discard,
		httpClient:     server.Client(),
		prefetchedRepo: map[string]string{},
		planned: map[string]plannedPackage{
			"pkg": {
				Name:    "pkg",
				Version: "1.0.0",
				Source:  sourceCRAN,
				Repo:    &record,
			},
		},
		order: []string{"pkg"},
	}
	for _, path := range []string{inst.tempRoot, inst.downloadRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	if err := inst.prefetchPlannedPackages(); err != nil {
		t.Fatalf("prefetchPlannedPackages() error = %v", err)
	}
	got := inst.planned["pkg"]
	if got.Repo == nil || got.Repo.DepsLoaded {
		t.Fatalf("planned repo should remain lazy after prefetch: %#v", got.Repo)
	}
	desc, err := inst.loadPrefetchedRepoDescription("pkg")
	if err != nil {
		t.Fatalf("loadPrefetchedRepoDescription() error = %v", err)
	}
	if !desc.NeedsCompilation {
		t.Fatalf("desc.NeedsCompilation = false, want true")
	}
	if !reflect.DeepEqual(desc.Dependencies, []packageRequirement{{Name: "cli"}}) {
		t.Fatalf("desc.Dependencies = %v, want cli dependency", desc.Dependencies)
	}
}

func TestPrefetchPlannedPackagesReportsSummary(t *testing.T) {
	dir := t.TempDir()
	cachedArchive := testTarGzBytes(t, map[string]string{
		"cached/DESCRIPTION": "Package: cached\nVersion: 1.0.0\nNeedsCompilation: no\n",
	})
	downloadedArchive := testTarGzBytes(t, map[string]string{
		"fresh/DESCRIPTION": "Package: fresh\nVersion: 2.0.0\nImports: cli\nNeedsCompilation: yes\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(downloadedArchive)
	}))
	defer server.Close()

	cachedRecord := repoRecord{
		Name:       "cached",
		Version:    "1.0.0",
		Source:     sourceCRAN,
		TarballURL: "https://example.test/src/contrib/cached_1.0.0.tar.gz",
		DepsLoaded: false,
	}
	freshRecord := repoRecord{
		Name:       "fresh",
		Version:    "2.0.0",
		Source:     sourceCRAN,
		TarballURL: server.URL + "/fresh_2.0.0.tar.gz",
		DepsLoaded: false,
	}

	stderr := &bytes.Buffer{}
	inst := nativeInstaller{
		tempRoot:       filepath.Join(dir, "tmp"),
		downloadRoot:   filepath.Join(dir, "downloads"),
		stderr:         stderr,
		httpClient:     server.Client(),
		prefetchedRepo: map[string]string{},
		planned: map[string]plannedPackage{
			"cached": {
				Name:    "cached",
				Version: "1.0.0",
				Source:  sourceCRAN,
				Repo:    &cachedRecord,
			},
			"fresh": {
				Name:    "fresh",
				Version: "2.0.0",
				Source:  sourceCRAN,
				Repo:    &freshRecord,
			},
		},
		order: []string{"cached", "fresh"},
	}
	for _, path := range []string{inst.tempRoot, inst.downloadRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}
	cachedPath := inst.repoDownloadPath(cachedRecord)
	if err := os.MkdirAll(filepath.Dir(cachedPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(cachedPath), err)
	}
	if err := os.WriteFile(cachedPath, cachedArchive, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", cachedPath, err)
	}

	if err := inst.prefetchPlannedPackages(); err != nil {
		t.Fatalf("prefetchPlannedPackages() error = %v", err)
	}

	log := stderr.String()
	if !strings.Contains(log, "prefetching 2 package artifact(s)") {
		t.Fatalf("prefetch log = %q, want prefetch stage", log)
	}
	if !strings.Contains(log, "prefetched 2 package artifact(s), downloaded 1, reused 1 cached") {
		t.Fatalf("prefetch log = %q, want summary counts", log)
	}
	if strings.Contains(log, "reusing cached") {
		t.Fatalf("prefetch log = %q, want multi-prefetch cache reuse lines to stay aggregated", log)
	}
	if strings.Contains(log, "downloading fresh_2.0.0.tar.gz") {
		t.Fatalf("prefetch log = %q, want multi-prefetch download progress to stay aggregated", log)
	}
}

func TestPrefetchPlannedPackagesEmitsStructuredEvents(t *testing.T) {
	dir := t.TempDir()
	archive := testTarGzBytes(t, map[string]string{
		"fresh/DESCRIPTION": "Package: fresh\nVersion: 2.0.0\nNeedsCompilation: no\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	record := repoRecord{
		Name:       "fresh",
		Version:    "2.0.0",
		Source:     sourceCRAN,
		TarballURL: server.URL + "/fresh_2.0.0.tar.gz",
		DepsLoaded: false,
	}
	events := []eventstream.Event{}
	inst := nativeInstaller{
		tempRoot:       filepath.Join(dir, "tmp"),
		downloadRoot:   filepath.Join(dir, "downloads"),
		stderr:         io.Discard,
		httpClient:     server.Client(),
		prefetchedRepo: map[string]string{},
		req: Request{
			ScriptPath: filepath.Join(dir, "analysis.R"),
			Events: func(event eventstream.Event) {
				events = append(events, event)
			},
		},
		planned: map[string]plannedPackage{
			"fresh": {
				Name:    "fresh",
				Version: "2.0.0",
				Source:  sourceCRAN,
				Repo:    &record,
			},
		},
		order: []string{"fresh"},
	}
	for _, path := range []string{inst.tempRoot, inst.downloadRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	if err := inst.prefetchPlannedPackages(); err != nil {
		t.Fatalf("prefetchPlannedPackages() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("prefetch events = %v, want 2", events)
	}
	if events[0].Kind != "prefetch_start" || events[1].Kind != "prefetch_complete" {
		t.Fatalf("prefetch event kinds = %v", []string{events[0].Kind, events[1].Kind})
	}
}

func TestInstallerNotefPrefixesMessage(t *testing.T) {
	var buf bytes.Buffer
	inst := nativeInstaller{stderr: &buf}
	inst.notef("native package install completed in %s", "12s")
	if got := buf.String(); got != "[rs] native package install completed in 12s\n" {
		t.Fatalf("notef() = %q", got)
	}
}

func TestLogInstallCompletionIncludesSlowInstallSummary(t *testing.T) {
	var buf bytes.Buffer
	inst := nativeInstaller{
		stderr: &buf,
		installTimings: []installTiming{
			{label: "sass", duration: 3*time.Minute + 30*time.Second},
			{label: "ragg", duration: 94 * time.Second},
		},
	}
	inst.logInstallCompletion(10*time.Minute, installSummaryStats{
		installedCount:       12,
		compiledInstallCount: 4,
		reusedCount:          3,
	})

	got := buf.String()
	if !strings.Contains(got, "[rs] slow installs: sass 3m30s, ragg 1m34s\n") {
		t.Fatalf("logInstallCompletion() missing slow install summary:\n%s", got)
	}
	if !strings.Contains(got, "[rs] native package install completed in 10m00s (12 installed, 4 compiled, 3 reused)\n") {
		t.Fatalf("logInstallCompletion() missing final summary:\n%s", got)
	}
}

func TestLogInstallCompletionEmitsStructuredEvent(t *testing.T) {
	events := []eventstream.Event{}
	inst := nativeInstaller{
		stderr: io.Discard,
		req: Request{
			ScriptPath: "/tmp/analysis.R",
			Events: func(event eventstream.Event) {
				events = append(events, event)
			},
		},
	}

	inst.logInstallCompletion(95*time.Second, installSummaryStats{
		reusedCount:          2,
		installedCount:       5,
		compiledInstallCount: 3,
	})

	if len(events) != 1 {
		t.Fatalf("logInstallCompletion() events = %v, want 1", events)
	}
	if events[0].Kind != "native_install_complete" {
		t.Fatalf("logInstallCompletion() event kind = %q", events[0].Kind)
	}
	if events[0].Duration == "" {
		t.Fatalf("logInstallCompletion() duration empty: %#v", events[0])
	}
}

func TestDefaultInstallerLogStoryStaysCompact(t *testing.T) {
	var log bytes.Buffer
	progresscmd.Stage(&log, "prefetching 99 package artifact(s)")
	progresscmd.Stage(&log, "prefetched 99 package artifact(s), downloaded 99")
	progresscmd.Stage(&log, formatBuildToolsCheckStage("stringi"))
	progresscmd.Stage(&log, formatDependencyLayerPlan(2, 5, 0, 3))

	inst := nativeInstaller{
		stderr: &log,
		installTimings: []installTiming{
			{label: "sass", duration: 3*time.Minute + 30*time.Second},
			{label: "ragg", duration: 94 * time.Second},
			{label: "stringi", duration: 53 * time.Second},
		},
	}
	inst.logInstallCompletion(34*time.Minute+28*time.Second, installSummaryStats{
		installedCount:       99,
		compiledInstallCount: 28,
	})

	got := log.String()
	for _, want := range []string{
		"[rs] prefetching 99 package artifact(s)",
		"[rs] prefetched 99 package artifact(s), downloaded 99",
		"[rs] validating source build toolchain for stringi",
		"[rs] dependency layer 2/5: 3 compiled package(s)",
		"[rs] slow installs: sass 3m30s, ragg 1m34s, stringi 53s",
		"[rs] native package install completed in 34m28s (99 installed, 28 compiled)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact log story missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"reused 1 stored package",
		"reused 1 cached package from 1 library snapshot",
		"installing 8 package(s) across 4 parallel batches",
		"installed cli in 12s",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("compact log story unexpectedly contains %q:\n%s", unwanted, got)
		}
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 6 {
		t.Fatalf("compact log story line budget = %d, want 6:\n%s", len(lines), got)
	}
}

func TestFetchPackagesFileRetriesBodyReadTimeout(t *testing.T) {
	packages := "Package: cli\nVersion: 3.6.5\n\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(packages))
	_ = gz.Close()

	attempts := 0
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: timeoutReadCloser{read: func(p []byte) (int, error) {
						return 0, timeoutErr{}
					}},
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
			}, nil
		}),
	}

	data, err := fetchPackagesFile(client, "https://example.test/src/contrib/PACKAGES.gz")
	if err != nil {
		t.Fatalf("fetchPackagesFile() error = %v", err)
	}
	if string(data) != packages {
		t.Fatalf("fetchPackagesFile() = %q, want %q", string(data), packages)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestFetchPackagesFileCachedReusesFreshCache(t *testing.T) {
	cacheDir := t.TempDir()
	packages := "Package: cli\nVersion: 3.6.5\n\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(packages))
	_ = gz.Close()

	attempts := 0
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
			}, nil
		}),
	}

	rawURL := "https://example.test/src/contrib/PACKAGES.gz"
	data, err := fetchPackagesFileCached(client, rawURL, cacheDir)
	if err != nil {
		t.Fatalf("fetchPackagesFileCached(first) error = %v", err)
	}
	if string(data) != packages {
		t.Fatalf("fetchPackagesFileCached(first) = %q, want %q", string(data), packages)
	}

	data, err = fetchPackagesFileCached(client, rawURL, cacheDir)
	if err != nil {
		t.Fatalf("fetchPackagesFileCached(second) error = %v", err)
	}
	if string(data) != packages {
		t.Fatalf("fetchPackagesFileCached(second) = %q, want %q", string(data), packages)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 with fresh cache reuse", attempts)
	}
}

func TestFetchPackagesFileCachedFallsBackToStaleCache(t *testing.T) {
	cacheDir := t.TempDir()
	rawURL := "https://example.test/src/contrib/PACKAGES.gz"
	cached := []byte("Package: cli\nVersion: 3.6.5\n\n")
	cachePath := repoIndexCachePath(cacheDir, rawURL)
	if err := writeRepoIndexCache(cachePath, cached); err != nil {
		t.Fatalf("writeRepoIndexCache() error = %v", err)
	}
	staleTime := time.Now().Add(-repoIndexCacheTTL - time.Minute)
	if err := os.Chtimes(cachePath, staleTime, staleTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", cachePath, err)
	}

	attempts := 0
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return nil, timeoutErr{}
		}),
	}

	data, err := fetchPackagesFileCached(client, rawURL, cacheDir)
	if err != nil {
		t.Fatalf("fetchPackagesFileCached() error = %v", err)
	}
	if string(data) != string(cached) {
		t.Fatalf("fetchPackagesFileCached() = %q, want stale cache %q", string(data), string(cached))
	}
	if attempts != httpRetryAttempts {
		t.Fatalf("attempts = %d, want %d retry attempts before stale fallback", attempts, httpRetryAttempts)
	}
}

func TestEnsureCRANArchiveCandidatesReusesCachedArchivePage(t *testing.T) {
	cacheRoot := t.TempDir()
	archiveHTML := `<html><body><a href="cli_3.6.4.tar.gz">cli_3.6.4.tar.gz</a></body></html>`
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/src/contrib/Archive/cli/" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, archiveHTML)
	}))
	defer server.Close()

	inst := nativeInstaller{
		req: Request{
			Repo: server.URL,
		},
		downloadRoot:      filepath.Join(cacheRoot, "downloads"),
		stderr:            io.Discard,
		httpClient:        server.Client(),
		cranIndex:         map[string][]repoRecord{},
		cranArchiveLoaded: map[string]bool{},
	}
	if err := inst.ensureCRANArchiveCandidates("cli"); err != nil {
		t.Fatalf("ensureCRANArchiveCandidates(first) error = %v", err)
	}
	if hits != 1 {
		t.Fatalf("server hits after first load = %d, want 1", hits)
	}
	if len(inst.cranIndex["cli"]) != 1 || inst.cranIndex["cli"][0].Version != "3.6.4" {
		t.Fatalf("cranIndex[cli] = %v, want cached archive candidate", inst.cranIndex["cli"])
	}

	inst2 := nativeInstaller{
		req: Request{
			Repo: server.URL,
		},
		downloadRoot: filepath.Join(cacheRoot, "downloads"),
		stderr:       io.Discard,
		httpClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return nil, timeoutErr{}
			}),
		},
		cranIndex:         map[string][]repoRecord{},
		cranArchiveLoaded: map[string]bool{},
	}
	if err := inst2.ensureCRANArchiveCandidates("cli"); err != nil {
		t.Fatalf("ensureCRANArchiveCandidates(second) error = %v", err)
	}
	if hits != 1 {
		t.Fatalf("server hits after cached load = %d, want still 1", hits)
	}
	if len(inst2.cranIndex["cli"]) != 1 || inst2.cranIndex["cli"][0].Version != "3.6.4" {
		t.Fatalf("cached cranIndex[cli] = %v, want archive candidate", inst2.cranIndex["cli"])
	}
}

func TestDownloadCacheNamePreservesOriginalBasename(t *testing.T) {
	got := downloadCacheName("https://cloud.r-project.org/bin/windows/contrib/4.4/jsonlite_2.0.0.zip", "jsonlite_2.0.0.zip")
	if filepath.Base(got) != "jsonlite_2.0.0.zip" {
		t.Fatalf("filepath.Base(downloadCacheName()) = %q, want jsonlite_2.0.0.zip", filepath.Base(got))
	}
	if filepath.Dir(got) == "." {
		t.Fatalf("downloadCacheName() = %q, want hashed subdirectory", got)
	}
}

func testTarGzBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var gzBuf bytes.Buffer
	gzWriter := gzip.NewWriter(&gzBuf)
	tarWriter := tar.NewWriter(gzWriter)
	for name, body := range files {
		data := []byte(body)
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("WriteHeader(%q) error = %v", name, err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatalf("Write(%q) error = %v", name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("tarWriter.Close() error = %v", err)
	}
	if err := gzWriter.Close(); err != nil {
		t.Fatalf("gzWriter.Close() error = %v", err)
	}
	return gzBuf.Bytes()
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
	if !strings.Contains(err.Error(), "build-essential gfortran cmake") {
		t.Fatalf("ensureLinuxSourceBuildTools() error = %v", err)
	}
	if !strings.Contains(err.Error(), brand.Command("toolchain detect")) {
		t.Fatalf("ensureLinuxSourceBuildTools() error missing rootless guidance = %v", err)
	}
	if !strings.Contains(err.Error(), brand.Command("doctor --toolchain-only")) {
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

func TestEnsureLinuxSourceBuildToolsFailsEarlyWhenFSPackageNeedsMissingCMake(t *testing.T) {
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
	writeTestCommand(t, binDir, "gcc", successUnix, successWindows)
	writeTestCommand(t, binDir, "g++", successUnix, successWindows)
	writeTestCommand(t, binDir, "gfortran", successUnix, successWindows)
	writeTestCommand(t, binDir, "make", successUnix, successWindows)

	err := ensureLinuxSourceBuildTools("fs", []string{"PATH=" + binDir})
	if err == nil {
		t.Fatal("ensureLinuxSourceBuildTools() error = nil, want missing cmake guidance")
	}
	if !strings.Contains(err.Error(), "cmake >= 3.10 was not found on PATH") {
		t.Fatalf("ensureLinuxSourceBuildTools() error = %v", err)
	}
}

func TestEnsureLinuxSourceBuildToolsFailsEarlyWhenFSPackageNeedsNewerCMake(t *testing.T) {
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
	writeTestCommand(t, binDir, "gcc", successUnix, successWindows)
	writeTestCommand(t, binDir, "g++", successUnix, successWindows)
	writeTestCommand(t, binDir, "gfortran", successUnix, successWindows)
	writeTestCommand(t, binDir, "make", successUnix, successWindows)
	writeTestCommand(t, binDir, "cmake", "#!/bin/sh\necho 'cmake version 2.8.12.2'\n", "@echo off\r\necho cmake version 2.8.12.2\r\n")

	err := ensureLinuxSourceBuildTools("fs", []string{"PATH=" + binDir})
	if err == nil {
		t.Fatal("ensureLinuxSourceBuildTools() error = nil, want old cmake guidance")
	}
	if !strings.Contains(err.Error(), "cmake >= 3.10 is required, but the active cmake is 2.8.12") {
		t.Fatalf("ensureLinuxSourceBuildTools() error = %v", err)
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

func TestEnsurePackageBuildToolsReadyCachesSuccessfulCheck(t *testing.T) {
	original := installerEnsureBuildTools
	t.Cleanup(func() {
		installerEnsureBuildTools = original
	})
	calls := 0
	installerEnsureBuildTools = func(pkg string, env []string) error {
		calls++
		return nil
	}

	inst := nativeInstaller{}
	if err := inst.ensurePackageBuildToolsReady("stringi"); err != nil {
		t.Fatalf("ensurePackageBuildToolsReady(first) error = %v", err)
	}
	if err := inst.ensurePackageBuildToolsReady("xml2"); err != nil {
		t.Fatalf("ensurePackageBuildToolsReady(second) error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("installerEnsureBuildTools calls = %d, want 1", calls)
	}
}

func TestEnsurePackageBuildToolsReadyReportsStageOnce(t *testing.T) {
	original := installerEnsureBuildTools
	t.Cleanup(func() {
		installerEnsureBuildTools = original
	})
	installerEnsureBuildTools = func(pkg string, env []string) error {
		return nil
	}

	var stderr bytes.Buffer
	inst := nativeInstaller{stderr: &stderr}
	if err := inst.ensurePackageBuildToolsReady("stringi"); err != nil {
		t.Fatalf("ensurePackageBuildToolsReady(first) error = %v", err)
	}
	if err := inst.ensurePackageBuildToolsReady("xml2"); err != nil {
		t.Fatalf("ensurePackageBuildToolsReady(second) error = %v", err)
	}

	log := stderr.String()
	if strings.Count(log, "validating source build toolchain") != 1 {
		t.Fatalf("build tools stage log = %q, want one stage line", log)
	}
	if !strings.Contains(log, "validating source build toolchain for stringi") {
		t.Fatalf("build tools stage log = %q, want first package name", log)
	}
}

func TestEnsurePackageBuildToolsReadyEmitsStructuredEvent(t *testing.T) {
	original := installerEnsureBuildTools
	t.Cleanup(func() {
		installerEnsureBuildTools = original
	})
	installerEnsureBuildTools = func(pkg string, env []string) error {
		return nil
	}

	events := []eventstream.Event{}
	inst := nativeInstaller{
		stderr: io.Discard,
		req: Request{
			ScriptPath: "/tmp/analysis.R",
			Events: func(event eventstream.Event) {
				events = append(events, event)
			},
		},
	}
	if err := inst.ensurePackageBuildToolsReady("stringi"); err != nil {
		t.Fatalf("ensurePackageBuildToolsReady() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ensurePackageBuildToolsReady() events = %v, want 1", events)
	}
	if events[0].Kind != "build_toolchain_validation" || events[0].Package != "stringi" {
		t.Fatalf("ensurePackageBuildToolsReady() event = %#v", events[0])
	}
}

func TestDependencyLayerHelpersEmitStructuredEvents(t *testing.T) {
	events := []eventstream.Event{}
	inst := nativeInstaller{
		req: Request{
			ScriptPath: "/tmp/analysis.R",
			Events: func(event eventstream.Event) {
				events = append(events, event)
			},
		},
	}

	inst.emitDependencyLayerStart(1, 3, 2, 1)
	inst.emitDependencyLayerComplete(1, 3, 2, 1, 2, 1, 37*time.Second)

	if len(events) != 2 {
		t.Fatalf("dependency layer events = %v, want 2", events)
	}
	if events[0].Kind != "dependency_layer_start" || events[1].Kind != "dependency_layer_complete" {
		t.Fatalf("dependency layer event kinds = %v", []string{events[0].Kind, events[1].Kind})
	}
	if events[1].Duration == "" {
		t.Fatalf("dependency layer complete duration empty: %#v", events[1])
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

	advice := rootlessToolchainAdvice(nil)
	if !strings.Contains(advice, "detected recommended preset on this machine: homebrew") {
		t.Fatalf("rootlessToolchainAdvice() = %q", advice)
	}
	if !strings.Contains(advice, filepath.Join(homebrewPrefix, "bin", "brew")) {
		t.Fatalf("rootlessToolchainAdvice() = %q", advice)
	}
	if !strings.Contains(advice, brand.Command("init --toolchain-preset homebrew")) {
		t.Fatalf("rootlessToolchainAdvice() = %q", advice)
	}
}

func TestRootlessToolchainAdvicePrefersActiveAdoptedEnvaEnvironment(t *testing.T) {
	dir := t.TempDir()
	setTestHomeDir(t, dir)
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", binDir, err)
	}
	writeTestCommand(t, binDir, "enva", "#!/bin/sh\nexit 0\n", "@echo off\r\nexit /b 0\r\n")

	actualPrefix := filepath.Join(dir, "MyMiniconda", "envs", "rs-sysdeps")
	for _, path := range []string{
		actualPrefix,
		filepath.Join(actualPrefix, "lib", "pkgconfig"),
		filepath.Join(actualPrefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	env := toolchainenv.Apply([]string{"PATH=" + binDir}, []string{actualPrefix}, []string{
		filepath.Join(actualPrefix, "lib", "pkgconfig"),
		filepath.Join(actualPrefix, "share", "pkgconfig"),
	})
	advice := rootlessToolchainAdvice(env)
	if !strings.Contains(advice, "detected recommended preset on this machine: enva") {
		t.Fatalf("rootlessToolchainAdvice(env) = %q", advice)
	}
	if !strings.Contains(advice, actualPrefix) {
		t.Fatalf("rootlessToolchainAdvice(env) = %q", advice)
	}
}

func TestToolchainProbeExampleUsesDirectCompilerForAdoptedEnvaEnvironment(t *testing.T) {
	oldGOOS := installerGOOS
	t.Cleanup(func() {
		installerGOOS = oldGOOS
	})
	installerGOOS = "linux"

	dir := t.TempDir()
	actualPrefix := filepath.Join(dir, "MyMiniconda", "envs", "rs-sysdeps")
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", binDir, err)
	}
	writeTestCommand(t, binDir, "enva", "#!/bin/sh\nexit 0\n", "@echo off\r\nexit /b 0\r\n")
	compilerDir := filepath.Join(actualPrefix, "bin")
	if err := os.MkdirAll(compilerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", compilerDir, err)
	}
	if err := os.WriteFile(filepath.Join(compilerDir, "x86_64-conda-linux-gnu-c++"), []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(compiler) error = %v", err)
	}
	env := toolchainenv.Apply([]string{"PATH=" + binDir + string(os.PathListSeparator) + "/usr/bin"}, []string{actualPrefix}, []string{
		filepath.Join(actualPrefix, "lib", "pkgconfig"),
		filepath.Join(actualPrefix, "share", "pkgconfig"),
	})
	got := toolchainProbeExample(env)
	want := `"` + filepath.Join(actualPrefix, "bin", "x86_64-conda-linux-gnu-c++") + `" smoke.cpp -o smoke`
	if got != want {
		t.Fatalf("toolchainProbeExample() = %q, want %q", got, want)
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
