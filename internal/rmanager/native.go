package rmanager

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rainoffallingstar/rs-reborn/internal/brand"
	"github.com/rainoffallingstar/rs-reborn/internal/progresscmd"
	"github.com/rainoffallingstar/rs-reborn/internal/toolchainenv"
)

var (
	nativeHTTPClient  = &http.Client{Timeout: 60 * time.Second}
	nativeCommand     = exec.Command
	nativeLookPath    = exec.LookPath
	nativeReadFile    = os.ReadFile
	nativeReadDir     = os.ReadDir
	nativeMkdirAll    = os.MkdirAll
	nativeRemoveAll   = os.RemoveAll
	nativeRename      = os.Rename
	nativeSymlink     = os.Symlink
	nativeLstat       = os.Lstat
	nativeReadlink    = os.Readlink
	nativeWriteFile   = os.WriteFile
	nativeStat        = os.Stat
	nativeWalkDir     = filepath.WalkDir
	nativeTempDir     = os.MkdirTemp
	nativeInstallSrc  = installFromSource
	nativeCheckHeader = checkHeaderWithCompiler
	nativeFindInPath  = toolchainenv.FindInPath
)

const versionsIndexURL = "https://cdn.posit.co/r/versions.json"

type installationMetadata struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Platform    string    `json:"platform,omitempty"`
	Arch        string    `json:"arch,omitempty"`
	OS          string    `json:"os,omitempty"`
	PackageType string    `json:"package_type,omitempty"`
	Selector    string    `json:"selector,omitempty"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	RscriptPath string    `json:"rscript_path"`
	RPath       string    `json:"r_path,omitempty"`
	Managed     bool      `json:"managed"`
	External    bool      `json:"external,omitempty"`
	Source      string    `json:"source"`
	InstalledAt time.Time `json:"installed_at,omitempty"`
}

type versionsIndex struct {
	Versions []string `json:"r_versions"`
}

type linuxDistro struct {
	ID        string
	IDLike    []string
	VersionID string
}

func nativeList(stdout, stderr io.Writer) error {
	installs, err := discoverInstallations()
	if err != nil {
		return err
	}
	if len(installs) == 0 {
		fmt.Fprintln(stdout, "no R installations found")
		return nil
	}
	for _, inst := range installs {
		prefix := " "
		if inst.Current || inst.Default {
			prefix = "*"
		}
		kind := "managed"
		if inst.External {
			kind = "external"
		}
		fmt.Fprintf(stdout, "%s %-8s %-8s %s\n", prefix, kind, inst.Version, inst.RscriptPath)
	}
	return nil
}

func nativeInstallWithOptions(opts InstallOptions) error {
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		return fmt.Errorf("R version is required")
	}
	method := opts.Method
	if method == "" {
		method = InstallMethodAuto
	}
	if !LooksLikeVersionSpec(version) {
		return fmt.Errorf("R version must be a version-like selector, got %q", version)
	}
	if err := validateVersionSelector(version); err != nil {
		return err
	}
	if opts.BootstrapToolchain {
		if err := maybeBootstrapNativeToolchain(opts); err != nil {
			return err
		}
	}
	concrete, err := resolveConcreteVersion(version)
	if err != nil {
		return err
	}
	targetDir, metaPath, err := managedInstallPaths(concrete)
	if err != nil {
		return err
	}
	if existing := managedRscriptPath(targetDir); existing != "" {
		if err := repairManagedInstall(targetDir); err != nil {
			if nativeGOOS == "darwin" {
				return fmt.Errorf("repair managed macOS R install: %w", err)
			}
			return fmt.Errorf("repair managed R install: %w", err)
		}
		if err := sanityCheckManagedR(targetDir); err != nil {
			if method == InstallMethodAuto {
				if opts.Stderr != nil {
					if nativeGOOS == "darwin" {
						fmt.Fprintf(opts.Stderr, "["+brand.CLIName+"] existing managed macOS R %s is not runnable after repair; rebuilding from source\n", concrete)
					} else {
						fmt.Fprintf(opts.Stderr, "["+brand.CLIName+"] existing managed R %s is not runnable after repair; rebuilding from source\n", concrete)
					}
				}
				if err := nativeRemoveAll(targetDir); err != nil {
					return fmt.Errorf("remove broken managed R install: %w", err)
				}
			} else {
				return fmt.Errorf("existing managed R install is not runnable: %w", err)
			}
		} else {
			fmt.Fprintf(opts.Stdout, "R %s is already installed at %s\n", concrete, targetDir)
			if _, err := nativeStat(currentPointerPath()); errors.Is(err, os.ErrNotExist) {
				_ = setCurrentInstall(targetDir)
			}
			return nil
		}
	}

	if err := nativeMkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		return fmt.Errorf("create R metadata dir: %w", err)
	}
	if err := nativeMkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return fmt.Errorf("create managed R versions dir: %w", err)
	}

	if err := installConcreteVersion(concrete, version, method, targetDir, opts.Stdout, opts.Stderr); err != nil {
		return err
	}
	meta := installationMetadata{
		ID:          filepath.Base(targetDir),
		Version:     concrete,
		Selector:    version,
		Name:        concrete,
		Path:        targetDir,
		RscriptPath: managedRscriptPath(targetDir),
		RPath:       managedRExecutablePath(targetDir),
		Managed:     true,
		Source:      "native",
		InstalledAt: time.Now().UTC(),
	}
	runtimeMeta, err := inspectRscriptMetadata(meta.RscriptPath)
	if err != nil {
		return fmt.Errorf("inspect managed R runtime: %w", err)
	}
	meta.Version = firstNonEmpty(runtimeMeta.Version, meta.Version)
	meta.Platform = runtimeMeta.Platform
	meta.Arch = runtimeMeta.Arch
	meta.OS = runtimeMeta.OS
	meta.PackageType = runtimeMeta.PackageType
	if err := writeInstallationMetadata(metaPath, meta); err != nil {
		return err
	}
	if _, err := nativeStat(currentPointerPath()); errors.Is(err, os.ErrNotExist) {
		_ = setCurrentInstall(targetDir)
	}
	fmt.Fprintf(opts.Stdout, "installed R %s to %s\n", concrete, targetDir)
	return nil
}

func maybeBootstrapNativeToolchain(opts InstallOptions) error {
	recommended, err := toolchainenv.RecommendedCandidate("")
	if err != nil {
		return err
	}
	if recommended != nil && recommended.Complete {
		return nil
	}
	_, err = toolchainenv.Bootstrap("auto", "", os.Environ(), opts.Stdout, opts.Stderr)
	return err
}

func nativeResolveVersionOrPath(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", fmt.Errorf("R version or Rscript path is required")
	}
	if looksLikePath(spec) || strings.Contains(strings.ToLower(spec), "rscript") {
		return resolveExplicitRscript(spec)
	}
	if err := validateVersionSelector(spec); err != nil {
		return "", err
	}

	installs, err := discoverInstallations()
	if err != nil {
		return "", err
	}
	if best := selectBestInstallation(installs, spec); best != nil {
		return best.RscriptPath, nil
	}
	return "", fmt.Errorf("could not find an installed Rscript for version %q; run `%s`, use `%s` to inspect available interpreters, or set rs.toml rscript manually", spec, brand.Command("r install", spec), brand.Command("r list"))
}

func nativeEnsureInstalledRscript(spec string, stdout, stderr io.Writer) (string, error) {
	requested := strings.TrimSpace(spec)
	target := requested
	switch {
	case requested == "", strings.EqualFold(requested, "Rscript"), strings.EqualFold(requested, "Rscript.exe"):
		target = defaultAutoInstallVersion()
	case !LooksLikeVersionSpec(requested):
		return "", fmt.Errorf("automatic R installation requires a version-like target, got %q", requested)
	}
	if err := validateVersionSelector(target); err != nil {
		return "", err
	}

	if resolved, err := nativeResolveVersionOrPath(target); err == nil {
		return resolved, nil
	}
	if stderr != nil {
		fmt.Fprintf(stderr, "["+brand.CLIName+"] Rscript is not available; installing R %s with the native manager\n", target)
	}
	if err := nativeInstallWithOptions(InstallOptions{
		Version: target,
		Method:  InstallMethodAuto,
		Stdout:  stdout,
		Stderr:  stderr,
	}); err != nil {
		return "", err
	}
	switch {
	case requested == "", strings.EqualFold(requested, "Rscript"), strings.EqualFold(requested, "Rscript.exe"), isNamedVersionSpec(requested):
		return nativeResolveVersionOrPath(defaultAutoInstallVersion())
	default:
		return nativeResolveVersionOrPath(requested)
	}
}

func nativeBootstrapAdvice() RBootstrapAdvice {
	return nativeBootstrapAdviceFor("")
}

func nativeBootstrapAdviceFor(spec string) RBootstrapAdvice {
	advice := RBootstrapAdvice{
		AutoEnableEnv: autoInstallREnv,
	}
	installTarget := bootstrapAdviceInstallTarget(spec)
	switch nativeGOOS {
	case "linux":
		distro, err := detectLinuxDistro()
		if err == nil && isArchLinux(distro) {
			advice.ManualMessage = fmt.Sprintf("install R build dependencies and then install a managed R version with %s", brand.CLIName)
			advice.ManualCommand = fmt.Sprintf("pacman -S --needed base-devel gcc-fortran curl xz bzip2 zlib readline pcre2 icu && %s", brand.Command("r install", installTarget, "--method source"))
			return advice
		}
		advice.ManualMessage = fmt.Sprintf("install a managed R version with %s or set rs.toml rscript manually", brand.CLIName)
		advice.ManualCommand = brand.Command("r install", installTarget)
		return advice
	case "darwin":
		advice.ManualMessage = fmt.Sprintf("install a managed R version with %s or set rs.toml rscript manually", brand.CLIName)
		advice.ManualCommand = brand.Command("r install", installTarget)
		return advice
	case "windows":
		advice.ManualMessage = fmt.Sprintf("install a managed R version with %s or set rs.toml rscript manually", brand.CLIName)
		advice.ManualCommand = brand.Command("r install", installTarget)
		return advice
	default:
		advice.ManualMessage = "set rs.toml rscript manually"
		return advice
	}
}

func bootstrapAdviceInstallTarget(spec string) string {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return "4.4"
	case strings.EqualFold(spec, "Rscript"), strings.EqualFold(spec, "Rscript.exe"):
		return "4.4"
	case LooksLikeVersionSpec(spec):
		return spec
	default:
		return "4.4"
	}
}

func installConcreteVersion(version, selector string, method InstallMethod, targetDir string, stdout, stderr io.Writer) error {
	distro := linuxDistro{}
	if nativeGOOS == "linux" {
		detected, err := detectLinuxDistro()
		if err != nil {
			return err
		}
		distro = detected
	}
	action, err := selectInstallAction(nativeGOOS, distro, method)
	if err != nil {
		return err
	}
	switch action {
	case installActionSource:
		return installFromSource(version, targetDir, stdout, stderr)
	case installActionMacOSBinary:
		return installBinaryWithFallback(
			version,
			method,
			targetDir,
			stdout,
			stderr,
			"macOS",
			func() error {
				return installMacOSBinary(version, targetDir, stdout, stderr)
			},
		)
	case installActionLinuxBinary:
		return installBinaryWithFallback(
			version,
			method,
			targetDir,
			stdout,
			stderr,
			"Linux",
			func() error {
				return installLinuxBinary(version, distro, targetDir, stdout, stderr)
			},
		)
	case installActionWindowsBinary:
		if err := installWindowsBinary(version, targetDir, stdout, stderr); err != nil {
			return err
		}
		if err := sanityCheckManagedR(targetDir); err != nil {
			return fmt.Errorf("managed Windows R install is not runnable: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported install action %q", action)
	}
}

func installBinaryWithFallback(version string, method InstallMethod, targetDir string, stdout, stderr io.Writer, platform string, install func() error) error {
	if err := install(); err != nil {
		if method == InstallMethodAuto {
			if stderr != nil {
				fmt.Fprintf(stderr, "["+brand.CLIName+"] %s binary install for R %s failed; falling back to source build\n", platform, version)
			}
			_ = nativeRemoveAll(targetDir)
			return nativeInstallSrc(version, targetDir, stdout, stderr)
		}
		return err
	}
	if err := sanityCheckManagedR(targetDir); err != nil {
		if method == InstallMethodAuto {
			if stderr != nil {
				fmt.Fprintf(stderr, "["+brand.CLIName+"] %s binary install for R %s was not runnable; falling back to source build\n", platform, version)
			}
			_ = nativeRemoveAll(targetDir)
			return nativeInstallSrc(version, targetDir, stdout, stderr)
		}
		return fmt.Errorf("managed %s R install is not runnable: %w", platform, err)
	}
	return nil
}

var errBinaryProviderUnsupported = errors.New("binary R provider does not support this platform")

func installMacOSBinary(version, targetDir string, stdout, stderr io.Writer) error {
	archiveDir, err := buildRoot()
	if err != nil {
		return err
	}
	downloadURL := macOSPkgURL(version)
	pkgPath := filepath.Join(archiveDir, filepath.Base(downloadURL))
	if err := downloadFile(downloadURL, pkgPath, "downloading macOS R "+version+" package", stderr); err != nil {
		return fmt.Errorf("download macOS R package: %w", err)
	}
	extractRoot := filepath.Join(archiveDir, "pkg-"+sanitizeVersion(version))
	if err := nativeRemoveAll(extractRoot); err != nil {
		return fmt.Errorf("prepare macOS package extraction dir: %w", err)
	}
	if _, err := nativeLookPath("pkgutil"); err != nil {
		return fmt.Errorf("macOS package extraction requires pkgutil: %w", err)
	}
	progresscmd.Stage(stderr, "expanding macOS R "+version+" package")
	expandCmd := nativeCommand("pkgutil", "--expand-full", pkgPath, extractRoot)
	if err := progresscmd.Run(expandCmd, "expanding macOS R "+version+" package", stderr, stderr); err != nil {
		return fmt.Errorf("expand macOS package: %w", err)
	}

	payloadDir := filepath.Join(extractRoot, "payload")
	root, mode, err := resolveMacOSInstallRoot(extractRoot, payloadDir, stdout, stderr)
	if err != nil {
		return err
	}
	return installNormalizedRoot(root, mode, targetDir)
}

func installLinuxBinary(version string, distro linuxDistro, targetDir string, stdout, stderr io.Writer) error {
	osID, err := linuxBinaryOSIdentifier(distro)
	if err != nil {
		return err
	}
	archiveDir, err := buildRoot()
	if err != nil {
		return err
	}
	url := linuxBinaryURL(version, osID)
	archivePath := filepath.Join(archiveDir, filepath.Base(url))
	if err := downloadFile(url, archivePath, "downloading Linux R "+version+" binary", stderr); err != nil {
		return fmt.Errorf("download Linux R binary: %w", err)
	}
	extractDir := filepath.Join(archiveDir, "linux-"+sanitizeVersion(version))
	if err := nativeRemoveAll(extractDir); err != nil {
		return fmt.Errorf("prepare Linux extraction dir: %w", err)
	}
	if err := nativeMkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create Linux extraction dir: %w", err)
	}
	progresscmd.Stage(stderr, "extracting Linux R "+version+" binary")
	if err := extractTarGz(archivePath, extractDir); err != nil {
		return fmt.Errorf("extract Linux R binary: %w", err)
	}
	root, mode, err := normalizeExtractedRoot(extractDir)
	if err != nil {
		return err
	}
	return installNormalizedRoot(root, mode, targetDir)
}

func installWindowsBinary(version, targetDir string, stdout, stderr io.Writer) error {
	archiveDir, err := buildRoot()
	if err != nil {
		return err
	}
	downloadURL := windowsInstallerURL(version)
	installerPath := filepath.Join(archiveDir, filepath.Base(downloadURL))
	if err := downloadFile(downloadURL, installerPath, "downloading Windows R "+version+" installer", stderr); err != nil {
		return fmt.Errorf("download Windows R installer: %w", err)
	}
	if err := nativeRemoveAll(targetDir); err != nil {
		return fmt.Errorf("prepare Windows install dir: %w", err)
	}
	if err := nativeMkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return fmt.Errorf("create Windows install parent dir: %w", err)
	}
	progresscmd.Stage(stderr, "installing Windows R "+version)
	cmd := nativeCommand(
		installerPath,
		"/VERYSILENT",
		"/SUPPRESSMSGBOXES",
		"/NORESTART",
		"/SP-",
		"/CURRENTUSER",
		"/NOICONS",
		"/DIR="+targetDir,
	)
	if err := progresscmd.Run(cmd, "installing Windows R "+version, stderr, stderr); err != nil {
		return fmt.Errorf("run Windows R installer: %w", err)
	}
	if managedRscriptPath(targetDir) == "" {
		return fmt.Errorf("managed Windows install is missing %s after install", rscriptExecutableName())
	}
	return nil
}

func installFromSource(version, targetDir string, stdout, stderr io.Writer) error {
	if err := preflightSourceBuild(version); err != nil {
		return err
	}
	buildEnv := sourceBuildEnvironment()
	archiveDir, err := buildRoot()
	if err != nil {
		return err
	}
	url := sourceTarballURL(version)
	archivePath := filepath.Join(archiveDir, filepath.Base(url))
	if err := downloadFile(url, archivePath, "downloading R "+version+" source", stderr); err != nil {
		return fmt.Errorf("download R source: %w", err)
	}
	sourceRoot := filepath.Join(archiveDir, "src-"+sanitizeVersion(version))
	if err := nativeRemoveAll(sourceRoot); err != nil {
		return fmt.Errorf("prepare source extraction dir: %w", err)
	}
	if err := nativeMkdirAll(sourceRoot, 0o755); err != nil {
		return fmt.Errorf("create source extraction dir: %w", err)
	}
	progresscmd.Stage(stderr, "extracting R "+version+" source")
	if err := extractTarGz(archivePath, sourceRoot); err != nil {
		return fmt.Errorf("extract R source: %w", err)
	}
	srcDir, err := findSourceRoot(sourceRoot)
	if err != nil {
		return err
	}
	if err := nativeMkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create managed R target dir: %w", err)
	}
	for _, cmdArgs := range [][]string{
		sourceConfigureArgs(targetDir),
		{"make", "-j2"},
		{"make", "install"},
	} {
		cmd := nativeCommand(cmdArgs[0], cmdArgs[1:]...)
		cmd.Dir = srcDir
		cmd.Stdin = os.Stdin
		cmd.Env = buildEnv
		if err := progresscmd.Run(cmd, sourceBuildStepLabel(version, cmdArgs), stderr, stderr); err != nil {
			return fmt.Errorf("run %s: %w", strings.Join(cmdArgs, " "), err)
		}
	}
	if _, err := nativeStat(filepath.Join(targetDir, "bin", rscriptExecutableName())); err != nil {
		return fmt.Errorf("managed source-built R is missing %s: %w", filepath.Join(targetDir, "bin", rscriptExecutableName()), err)
	}
	return nil
}

func sourceBuildStepLabel(version string, cmdArgs []string) string {
	switch {
	case len(cmdArgs) > 0 && cmdArgs[0] == "./configure":
		return fmt.Sprintf("configuring R %s source build", version)
	case len(cmdArgs) > 1 && cmdArgs[0] == "make" && cmdArgs[1] == "install":
		return fmt.Sprintf("installing R %s source build", version)
	default:
		return fmt.Sprintf("compiling R %s source build", version)
	}
}

func sourceConfigureArgs(targetDir string) []string {
	args := []string{"./configure", "--prefix=" + targetDir}
	if nativeGOOS == "darwin" {
		args = append(args, "--without-x")
	}
	return args
}

func managedInstallPaths(version string) (string, string, error) {
	root, err := managedRoot()
	if err != nil {
		return "", "", err
	}
	id := fmt.Sprintf("%s-%s-%s", sanitizeVersion(version), nativeGOOS, nativeGOARCH)
	return filepath.Join(root, "versions", id), filepath.Join(root, "metadata", id+".json"), nil
}

func managedRoot() (string, error) {
	if value := strings.TrimSpace(os.Getenv(managerRootEnv)); value != "" {
		return value, nil
	}
	if home := strings.TrimSpace(os.Getenv("RS_HOME")); home != "" {
		return filepath.Join(home, "r"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate user home dir: %w", err)
	}
	switch nativeGOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "rs", "r"), nil
	case "windows":
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return filepath.Join(localAppData, "rs", "r"), nil
		}
		return filepath.Join(home, "AppData", "Local", "rs", "r"), nil
	default:
		return filepath.Join(home, ".local", "share", "rs", "r"), nil
	}
}

func buildRoot() (string, error) {
	root, err := managedRoot()
	if err != nil {
		return "", err
	}
	build := filepath.Join(root, "build")
	if err := nativeMkdirAll(build, 0o755); err != nil {
		return "", fmt.Errorf("create managed R build dir: %w", err)
	}
	return build, nil
}

func currentPointerPath() string {
	root, err := managedRoot()
	if err != nil {
		return filepath.Join(os.TempDir(), "rs-r-current")
	}
	return filepath.Join(root, "current")
}

func setCurrentInstall(targetDir string) error {
	current := currentPointerPath()
	_ = nativeRemoveAll(current)
	if err := nativeSymlink(targetDir, current); err == nil {
		return nil
	}
	return nativeWriteFile(current, []byte(targetDir), 0o644)
}

func readCurrentInstall() string {
	current := currentPointerPath()
	info, err := nativeLstat(current)
	if err != nil {
		return ""
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := nativeReadlink(current)
		if err == nil {
			return target
		}
	}
	data, err := nativeReadFile(current)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func discoverInstallations() ([]Installation, error) {
	managed, err := discoverManagedInstallations()
	if err != nil {
		return nil, err
	}
	external, err := discoverExternalInstallations()
	if err != nil {
		return nil, err
	}
	current := readCurrentInstall()
	seen := map[string]Installation{}
	for _, inst := range append(managed, external...) {
		inst.Current = current != "" && samePath(current, inst.RscriptPath) || current != "" && samePath(current, instRRoot(inst))
		if existing, ok := seen[inst.RscriptPath]; !ok || installationLess(existing, inst) {
			seen[inst.RscriptPath] = inst
		}
	}
	out := make([]Installation, 0, len(seen))
	for _, inst := range seen {
		out = append(out, inst)
	}
	slices.SortFunc(out, func(left, right Installation) int {
		if left.Current != right.Current {
			if left.Current {
				return -1
			}
			return 1
		}
		if left.Managed != right.Managed {
			if left.Managed {
				return -1
			}
			return 1
		}
		if cmp := compareRscriptCandidates(left.RscriptPath, right.RscriptPath); cmp != 0 {
			return -cmp
		}
		return strings.Compare(left.RscriptPath, right.RscriptPath)
	})
	return out, nil
}

func installationLess(left, right Installation) bool {
	if left.Managed != right.Managed {
		return !left.Managed && right.Managed
	}
	return compareRscriptCandidates(left.RscriptPath, right.RscriptPath) < 0
}

func discoverManagedInstallations() ([]Installation, error) {
	root, err := managedRoot()
	if err != nil {
		return nil, err
	}
	metaDir := filepath.Join(root, "metadata")
	entries, err := nativeReadDir(metaDir)
	if errors.Is(err, os.ErrNotExist) {
		return discoverManagedFromVersionsDir(root)
	}
	if err != nil {
		return nil, fmt.Errorf("read managed R metadata dir: %w", err)
	}

	var installs []Installation
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(metaDir, entry.Name())
		data, err := nativeReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read managed R metadata: %w", err)
		}
		var meta installationMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parse managed R metadata: %w", err)
		}
		if _, err := nativeStat(meta.RscriptPath); err != nil {
			continue
		}
		installs = append(installs, Installation{
			Name:        meta.Name,
			Version:     meta.Version,
			Platform:    meta.Platform,
			Arch:        meta.Arch,
			OS:          meta.OS,
			PackageType: meta.PackageType,
			RscriptPath: meta.RscriptPath,
			RPath:       meta.RPath,
			Managed:     true,
			Source:      meta.Source,
		})
	}
	if len(installs) > 0 {
		return installs, nil
	}
	return discoverManagedFromVersionsDir(root)
}

func discoverManagedFromVersionsDir(root string) ([]Installation, error) {
	versionsDir := filepath.Join(root, "versions")
	entries, err := nativeReadDir(versionsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read managed R versions dir: %w", err)
	}
	var installs []Installation
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		targetDir := filepath.Join(versionsDir, entry.Name())
		path := managedRscriptPath(targetDir)
		if path == "" {
			continue
		}
		version, _ := inspectRscriptVersion(path)
		installs = append(installs, Installation{
			Name:        entry.Name(),
			Version:     firstNonEmpty(version, versionFromPath(path)),
			RscriptPath: path,
			RPath:       managedRExecutablePath(targetDir),
			Managed:     true,
			Source:      "native",
		})
	}
	return installs, nil
}

func discoverExternalInstallations() ([]Installation, error) {
	candidates, err := installedRscriptCandidates("*")
	if err != nil {
		return nil, err
	}
	if path, err := nativeLookPath("Rscript"); err == nil {
		candidates = append(candidates, path)
	}
	if nativeGOOS == "windows" {
		if path, err := nativeLookPath("Rscript.exe"); err == nil {
			candidates = append(candidates, path)
		}
		registryCandidates, err := windowsRegistryRscriptCandidates()
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, registryCandidates...)
	}
	root, _ := managedRoot()
	seen := map[string]struct{}{}
	var installs []Installation
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if root != "" && strings.HasPrefix(candidate, filepath.Join(root, "versions")+string(filepath.Separator)) {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		info, err := nativeStat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		version, _ := inspectRscriptVersion(candidate)
		installs = append(installs, Installation{
			Name:        filepath.Base(candidate),
			Version:     firstNonEmpty(version, versionFromPath(candidate)),
			RscriptPath: candidate,
			RPath:       rExecutablePathFromRscript(candidate),
			External:    true,
			Source:      "external",
		})
	}
	return installs, nil
}

func currentManagedRscript() (string, error) {
	installs, err := discoverInstallations()
	if err != nil {
		return "", err
	}
	for _, inst := range installs {
		if inst.Managed && inst.Current && inst.RscriptPath != "" {
			return inst.RscriptPath, nil
		}
	}
	return "", fmt.Errorf("no current managed R installation is configured")
}

func lookupManagedInstallation(rscriptPath string) (Installation, bool, error) {
	rscriptPath = strings.TrimSpace(rscriptPath)
	if rscriptPath == "" {
		return Installation{}, false, nil
	}
	root, err := managedRoot()
	if err != nil {
		return Installation{}, false, err
	}
	targetRoot := rRootFromRscriptPath(rscriptPath)
	if !pathHasPrefix(targetRoot, filepath.Join(root, "versions")) {
		return Installation{}, false, nil
	}
	metaPath := filepath.Join(root, "metadata", filepath.Base(targetRoot)+".json")
	data, err := nativeReadFile(metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return Installation{}, false, nil
	}
	if err != nil {
		return Installation{}, false, fmt.Errorf("read managed R metadata: %w", err)
	}
	var meta installationMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return Installation{}, false, fmt.Errorf("parse managed R metadata: %w", err)
	}
	if !samePath(meta.RscriptPath, rscriptPath) && !samePath(meta.Path, targetRoot) {
		return Installation{}, false, nil
	}
	return Installation{
		Name:        meta.Name,
		Version:     meta.Version,
		Platform:    meta.Platform,
		Arch:        meta.Arch,
		OS:          meta.OS,
		PackageType: meta.PackageType,
		RscriptPath: meta.RscriptPath,
		RPath:       meta.RPath,
		Managed:     true,
		Source:      meta.Source,
	}, true, nil
}

func windowsRegistryRscriptCandidates() ([]string, error) {
	if nativeGOOS != "windows" {
		return nil, nil
	}
	keys := []string{
		`HKCU\Software\R-core\R`,
		`HKLM\Software\R-core\R`,
		`HKLM\Software\WOW6432Node\R-core\R`,
	}
	seen := map[string]struct{}{}
	var candidates []string
	for _, key := range keys {
		cmd := nativeCommand("reg", "query", key, "/s", "/v", "InstallPath")
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		for _, installPath := range parseWindowsRegistryInstallPaths(string(output)) {
			for _, candidate := range []string{
				filepath.Join(installPath, "bin", rscriptExecutableName()),
				filepath.Join(installPath, "bin", "x64", rscriptExecutableName()),
			} {
				if _, ok := seen[candidate]; ok {
					continue
				}
				seen[candidate] = struct{}{}
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates, nil
}

func parseWindowsRegistryInstallPaths(output string) []string {
	seen := map[string]struct{}{}
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, "InstallPath") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		value := strings.Join(fields[2:], " ")
		value = strings.TrimSpace(strings.Trim(value, `"`))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		paths = append(paths, value)
	}
	return paths
}

func selectBestInstallation(installs []Installation, spec string) *Installation {
	var filtered []Installation
	for _, inst := range installs {
		if inst.Version == "" {
			continue
		}
		if spec == "release" {
			filtered = append(filtered, inst)
			continue
		}
		if VersionMatchesSpec(spec, inst.Version) {
			filtered = append(filtered, inst)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	slices.SortFunc(filtered, func(left, right Installation) int {
		if left.Managed != right.Managed {
			if left.Managed {
				return -1
			}
			return 1
		}
		return -compareRscriptCandidates(left.Version, right.Version)
	})
	return &filtered[0]
}

func resolveConcreteVersion(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if err := validateVersionSelector(spec); err != nil {
		return "", err
	}
	if spec != "release" && strings.Count(spec, ".") >= 2 {
		installs, err := discoverInstallations()
		if err == nil {
			for _, inst := range installs {
				if inst.Version == spec {
					return spec, nil
				}
			}
		}
	}
	versions, err := availableVersions()
	if err != nil {
		return "", err
	}
	if spec == "release" {
		return versions[0], nil
	}
	for _, version := range versions {
		if VersionMatchesSpec(spec, version) {
			return version, nil
		}
	}
	return "", fmt.Errorf("could not resolve R version selector %q", spec)
}

func availableVersions() ([]string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, versionsIndexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build versions index request: %w", err)
	}
	resp, err := nativeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download R versions index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download R versions index: unexpected HTTP status %s", resp.Status)
	}
	var index versionsIndex
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("parse R versions index: %w", err)
	}
	out := make([]string, 0, len(index.Versions))
	for _, version := range index.Versions {
		if len(version) > 0 && version[0] >= '0' && version[0] <= '9' {
			out = append(out, version)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("R versions index did not include any concrete versions")
	}
	return out, nil
}

func downloadFile(url, destination string, label string, progress io.Writer) error {
	if err := nativeMkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	resp, err := nativeHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %s for %s", resp.Status, url)
	}
	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("create download file: %w", err)
	}
	defer file.Close()
	if err := progresscmd.Copy(file, resp.Body, resp.ContentLength, label, progress); err != nil {
		return fmt.Errorf("write download file: %w", err)
	}
	return nil
}

func extractTarGz(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destination, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := nativeMkdirAll(target, fs.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := nativeMkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, tr); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := nativeMkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = nativeRemoveAll(target)
			if err := nativeSymlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeExtractedRoot(base string) (string, string, error) {
	rscriptPath, err := findRscriptBelow(base)
	if err != nil {
		return "", "", err
	}
	path := filepath.ToSlash(rscriptPath)
	if strings.Contains(path, "/Resources/bin/"+rscriptExecutableName()) {
		parts := strings.Split(path, "/Resources/bin/"+rscriptExecutableName())
		return filepath.FromSlash(parts[0] + "/Resources"), "resources", nil
	}
	return filepath.Dir(filepath.Dir(rscriptPath)), "bin", nil
}

func installNormalizedRoot(root, mode, targetDir string) error {
	_ = nativeRemoveAll(targetDir)
	if err := nativeMkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return err
	}
	if err := copyTree(root, targetDir); err != nil {
		return err
	}
	switch mode {
	case "resources":
		if err := repairManagedInstall(targetDir); err != nil {
			return err
		}
		if _, err := nativeStat(filepath.Join(targetDir, "bin", rscriptExecutableName())); err != nil {
			return fmt.Errorf("normalized macOS install is missing %s: %w", filepath.Join(targetDir, "bin", rscriptExecutableName()), err)
		}
	default:
		if err := repairManagedInstall(targetDir); err != nil {
			return err
		}
		if _, err := nativeStat(filepath.Join(targetDir, "bin", rscriptExecutableName())); err != nil {
			return fmt.Errorf("normalized install is missing %s: %w", filepath.Join(targetDir, "bin", rscriptExecutableName()), err)
		}
	}
	return nil
}

func repairManagedInstall(targetDir string) error {
	if nativeGOOS == "windows" {
		if managedRscriptPath(targetDir) == "" {
			return fmt.Errorf("managed Windows install is missing %s", rscriptExecutableName())
		}
		return nil
	}
	if nativeGOOS == "darwin" {
		if err := relinkMacOSInstallNames(targetDir); err != nil {
			return err
		}
	}
	return rewriteManagedLaunchers(targetDir)
}

func relinkMacOSInstallNames(targetDir string) error {
	if _, err := nativeLookPath("otool"); err != nil {
		return fmt.Errorf("macOS install relinking requires otool: %w", err)
	}
	if _, err := nativeLookPath("install_name_tool"); err != nil {
		return fmt.Errorf("macOS install relinking requires install_name_tool: %w", err)
	}
	return nativeWalkDir(targetDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return rewriteMacOSInstallNames(path, targetDir)
	})
}

func rewriteMacOSInstallNames(path, targetDir string) error {
	deps, err := macOSLoadCommands(path, "-L")
	if err != nil || len(deps) == 0 {
		return nil
	}

	oldRoot := ""
	for _, dep := range deps {
		if root, ok := macOSResourcesRoot(dep); ok {
			oldRoot = root
			break
		}
	}
	if oldRoot == "" {
		return nil
	}

	args := make([]string, 0, len(deps)*3+3)
	if ids, err := macOSLoadCommands(path, "-D"); err == nil && len(ids) > 0 {
		id := ids[0]
		if strings.HasPrefix(id, oldRoot) {
			args = append(args, "-id", targetDir+strings.TrimPrefix(id, oldRoot))
		}
	}
	for _, dep := range deps {
		if strings.HasPrefix(dep, oldRoot) {
			args = append(args, "-change", dep, targetDir+strings.TrimPrefix(dep, oldRoot))
		}
	}
	if len(args) == 0 {
		return nil
	}
	args = append(args, path)
	cmd := nativeCommand("install_name_tool", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rewrite macOS install names for %s: %v: %s", path, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func macOSLoadCommands(path string, flag string) ([]string, error) {
	cmd := nativeCommand("otool", flag, path)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(output), "\n")
	out := make([]string, 0, len(lines))
	for idx, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if idx == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		out = append(out, fields[0])
	}
	return out, nil
}

func macOSResourcesRoot(path string) (string, bool) {
	marker := "/Resources"
	idx := strings.Index(filepath.ToSlash(path), marker)
	if idx == -1 {
		return "", false
	}
	root := filepath.FromSlash(filepath.ToSlash(path)[:idx+len(marker)])
	if strings.Contains(filepath.ToSlash(root), "/Library/Frameworks/R.framework/Versions/") {
		return root, true
	}
	return "", false
}

func rewriteManagedLaunchers(targetDir string) error {
	if nativeGOOS == "windows" {
		return nil
	}
	if err := rewriteManagedRLauncher(filepath.Join(targetDir, "bin", "R"), targetDir); err != nil {
		return err
	}
	if nativeGOOS == "darwin" {
		if err := rewriteMacOSRenviron(filepath.Join(targetDir, "etc", "Renviron"), targetDir); err != nil {
			return err
		}
	}
	if err := installManagedRscriptWrapper(filepath.Join(targetDir, "bin", "Rscript"), targetDir); err != nil {
		return err
	}
	topLevelRscript := filepath.Join(targetDir, "Rscript")
	if _, err := nativeStat(topLevelRscript); err == nil {
		_ = nativeRemoveAll(topLevelRscript)
		if err := nativeSymlink(filepath.Join("bin", "Rscript"), topLevelRscript); err != nil {
			return fmt.Errorf("create top-level Rscript symlink: %w", err)
		}
	}
	return nil
}

func rewriteManagedRLauncher(path, targetDir string) error {
	data, err := nativeReadFile(path)
	if err != nil {
		return fmt.Errorf("read R launcher: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	for idx := 0; idx < len(lines); idx++ {
		line := lines[idx]
		switch {
		case strings.HasPrefix(line, "R_HOME_DIR="):
			out = append(out, "R_HOME_DIR="+shellSingleQuote(targetDir))
		case strings.HasPrefix(line, "R_SHARE_DIR="):
			out = append(out, "R_SHARE_DIR="+shellSingleQuote(filepath.Join(targetDir, "share")))
		case strings.HasPrefix(line, "R_INCLUDE_DIR="):
			out = append(out, "R_INCLUDE_DIR="+shellSingleQuote(filepath.Join(targetDir, "include")))
		case strings.HasPrefix(line, "R_DOC_DIR="):
			out = append(out, "R_DOC_DIR="+shellSingleQuote(filepath.Join(targetDir, "doc")))
		case isManagedLauncherFallbackBlockStart(line):
			end, err := skipShellIfBlock(lines, idx)
			if err != nil {
				return fmt.Errorf("parse R launcher fallback block: %w", err)
			}
			out = append(out,
				`if test "${R_HOME_DIR}" = `+shellSingleQuote(targetDir)+`; then`,
				"  :",
				"fi",
			)
			idx = end
		case isManagedLauncherFallbackLine(line):
			continue
		default:
			out = append(out, line)
		}
	}
	content := strings.Join(out, "\n")
	info, err := nativeStat(path)
	if err != nil {
		return fmt.Errorf("stat R launcher: %w", err)
	}
	if err := nativeWriteFile(path, []byte(content), info.Mode()); err != nil {
		return fmt.Errorf("write R launcher: %w", err)
	}
	return nil
}

func isManagedLauncherFallbackBlockStart(line string) bool {
	if !strings.HasPrefix(line, `if test "${R_HOME_DIR}" = "`) {
		return false
	}
	return strings.Contains(line, "/opt/R/") || strings.Contains(line, "/Library/Frameworks/")
}

func isManagedLauncherFallbackLine(line string) bool {
	return strings.Contains(line, `/Library/Frameworks/${libnn}/R"`) ||
		strings.Contains(line, `/Library/Frameworks/${libnn_fallback}/R"`) ||
		(strings.Contains(line, `/opt/R/`) && strings.Contains(line, `${libnn}`)) ||
		(strings.Contains(line, `/opt/R/`) && strings.Contains(line, `${libnn_fallback}`))
}

func skipShellIfBlock(lines []string, start int) (int, error) {
	depth := 1
	for idx := start + 1; idx < len(lines); idx++ {
		trimmed := strings.TrimSpace(lines[idx])
		if strings.HasPrefix(trimmed, "if ") {
			depth++
		}
		if trimmed == "fi" {
			depth--
			if depth == 0 {
				return idx, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated shell if block starting at line %d", start+1)
}

func rewriteMacOSRenviron(path, targetDir string) error {
	data, err := nativeReadFile(path)
	if err != nil {
		return fmt.Errorf("read macOS Renviron: %w", err)
	}
	content := string(data)
	content = strings.ReplaceAll(content, `/Library/Frameworks/R.framework/Resources/bin/qpdf`, filepath.Join(targetDir, "bin", "qpdf"))
	info, err := nativeStat(path)
	if err != nil {
		return fmt.Errorf("stat macOS Renviron: %w", err)
	}
	if err := nativeWriteFile(path, []byte(content), info.Mode()); err != nil {
		return fmt.Errorf("write macOS Renviron: %w", err)
	}
	return nil
}

func installManagedRscriptWrapper(path, targetDir string) error {
	info, err := nativeStat(path)
	if err != nil {
		return fmt.Errorf("stat Rscript launcher: %w", err)
	}
	wrapper := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

R_HOME_DIR=%s
export R_HOME="$R_HOME_DIR"
export R_SHARE_DIR="$R_HOME/share"
export R_INCLUDE_DIR="$R_HOME/include"
export R_DOC_DIR="$R_HOME/doc"

r_args=(--no-echo --no-restore)
script=""
script_args=()

while (($#)); do
  case "$1" in
    --help)
      cat <<'EOF'
Usage: Rscript [options] file [args]
   or: Rscript [options] -e expr [-e expr2 ...] [args]
EOF
      exit 0
      ;;
    --version)
      exec "$R_HOME_DIR/bin/R" --version
      ;;
    --verbose|--default-packages=*)
      r_args+=("$1")
      shift
      ;;
    -e)
      if (($# < 2)); then
        echo "option '-e' requires a non-empty argument" >&2
        exit 1
      fi
      r_args+=("-e" "$2")
      shift 2
      ;;
    --)
      shift
      script_args=("$@")
      break
      ;;
    -*)
      r_args+=("$1")
      shift
      ;;
    *)
      script="$1"
      shift
      script_args=("$@")
      break
      ;;
  esac
done

if [[ -n "$script" ]]; then
  if ((${#script_args[@]})); then
    exec "$R_HOME_DIR/bin/R" "${r_args[@]}" --file="$script" --args "${script_args[@]}"
  fi
  exec "$R_HOME_DIR/bin/R" "${r_args[@]}" --file="$script"
fi

if ((${#script_args[@]})); then
  exec "$R_HOME_DIR/bin/R" "${r_args[@]}" --args "${script_args[@]}"
fi

exec "$R_HOME_DIR/bin/R" "${r_args[@]}"
`, shellSingleQuote(targetDir))
	if err := nativeWriteFile(path, []byte(wrapper), info.Mode()); err != nil {
		return fmt.Errorf("write Rscript wrapper: %w", err)
	}
	return nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func copyTree(src, dst string) error {
	return nativeWalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return nativeMkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return nativeSymlink(link, target)
		}
		if err := nativeMkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		defer source.Close()
		dest, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		if _, err := io.Copy(dest, source); err != nil {
			dest.Close()
			return err
		}
		return dest.Close()
	})
}

func findRscriptBelow(base string) (string, error) {
	var found []string
	err := nativeWalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) == rscriptExecutableName() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", fmt.Errorf("could not find %s in extracted R install", rscriptExecutableName())
	}
	slices.SortFunc(found, compareRscriptCandidates)
	return found[len(found)-1], nil
}

func findSourceRoot(base string) (string, error) {
	entries, err := nativeReadDir(base)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(base, entry.Name(), "configure")
		if info, err := nativeStat(path); err == nil && !info.IsDir() {
			return filepath.Join(base, entry.Name()), nil
		}
	}
	return "", fmt.Errorf("could not find extracted R source tree in %s", base)
}

func writeInstallationMetadata(path string, meta installationMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal managed R metadata: %w", err)
	}
	if err := nativeWriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write managed R metadata: %w", err)
	}
	return nil
}

func detectLinuxDistro() (linuxDistro, error) {
	data, err := nativeReadFile("/etc/os-release")
	if err != nil {
		return linuxDistro{}, fmt.Errorf("read /etc/os-release: %w", err)
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return linuxDistro{}, fmt.Errorf("parse /etc/os-release: %w", err)
	}
	return linuxDistro{
		ID:        values["ID"],
		IDLike:    strings.Fields(values["ID_LIKE"]),
		VersionID: values["VERSION_ID"],
	}, nil
}

func linuxBinaryOSIdentifier(distro linuxDistro) (string, error) {
	ids := append([]string{strings.ToLower(distro.ID)}, distro.IDLike...)
	contains := func(values ...string) bool {
		for _, want := range values {
			for _, id := range ids {
				if strings.EqualFold(id, want) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case strings.EqualFold(distro.ID, "ubuntu"):
		return "ubuntu-" + strings.ReplaceAll(distro.VersionID, ".", ""), nil
	case strings.EqualFold(distro.ID, "debian"):
		return "debian-" + distro.VersionID, nil
	case strings.EqualFold(distro.ID, "fedora"):
		return "fedora-" + distro.VersionID, nil
	case strings.EqualFold(distro.ID, "opensuse-leap"), strings.EqualFold(distro.ID, "opensuse"), strings.EqualFold(distro.ID, "opensuse-tumbleweed"):
		return "opensuse-" + strings.ReplaceAll(distro.VersionID, ".", ""), nil
	case contains("rhel", "centos", "rocky", "almalinux", "alma"):
		major := distroMajorVersion(distro.VersionID)
		switch major {
		case "7":
			return "centos-7", nil
		case "8":
			return "centos-8", nil
		case "9", "10":
			return "rhel-" + major, nil
		}
	}
	return "", fmt.Errorf("%w: unsupported Linux distribution %s %s", errBinaryProviderUnsupported, distro.ID, distro.VersionID)
}

func linuxBinaryURL(version, osID string) string {
	archSuffix := ""
	if nativeGOARCH == "arm64" {
		archSuffix = "-arm64"
	}
	return fmt.Sprintf("https://cdn.posit.co/r/%s/R-%s-%s%s.tar.gz", osID, version, osID, archSuffix)
}

func sourceTarballURL(version string) string {
	majorMinor := version
	if parts, ok := parseVersionHint(version); ok && len(parts) >= 2 {
		majorMinor = strconv.Itoa(parts[0]) + "." + strconv.Itoa(parts[1])
	}
	major := strings.SplitN(majorMinor, ".", 2)[0]
	return fmt.Sprintf("https://cran.r-project.org/src/base/R-%s/R-%s.tar.gz", major, version)
}

func macOSPkgURL(version string) string {
	switch nativeGOARCH {
	case "arm64":
		return fmt.Sprintf("https://mac.r-project.org/bin/macosx/big-sur-arm64/base/R-%s-arm64.pkg", version)
	default:
		return fmt.Sprintf("https://mac.r-project.org/bin/macosx/big-sur-x86_64/base/R-%s-x86_64.pkg", version)
	}
}

func windowsInstallerURL(version string) string {
	return fmt.Sprintf("https://cran.r-project.org/bin/windows/base/old/%s/R-%s-win.exe", version, version)
}

func preflightSourceBuild(version string) error {
	missingTools := sourceBuildMissingTools()
	missingHeaders := sourceBuildMissingHeaders()
	if len(missingTools) == 0 && len(missingHeaders) == 0 {
		return nil
	}
	return fmt.Errorf(
		"source build prerequisites are missing: %s\nnext step: %s",
		formatMissingSourceBuildRequirements(missingTools, missingHeaders),
		sourceBuildAdvice(version, missingTools, missingHeaders),
	)
}

func sourceBuildMissingTools() []string {
	missingTools := []string{}
	for _, tool := range []string{"gcc", "g++", "make", "curl", "xz", "gfortran"} {
		if _, err := findSourceBuildTool(tool); err != nil {
			missingTools = append(missingTools, tool)
		}
	}
	return missingTools
}

func sourceBuildMissingHeaders() []string {
	headers := []string{}
	switch nativeGOOS {
	case "darwin":
		headers = []string{"lzma.h"}
	case "linux":
		headers = []string{"lzma.h", "bzlib.h", "zlib.h", "readline/readline.h", "pcre2.h"}
	default:
		return nil
	}
	missing := make([]string, 0, len(headers))
	for _, header := range headers {
		if !sourceBuildHeaderAvailable(header) {
			missing = append(missing, header)
		}
	}
	return missing
}

func sourceBuildHeaderAvailable(header string) bool {
	return nativeCheckHeader(header) == nil
}

func checkHeaderWithCompiler(header string) error {
	compiler := firstAvailableCompiler()
	if compiler == "" {
		return fmt.Errorf("no C compiler available for header probe")
	}
	cmd := nativeCommand(compiler, "-x", "c", "-E", "-")
	cmd.Stdin = strings.NewReader(headerProbeSource(header))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Env = sourceBuildEnvironment()
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func headerProbeSource(header string) string {
	switch header {
	case "pcre2.h":
		return "#define PCRE2_CODE_UNIT_WIDTH 8\n#include <pcre2.h>\n"
	default:
		return "#include <" + header + ">\n"
	}
}

func firstAvailableCompiler() string {
	candidates := []string{}
	if cc := strings.TrimSpace(os.Getenv("CC")); cc != "" {
		candidates = append(candidates, cc)
	}
	candidates = append(candidates, "cc", "gcc", "clang")
	for _, candidate := range candidates {
		if _, err := findSourceBuildTool(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func formatMissingSourceBuildRequirements(missingTools, missingHeaders []string) string {
	requirements := make([]string, 0, len(missingTools)+len(missingHeaders))
	requirements = append(requirements, missingTools...)
	for _, header := range missingHeaders {
		requirements = append(requirements, header+" header")
	}
	return strings.Join(requirements, ", ")
}

func sourceBuildAdvice(version string, missingTools, missingHeaders []string) string {
	target := strings.TrimSpace(version)
	if target == "" {
		target = defaultAutoInstallVersion()
	}
	rootlessNote := "if you are in a rootless environment, set RS_TOOLCHAIN_PREFIXES=/path/to/prefix and optionally RS_PKG_CONFIG_PATH=/path/to/pkgconfig before retrying, or configure toolchain_prefixes/pkg_config_path in rs.toml for project-managed package builds"
	if candidate, err := toolchainenv.RecommendedCandidate(""); err == nil && candidate != nil {
		rootlessNote = fmt.Sprintf("%s; detected recommended preset on this machine: %s; setup follow-up: `%s`; project follow-up: `%s`", rootlessNote, candidate.Preset, candidate.SuggestedSetupCommand, candidate.SuggestedInitCommand)
	}

	switch nativeGOOS {
	case "darwin":
		parts := []string{
			fmt.Sprintf("prefer a managed binary install first: %s", brand.Command("r install", target, "--method binary")),
			"if source build is required, install Xcode Command Line Tools plus the missing libraries in a user-local prefix such as Homebrew or Conda",
		}
		if len(missingHeaders) > 0 {
			parts = append(parts, "for example make sure xz/liblzma headers are available")
		}
		parts = append(parts, rootlessNote)
		return strings.Join(parts, "; ")
	case "linux":
		if distro, err := detectLinuxDistro(); err == nil {
			switch {
			case isArchLinux(distro):
				return "pacman -S --needed base-devel gcc-fortran curl xz bzip2 zlib readline pcre2 icu; " + rootlessNote
			case isDebianLike(distro):
				return "apt-get update && apt-get install -y build-essential gfortran curl xz-utils libbz2-dev zlib1g-dev libreadline-dev libpcre2-dev liblzma-dev; " + rootlessNote
			case isRHELLike(distro):
				return "dnf install -y gcc gcc-c++ gcc-gfortran make curl xz xz-devel bzip2-devel zlib-devel readline-devel pcre2-devel; " + rootlessNote
			}
		}
		return "install the missing C/C++/Fortran toolchain and source-build headers for R, then retry; " + rootlessNote
	default:
		return "install the missing source-build toolchain and headers, then retry"
	}
}

func sourceBuildEnvironment() []string {
	env := os.Environ()
	prefixes, pkgConfig, _, err := toolchainenv.MergeWithDetected(toolchainenv.PrefixesFromEnv(env), toolchainenv.PkgConfigPathsFromEnv(env), "")
	if err != nil {
		return toolchainenv.Apply(env, toolchainenv.PrefixesFromEnv(env), toolchainenv.PkgConfigPathsFromEnv(env))
	}
	return toolchainenv.Apply(env, prefixes, pkgConfig)
}

func findSourceBuildTool(name string) (string, error) {
	return nativeFindInPath(name, sourceBuildEnvironment())
}

func isArchLinux(distro linuxDistro) bool {
	if strings.EqualFold(distro.ID, "arch") || strings.EqualFold(distro.ID, "manjaro") {
		return true
	}
	for _, id := range distro.IDLike {
		if strings.EqualFold(id, "arch") {
			return true
		}
	}
	return false
}

func isDebianLike(distro linuxDistro) bool {
	if strings.EqualFold(distro.ID, "debian") || strings.EqualFold(distro.ID, "ubuntu") {
		return true
	}
	for _, id := range distro.IDLike {
		if strings.EqualFold(id, "debian") || strings.EqualFold(id, "ubuntu") {
			return true
		}
	}
	return false
}

func isRHELLike(distro linuxDistro) bool {
	for _, value := range append([]string{distro.ID}, distro.IDLike...) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "rhel", "centos", "rocky", "almalinux", "fedora":
			return true
		}
	}
	return false
}

func distroMajorVersion(version string) string {
	if version == "" {
		return ""
	}
	return strings.SplitN(version, ".", 2)[0]
}

func sanitizeVersion(version string) string {
	replacer := strings.NewReplacer("/", "-", " ", "-", ":", "-", "\\", "-")
	return replacer.Replace(version)
}

func unsupportedNativeSelector(spec string) bool {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "oldrel", "devel", "next":
		return true
	default:
		return false
	}
}

type installAction string

const (
	installActionSource        installAction = "source"
	installActionMacOSBinary   installAction = "macos_binary"
	installActionLinuxBinary   installAction = "linux_binary"
	installActionWindowsBinary installAction = "windows_binary"
)

func selectInstallAction(goos string, distro linuxDistro, method InstallMethod) (installAction, error) {
	if method == "" {
		method = InstallMethodAuto
	}
	switch goos {
	case "darwin":
		switch method {
		case InstallMethodSource:
			return installActionSource, nil
		case InstallMethodBinary, InstallMethodAuto:
			return installActionMacOSBinary, nil
		default:
			return "", fmt.Errorf("unsupported install method %q", method)
		}
	case "linux":
		switch method {
		case InstallMethodSource:
			return installActionSource, nil
		case InstallMethodBinary:
			if _, err := linuxBinaryOSIdentifier(distro); err != nil {
				return "", err
			}
			return installActionLinuxBinary, nil
		case InstallMethodAuto:
			if isArchLinux(distro) {
				return installActionSource, nil
			}
			if _, err := linuxBinaryOSIdentifier(distro); err == nil {
				return installActionLinuxBinary, nil
			} else if errors.Is(err, errBinaryProviderUnsupported) {
				return installActionSource, nil
			} else {
				return "", err
			}
		default:
			return "", fmt.Errorf("unsupported install method %q", method)
		}
	case "windows":
		switch method {
		case InstallMethodBinary, InstallMethodAuto:
			return installActionWindowsBinary, nil
		case InstallMethodSource:
			return "", fmt.Errorf("native Windows R installs currently support only binary installs; use --method auto or --method binary")
		default:
			return "", fmt.Errorf("unsupported install method %q", method)
		}
	default:
		return "", fmt.Errorf("native R manager is not supported on %s", goos)
	}
}

func validateVersionSelector(spec string) error {
	spec = strings.TrimSpace(spec)
	if unsupportedNativeSelector(spec) {
		return fmt.Errorf("native R manager does not yet support selector %q; use an explicit version like 4.4 or 4.4.3, or set rs.toml rscript manually", spec)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func managedRscriptPath(targetDir string) string {
	return firstExistingPath(managedRscriptCandidates(targetDir)...)
}

func managedRExecutablePath(targetDir string) string {
	return firstExistingPath(managedRExecutableCandidates(targetDir)...)
}

func managedRscriptCandidates(targetDir string) []string {
	candidates := []string{filepath.Join(targetDir, "bin", rscriptExecutableName())}
	if nativeGOOS == "windows" {
		candidates = append(candidates, filepath.Join(targetDir, "bin", "x64", rscriptExecutableName()))
	}
	return candidates
}

func managedRExecutableCandidates(targetDir string) []string {
	candidates := []string{filepath.Join(targetDir, "bin", nativeRExecutableName())}
	if nativeGOOS == "windows" {
		candidates = append(candidates, filepath.Join(targetDir, "bin", "x64", nativeRExecutableName()))
	}
	return candidates
}

func firstExistingPath(candidates ...string) string {
	for _, candidate := range candidates {
		info, err := nativeStat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func rExecutablePathFromRscript(path string) string {
	dir := filepath.Dir(path)
	candidates := []string{filepath.Join(dir, nativeRExecutableName())}
	if nativeGOOS == "windows" && strings.EqualFold(filepath.Base(dir), "x64") {
		candidates = append(candidates, filepath.Join(filepath.Dir(dir), nativeRExecutableName()))
	} else if nativeGOOS == "windows" {
		candidates = append(candidates, filepath.Join(dir, "x64", nativeRExecutableName()))
	}
	return firstExistingPath(candidates...)
}

func rRootFromRscriptPath(path string) string {
	dir := filepath.Dir(path)
	if nativeGOOS == "windows" && strings.EqualFold(filepath.Base(dir), "x64") {
		dir = filepath.Dir(dir)
	}
	return filepath.Dir(dir)
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if nativeGOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func pathHasPrefix(path, prefix string) bool {
	if path == "" || prefix == "" {
		return false
	}
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	if samePath(path, prefix) {
		return true
	}
	prefix = prefix + string(filepath.Separator)
	if nativeGOOS == "windows" {
		return strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix))
	}
	return strings.HasPrefix(path, prefix)
}

func instRRoot(inst Installation) string {
	return rRootFromRscriptPath(inst.RscriptPath)
}

func versionFromPath(path string) string {
	root := rRootFromRscriptPath(path)
	if version, ok := parseLeadingVersion(filepath.Base(root)); ok {
		parts := make([]string, 0, len(version))
		for _, part := range version {
			parts = append(parts, strconv.Itoa(part))
		}
		return strings.Join(parts, ".")
	}
	if version, ok := parseLeadingVersion(path); ok {
		parts := make([]string, 0, len(version))
		for _, part := range version {
			parts = append(parts, strconv.Itoa(part))
		}
		return strings.Join(parts, ".")
	}
	return ""
}

func inspectRscriptVersion(path string) (string, error) {
	cmd := nativeCommand(path, "-e", "cat(as.character(getRversion()))")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

type rscriptRuntimeMetadata struct {
	Version     string
	Platform    string
	Arch        string
	OS          string
	PackageType string
}

func inspectRscriptMetadata(path string) (rscriptRuntimeMetadata, error) {
	cmd := nativeCommand(path, "-e", `cat("version\t", as.character(getRversion()), "\n", sep = ""); cat("platform\t", R.version$platform, "\n", sep = ""); cat("arch\t", R.version$arch, "\n", sep = ""); cat("os\t", R.version$os, "\n", sep = ""); cat("pkg_type\t", getOption("pkgType"), "\n", sep = "")`)
	output, err := cmd.Output()
	if err != nil {
		return rscriptRuntimeMetadata{}, err
	}
	meta := rscriptRuntimeMetadata{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "version":
			meta.Version = value
		case "platform":
			meta.Platform = value
		case "arch":
			meta.Arch = value
		case "os":
			meta.OS = value
		case "pkg_type":
			meta.PackageType = value
		}
	}
	return meta, nil
}

func sanityCheckManagedR(targetDir string) error {
	path := managedRscriptPath(targetDir)
	if path == "" {
		return fmt.Errorf("managed R install is missing %s", rscriptExecutableName())
	}
	if _, err := inspectRscriptVersion(path); err != nil {
		return err
	}
	return nil
}

func nativeRExecutableName() string {
	if nativeGOOS == "windows" {
		return "R.exe"
	}
	return "R"
}

func findPayloadFile(root string) (string, error) {
	entries, err := findPayloadEntries(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir {
			return entry.Path, nil
		}
	}
	return "", fmt.Errorf("could not find file-backed Payload inside macOS package")
}

type payloadEntry struct {
	Path  string
	IsDir bool
}

func findPayloadEntries(root string) ([]payloadEntry, error) {
	var entries []payloadEntry
	err := nativeWalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if filepath.Base(path) == "Payload" {
			entries = append(entries, payloadEntry{
				Path:  path,
				IsDir: d.IsDir(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("could not find Payload inside macOS package")
	}
	slices.SortFunc(entries, func(left, right payloadEntry) int {
		leftScore := payloadPriority(left.Path)
		rightScore := payloadPriority(right.Path)
		if leftScore != rightScore {
			if leftScore > rightScore {
				return -1
			}
			return 1
		}
		if left.IsDir != right.IsDir {
			if left.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(left.Path, right.Path)
	})
	return entries, nil
}

func payloadPriority(path string) int {
	lower := strings.ToLower(filepath.ToSlash(path))
	switch {
	case strings.Contains(lower, "/r-fw.pkg/"):
		return 40
	case strings.Contains(lower, "/r.pkg/"), strings.Contains(lower, "/r-core.pkg/"):
		return 30
	case strings.Contains(lower, "/r-app.pkg/"):
		return 20
	case strings.Contains(lower, "/tcltk.pkg/"):
		return 10
	case strings.Contains(lower, "/texinfo.pkg/"):
		return 5
	default:
		return 0
	}
}

func resolveMacOSInstallRoot(extractRoot, payloadDir string, stdout, stderr io.Writer) (string, string, error) {
	if root, mode, err := normalizeExtractedRoot(extractRoot); err == nil {
		return root, mode, nil
	}

	entries, err := findPayloadEntries(extractRoot)
	if err != nil {
		return "", "", err
	}

	var lastErr error
	for _, entry := range entries {
		if entry.IsDir {
			root, mode, err := normalizeExtractedRoot(entry.Path)
			if err == nil {
				return root, mode, nil
			}
			lastErr = err
			continue
		}

		if err := nativeRemoveAll(payloadDir); err != nil {
			return "", "", fmt.Errorf("prepare macOS payload dir: %w", err)
		}
		if err := nativeMkdirAll(payloadDir, 0o755); err != nil {
			return "", "", fmt.Errorf("create macOS payload dir: %w", err)
		}
		if err := extractPkgPayload(entry.Path, payloadDir, stdout, stderr); err != nil {
			lastErr = err
			continue
		}
		root, mode, err := normalizeExtractedRoot(payloadDir)
		if err == nil {
			return root, mode, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("could not find %s in expanded macOS package", rscriptExecutableName())
	}
	return "", "", lastErr
}

func extractPkgPayload(payloadPath, destination string, stdout, stderr io.Writer) error {
	commands := [][]string{
		{"sh", "-c", fmt.Sprintf("cat %q | gunzip -dc | (cd %q && cpio -idm --quiet)", payloadPath, destination)},
		{"sh", "-c", fmt.Sprintf("cat %q | xz -dc | (cd %q && cpio -idm --quiet)", payloadPath, destination)},
		{"sh", "-c", fmt.Sprintf("cat %q | (cd %q && cpio -idm --quiet)", payloadPath, destination)},
	}
	var lastErr error
	for _, args := range commands {
		cmd := nativeCommand(args[0], args[1:]...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err == nil {
			if _, statErr := findRscriptBelow(destination); statErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("macOS package payload extraction produced no usable Rscript")
	}
	return fmt.Errorf("extract macOS package payload: %w", lastErr)
}
