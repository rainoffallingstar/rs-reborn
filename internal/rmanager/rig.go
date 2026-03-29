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
)

var (
	rigLookPath = exec.LookPath
	rigCommand  = exec.Command
	rigGlob     = filepath.Glob
	rigStat     = os.Stat
	rigAbs      = filepath.Abs
	rigHomeDir  = os.UserHomeDir
)

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

	candidates, err := installedRscriptCandidates(spec)
	if err != nil {
		return "", err
	}
	for _, candidate := range candidates {
		info, err := rigStat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		return candidate, nil
	}

	return "", fmt.Errorf("could not find an installed Rscript for version %q; run `rs r list`, install it with `rs r install %s`, or set rs.toml rscript manually", spec, spec)
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

func rscriptExecutableName() string {
	if runtime.GOOS == "windows" {
		return "Rscript.exe"
	}
	return "Rscript"
}

func looksLikePath(target string) bool {
	return filepath.IsAbs(target) || strings.ContainsAny(target, `/\`)
}
