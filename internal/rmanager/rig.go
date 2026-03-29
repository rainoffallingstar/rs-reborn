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
	rigLookPath  = exec.LookPath
	rigCommand   = exec.Command
	rigGlob      = filepath.Glob
	rigStat      = os.Stat
	rigAbs       = filepath.Abs
	rigHomeDir   = os.UserHomeDir
	toolLookPath = exec.LookPath
	toolCommand  = exec.Command
	rigOS        = runtime.GOOS
)

const defaultAutoInstallVersionEnv = "RS_R_VERSION"
const autoInstallRigEnv = "RS_AUTO_INSTALL_RIG"

type rigBootstrapStep struct {
	Name string
	Args []string
}

type RigBootstrapAdvice struct {
	ManualMessage string
	ManualCommand string
	AutoEnableEnv string
}

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
	rigPath, err := ensureRigAvailable(stdout, stderr)
	if err != nil {
		return err
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

func RigAvailable() bool {
	_, err := rigLookPath("rig")
	return err == nil
}

func AutoInstallRigEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(autoInstallRigEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func BootstrapAdvice() RigBootstrapAdvice {
	plan := detectRigBootstrapPlan()
	advice := RigBootstrapAdvice{
		AutoEnableEnv: autoInstallRigEnv,
	}
	if plan.manualMessage != "" {
		advice.ManualMessage = plan.manualMessage
		advice.ManualCommand = plan.manualCommand
		return advice
	}
	advice.ManualMessage = "install rig for your platform from the official releases page and make sure it is available on PATH"
	return advice
}

func ensureRigAvailable(stdout, stderr io.Writer) (string, error) {
	rigPath, err := rigLookPath("rig")
	if err == nil {
		return rigPath, nil
	}

	advice := BootstrapAdvice()
	if !AutoInstallRigEnabled() {
		return "", fmt.Errorf("rig is required but is not available on PATH: %w\nnext step: %s\nexplicit auto-install: set %s=1 and retry", err, advice.ManualMessageWithCommand(), advice.AutoEnableEnv)
	}

	if stderr != nil {
		fmt.Fprintf(stderr, "[rs] rig is not available; attempting installation because %s=1\n", autoInstallRigEnv)
	}
	if err := installRig(stdout, stderr); err != nil {
		return "", fmt.Errorf("rig is required but is not available on PATH: %w\nautomatic rig installation failed: %v\nnext step: %s", err, err, advice.ManualMessageWithCommand())
	}

	rigPath, err = rigLookPath("rig")
	if err != nil {
		return "", fmt.Errorf("rig installation completed but `rig` is still not available on PATH: %w\nnext step: %s", err, advice.ManualMessageWithCommand())
	}
	return rigPath, nil
}

func installRig(stdout, stderr io.Writer) error {
	plan := detectRigBootstrapPlan()
	if len(plan.steps) == 0 {
		advice := BootstrapAdvice()
		return fmt.Errorf("no supported automatic rig installer was detected for %s; %s", rigOS, advice.ManualMessageWithCommand())
	}

	for _, step := range plan.steps {
		if stderr != nil {
			fmt.Fprintf(stderr, "[rs] bootstrapping rig: %s\n", renderBootstrapStep(step))
		}
		cmd := toolCommand(step.Name, step.Args...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("run %s: %w", renderBootstrapStep(step), err)
		}
	}
	return nil
}

type rigBootstrapPlan struct {
	manualMessage string
	manualCommand string
	steps         []rigBootstrapStep
}

func detectRigBootstrapPlan() rigBootstrapPlan {
	switch rigOS {
	case "darwin":
		if hasTool("brew") {
			return rigBootstrapPlan{
				manualMessage: "install rig with Homebrew and rerun rs",
				manualCommand: "brew tap r-lib/rig && brew install --cask rig",
				steps: []rigBootstrapStep{
					{Name: "brew", Args: []string{"tap", "r-lib/rig"}},
					{Name: "brew", Args: []string{"install", "--cask", "rig"}},
				},
			}
		}
	case "windows":
		if hasTool("winget") {
			return rigBootstrapPlan{
				manualMessage: "install rig with WinGet and rerun rs",
				manualCommand: "winget install posit.rig",
				steps: []rigBootstrapStep{
					{Name: "winget", Args: []string{"install", "posit.rig"}},
				},
			}
		}
		if hasTool("choco") {
			return rigBootstrapPlan{
				manualMessage: "install rig with Chocolatey and rerun rs",
				manualCommand: "choco install rig",
				steps: []rigBootstrapStep{
					{Name: "choco", Args: []string{"install", "rig", "-y"}},
				},
			}
		}
		if hasTool("scoop") {
			return rigBootstrapPlan{
				manualMessage: "install rig with Scoop and rerun rs",
				manualCommand: "scoop bucket add r-bucket https://github.com/cderv/r-bucket.git && scoop install rig",
				steps: []rigBootstrapStep{
					{Name: "scoop", Args: []string{"bucket", "add", "r-bucket", "https://github.com/cderv/r-bucket.git"}},
					{Name: "scoop", Args: []string{"install", "rig"}},
				},
			}
		}
	default:
		if hasTool("apt") || hasTool("apt-get") {
			aptCmd := "apt"
			if hasTool("apt-get") {
				aptCmd = "apt-get"
			}
			return rigBootstrapPlan{
				manualMessage: "install rig from the official Debian/Ubuntu repository and rerun rs",
				manualCommand: "curl -L https://rig.r-pkg.org/deb/rig.gpg -o /etc/apt/trusted.gpg.d/rig.gpg && echo 'deb http://rig.r-pkg.org/deb rig main' > /etc/apt/sources.list.d/rig.list && apt update && apt install r-rig",
				steps: []rigBootstrapStep{
					withPrivilege("curl", "-L", "https://rig.r-pkg.org/deb/rig.gpg", "-o", "/etc/apt/trusted.gpg.d/rig.gpg"),
					withPrivilege("sh", "-c", `echo "deb http://rig.r-pkg.org/deb rig main" > /etc/apt/sources.list.d/rig.list`),
					withPrivilege(aptCmd, "update"),
					withPrivilege(aptCmd, "install", "-y", "r-rig"),
				},
			}
		}
		if hasTool("dnf") {
			return rigBootstrapPlan{
				manualMessage: "install rig from the official RPM package and rerun rs",
				manualCommand: "dnf install -y https://github.com/r-lib/rig/releases/download/latest/r-rig-latest-1.$(arch).rpm",
				steps: []rigBootstrapStep{
					withPrivilege("dnf", "install", "-y", "https://github.com/r-lib/rig/releases/download/latest/r-rig-latest-1.$(arch).rpm"),
				},
			}
		}
		if hasTool("yum") {
			return rigBootstrapPlan{
				manualMessage: "install rig from the official RPM package and rerun rs",
				manualCommand: "yum install -y https://github.com/r-lib/rig/releases/download/latest/r-rig-latest-1.$(arch).rpm",
				steps: []rigBootstrapStep{
					withPrivilege("yum", "install", "-y", "https://github.com/r-lib/rig/releases/download/latest/r-rig-latest-1.$(arch).rpm"),
				},
			}
		}
		if hasTool("zypper") {
			return rigBootstrapPlan{
				manualMessage: "install rig from the official OpenSUSE/SLES RPM package and rerun rs",
				manualCommand: "zypper install -y --allow-unsigned-rpm https://github.com/r-lib/rig/releases/download/latest/r-rig-latest-1.$(arch).rpm",
				steps: []rigBootstrapStep{
					withPrivilege("zypper", "install", "-y", "--allow-unsigned-rpm", "https://github.com/r-lib/rig/releases/download/latest/r-rig-latest-1.$(arch).rpm"),
				},
			}
		}
	}
	return rigBootstrapPlan{}
}

func withPrivilege(name string, args ...string) rigBootstrapStep {
	if rigOS != "windows" && hasTool("sudo") {
		return rigBootstrapStep{Name: "sudo", Args: append([]string{name}, args...)}
	}
	return rigBootstrapStep{Name: name, Args: args}
}

func hasTool(name string) bool {
	_, err := toolLookPath(name)
	return err == nil
}

func renderBootstrapStep(step rigBootstrapStep) string {
	parts := append([]string{step.Name}, step.Args...)
	return strings.Join(parts, " ")
}

func (a RigBootstrapAdvice) ManualMessageWithCommand() string {
	if a.ManualCommand != "" {
		return fmt.Sprintf("%s: %s", a.ManualMessage, a.ManualCommand)
	}
	return a.ManualMessage
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
