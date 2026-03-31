package rmanager

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
)

var (
	rscriptLookPath = exec.LookPath
	rscriptGlob     = filepath.Glob
	rscriptStat     = os.Stat
	rscriptAbs      = filepath.Abs
	rscriptHomeDir  = os.UserHomeDir
	nativeGOOS      = runtime.GOOS
	nativeGOARCH    = runtime.GOARCH
)

type RBootstrapAdvice struct {
	ManualMessage string
	ManualCommand string
	AutoEnableEnv string
}

func (a RBootstrapAdvice) ManualMessageWithCommand() string {
	if a.ManualCommand != "" {
		return fmt.Sprintf("%s: %s", a.ManualMessage, a.ManualCommand)
	}
	return a.ManualMessage
}

func LooksLikeVersionSpec(spec string) bool {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return false
	}
	if isNamedVersionSpec(spec) {
		return true
	}

	hasDigit := false
	for _, r := range spec {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case r == '.', r == '-', unicode.IsLetter(r):
		default:
			return false
		}
	}
	return hasDigit
}

func VersionMatchesSpec(spec, actual string) bool {
	spec = strings.TrimSpace(strings.ToLower(spec))
	actual = strings.TrimSpace(strings.ToLower(actual))
	if spec == "" || actual == "" {
		return true
	}
	if isNamedVersionSpec(spec) {
		return true
	}

	specVersion, specOK := parseVersionHint(spec)
	actualVersion, actualOK := parseVersionHint(actual)
	if specOK && actualOK {
		if len(specVersion) > len(actualVersion) {
			return false
		}
		for i := range specVersion {
			if specVersion[i] != actualVersion[i] {
				return false
			}
		}
		return true
	}
	return spec == actual
}

func resolveExplicitRscript(target string) (string, error) {
	if looksLikePath(target) {
		path := target
		if !filepath.IsAbs(path) {
			absPath, err := rscriptAbs(path)
			if err != nil {
				return "", fmt.Errorf("resolve Rscript path %q: %w", target, err)
			}
			path = absPath
		}
		info, err := rscriptStat(path)
		if err != nil {
			return "", fmt.Errorf("Rscript %q is not available: %w", target, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("Rscript %q is a directory", path)
		}
		return path, nil
	}

	path, err := rscriptLookPath(target)
	if err != nil {
		return "", fmt.Errorf("Rscript command %q is not available: %w", target, err)
	}
	return path, nil
}

func installedRscriptCandidates(version string) ([]string, error) {
	home, _ := rscriptHomeDir()

	patterns := []string{}
	switch nativeGOOS {
	case "windows":
		programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
		if programFiles != "" {
			patterns = append(patterns,
				filepath.Join(programFiles, "R", "R-"+version+"*", "bin", rscriptExecutableName()),
				filepath.Join(programFiles, "R", "R-"+version+"*", "bin", "x64", rscriptExecutableName()),
			)
		}
		programFilesX86 := strings.TrimSpace(os.Getenv("ProgramFiles(x86)"))
		if programFilesX86 != "" {
			patterns = append(patterns,
				filepath.Join(programFilesX86, "R", "R-"+version+"*", "bin", rscriptExecutableName()),
				filepath.Join(programFilesX86, "R", "R-"+version+"*", "bin", "x64", rscriptExecutableName()),
			)
		}
		localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if localAppData != "" {
			patterns = append(patterns,
				filepath.Join(localAppData, "Programs", "R", "R-"+version+"*", "bin", rscriptExecutableName()),
				filepath.Join(localAppData, "Programs", "R", "R-"+version+"*", "bin", "x64", rscriptExecutableName()),
			)
		}
	default:
		patterns = append(patterns,
			filepath.Join("/Library/Frameworks/R.framework/Versions", version+"*", "Resources", "bin", rscriptExecutableName()),
			filepath.Join("/opt/R", version+"*", "bin", rscriptExecutableName()),
			filepath.Join("/usr/local/lib/R", version+"*", "bin", rscriptExecutableName()),
		)
		if home != "" {
			patterns = append(patterns,
				filepath.Join(home, ".local", "share", "rs", "r", "versions", version+"*", "bin", rscriptExecutableName()),
				filepath.Join(home, ".rig", "R", version+"*", "bin", rscriptExecutableName()),
				filepath.Join(home, ".local", "share", "rig", "R", version+"*", "bin", rscriptExecutableName()),
				filepath.Join(home, "Library", "Application Support", "rig", "R", version+"*", "bin", rscriptExecutableName()),
			)
		}
	}
	if home != "" && nativeGOOS == "windows" {
		patterns = append(patterns,
			filepath.Join(home, "AppData", "Local", "rs", "r", "versions", version+"*", "bin", rscriptExecutableName()),
			filepath.Join(home, "AppData", "Local", "Programs", "R", "R-"+version+"*", "bin", rscriptExecutableName()),
		)
	}

	seen := map[string]struct{}{}
	var matches []string
	for _, pattern := range patterns {
		hits, err := rscriptGlob(pattern)
		if err != nil {
			return nil, fmt.Errorf("glob installed R versions: %w", err)
		}
		for _, hit := range hits {
			if _, ok := seen[hit]; ok {
				continue
			}
			seen[hit] = struct{}{}
			matches = append(matches, hit)
		}
	}
	slices.Sort(matches)
	return matches, nil
}

func compareRscriptCandidates(left, right string) int {
	leftVersion, leftOK := parseVersionHint(left)
	rightVersion, rightOK := parseVersionHint(right)
	switch {
	case leftOK && rightOK:
		for i := 0; i < len(leftVersion) || i < len(rightVersion); i++ {
			leftPart := 0
			if i < len(leftVersion) {
				leftPart = leftVersion[i]
			}
			rightPart := 0
			if i < len(rightVersion) {
				rightPart = rightVersion[i]
			}
			if leftPart > rightPart {
				return 1
			}
			if leftPart < rightPart {
				return -1
			}
		}
	case leftOK:
		return 1
	case rightOK:
		return -1
	}

	if left > right {
		return 1
	}
	if left < right {
		return -1
	}
	return 0
}

func parseVersionHint(path string) ([]int, bool) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if version, ok := parseLeadingVersion(parts[i]); ok {
			return version, true
		}
	}
	return nil, false
}

func parseLeadingVersion(component string) ([]int, bool) {
	var current int
	haveCurrent := false
	var version []int
	for _, r := range component {
		switch {
		case unicode.IsDigit(r):
			haveCurrent = true
			current = current*10 + int(r-'0')
		case r == '.' && haveCurrent:
			version = append(version, current)
			current = 0
			haveCurrent = false
		default:
			if haveCurrent {
				version = append(version, current)
			}
			return version, len(version) > 0
		}
	}
	if haveCurrent {
		version = append(version, current)
	}
	return version, len(version) > 0
}

func defaultAutoInstallVersion() string {
	if value := strings.TrimSpace(os.Getenv(defaultAutoInstallVersionEnv)); value != "" {
		return value
	}
	return "release"
}

func isNamedVersionSpec(spec string) bool {
	switch strings.TrimSpace(strings.ToLower(spec)) {
	case "release", "oldrel", "devel", "next":
		return true
	default:
		return false
	}
}

func rscriptExecutableName() string {
	if nativeGOOS == "windows" {
		return "Rscript.exe"
	}
	return "Rscript"
}

func looksLikePath(target string) bool {
	return filepath.IsAbs(target) || strings.ContainsAny(target, `/\`)
}
