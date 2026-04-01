package toolchainenv

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

var (
	detectStat       = os.Stat
	detectRunCommand = runBootstrapCommand
	detectOutput     = runCommandOutput
)

type Candidate struct {
	Preset                string   `json:"preset"`
	ToolchainPrefixes     []string `json:"toolchain_prefixes"`
	PkgConfigPath         []string `json:"pkg_config_path"`
	ExistingPrefixes      []string `json:"existing_prefixes"`
	ExistingPkgConfigPath []string `json:"existing_pkg_config_path"`
	SuggestedInitCommand  string   `json:"suggested_init_command"`
	SuggestedSetupCommand string   `json:"suggested_setup_command"`
	SuggestedSetupNote    string   `json:"suggested_setup_note"`
	Recommended           bool     `json:"recommended"`
	Complete              bool     `json:"complete"`
}

func SupportedPresets() []string {
	return []string{"enva", "micromamba", "mamba", "conda", "homebrew", "spack"}
}

func autoPresets() []string {
	return []string{"enva", "homebrew", "spack"}
}

func detectCandidatesForPresets(home string, presets []string) ([]Candidate, error) {
	homeDir, err := resolveHomeDir(home)
	if err != nil {
		return nil, err
	}

	candidates := []Candidate{}
	for _, preset := range presets {
		candidate, err := candidateForPreset(preset, homeDir)
		if err != nil {
			return nil, err
		}
		if len(candidate.ExistingPrefixes) == 0 && len(candidate.ExistingPkgConfigPath) == 0 {
			continue
		}
		candidates = append(candidates, candidate)
	}
	slices.SortFunc(candidates, compareCandidates)
	if len(candidates) > 0 {
		candidates[0].Recommended = true
	}
	return candidates, nil
}

func ResolvePreset(name, home string) ([]string, []string, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return nil, nil, nil
	}

	homeDir, err := resolveHomeDir(home)
	if err != nil {
		return nil, nil, err
	}

	if name == "auto" {
		candidates, err := DetectCandidates(homeDir)
		if err != nil {
			return nil, nil, err
		}
		if len(candidates) == 0 {
			return nil, nil, errorsNoDetectedPreset()
		}
		return candidates[0].ToolchainPrefixes, candidates[0].PkgConfigPath, nil
	}

	switch name {
	case "enva":
		prefix := filepath.Join(homeDir, ".local", "share", "rattler", "envs", "rs-sysdeps")
		return []string{prefix}, []string{
			filepath.Join(prefix, "lib", "pkgconfig"),
			filepath.Join(prefix, "share", "pkgconfig"),
		}, nil
	case "micromamba":
		prefix := filepath.Join(homeDir, "micromamba", "envs", "rs-sysdeps")
		return []string{prefix}, []string{
			filepath.Join(prefix, "lib", "pkgconfig"),
			filepath.Join(prefix, "share", "pkgconfig"),
		}, nil
	case "mamba":
		prefix := filepath.Join(homeDir, ".local", "share", "mamba", "envs", "rs-sysdeps")
		return []string{prefix}, []string{
			filepath.Join(prefix, "lib", "pkgconfig"),
			filepath.Join(prefix, "share", "pkgconfig"),
		}, nil
	case "conda":
		prefix := filepath.Join(homeDir, ".conda", "envs", "rs-sysdeps")
		return []string{prefix}, []string{
			filepath.Join(prefix, "lib", "pkgconfig"),
			filepath.Join(prefix, "share", "pkgconfig"),
		}, nil
	case "homebrew":
		prefix := filepath.Join(homeDir, "homebrew")
		return []string{prefix}, []string{
			filepath.Join(prefix, "lib", "pkgconfig"),
			filepath.Join(prefix, "share", "pkgconfig"),
		}, nil
	case "spack":
		prefix := filepath.Join(homeDir, "spack", "views", "rs-sysdeps")
		return []string{prefix}, []string{
			filepath.Join(prefix, "lib", "pkgconfig"),
			filepath.Join(prefix, "share", "pkgconfig"),
		}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported --toolchain-preset %q; supported presets: auto, enva, micromamba, mamba, conda, homebrew, spack", name)
	}
}

func DetectCandidates(home string) ([]Candidate, error) {
	return detectCandidatesForPresets(home, SupportedPresets())
}

func RecommendedCandidate(home string) (*Candidate, error) {
	candidates, err := detectCandidatesForPresets(home, autoPresets())
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	candidate := candidates[0]
	return &candidate, nil
}

func DescribePreset(name, home string) (*Candidate, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	homeDir, err := resolveHomeDir(home)
	if err != nil {
		return nil, err
	}
	if name == "auto" {
		candidate, err := RecommendedCandidate(homeDir)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			return nil, errorsNoDetectedPreset()
		}
		return candidate, nil
	}
	candidate, err := candidateForPreset(name, homeDir)
	if err != nil {
		return nil, err
	}
	if recommended, err := RecommendedCandidate(homeDir); err == nil && recommended != nil && recommended.Preset == candidate.Preset {
		candidate.Recommended = true
	}
	return &candidate, nil
}

func resolveHomeDir(home string) (string, error) {
	if strings.TrimSpace(home) != "" {
		return filepath.Clean(home), nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for --toolchain-preset: %w", err)
	}
	return filepath.Clean(homeDir), nil
}

func errorsNoDetectedPreset() error {
	return fmt.Errorf("could not auto-detect a common rootless toolchain preset on this machine; run `rs toolchain detect` or choose one of: %s", strings.Join(SupportedPresets(), ", "))
}

func MergeWithDetected(prefixes, pkgConfig []string, home string) ([]string, []string, *Candidate, error) {
	cleanPrefixes := cleanList(prefixes)
	cleanPkgConfig := cleanList(pkgConfig)
	if len(cleanPrefixes) > 0 || len(cleanPkgConfig) > 0 {
		return cleanPrefixes, cleanPkgConfig, nil, nil
	}
	candidate, err := RecommendedCandidate(home)
	if err != nil {
		return nil, nil, nil, err
	}
	if candidate == nil {
		return nil, nil, nil, nil
	}
	return append([]string(nil), candidate.ToolchainPrefixes...), append([]string(nil), candidate.PkgConfigPath...), candidate, nil
}

func Bootstrap(name, home string, env []string, stdout, stderr io.Writer) (*Candidate, error) {
	candidate, err := BootstrapCandidate(name, home, env)
	if err != nil {
		return nil, err
	}
	if stderr != nil {
		fmt.Fprintf(stderr, "[rs] bootstrapping rootless toolchain preset: %s\n", candidate.Preset)
	}
	if err := detectRunCommand(candidate.SuggestedSetupCommand, env, stdout, stderr); err != nil {
		return nil, fmt.Errorf("bootstrap rootless toolchain preset %s: %w", candidate.Preset, err)
	}
	candidate = resolveBootstrappedCandidate(*candidate, env)
	return candidate, nil
}

func BootstrapCandidate(name, home string, env []string) (*Candidate, error) {
	if len(env) == 0 {
		env = os.Environ()
	}
	candidates, err := bootstrapCandidates(name, home)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		command, ok := bootstrapCommandForCandidate(candidate, env)
		if !ok {
			continue
		}
		candidate.SuggestedSetupCommand = command
		return &candidate, nil
	}
	if strings.TrimSpace(strings.ToLower(name)) == "auto" {
		return nil, fmt.Errorf("could not auto-bootstrap a rootless toolchain on this machine; no supported auto-bootstrap manager command is callable. Try `rs toolchain detect`, `rs toolchain bootstrap enva`, `rs toolchain bootstrap homebrew`, or `rs toolchain bootstrap spack`")
	}
	return nil, fmt.Errorf("toolchain preset %s is not callable on this machine; install or expose the matching manager first, or try `rs toolchain detect`", name)
}

func candidateForPreset(name, home string) (Candidate, error) {
	prefixes, pkgConfig, err := ResolvePreset(name, home)
	if err != nil {
		return Candidate{}, err
	}
	suggestedInitCommand := fmt.Sprintf("rs init --toolchain-preset %s", name)
	originalPrefixes := append([]string(nil), prefixes...)
	originalPkgConfig := append([]string(nil), pkgConfig...)
	actualPrefixes, actualPkgConfig := detectedCandidatePaths(name, prefixes, pkgConfig)
	if len(actualPrefixes) > 0 {
		prefixes = actualPrefixes
		pkgConfig = actualPkgConfig
		if !slices.Equal(originalPrefixes, actualPrefixes) || !slices.Equal(originalPkgConfig, actualPkgConfig) {
			suggestedInitCommand = explicitInitCommand(actualPrefixes, actualPkgConfig)
		}
	}
	existingPrefixes := existingTemplatePaths(prefixes)
	existingPkgConfig := existingTemplatePaths(pkgConfig)
	return Candidate{
		Preset:                name,
		ToolchainPrefixes:     prefixes,
		PkgConfigPath:         pkgConfig,
		ExistingPrefixes:      existingPrefixes,
		ExistingPkgConfigPath: existingPkgConfig,
		SuggestedInitCommand:  suggestedInitCommand,
		SuggestedSetupCommand: suggestedSetupCommand(name, prefixes),
		SuggestedSetupNote:    suggestedSetupNote(name, prefixes),
		Complete:              len(existingPrefixes) == len(prefixes) && len(existingPkgConfig) == len(pkgConfig),
	}, nil
}

func detectedCandidatePaths(name string, prefixes, pkgConfig []string) ([]string, []string) {
	if len(prefixes) == 0 {
		return nil, nil
	}
	switch name {
	case "enva":
		envName := strings.TrimSpace(filepath.Base(prefixes[0]))
		if envName == "" {
			return nil, nil
		}
		actualPrefixes := discoverEnvaEnvironmentPrefixes(os.Environ(), envName)
		if len(actualPrefixes) == 0 {
			return nil, nil
		}
		actualPkgConfig := pkgConfigPathsForPrefixes(actualPrefixes)
		if len(existingTemplatePaths(actualPrefixes)) == 0 && len(existingTemplatePaths(actualPkgConfig)) == 0 {
			return nil, nil
		}
		return actualPrefixes, actualPkgConfig
	default:
		return nil, nil
	}
}

func resolveBootstrappedCandidate(candidate Candidate, env []string) *Candidate {
	actualPrefixes, actualPkgConfig := discoverBootstrappedPaths(candidate, env)
	if len(actualPrefixes) == 0 {
		return &candidate
	}
	originalPrefixes := append([]string(nil), candidate.ToolchainPrefixes...)
	originalPkgConfig := append([]string(nil), candidate.PkgConfigPath...)
	candidate.ToolchainPrefixes = actualPrefixes
	candidate.PkgConfigPath = actualPkgConfig
	candidate.ExistingPrefixes = existingTemplatePaths(actualPrefixes)
	candidate.ExistingPkgConfigPath = existingTemplatePaths(actualPkgConfig)
	candidate.Complete = len(candidate.ExistingPrefixes) == len(actualPrefixes) && len(candidate.ExistingPkgConfigPath) == len(actualPkgConfig)
	if !slices.Equal(originalPrefixes, actualPrefixes) || !slices.Equal(originalPkgConfig, actualPkgConfig) {
		candidate.SuggestedInitCommand = explicitInitCommand(actualPrefixes, actualPkgConfig)
	}
	return &candidate
}

func discoverBootstrappedPaths(candidate Candidate, env []string) ([]string, []string) {
	if len(candidate.ToolchainPrefixes) == 0 {
		return nil, nil
	}
	envName := strings.TrimSpace(filepath.Base(candidate.ToolchainPrefixes[0]))
	if envName == "" {
		return nil, nil
	}
	switch candidate.Preset {
	case "enva":
		prefixes := discoverEnvaEnvironmentPrefixes(env, envName)
		if len(prefixes) == 0 {
			return nil, nil
		}
		return prefixes, pkgConfigPathsForPrefixes(prefixes)
	default:
		return nil, nil
	}
}

func discoverEnvaEnvironmentPrefixes(env []string, envName string) []string {
	path, err := FindInPath("enva", env)
	if err != nil {
		return nil
	}
	output, err := detectOutput(path, []string{"list"}, env)
	if err != nil {
		return nil
	}
	return parseEnvaList(output, envName)
}

func parseEnvaList(output string, envName string) []string {
	if strings.TrimSpace(output) == "" || strings.TrimSpace(envName) == "" {
		return nil
	}
	prefixes := []string{}
	seen := map[string]struct{}{}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name != envName {
			continue
		}
		for _, prefix := range strings.Split(strings.TrimSpace(parts[2]), ",") {
			cleaned := filepath.Clean(strings.TrimSpace(prefix))
			if cleaned == "." || cleaned == "" {
				continue
			}
			if _, ok := seen[cleaned]; ok {
				continue
			}
			seen[cleaned] = struct{}{}
			prefixes = append(prefixes, cleaned)
		}
	}
	return prefixes
}

func pkgConfigPathsForPrefixes(prefixes []string) []string {
	paths := make([]string, 0, len(prefixes)*2)
	seen := map[string]struct{}{}
	for _, prefix := range prefixes {
		for _, path := range []string{
			filepath.Join(prefix, "lib", "pkgconfig"),
			filepath.Join(prefix, "share", "pkgconfig"),
		} {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	return paths
}

func explicitInitCommand(prefixes, pkgConfig []string) string {
	parts := []string{"rs", "init"}
	for _, prefix := range prefixes {
		parts = append(parts, "--toolchain-prefix", prefix)
	}
	for _, path := range pkgConfig {
		parts = append(parts, "--pkg-config-path", path)
	}
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.ContainsAny(part, " \t\"'") {
			quoted = append(quoted, strconv.Quote(part))
		} else {
			quoted = append(quoted, part)
		}
	}
	return strings.Join(quoted, " ")
}

func bootstrapCandidates(name, home string) ([]Candidate, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name != "" && name != "auto" {
		candidate, err := DescribePreset(name, home)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			return nil, fmt.Errorf("toolchain preset %s is not available", name)
		}
		return []Candidate{*candidate}, nil
	}

	homeDir, err := resolveHomeDir(home)
	if err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, len(autoPresets()))
	for _, preset := range autoPresets() {
		candidate, err := candidateForPreset(preset, homeDir)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	slices.SortFunc(candidates, compareCandidates)
	if len(candidates) > 0 {
		candidates[0].Recommended = true
	}
	return candidates, nil
}

func bootstrapCommandForCandidate(candidate Candidate, env []string) (string, bool) {
	prefix := ""
	if len(candidate.ToolchainPrefixes) > 0 {
		prefix = candidate.ToolchainPrefixes[0]
	}
	switch candidate.Preset {
	case "enva":
		path, err := FindInPath("enva", env)
		if err != nil {
			return "", false
		}
		return envaBootstrapCommand(path), true
	case "micromamba":
		path, err := FindInPath("micromamba", env)
		if err != nil {
			return "", false
		}
		return fmt.Sprintf(`"%s" create -y -p "%s" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`, path, prefix), true
	case "mamba":
		path, err := FindInPath("mamba", env)
		if err != nil {
			return "", false
		}
		return fmt.Sprintf(`"%s" create -y -p "%s" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`, path, prefix), true
	case "conda":
		path, err := FindInPath("conda", env)
		if err != nil {
			return "", false
		}
		return fmt.Sprintf(`"%s" create -y -p "%s" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`, path, prefix), true
	case "homebrew":
		brewPath := filepath.Join(prefix, "bin", "brew")
		if info, err := detectStat(brewPath); err == nil && !info.IsDir() {
			return fmt.Sprintf(`"%s" install pkg-config gcc cmake`, brewPath), true
		}
		if len(candidate.ExistingPrefixes) > 0 {
			if path, err := FindInPath("brew", env); err == nil {
				return fmt.Sprintf(`"%s" install pkg-config gcc cmake`, path), true
			}
		}
		return "", false
	case "spack":
		path, err := FindInPath("spack", env)
		if err != nil {
			return "", false
		}
		return fmt.Sprintf(`"%s" view symlink "%s" pkgconf gcc cmake`, path, prefix), true
	default:
		return "", false
	}
}

func runBootstrapCommand(command string, env []string, stdout, stderr io.Writer) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-lc", command)
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func runCommandOutput(name string, args []string, env []string) (string, error) {
	cmd := exec.Command(name, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func existingTemplatePaths(paths []string) []string {
	existing := []string{}
	for _, candidatePath := range paths {
		info, err := os.Stat(candidatePath)
		if err != nil || !info.IsDir() {
			continue
		}
		existing = append(existing, candidatePath)
	}
	return existing
}

func compareCandidates(left, right Candidate) int {
	if left.Complete != right.Complete {
		if left.Complete {
			return -1
		}
		return 1
	}
	leftFound := len(left.ExistingPrefixes) + len(left.ExistingPkgConfigPath)
	rightFound := len(right.ExistingPrefixes) + len(right.ExistingPkgConfigPath)
	if leftFound != rightFound {
		if leftFound > rightFound {
			return -1
		}
		return 1
	}
	leftPriority := presetPriority(left.Preset)
	rightPriority := presetPriority(right.Preset)
	if leftPriority != rightPriority {
		if leftPriority < rightPriority {
			return -1
		}
		return 1
	}
	return strings.Compare(left.Preset, right.Preset)
}

func presetPriority(preset string) int {
	switch runtime.GOOS {
	case "darwin":
		switch preset {
		case "enva":
			return 0
		case "micromamba":
			return 1
		case "mamba":
			return 2
		case "conda":
			return 3
		case "homebrew":
			return 4
		case "spack":
			return 5
		}
	default:
		switch preset {
		case "enva":
			return 0
		case "micromamba":
			return 1
		case "mamba":
			return 2
		case "conda":
			return 3
		case "homebrew":
			return 4
		case "spack":
			return 5
		}
	}
	return 100
}

func suggestedSetupCommand(preset string, prefixes []string) string {
	prefix := ""
	if len(prefixes) > 0 {
		prefix = prefixes[0]
	}
	switch preset {
	case "enva":
		return envaBootstrapCommand("enva")
	case "micromamba":
		if prefix == "" {
			return `micromamba create -y -p "$HOME/micromamba/envs/rs-sysdeps" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`
		}
		return fmt.Sprintf(`micromamba create -y -p "%s" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`, prefix)
	case "mamba":
		if prefix == "" {
			return `mamba create -y -p "$HOME/.local/share/mamba/envs/rs-sysdeps" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`
		}
		return fmt.Sprintf(`mamba create -y -p "%s" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`, prefix)
	case "conda":
		if prefix == "" {
			return `conda create -y -p "$HOME/.conda/envs/rs-sysdeps" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`
		}
		return fmt.Sprintf(`conda create -y -p "%s" -c conda-forge compilers binutils sysroot_linux-64=2.17 pkg-config make cmake`, prefix)
	case "homebrew":
		if prefix == "" {
			return `"$HOME/homebrew/bin/brew" install pkg-config gcc cmake`
		}
		return fmt.Sprintf(`"%s" install pkg-config gcc cmake`, filepath.Join(prefix, "bin", "brew"))
	case "spack":
		if prefix == "" {
			return `spack view symlink "$HOME/spack/views/rs-sysdeps" pkgconf gcc cmake`
		}
		return fmt.Sprintf(`spack view symlink "%s" pkgconf gcc cmake`, prefix)
	default:
		return ""
	}
}

func suggestedSetupNote(preset string, prefixes []string) string {
	prefix := ""
	if len(prefixes) > 0 {
		prefix = prefixes[0]
	}
	switch preset {
	case "enva":
		if prefix == "" {
			return "create a dedicated rattler-managed rs-sysdeps environment with enva, then let rs wire its bin/include/lib/pkgconfig paths automatically"
		}
		return fmt.Sprintf("this creates a dedicated enva-managed build-tools environment at %s that rs can wire into PATH, CPPFLAGS, LDFLAGS, and PKG_CONFIG_PATH", prefix)
	case "micromamba":
		if prefix == "" {
			return "create a dedicated micromamba or Conda environment for build tools instead of reusing a large runtime R environment"
		}
		return fmt.Sprintf("this creates a dedicated build-tools environment at %s that rs can wire into PATH, CPPFLAGS, LDFLAGS, and PKG_CONFIG_PATH", prefix)
	case "mamba":
		if prefix == "" {
			return "create a dedicated mamba environment for build tools instead of reusing a large runtime R environment"
		}
		return fmt.Sprintf("this creates a dedicated mamba-managed build-tools environment at %s that rs can wire into PATH, CPPFLAGS, LDFLAGS, and PKG_CONFIG_PATH", prefix)
	case "conda":
		if prefix == "" {
			return "create a dedicated conda environment for build tools instead of reusing a large runtime R environment"
		}
		return fmt.Sprintf("this creates a dedicated conda-managed build-tools environment at %s that rs can wire into PATH, CPPFLAGS, LDFLAGS, and PKG_CONFIG_PATH", prefix)
	case "homebrew":
		if prefix == "" {
			return "install or reuse a Homebrew prefix in your home directory first, then use brew to add pkg-config, gcc, and any headers or libraries your R packages need"
		}
		return fmt.Sprintf("install or reuse Homebrew under %s first, then add pkg-config, gcc, and any package-specific libraries there", prefix)
	case "spack":
		if prefix == "" {
			return "use a Spack view that contains pkgconf, compiler toolchains, and any package-specific libraries your source builds need"
		}
		return fmt.Sprintf("this assumes your site already provides Spack; populate the view at %s with pkgconf, compiler toolchains, and any package-specific libraries", prefix)
	default:
		return ""
	}
}

func envaBootstrapCommand(path string) string {
	if strings.TrimSpace(path) == "" {
		path = "enva"
	}
	return fmt.Sprintf(`tmp="$(mktemp "${TMPDIR:-/tmp}/rs-enva-XXXXXX.yaml")" && cat >"$tmp" <<'EOF'
channels:
  - conda-forge
dependencies:
  - compilers
  - binutils
  - sysroot_linux-64=2.17
  - pkg-config
  - make
  - cmake
EOF
"%s" create --yaml "$tmp" --name rs-sysdeps --force --clean-cache`, path)
}
