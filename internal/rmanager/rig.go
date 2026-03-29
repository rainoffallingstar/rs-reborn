package rmanager

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
)

var (
	rigLookPath = exec.LookPath
	rigCommand  = exec.Command
	rigGlob     = filepath.Glob
	rigStat     = os.Stat
	rigAbs      = filepath.Abs
	rigHomeDir  = os.UserHomeDir
)

const defaultAutoInstallVersionEnv = "RS_R_VERSION"

func List(stdout, stderr io.Writer) error {
	return runRig(stdout, stderr, "list")
}

func Install(version string, stdout, stderr io.Writer) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return fmt.Errorf("R version is required")
	}
	return runRig(stdout, stderr, "add", version)
}

func ResolveVersionOrPath(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", fmt.Errorf("R version or Rscript path is required")
	}

	if looksLikePath(spec) || strings.Contains(strings.ToLower(spec), "rscript") {
		return resolveExplicitRscript(spec)
	}

	versionSpec := spec
	if isNamedVersionSpec(spec) {
		versionSpec = "*"
	}
	candidate, err := bestInstalledRscript(versionSpec)
	if err == nil {
		return candidate, nil
	}

	return "", fmt.Errorf("could not find an installed Rscript for version %q; run `rs r list`, install it with `rs r install %s`, or set rs.toml rscript manually", spec, spec)
}

func EnsureInstalledRscript(spec string, stdout, stderr io.Writer) (string, error) {
	spec = strings.TrimSpace(spec)
	requested := spec
	target := spec
	switch {
	case requested == "", strings.EqualFold(requested, "Rscript"), strings.EqualFold(requested, "Rscript.exe"):
		target = defaultAutoInstallVersion()
	case !LooksLikeVersionSpec(requested):
		return "", fmt.Errorf("automatic R installation requires a version-like target, got %q", requested)
	}

	if stderr != nil {
		fmt.Fprintf(stderr, "[rs] Rscript is not available; installing R %s via rig\n", target)
	}
	if err := Install(target, io.Discard, stderr); err != nil {
		return "", err
	}

	switch {
	case requested == "", strings.EqualFold(requested, "Rscript"), strings.EqualFold(requested, "Rscript.exe"), isNamedVersionSpec(requested):
		return bestInstalledRscript("*")
	default:
		return ResolveVersionOrPath(requested)
	}
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

func runRig(stdout, stderr io.Writer, args ...string) error {
	rigPath, err := rigLookPath("rig")
	if err != nil {
		return fmt.Errorf("rig is required for `rs r`; install rig first: %w", err)
	}

	cmd := rigCommand(rigPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run rig %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func resolveExplicitRscript(target string) (string, error) {
	if looksLikePath(target) {
		path := target
		if !filepath.IsAbs(path) {
			absPath, err := rigAbs(path)
			if err != nil {
				return "", fmt.Errorf("resolve Rscript path %q: %w", target, err)
			}
			path = absPath
		}
		info, err := rigStat(path)
		if err != nil {
			return "", fmt.Errorf("Rscript %q is not available: %w", target, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("Rscript %q is a directory", path)
		}
		return path, nil
	}

	path, err := rigLookPath(target)
	if err != nil {
		return "", fmt.Errorf("Rscript command %q is not available: %w", target, err)
	}
	return path, nil
}

func installedRscriptCandidates(version string) ([]string, error) {
	home, _ := rigHomeDir()

	patterns := []string{
		filepath.Join("/Library/Frameworks/R.framework/Versions", version+"*", "Resources", "bin", rscriptExecutableName()),
		filepath.Join("/opt/R", version+"*", "bin", rscriptExecutableName()),
		filepath.Join("/usr/local/lib/R", version+"*", "bin", rscriptExecutableName()),
	}
	if home != "" {
		patterns = append(patterns,
			filepath.Join(home, ".rig", "R", version+"*", "bin", rscriptExecutableName()),
			filepath.Join(home, ".local", "share", "rig", "R", version+"*", "bin", rscriptExecutableName()),
			filepath.Join(home, "Library", "Application Support", "rig", "R", version+"*", "bin", rscriptExecutableName()),
		)
	}

	seen := map[string]struct{}{}
	var matches []string
	for _, pattern := range patterns {
		hits, err := rigGlob(pattern)
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

func bestInstalledRscript(version string) (string, error) {
	candidates, err := installedRscriptCandidates(version)
	if err != nil {
		return "", err
	}

	best := ""
	for _, candidate := range candidates {
		info, err := rigStat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if best == "" || compareRscriptCandidates(candidate, best) > 0 {
			best = candidate
		}
	}
	if best == "" {
		return "", fmt.Errorf("no installed Rscript found for version %q", version)
	}
	return best, nil
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
	if runtime.GOOS == "windows" {
		return "Rscript.exe"
	}
	return "Rscript"
}

func looksLikePath(target string) bool {
	return filepath.IsAbs(target) || strings.ContainsAny(target, `/\`)
}
