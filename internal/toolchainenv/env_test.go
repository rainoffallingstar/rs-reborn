package toolchainenv

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func setTestHomeDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func writeToolExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", dir, err)
	}
	fileName := name
	if runtime.GOOS == "windows" {
		fileName += ".exe"
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func TestApplyPrependsPrefixesAndPkgConfigPaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "opt", "demo")
	customPkg := filepath.Join(string(filepath.Separator), "custom", "pkgconfig")
	existingPath := filepath.Join(string(filepath.Separator), "usr", "bin")
	existingCPP := "-I" + filepath.Join(string(filepath.Separator), "existing", "include")
	existingLD := "-L" + filepath.Join(string(filepath.Separator), "existing", "lib")
	existingPkg := filepath.Join(string(filepath.Separator), "existing", "pkgconfig")
	base := []string{
		"PATH=" + existingPath,
		"CPPFLAGS=" + existingCPP,
		"LDFLAGS=" + existingLD,
		"PKG_CONFIG_PATH=" + existingPkg,
	}

	env := Apply(base, []string{root}, []string{customPkg})

	pathValue := envValue(env, "PATH")
	if !strings.HasPrefix(pathValue, filepath.Join(root, "bin")+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q", pathValue)
	}
	if cpp := envValue(env, "CPPFLAGS"); !strings.Contains(cpp, "-I"+filepath.Join(root, "include")) {
		t.Fatalf("CPPFLAGS = %q", cpp)
	}
	if ld := envValue(env, "LDFLAGS"); !strings.Contains(ld, "-L"+filepath.Join(root, "lib")) {
		t.Fatalf("LDFLAGS = %q", ld)
	}
	if pkg := envValue(env, "PKG_CONFIG_PATH"); !strings.HasPrefix(pkg, customPkg+string(os.PathListSeparator)) {
		t.Fatalf("PKG_CONFIG_PATH = %q", pkg)
	}
	if got := envValue(env, PrefixesEnv); got != root {
		t.Fatalf("%s = %q", PrefixesEnv, got)
	}
	if got := envValue(env, PkgConfigEnv); got != customPkg {
		t.Fatalf("%s = %q", PkgConfigEnv, got)
	}
}

func TestFindInPathUsesConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	name := "demo-tool"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := FindInPath("demo-tool", []string{"PATH=" + dir})
	if err != nil {
		t.Fatalf("FindInPath() error = %v", err)
	}
	if got != target {
		t.Fatalf("FindInPath() = %q, want %q", got, target)
	}
}

func TestValidateReportsMissingPrefixesAndPkgConfigPaths(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("demo"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result := Validate(
		[]string{filepath.Join(dir, "missing-prefix"), filePath},
		[]string{filepath.Join(dir, "missing-pkgconfig"), filePath},
		[]string{"PATH=" + filepath.Join(dir, "empty-bin")},
	)

	for _, want := range []string{
		"toolchain prefix does not exist: " + filepath.Join(dir, "missing-prefix"),
		"toolchain prefix is not a directory: " + filePath,
		"pkg-config path does not exist: " + filepath.Join(dir, "missing-pkgconfig"),
		"pkg-config path is not a directory: " + filePath,
	} {
		if !strings.Contains(strings.Join(result.Errors, "\n"), want) {
			t.Fatalf("Validate().Errors missing %q in %v", want, result.Errors)
		}
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "pkg-config is not available on PATH") {
		t.Fatalf("Validate().Warnings = %v", result.Warnings)
	}
}

func TestValidateAcceptsExistingDirectoriesWithPkgConfig(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "prefix")
	pkgConfigDir := filepath.Join(dir, "pkgconfig")
	binDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	if err := os.MkdirAll(pkgConfigDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(pkgConfigDir) error = %v", err)
	}
	name := "pkg-config"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(pkg-config) error = %v", err)
	}

	env := Apply([]string{"PATH=/usr/bin"}, []string{prefix}, []string{pkgConfigDir})
	result := Validate([]string{prefix}, []string{pkgConfigDir}, env)
	if len(result.Errors) != 0 {
		t.Fatalf("Validate().Errors = %v", result.Errors)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Validate().Warnings = %v", result.Warnings)
	}
}

func TestBuildPreviewIncludesDerivedPathsAndFlags(t *testing.T) {
	first := filepath.Join(string(filepath.Separator), "opt", "demo")
	second := filepath.Join(string(filepath.Separator), "opt", "extra")
	customPkg := filepath.Join(string(filepath.Separator), "custom", "pkgconfig")
	preview := BuildPreview(
		[]string{first, second},
		[]string{customPkg},
	)
	if !reflect.DeepEqual(preview.Path, []string{filepath.Join(first, "bin"), filepath.Join(second, "bin")}) {
		t.Fatalf("preview.Path = %v", preview.Path)
	}
	if !reflect.DeepEqual(preview.CPPFLAGS, []string{"-I" + filepath.Join(first, "include"), "-I" + filepath.Join(second, "include")}) {
		t.Fatalf("preview.CPPFLAGS = %v", preview.CPPFLAGS)
	}
	if !reflect.DeepEqual(preview.LDFLAGS, []string{"-L" + filepath.Join(first, "lib"), "-L" + filepath.Join(second, "lib")}) {
		t.Fatalf("preview.LDFLAGS = %v", preview.LDFLAGS)
	}
	if !reflect.DeepEqual(preview.PkgConfigPath, []string{
		filepath.Join(first, "lib", "pkgconfig"),
		filepath.Join(first, "share", "pkgconfig"),
		filepath.Join(second, "lib", "pkgconfig"),
		filepath.Join(second, "share", "pkgconfig"),
		customPkg,
	}) {
		t.Fatalf("preview.PkgConfigPath = %v", preview.PkgConfigPath)
	}
}

func TestSupportedPresetsIncludesEnvaAndCondaFamily(t *testing.T) {
	if !reflect.DeepEqual(SupportedPresets(), []string{"enva", "micromamba", "mamba", "conda", "homebrew", "spack"}) {
		t.Fatalf("SupportedPresets() = %v", SupportedPresets())
	}
}

func TestRecommendedCandidateIncludesSetupGuidance(t *testing.T) {
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

	candidate, err := RecommendedCandidate(dir)
	if err != nil {
		t.Fatalf("RecommendedCandidate() error = %v", err)
	}
	if candidate == nil {
		t.Fatal("RecommendedCandidate() = nil, want detected candidate")
	}
	if candidate.Preset != "homebrew" {
		t.Fatalf("candidate.Preset = %q, want homebrew", candidate.Preset)
	}
	if !candidate.Recommended {
		t.Fatalf("candidate.Recommended = false, want true")
	}
	if !strings.Contains(candidate.SuggestedSetupCommand, filepath.Join(homebrewPrefix, "bin", "brew")) {
		t.Fatalf("candidate.SuggestedSetupCommand = %q", candidate.SuggestedSetupCommand)
	}
	if !strings.Contains(candidate.SuggestedSetupNote, "install or reuse Homebrew under") {
		t.Fatalf("candidate.SuggestedSetupNote = %q", candidate.SuggestedSetupNote)
	}
}

func TestResolvePresetAutoRejectsWhenNothingDetected(t *testing.T) {
	dir := t.TempDir()

	_, _, err := ResolvePreset("auto", dir)
	if err == nil {
		t.Fatal("ResolvePreset() error = nil, want auto-detect failure")
	}
	if !strings.Contains(err.Error(), "could not auto-detect a common rootless toolchain preset on this machine") {
		t.Fatalf("ResolvePreset() error = %v", err)
	}
}

func TestMergeWithDetectedUsesRecommendedCandidateWhenUnset(t *testing.T) {
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

	prefixes, pkgConfig, candidate, err := MergeWithDetected(nil, nil, dir)
	if err != nil {
		t.Fatalf("MergeWithDetected() error = %v", err)
	}
	if candidate == nil || candidate.Preset != "homebrew" {
		t.Fatalf("candidate = %#v, want recommended homebrew", candidate)
	}
	if !reflect.DeepEqual(prefixes, []string{homebrewPrefix}) {
		t.Fatalf("prefixes = %v", prefixes)
	}
	if !reflect.DeepEqual(pkgConfig, []string{
		filepath.Join(homebrewPrefix, "lib", "pkgconfig"),
		filepath.Join(homebrewPrefix, "share", "pkgconfig"),
	}) {
		t.Fatalf("pkgConfig = %v", pkgConfig)
	}
}

func TestBootstrapCandidateAutoUsesCallableMicromamba(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	name := "micromamba"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(micromamba) error = %v", err)
	}

	candidate, err := BootstrapCandidate("auto", dir, []string{"PATH=" + binDir})
	if err != nil {
		t.Fatalf("BootstrapCandidate() error = %v", err)
	}
	if candidate == nil || candidate.Preset != "micromamba" {
		t.Fatalf("candidate = %#v, want micromamba", candidate)
	}
	if !strings.Contains(candidate.SuggestedSetupCommand, "create -y -p") {
		t.Fatalf("candidate.SuggestedSetupCommand = %q", candidate.SuggestedSetupCommand)
	}
}

func TestBootstrapCandidateAutoPrefersEnvaBeforeMicromambaMambaAndConda(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	for _, name := range []string{"enva", "micromamba", "mamba", "conda"} {
		fileName := name
		if runtime.GOOS == "windows" {
			fileName += ".exe"
		}
		if err := os.WriteFile(filepath.Join(binDir, fileName), []byte("binary"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", fileName, err)
		}
	}

	candidate, err := BootstrapCandidate("auto", dir, []string{"PATH=" + binDir})
	if err != nil {
		t.Fatalf("BootstrapCandidate() error = %v", err)
	}
	if candidate == nil || candidate.Preset != "enva" {
		t.Fatalf("candidate = %#v, want enva", candidate)
	}
	if !strings.Contains(candidate.SuggestedSetupCommand, `create --yaml "$tmp" --name rs-sysdeps --force --clean-cache`) {
		t.Fatalf("candidate.SuggestedSetupCommand = %q", candidate.SuggestedSetupCommand)
	}
}

func TestBootstrapCandidateAutoFallsBackToMambaThenConda(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	for _, name := range []string{"mamba", "conda"} {
		fileName := name
		if runtime.GOOS == "windows" {
			fileName += ".exe"
		}
		if err := os.WriteFile(filepath.Join(binDir, fileName), []byte("binary"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", fileName, err)
		}
	}

	candidate, err := BootstrapCandidate("auto", dir, []string{"PATH=" + binDir})
	if err != nil {
		t.Fatalf("BootstrapCandidate() error = %v", err)
	}
	if candidate == nil || candidate.Preset != "mamba" {
		t.Fatalf("candidate = %#v, want mamba", candidate)
	}
	if !strings.Contains(candidate.SuggestedSetupCommand, `" create -y -p "`) || !strings.Contains(candidate.SuggestedSetupCommand, "compilers binutils sysroot_linux-64=2.17 pkg-config make") {
		t.Fatalf("candidate.SuggestedSetupCommand = %q", candidate.SuggestedSetupCommand)
	}

	for _, name := range []string{"mamba", "mamba.exe"} {
		_ = os.Remove(filepath.Join(binDir, name))
	}
	candidate, err = BootstrapCandidate("auto", dir, []string{"PATH=" + binDir})
	if err != nil {
		t.Fatalf("BootstrapCandidate() error after mamba removal = %v", err)
	}
	if candidate == nil || candidate.Preset != "conda" {
		t.Fatalf("candidate = %#v, want conda", candidate)
	}
	if !strings.Contains(candidate.SuggestedSetupCommand, `" create -y -p "`) || !strings.Contains(candidate.SuggestedSetupCommand, "compilers binutils sysroot_linux-64=2.17 pkg-config make") {
		t.Fatalf("candidate.SuggestedSetupCommand = %q", candidate.SuggestedSetupCommand)
	}
}

func TestBootstrapRunsDetectedCommand(t *testing.T) {
	oldRun := detectRunCommand
	t.Cleanup(func() {
		detectRunCommand = oldRun
	})
	detectRunCommand = func(command string, env []string, stdout, stderr io.Writer) error {
		if !strings.Contains(command, "create -y -p") {
			t.Fatalf("command = %q", command)
		}
		return nil
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(binDir) error = %v", err)
	}
	name := "micromamba"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile(micromamba) error = %v", err)
	}

	candidate, err := Bootstrap("auto", dir, []string{"PATH=" + binDir}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if candidate == nil || candidate.Preset != "micromamba" {
		t.Fatalf("candidate = %#v, want micromamba", candidate)
	}
}

func TestResolvePresetSupportsEnvaMambaAndConda(t *testing.T) {
	dir := t.TempDir()

	prefixes, pkgConfig, err := ResolvePreset("enva", dir)
	if err != nil {
		t.Fatalf("ResolvePreset(enva) error = %v", err)
	}
	envaPrefix := filepath.Join(dir, ".local", "share", "rattler", "envs", "rs-sysdeps")
	if !reflect.DeepEqual(prefixes, []string{envaPrefix}) {
		t.Fatalf("ResolvePreset(enva) prefixes = %v", prefixes)
	}
	if !reflect.DeepEqual(pkgConfig, []string{
		filepath.Join(envaPrefix, "lib", "pkgconfig"),
		filepath.Join(envaPrefix, "share", "pkgconfig"),
	}) {
		t.Fatalf("ResolvePreset(enva) pkgConfig = %v", pkgConfig)
	}

	prefixes, _, err = ResolvePreset("mamba", dir)
	if err != nil {
		t.Fatalf("ResolvePreset(mamba) error = %v", err)
	}
	if !reflect.DeepEqual(prefixes, []string{filepath.Join(dir, ".local", "share", "mamba", "envs", "rs-sysdeps")}) {
		t.Fatalf("ResolvePreset(mamba) prefixes = %v", prefixes)
	}

	prefixes, _, err = ResolvePreset("conda", dir)
	if err != nil {
		t.Fatalf("ResolvePreset(conda) error = %v", err)
	}
	if !reflect.DeepEqual(prefixes, []string{filepath.Join(dir, ".conda", "envs", "rs-sysdeps")}) {
		t.Fatalf("ResolvePreset(conda) prefixes = %v", prefixes)
	}
}

func TestCandidateFromPathsMatchesEnvaPreset(t *testing.T) {
	dir := t.TempDir()
	setTestHomeDir(t, dir)

	prefixes, pkgConfig, err := ResolvePreset("enva", dir)
	if err != nil {
		t.Fatalf("ResolvePreset(enva) error = %v", err)
	}
	candidate, err := CandidateFromPaths(prefixes, pkgConfig, "")
	if err != nil {
		t.Fatalf("CandidateFromPaths() error = %v", err)
	}
	if candidate == nil || candidate.Preset != "enva" {
		t.Fatalf("candidate = %#v, want enva", candidate)
	}
}

func TestWrapCommandUsesEnvaRunForManagedToolchain(t *testing.T) {
	dir := t.TempDir()
	setTestHomeDir(t, dir)
	binDir := filepath.Join(dir, "bin")
	envaPath := writeToolExecutable(t, binDir, "enva")

	prefixes, pkgConfig, err := ResolvePreset("enva", dir)
	if err != nil {
		t.Fatalf("ResolvePreset(enva) error = %v", err)
	}
	env := Apply([]string{"PATH=" + binDir}, prefixes, pkgConfig)

	name, args, _, wrapped, err := WrapCommand("R", []string{"CMD", "INSTALL", "pkg.tar.gz"}, env)
	if err != nil {
		t.Fatalf("WrapCommand() error = %v", err)
	}
	if !wrapped {
		t.Fatal("WrapCommand() wrapped = false, want true")
	}
	if name != envaPath {
		t.Fatalf("name = %q, want %q", name, envaPath)
	}
	wantArgs := []string{"run", "rs-sysdeps", "--", "R", "CMD", "INSTALL", "pkg.tar.gz"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
}

func TestWrapCommandUsesMicromambaRunForManagedToolchain(t *testing.T) {
	dir := t.TempDir()
	setTestHomeDir(t, dir)
	binDir := filepath.Join(dir, "bin")
	micromambaPath := writeToolExecutable(t, binDir, "micromamba")

	prefixes, pkgConfig, err := ResolvePreset("micromamba", dir)
	if err != nil {
		t.Fatalf("ResolvePreset(micromamba) error = %v", err)
	}
	env := Apply([]string{"PATH=" + binDir}, prefixes, pkgConfig)

	name, args, _, wrapped, err := WrapCommand("R", []string{"CMD", "INSTALL", "pkg.tar.gz"}, env)
	if err != nil {
		t.Fatalf("WrapCommand() error = %v", err)
	}
	if !wrapped {
		t.Fatal("WrapCommand() wrapped = false, want true")
	}
	if name != micromambaPath {
		t.Fatalf("name = %q, want %q", name, micromambaPath)
	}
	wantArgs := []string{"run", "-p", prefixes[0], "--", "R", "CMD", "INSTALL", "pkg.tar.gz"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
}
