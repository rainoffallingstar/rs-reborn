package rmanager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rainoffallingstar/rs-reborn/internal/brand"
)

func writeFakeManagedRscript(t *testing.T, root, version string) string {
	t.Helper()

	path := filepath.Join(root, "bin", rscriptExecutableName())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "-e" ]; then
	printf "%s"
	exit 0
fi
echo "unexpected args: $*" >&2
exit 1
`, version)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func setTestHomeDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func TestResolveVersionOrPathAcceptsExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := writeFakeManagedRscript(t, dir, "4.4.3")

	got, err := ResolveVersionOrPath(path)
	if err != nil {
		t.Fatalf("ResolveVersionOrPath() error = %v", err)
	}
	if got != path {
		t.Fatalf("ResolveVersionOrPath() = %q, want %q", got, path)
	}
}

func TestResolveVersionOrPathUsesHighestInstalledManagedRelease(t *testing.T) {
	root := t.TempDir()
	t.Setenv(managerRootEnv, root)

	oldPath := writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.4.3-"+runtime.GOOS+"-"+runtime.GOARCH), "4.4.3")
	newPath := writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.5.2-"+runtime.GOOS+"-"+runtime.GOARCH), "4.5.2")

	got, err := ResolveVersionOrPath("release")
	if err != nil {
		t.Fatalf("ResolveVersionOrPath() error = %v", err)
	}
	if got != newPath {
		t.Fatalf("ResolveVersionOrPath() = %q, want %q (old=%q)", got, newPath, oldPath)
	}
}

func TestResolveVersionOrPathUsesHighestInstalledPartialVersion(t *testing.T) {
	root := t.TempDir()
	t.Setenv(managerRootEnv, root)

	oldPath := writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.4.2-"+runtime.GOOS+"-"+runtime.GOARCH), "4.4.2")
	newPath := writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.4.5-"+runtime.GOOS+"-"+runtime.GOARCH), "4.4.5")

	got, err := ResolveVersionOrPath("4.4")
	if err != nil {
		t.Fatalf("ResolveVersionOrPath() error = %v", err)
	}
	if got != newPath {
		t.Fatalf("ResolveVersionOrPath() = %q, want %q (old=%q)", got, newPath, oldPath)
	}
}

func TestResolveVersionOrPathRejectsUnsupportedSelector(t *testing.T) {
	_, err := ResolveVersionOrPath("oldrel")
	if err == nil {
		t.Fatal("ResolveVersionOrPath() error = nil, want unsupported selector")
	}
	if !strings.Contains(err.Error(), `native R manager does not yet support selector "oldrel"`) {
		t.Fatalf("ResolveVersionOrPath() error = %v", err)
	}
}

func TestListShowsManagedAndExternalInstallations(t *testing.T) {
	root := t.TempDir()
	t.Setenv(managerRootEnv, root)

	managedPath := writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.4.3-"+runtime.GOOS+"-"+runtime.GOARCH), "4.4.3")
	externalRoot := t.TempDir()
	externalPath := writeFakeManagedRscript(t, externalRoot, "4.3.2")
	t.Cleanup(func() {
		rscriptLookPath = execLookPath
		nativeLookPath = execNativeLookPath
	})
	rscriptLookPath = func(file string) (string, error) { return "", fmt.Errorf("missing %s", file) }
	nativeLookPath = func(file string) (string, error) {
		if file == "Rscript" {
			return externalPath, nil
		}
		return "", fmt.Errorf("missing %s", file)
	}
	if err := setCurrentInstall(filepath.Dir(filepath.Dir(managedPath))); err != nil {
		t.Fatalf("setCurrentInstall() error = %v", err)
	}

	var stdout bytes.Buffer
	if err := List(&stdout, io.Discard); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "managed") || !strings.Contains(out, managedPath) {
		t.Fatalf("List() output = %q, want managed entry", out)
	}
	if !strings.Contains(out, "external") || !strings.Contains(out, externalPath) {
		t.Fatalf("List() output = %q, want external entry", out)
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

func TestVersionMatchesSpec(t *testing.T) {
	cases := []struct {
		spec   string
		actual string
		want   bool
	}{
		{spec: "4.4", actual: "4.4.3", want: true},
		{spec: "4.4.3", actual: "4.4.3", want: true},
		{spec: "4.4.3", actual: "4.4.4", want: false},
		{spec: "4.5", actual: "4.4.3", want: false},
		{spec: "release", actual: "4.5.3", want: true},
	}

	for _, tc := range cases {
		if got := VersionMatchesSpec(tc.spec, tc.actual); got != tc.want {
			t.Fatalf("VersionMatchesSpec(%q, %q) = %v, want %v", tc.spec, tc.actual, got, tc.want)
		}
	}
}

func TestAutoInstallREnabledIgnoresLegacyRigAlias(t *testing.T) {
	t.Setenv(autoInstallREnv, "")
	t.Setenv("RS_AUTO_INSTALL_RIG", "1")

	if AutoInstallREnabled() {
		t.Fatalf("AutoInstallREnabled() = true, want false when only RS_AUTO_INSTALL_RIG is set")
	}
}

func TestValidateVersionSelectorRejectsUnsupportedSelectors(t *testing.T) {
	for _, spec := range []string{"oldrel", "devel", "next"} {
		err := ValidateVersionSelector(spec)
		if err == nil {
			t.Fatalf("ValidateVersionSelector(%q) error = nil, want unsupported selector", spec)
		}
		if !strings.Contains(err.Error(), `native R manager does not yet support selector "`) {
			t.Fatalf("ValidateVersionSelector(%q) error = %v", spec, err)
		}
	}
}

func TestResolveConcreteVersionUsesAvailableVersionsForPartialSelector(t *testing.T) {
	oldClient := nativeHTTPClient
	t.Cleanup(func() {
		nativeHTTPClient = oldClient
	})
	nativeHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := io.NopCloser(strings.NewReader(`{"r_versions":["next","4.5.3","4.4.7","4.4.2"]}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       body,
				Header:     make(http.Header),
			}, nil
		}),
	}

	got, err := resolveConcreteVersion("4.4")
	if err != nil {
		t.Fatalf("resolveConcreteVersion() error = %v", err)
	}
	if got != "4.4.7" {
		t.Fatalf("resolveConcreteVersion() = %q, want 4.4.7", got)
	}
}

func TestResolveConcreteVersionRejectsUnknownExplicitVersion(t *testing.T) {
	oldClient := nativeHTTPClient
	t.Cleanup(func() {
		nativeHTTPClient = oldClient
	})
	nativeHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := io.NopCloser(strings.NewReader(`{"r_versions":["4.5.3","4.4.7","4.4.2"]}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       body,
				Header:     make(http.Header),
			}, nil
		}),
	}

	_, err := resolveConcreteVersion("5.3.2")
	if err == nil {
		t.Fatal("resolveConcreteVersion() error = nil, want unknown explicit version failure")
	}
	if !strings.Contains(err.Error(), `could not resolve R version selector "5.3.2"`) {
		t.Fatalf("resolveConcreteVersion() error = %v", err)
	}
}

func TestDiscoverInstallationsMarksCurrentManagedInterpreter(t *testing.T) {
	root := t.TempDir()
	t.Setenv(managerRootEnv, root)

	currentPath := writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.4.3-"+runtime.GOOS+"-"+runtime.GOARCH), "4.4.3")
	otherPath := writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.5.1-"+runtime.GOOS+"-"+runtime.GOARCH), "4.5.1")
	if err := setCurrentInstall(filepath.Dir(filepath.Dir(currentPath))); err != nil {
		t.Fatalf("setCurrentInstall() error = %v", err)
	}

	installs, err := discoverInstallations()
	if err != nil {
		t.Fatalf("discoverInstallations() error = %v", err)
	}
	if len(installs) < 2 {
		t.Fatalf("discoverInstallations() = %v, want at least 2 installs", installs)
	}
	if installs[0].RscriptPath != currentPath || !installs[0].Current || !installs[0].Managed {
		t.Fatalf("discoverInstallations()[0] = %#v, want current managed install %q", installs[0], currentPath)
	}
	foundOther := false
	for _, inst := range installs {
		if inst.RscriptPath == otherPath && !inst.Current {
			foundOther = true
			break
		}
	}
	if !foundOther {
		t.Fatalf("discoverInstallations() = %v, want non-current secondary install %q", installs, otherPath)
	}
}

func TestCurrentManagedRscriptReturnsCurrentManagedInstall(t *testing.T) {
	root := t.TempDir()
	t.Setenv(managerRootEnv, root)

	currentPath := writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.5.3-"+runtime.GOOS+"-"+runtime.GOARCH), "4.5.3")
	_ = writeFakeManagedRscript(t, filepath.Join(root, "versions", "4.4.3-"+runtime.GOOS+"-"+runtime.GOARCH), "4.4.3")
	if err := setCurrentInstall(filepath.Dir(filepath.Dir(currentPath))); err != nil {
		t.Fatalf("setCurrentInstall() error = %v", err)
	}

	got, err := CurrentManagedRscript()
	if err != nil {
		t.Fatalf("CurrentManagedRscript() error = %v", err)
	}
	if got != currentPath {
		t.Fatalf("CurrentManagedRscript() = %q, want %q", got, currentPath)
	}
}

func TestLookupManagedInstallationUsesStoredRuntimeMetadata(t *testing.T) {
	root := t.TempDir()
	t.Setenv(managerRootEnv, root)

	targetDir := filepath.Join(root, "versions", "4.5.3-"+runtime.GOOS+"-"+runtime.GOARCH)
	rscriptPath := writeFakeManagedRscript(t, targetDir, "4.5.3")
	metaDir := filepath.Join(root, "metadata")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(metadata) error = %v", err)
	}
	meta := installationMetadata{
		ID:          filepath.Base(targetDir),
		Version:     "4.5.3",
		Platform:    "x86_64-pc-linux-gnu",
		Arch:        "x86_64",
		OS:          "linux-gnu",
		PackageType: "source",
		Name:        "4.5.3",
		Path:        targetDir,
		RscriptPath: rscriptPath,
		RPath:       managedRExecutablePath(targetDir),
		Managed:     true,
		Source:      "native",
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal(metadata) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, meta.ID+".json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(metadata) error = %v", err)
	}

	got, ok, err := LookupManagedInstallation(rscriptPath)
	if err != nil {
		t.Fatalf("LookupManagedInstallation() error = %v", err)
	}
	if !ok {
		t.Fatalf("LookupManagedInstallation() ok = false, want true")
	}
	if got.Version != "4.5.3" || got.Platform != "x86_64-pc-linux-gnu" || got.PackageType != "source" || !got.Managed {
		t.Fatalf("LookupManagedInstallation() = %#v", got)
	}
}

func TestBootstrapAdviceForUsesRequestedVersion(t *testing.T) {
	advice := BootstrapAdviceFor("5.3.2")
	if !strings.Contains(advice.ManualCommand, brand.Command("r install 5.3.2")) {
		t.Fatalf("BootstrapAdviceFor() command = %q", advice.ManualCommand)
	}
}

func TestSelectInstallAction(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		distro linuxDistro
		method InstallMethod
		want   installAction
		errSub string
	}{
		{name: "darwin auto", goos: "darwin", method: InstallMethodAuto, want: installActionMacOSBinary},
		{name: "darwin source", goos: "darwin", method: InstallMethodSource, want: installActionSource},
		{name: "linux binary", goos: "linux", distro: linuxDistro{ID: "ubuntu", VersionID: "2404"}, method: InstallMethodBinary, want: installActionLinuxBinary},
		{name: "linux arch auto", goos: "linux", distro: linuxDistro{ID: "arch"}, method: InstallMethodAuto, want: installActionSource},
		{name: "linux unsupported auto falls back to source", goos: "linux", distro: linuxDistro{ID: "gentoo", VersionID: "latest"}, method: InstallMethodAuto, want: installActionSource},
		{name: "linux unsupported binary fails", goos: "linux", distro: linuxDistro{ID: "gentoo", VersionID: "latest"}, method: InstallMethodBinary, errSub: "unsupported Linux distribution"},
		{name: "windows auto", goos: "windows", method: InstallMethodAuto, want: installActionWindowsBinary},
		{name: "windows binary", goos: "windows", method: InstallMethodBinary, want: installActionWindowsBinary},
		{name: "windows source unsupported", goos: "windows", method: InstallMethodSource, errSub: "currently support only binary installs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectInstallAction(tc.goos, tc.distro, tc.method)
			if tc.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("selectInstallAction() error = %v, want substring %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectInstallAction() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("selectInstallAction() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestManagedRootWindowsUsesLocalAppData(t *testing.T) {
	oldGOOS := nativeGOOS
	t.Cleanup(func() {
		nativeGOOS = oldGOOS
	})
	nativeGOOS = "windows"
	t.Setenv("LOCALAPPDATA", `C:\Users\alice\AppData\Local`)

	got, err := managedRoot()
	if err != nil {
		t.Fatalf("managedRoot() error = %v", err)
	}
	want := filepath.Join(`C:\Users\alice\AppData\Local`, "rs", "r")
	if got != want {
		t.Fatalf("managedRoot() = %q, want %q", got, want)
	}
}

func TestWindowsInstallerURL(t *testing.T) {
	if got := windowsInstallerURL("4.4.3"); got != "https://cran.r-project.org/bin/windows/base/old/4.4.3/R-4.4.3-win.exe" {
		t.Fatalf("windowsInstallerURL() = %q", got)
	}
}

func TestParseWindowsRegistryInstallPaths(t *testing.T) {
	output := `
HKEY_LOCAL_MACHINE\Software\R-core\R\4.4.3
    InstallPath    REG_SZ    C:\Program Files\R\R-4.4.3

HKEY_CURRENT_USER\Software\R-core\R
    InstallPath    REG_SZ    C:\Users\alice\AppData\Local\Programs\R\R-4.5.0
`
	got := parseWindowsRegistryInstallPaths(output)
	want := []string{
		`C:\Program Files\R\R-4.4.3`,
		`C:\Users\alice\AppData\Local\Programs\R\R-4.5.0`,
	}
	if len(got) != len(want) {
		t.Fatalf("parseWindowsRegistryInstallPaths() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseWindowsRegistryInstallPaths()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagedWindowsHelpersPreferX64Binaries(t *testing.T) {
	oldGOOS := nativeGOOS
	t.Cleanup(func() {
		nativeGOOS = oldGOOS
	})
	nativeGOOS = "windows"

	root := t.TempDir()
	rscriptPath := filepath.Join(root, "bin", "x64", "Rscript.exe")
	rPath := filepath.Join(root, "bin", "x64", "R.exe")
	if err := os.MkdirAll(filepath.Dir(rscriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(rscriptPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(Rscript.exe) error = %v", err)
	}
	if err := os.WriteFile(rPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(R.exe) error = %v", err)
	}

	if got := managedRscriptPath(root); got != rscriptPath {
		t.Fatalf("managedRscriptPath() = %q, want %q", got, rscriptPath)
	}
	if got := managedRExecutablePath(root); got != rPath {
		t.Fatalf("managedRExecutablePath() = %q, want %q", got, rPath)
	}
	if got := rRootFromRscriptPath(rscriptPath); got != root {
		t.Fatalf("rRootFromRscriptPath() = %q, want %q", got, root)
	}
}

func TestPreflightSourceBuildMissingTools(t *testing.T) {
	oldFindInPath := nativeFindInPath
	oldCheckHeader := nativeCheckHeader
	t.Cleanup(func() {
		nativeFindInPath = oldFindInPath
		nativeCheckHeader = oldCheckHeader
	})
	nativeFindInPath = func(file string, env []string) (string, error) {
		return "", fmt.Errorf("missing %s", file)
	}
	nativeCheckHeader = func(header string) error { return fmt.Errorf("missing %s", header) }

	err := preflightSourceBuild("4.4.3")
	if err == nil {
		t.Fatal("preflightSourceBuild() error = nil, want missing tools")
	}
	for _, want := range []string{"gcc", "g++", "gfortran", "make", "curl", "xz"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("preflightSourceBuild() error = %v, want %q", err, want)
		}
	}
}

func TestPreflightSourceBuildArchHint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Arch-specific source build hint only applies on Linux")
	}
	oldFindInPath := nativeFindInPath
	oldReadFile := nativeReadFile
	oldCheckHeader := nativeCheckHeader
	t.Cleanup(func() {
		nativeFindInPath = oldFindInPath
		nativeReadFile = oldReadFile
		nativeCheckHeader = oldCheckHeader
	})
	nativeFindInPath = func(file string, env []string) (string, error) {
		return "", fmt.Errorf("missing %s", file)
	}
	nativeCheckHeader = func(header string) error { return fmt.Errorf("missing %s", header) }
	nativeReadFile = func(path string) ([]byte, error) {
		if path == "/etc/os-release" {
			return []byte("ID=arch\nID_LIKE=arch\nVERSION_ID=rolling\n"), nil
		}
		return nil, fmt.Errorf("unexpected read %s", path)
	}

	err := preflightSourceBuild("4.4.3")
	if err == nil {
		t.Fatal("preflightSourceBuild() error = nil, want Arch hint")
	}
	if !strings.Contains(err.Error(), "pacman -S --needed base-devel gcc-fortran curl xz bzip2 zlib readline pcre2 icu") {
		t.Fatalf("preflightSourceBuild() error = %v", err)
	}
}

func TestPreflightSourceBuildMacOSMissingLzmaHeader(t *testing.T) {
	oldGOOS := nativeGOOS
	oldFindInPath := nativeFindInPath
	oldCheckHeader := nativeCheckHeader
	t.Cleanup(func() {
		nativeGOOS = oldGOOS
		nativeFindInPath = oldFindInPath
		nativeCheckHeader = oldCheckHeader
	})
	nativeGOOS = "darwin"
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
	nativeFindInPath = func(file string, env []string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	nativeCheckHeader = func(header string) error { return fmt.Errorf("missing %s", header) }

	err := preflightSourceBuild("4.4.3")
	if err == nil {
		t.Fatal("preflightSourceBuild() error = nil, want missing macOS header")
	}
	if !strings.Contains(err.Error(), "lzma.h header") {
		t.Fatalf("preflightSourceBuild() error = %v", err)
	}
	if !strings.Contains(err.Error(), brand.Command("r install 4.4.3 --method binary")) {
		t.Fatalf("preflightSourceBuild() error = %v", err)
	}
	if !strings.Contains(err.Error(), "rootless environment") {
		t.Fatalf("preflightSourceBuild() error = %v", err)
	}
	if !strings.Contains(err.Error(), "detected recommended preset on this machine: homebrew") {
		t.Fatalf("preflightSourceBuild() error = %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(homebrewPrefix, "bin", "brew")) {
		t.Fatalf("preflightSourceBuild() error = %v", err)
	}
}

func TestHeaderProbeSourceHandlesPCRE2(t *testing.T) {
	got := headerProbeSource("pcre2.h")
	if !strings.Contains(got, "#define PCRE2_CODE_UNIT_WIDTH 8") {
		t.Fatalf("headerProbeSource() = %q, want PCRE2 width define", got)
	}
	if !strings.Contains(got, "#include <pcre2.h>") {
		t.Fatalf("headerProbeSource() = %q, want pcre2 include", got)
	}
}

func TestHeaderProbeSourceDefaultInclude(t *testing.T) {
	got := headerProbeSource("zlib.h")
	if got != "#include <zlib.h>\n" {
		t.Fatalf("headerProbeSource() = %q, want default include", got)
	}
}

func TestSourceBuildEnvironmentUsesRSToolchainPrefixes(t *testing.T) {
	firstPrefix := filepath.Join(string(filepath.Separator), "opt", "demo")
	secondPrefix := filepath.Join(string(filepath.Separator), "opt", "demo2")
	pkgPrefix := filepath.Join(string(filepath.Separator), "opt", "pkgconfig")
	t.Setenv("RS_TOOLCHAIN_PREFIXES", strings.Join([]string{firstPrefix, secondPrefix}, string(os.PathListSeparator)))
	t.Setenv("RS_PKG_CONFIG_PATH", strings.Join([]string{pkgPrefix}, string(os.PathListSeparator)))

	env := sourceBuildEnvironment()
	var pathValue, prefixValue, pkgValue string
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, "PATH="):
			pathValue = strings.TrimPrefix(entry, "PATH=")
		case strings.HasPrefix(entry, "RS_TOOLCHAIN_PREFIXES="):
			prefixValue = strings.TrimPrefix(entry, "RS_TOOLCHAIN_PREFIXES=")
		case strings.HasPrefix(entry, "RS_PKG_CONFIG_PATH="):
			pkgValue = strings.TrimPrefix(entry, "RS_PKG_CONFIG_PATH=")
		}
	}
	if !strings.HasPrefix(pathValue, filepath.Join(secondPrefix, "bin")+string(os.PathListSeparator)) && !strings.HasPrefix(pathValue, filepath.Join(firstPrefix, "bin")+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q", pathValue)
	}
	if prefixValue == "" || pkgValue == "" {
		t.Fatalf("sourceBuildEnvironment() missing %s toolchain markers: %v", brand.CLIName, env)
	}
}

func TestSourceBuildEnvironmentAutoDetectsToolchainWhenUnset(t *testing.T) {
	dir := t.TempDir()
	setTestHomeDir(t, dir)
	t.Setenv("RS_TOOLCHAIN_PREFIXES", "")
	t.Setenv("RS_PKG_CONFIG_PATH", "")
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

	env := sourceBuildEnvironment()
	var prefixValue string
	for _, entry := range env {
		if strings.HasPrefix(entry, "RS_TOOLCHAIN_PREFIXES=") {
			prefixValue = strings.TrimPrefix(entry, "RS_TOOLCHAIN_PREFIXES=")
			break
		}
	}
	if prefixValue != homebrewPrefix {
		t.Fatalf("RS_TOOLCHAIN_PREFIXES = %q, want %q (%v)", prefixValue, homebrewPrefix, env)
	}
}

func TestLinuxBinaryOSIdentifierRejectsUnsupportedDistro(t *testing.T) {
	_, err := linuxBinaryOSIdentifier(linuxDistro{ID: "gentoo", VersionID: "latest"})
	if err == nil {
		t.Fatal("linuxBinaryOSIdentifier() error = nil, want unsupported distro")
	}
	if !strings.Contains(err.Error(), "unsupported Linux distribution gentoo latest") {
		t.Fatalf("linuxBinaryOSIdentifier() error = %v", err)
	}
}

func TestResolveMacOSInstallRootUsesExpandedPkgTree(t *testing.T) {
	root := t.TempDir()
	rscriptPath := filepath.Join(root, "R-fw.pkg", "Payload", "R.framework", "Versions", "4.4-arm64", "Resources", "bin", rscriptExecutableName())
	if err := os.MkdirAll(filepath.Dir(rscriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(rscriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	gotRoot, gotMode, err := resolveMacOSInstallRoot(root, filepath.Join(root, "scratch-payload"), io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("resolveMacOSInstallRoot() error = %v", err)
	}
	wantRoot := filepath.Join(root, "R-fw.pkg", "Payload", "R.framework", "Versions", "4.4-arm64", "Resources")
	if gotRoot != wantRoot {
		t.Fatalf("resolveMacOSInstallRoot() root = %q, want %q", gotRoot, wantRoot)
	}
	if gotMode != "resources" {
		t.Fatalf("resolveMacOSInstallRoot() mode = %q, want resources", gotMode)
	}
}

func TestRewriteManagedRLauncherRewritesLinuxPrefix(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "managed-r")
	launcherPath := filepath.Join(target, "bin", "R")
	if err := os.MkdirAll(filepath.Dir(launcherPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	launcher := `#!/bin/bash
R_HOME_DIR=/opt/R/4.4.3/lib/R
if test "${R_HOME_DIR}" = "/opt/R/4.4.3/lib/R"; then
   if [ -x "/opt/R/4.4.3/${libnn}/R/bin/exec/R" ]; then
      R_HOME_DIR="/opt/R/4.4.3/${libnn}/R"
   elif [ -x "/opt/R/4.4.3/${libnn_fallback}/R/bin/exec/R" ]; then
      R_HOME_DIR="/opt/R/4.4.3/${libnn_fallback}/R"
   fi
fi
R_SHARE_DIR=/opt/R/4.4.3/lib/R/share
R_INCLUDE_DIR=/opt/R/4.4.3/lib/R/include
R_DOC_DIR=/opt/R/4.4.3/lib/R/doc
`
	if err := os.WriteFile(launcherPath, []byte(launcher), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := rewriteManagedRLauncher(launcherPath, target); err != nil {
		t.Fatalf("rewriteManagedRLauncher() error = %v", err)
	}

	data, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "R_HOME_DIR="+shellSingleQuote(target)) {
		t.Fatalf("launcher = %q, want rewritten R_HOME_DIR", text)
	}
	if !strings.Contains(text, "R_SHARE_DIR="+shellSingleQuote(filepath.Join(target, "share"))) {
		t.Fatalf("launcher = %q, want rewritten R_SHARE_DIR", text)
	}
	if strings.Contains(text, "/opt/R/4.4.3/${libnn}/R") || strings.Contains(text, "/opt/R/4.4.3/${libnn_fallback}/R") {
		t.Fatalf("launcher = %q, want linux fallback paths removed", text)
	}
	if strings.Contains(text, "elif [ -x") {
		t.Fatalf("launcher = %q, want nested distro fallback block removed", text)
	}
	if bashPath, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command(bashPath, "-n", launcherPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("rewritten launcher failed bash -n: %v\n%s", err, output)
		}
	}
}

func TestSourceConfigureArgs(t *testing.T) {
	target := filepath.Join(t.TempDir(), "managed-r")
	args := sourceConfigureArgs(target)
	if len(args) < 2 {
		t.Fatalf("sourceConfigureArgs() = %v, want configure command and prefix", args)
	}
	if args[0] != "./configure" {
		t.Fatalf("sourceConfigureArgs()[0] = %q, want ./configure", args[0])
	}
	if args[1] != "--prefix="+target {
		t.Fatalf("sourceConfigureArgs()[1] = %q, want --prefix=%s", args[1], target)
	}
	hasWithoutX := false
	for _, arg := range args[2:] {
		if arg == "--without-x" {
			hasWithoutX = true
			break
		}
	}
	if runtime.GOOS == "darwin" && !hasWithoutX {
		t.Fatalf("sourceConfigureArgs() = %v, want --without-x on darwin", args)
	}
	if runtime.GOOS != "darwin" && hasWithoutX {
		t.Fatalf("sourceConfigureArgs() = %v, do not want --without-x outside darwin", args)
	}
}

func TestInstallBinaryWithFallbackFallsBackOnUnrunnableBinaryInAutoMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "managed-r")
	if err := os.MkdirAll(filepath.Join(target, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	rscriptPath := filepath.Join(target, "bin", rscriptExecutableName())
	if err := os.WriteFile(rscriptPath, []byte("placeholder"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var stderr bytes.Buffer
	fallbackCalled := false
	oldCommand := nativeCommand
	oldInstallSrc := nativeInstallSrc
	t.Cleanup(func() {
		nativeCommand = oldCommand
		nativeInstallSrc = oldInstallSrc
	})
	nativeCommand = func(name string, args ...string) *exec.Cmd {
		switch {
		case name == rscriptPath:
			return failingShellCommand()
		default:
			return exec.Command(name, args...)
		}
	}
	nativeInstallSrc = func(version, targetDir string, stdout, stderr io.Writer) error {
		fallbackCalled = true
		return nil
	}

	if err := installBinaryWithFallback("4.4.3", InstallMethodAuto, target, io.Discard, &stderr, "Linux", func() error { return nil }); err != nil {
		t.Fatalf("installBinaryWithFallback() error = %v", err)
	}
	if !fallbackCalled {
		t.Fatal("installBinaryWithFallback() did not trigger source fallback")
	}
	if !strings.Contains(stderr.String(), "Linux binary install for R 4.4.3 was not runnable; falling back to source build") {
		t.Fatalf("stderr = %q, want auto fallback message", stderr.String())
	}
}

var execLookPath = rscriptLookPath
var execNativeLookPath = nativeLookPath

func failingShellCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", "exit /b 127")
	}
	return exec.Command("/bin/sh", "-c", "exit 127")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
