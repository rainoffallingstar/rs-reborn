package toolchainenv

import (
	"path/filepath"
	"slices"
	"strings"
)

func isEnvaManagedPrefix(prefix string) bool {
	cleaned := strings.ToLower(filepath.ToSlash(strings.TrimSpace(prefix)))
	return strings.Contains(cleaned, "/rattler/envs/")
}

// WrapCommand prefers a manager-native execution path for known rootless
// toolchain environments, then falls back to the original command.
func WrapCommand(name string, args []string, env []string) (string, []string, []string, bool, error) {
	candidate, err := CandidateFromEnvironment(env)
	if err != nil || candidate == nil {
		return name, append([]string(nil), args...), env, false, err
	}
	if len(candidate.ToolchainPrefixes) == 0 {
		return name, append([]string(nil), args...), env, false, nil
	}

	switch candidate.Preset {
	case "enva":
		if !isEnvaManagedPrefix(candidate.ToolchainPrefixes[0]) {
			return name, append([]string(nil), args...), env, false, nil
		}
		runner, err := FindInPath("enva", env)
		if err != nil {
			return name, append([]string(nil), args...), env, false, nil
		}
		envName := strings.TrimSpace(filepath.Base(candidate.ToolchainPrefixes[0]))
		if envName == "" {
			return name, append([]string(nil), args...), env, false, nil
		}
		wrapped := append([]string{"run", envName, "--", name}, args...)
		return runner, wrapped, env, true, nil
	case "micromamba", "mamba", "conda":
		runner, err := FindInPath(candidate.Preset, env)
		if err != nil {
			return name, append([]string(nil), args...), env, false, nil
		}
		wrapped := append([]string{"run", "-p", candidate.ToolchainPrefixes[0], "--", name}, args...)
		return runner, wrapped, env, true, nil
	default:
		return name, append([]string(nil), args...), env, false, nil
	}
}

func CandidateFromEnvironment(env []string) (*Candidate, error) {
	candidate, err := CandidateFromPaths(PrefixesFromEnv(env), PkgConfigPathsFromEnv(env), "")
	if err != nil || candidate != nil {
		return candidate, err
	}
	return heuristicCandidateFromEnvironment(env), nil
}

func CandidateFromPaths(prefixes, pkgConfig []string, home string) (*Candidate, error) {
	cleanPrefixes := cleanList(prefixes)
	cleanPkgConfig := cleanList(pkgConfig)
	if len(cleanPrefixes) == 0 && len(cleanPkgConfig) == 0 {
		return nil, nil
	}

	homeDir, err := resolveHomeDir(home)
	if err != nil {
		return nil, err
	}
	for _, preset := range SupportedPresets() {
		candidate, err := candidateForPreset(preset, homeDir)
		if err != nil {
			return nil, err
		}
		if candidateMatchesPaths(candidate, cleanPrefixes, cleanPkgConfig) {
			return &candidate, nil
		}
	}
	return nil, nil
}

func candidateMatchesPaths(candidate Candidate, prefixes, pkgConfig []string) bool {
	if len(prefixes) > 0 && !slices.Equal(prefixes, candidate.ToolchainPrefixes) {
		return false
	}
	if len(pkgConfig) > 0 && !slices.Equal(pkgConfig, candidate.PkgConfigPath) {
		return false
	}
	return true
}

func heuristicCandidateFromEnvironment(env []string) *Candidate {
	prefixes := cleanList(PrefixesFromEnv(env))
	pkgConfig := cleanList(PkgConfigPathsFromEnv(env))
	if len(prefixes) == 0 && len(pkgConfig) == 0 {
		return nil
	}
	if len(prefixes) == 0 {
		return nil
	}
	firstPrefix := strings.ToLower(filepath.ToSlash(prefixes[0]))
	if filepath.Base(prefixes[0]) != "rs-sysdeps" {
		return nil
	}
	if !strings.Contains(firstPrefix, "/envs/") {
		return nil
	}

	preset := ""
	switch {
	case commandAvailableInEnv("enva", env):
		preset = "enva"
	case strings.Contains(firstPrefix, "/micromamba/") && commandAvailableInEnv("micromamba", env):
		preset = "micromamba"
	case strings.Contains(firstPrefix, "/mamba/") && commandAvailableInEnv("mamba", env):
		preset = "mamba"
	case (strings.Contains(firstPrefix, "miniconda") || strings.Contains(firstPrefix, "anaconda") || strings.Contains(firstPrefix, "/conda/")) && commandAvailableInEnv("conda", env):
		preset = "conda"
	default:
		return nil
	}

	candidate := &Candidate{
		Preset:                preset,
		ToolchainPrefixes:     prefixes,
		PkgConfigPath:         pkgConfig,
		ExistingPrefixes:      existingTemplatePaths(prefixes),
		ExistingPkgConfigPath: existingTemplatePaths(pkgConfig),
		SuggestedInitCommand:  explicitInitCommand(prefixes, pkgConfig),
		SuggestedSetupCommand: suggestedSetupCommand(preset, prefixes),
		SuggestedSetupNote:    suggestedSetupNote(preset, prefixes),
	}
	candidate.Complete = len(candidate.ExistingPrefixes) == len(prefixes) && len(candidate.ExistingPkgConfigPath) == len(pkgConfig)
	return candidate
}

func commandAvailableInEnv(name string, env []string) bool {
	_, err := FindInPath(name, env)
	return err == nil
}
