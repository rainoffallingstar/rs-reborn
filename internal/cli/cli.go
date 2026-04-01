package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/rainoffallingstar/rs-reborn/internal/project"
	"github.com/rainoffallingstar/rs-reborn/internal/rdeps"
	"github.com/rainoffallingstar/rs-reborn/internal/rmanager"
	"github.com/rainoffallingstar/rs-reborn/internal/runner"
	"github.com/rainoffallingstar/rs-reborn/internal/toolchainenv"
)

type stringList []string

type scanReport struct {
	Script          string   `json:"script"`
	Packages        []string `json:"packages"`
	CRANPackages    []string `json:"cran_packages"`
	BiocPackages    []string `json:"bioc_packages"`
	InstallableOnly bool     `json:"installable_only"`
}

type toolchainDetectReport struct {
	Candidates []toolchainDetection `json:"candidates"`
}

type toolchainDetection = toolchainenv.Candidate

type toolchainBootstrapReport struct {
	Candidate            toolchainDetection `json:"candidate"`
	TemplateCommand      string             `json:"template_command"`
	EnvTemplateCommand   string             `json:"env_template_command"`
	InitCommand          string             `json:"init_command"`
	DoctorCommand        string             `json:"doctor_command"`
	TemplateCheckCommand string             `json:"template_check_command"`
}

type toolchainPackageGroup = toolchainenv.PackageGroup

type toolchainPackagePlan = toolchainenv.PackagePlan

type toolchainPlanReport struct {
	Candidate       toolchainDetection         `json:"candidate"`
	DependencyPlan  runner.ToolchainPlanReport `json:"dependency_plan"`
	Phase           string                     `json:"phase"`
	PackagePlan     toolchainPackagePlan       `json:"package_plan"`
	PlannedPackages []string                   `json:"planned_packages"`
	SetupCommand    string                     `json:"setup_command"`
	InitCommand     string                     `json:"init_command"`
	DoctorCommand   string                     `json:"doctor_command"`
}

type installFailureDiagnostic = runner.InstallFailureDiagnostic

var (
	cliValidateVersionSelector = rmanager.ValidateVersionSelector
	cliResolveVersionOrPath    = rmanager.ResolveVersionOrPath
	cliResolveVersionSelector  = rmanager.ResolveVersionSelector
	cliCurrentManagedRscript   = rmanager.CurrentManagedRscript
	cliInstallRWithOptions     = rmanager.InstallWithOptions
	cliDoctor                  = runner.Doctor
	cliPlanToolchain           = runner.PlanToolchain
	cliUserHomeDir             = os.UserHomeDir
	cliStat                    = os.Stat
	cliDescribeToolchainPreset = toolchainenv.DescribePreset
	cliRecommendedToolchain    = toolchainenv.RecommendedCandidate
	cliBootstrapToolchain      = toolchainenv.BootstrapWithPackages
	cliBootstrapCandidate      = toolchainenv.BootstrapCandidate
	cliBuildToolchainPackages  = toolchainenv.BuildPackagePlan
	cliToolchainSetupCommand   = toolchainenv.SetupCommandForCandidate
	cliDiagnoseInstallError    = runner.DiagnoseInstallError
	cliReadFile                = os.ReadFile
	cliVersion                 = "dev"
	cliCommit                  = "unknown"
	cliBuildDate               = "unknown"
)

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func Run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "init":
		return initCommand(args[1:])
	case "add":
		return addCommand(args[1:])
	case "remove":
		return removeCommand(args[1:])
	case "lock":
		return lockCommand(args[1:])
	case "r":
		return rCommand(args[1:])
	case "list":
		return listCommand(args[1:])
	case "prune":
		return pruneCommand(args[1:])
	case "shell":
		return shellCommand(args[1:])
	case "exec":
		return execCommand(args[1:])
	case "cache":
		return cacheCommand(args[1:])
	case "run":
		return runCommand(args[1:])
	case "scan":
		return scanCommand(args[1:])
	case "sync":
		return syncCommand(args[1:])
	case "check":
		return checkCommand(args[1:])
	case "doctor":
		return doctorCommand(args[1:])
	case "diagnose-install-error":
		return diagnoseInstallErrorCommand(args[1:])
	case "toolchain":
		return toolchainCommand(args[1:])
	case "version", "--version":
		return versionCommand(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usageText())
	}
}

func versionCommand(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: rs version")
	}
	fmt.Fprintf(os.Stdout, "rs %s\n", cliVersion)
	fmt.Fprintf(os.Stdout, "commit: %s\n", cliCommit)
	fmt.Fprintf(os.Stdout, "build_date: %s\n", cliBuildDate)
	return nil
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to install missing packages")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use for resolution and execution")
	noInstall := fs.Bool("no-install", false, "skip package installation and use current libraries only")
	locked := fs.Bool("locked", false, "require a valid lockfile but allow installing missing packages without updating it")
	frozen := fs.Bool("frozen", false, "require a valid lockfile and installed packages; do not modify dependencies")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before execution")
	bootstrapToolchain := fs.Bool("bootstrap-toolchain", false, "when no rootless toolchain is configured, try to create one automatically with a detected external manager")
	fs.Var(&packages, "package", "extra R package to install before running the script (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to install before running the script (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include before running the script (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved environment (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("missing R script path\n\nusage: rs run [flags] path/to/script.R [script args...]")
	}

	scriptPath, err := filepath.Abs(rest[0])
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	runArgs := rest[1:]
	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	opts := runner.RunOptions{
		ScriptPath:         scriptPath,
		ScriptArgs:         runArgs,
		ExtraDeps:          mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:      mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:        excludePackages,
		Repo:               *repo,
		CacheDir:           *cacheDir,
		RscriptPath:        *rscript,
		SkipInstall:        *noInstall,
		Locked:             *locked,
		Frozen:             *frozen,
		Verbose:            *verbose,
		BootstrapToolchain: *bootstrapToolchain,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
	}

	return runner.Run(opts)
}

func toolchainCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rs toolchain <template|detect|bootstrap|plan|init> ...")
	}

	switch args[0] {
	case "template":
		return toolchainTemplateCommand(args[1:])
	case "detect":
		return toolchainDetectCommand(args[1:])
	case "bootstrap":
		return toolchainBootstrapCommand(args[1:])
	case "plan":
		return toolchainPlanCommand(args[1:])
	case "init":
		return toolchainInitCommand(args[1:])
	default:
		return fmt.Errorf("unknown toolchain subcommand %q\n\n%s", args[0], usageText())
	}
}

func diagnoseInstallErrorCommand(args []string) error {
	fs := flag.NewFlagSet("diagnose-install-error", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	jsonOutput := fs.Bool("json", false, "print the install failure diagnostic as JSON")
	filePath := fs.String("file", "", "path to a file containing install error output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *filePath != "" && fs.NArg() > 0 {
		return errors.New("usage: rs diagnose-install-error [--json] [--file path/to/error.log] [error text]")
	}
	if *filePath == "" && fs.NArg() == 0 {
		return errors.New("usage: rs diagnose-install-error [--json] [--file path/to/error.log] [error text]")
	}

	var raw string
	if *filePath != "" {
		data, err := cliReadFile(*filePath)
		if err != nil {
			return fmt.Errorf("read install error file: %w", err)
		}
		raw = string(data)
	} else {
		raw = strings.Join(fs.Args(), " ")
	}

	diag, err := cliDiagnoseInstallError(raw)
	if err != nil {
		return err
	}
	if *jsonOutput {
		data, err := json.MarshalIndent(diag, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal install failure diagnostic: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	}

	fmt.Fprintf(os.Stdout, "[diagnose] message: %s\n", strings.TrimSpace(diag.Message))
	if diag.SharedObjectPath != "" {
		fmt.Fprintf(os.Stdout, "[diagnose] shared object: %s\n", diag.SharedObjectPath)
	}
	for _, detail := range diag.Details {
		fmt.Fprintf(os.Stdout, "[diagnose] %s: %s\n", detail.Kind, detail.Message)
	}
	return nil
}

func toolchainTemplateCommand(args []string) error {
	fs := flag.NewFlagSet("toolchain template", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	format := fs.String("format", "toml", "output format: toml or env")
	check := fs.Bool("check", false, "check whether the preset paths exist on this machine")
	if err := fs.Parse(normalizeToolchainTemplateArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs toolchain template <preset|auto> [--format toml|env] [--check]")
	}

	prefixes, pkgConfig, err := resolveToolchainPreset(fs.Arg(0))
	if err != nil {
		return err
	}

	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "toml":
		fmt.Fprint(os.Stdout, renderToolchainTemplateTOML(prefixes, pkgConfig))
	case "env":
		fmt.Fprint(os.Stdout, renderToolchainTemplateEnv(prefixes, pkgConfig))
	default:
		return fmt.Errorf("unsupported --format %q; supported formats: toml, env", *format)
	}

	if *check {
		issues := checkToolchainTemplatePaths(prefixes, pkgConfig)
		for _, issue := range issues {
			fmt.Fprintf(os.Stdout, "[check] %s\n", issue)
		}
		if len(issues) == 0 {
			fmt.Fprintln(os.Stdout, "[ok] all preset toolchain paths exist on this machine")
			return nil
		}
		fmt.Fprintln(os.Stdout, "[summary] preset paths are missing on this machine")
		return fmt.Errorf("toolchain preset check failed")
	}
	return nil
}

func normalizeToolchainTemplateArgs(args []string) []string {
	if len(args) <= 1 {
		return args
	}
	if strings.HasPrefix(args[0], "-") {
		return args
	}
	normalized := append([]string(nil), args[1:]...)
	normalized = append(normalized, args[0])
	return normalized
}

func toolchainDetectCommand(args []string) error {
	fs := flag.NewFlagSet("toolchain detect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	jsonOutput := fs.Bool("json", false, "print detected toolchain candidates as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: rs toolchain detect [--json]")
	}

	candidates, err := detectToolchainCandidates()
	if err != nil {
		return err
	}

	if *jsonOutput {
		report := toolchainDetectReport{Candidates: candidates}
		if report.Candidates == nil {
			report.Candidates = []toolchainDetection{}
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal toolchain detect report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	}

	if len(candidates) == 0 {
		fmt.Fprintln(os.Stdout, "no common rootless toolchain presets detected on this machine")
		fmt.Fprintf(os.Stdout, "next step: try `rs toolchain template %s`\n", strings.Join(toolchainenv.SupportedPresets(), "`, `rs toolchain template "))
		return nil
	}

	for _, candidate := range candidates {
		status := "partial"
		if candidate.Complete {
			status = "complete"
		}
		if candidate.Recommended {
			status += ", recommended"
		}
		fmt.Fprintf(os.Stdout, "[detect] %s (%s)\n", candidate.Preset, status)
		fmt.Fprintf(os.Stdout, "[detect] toolchain prefixes: %s\n", strings.Join(candidate.ToolchainPrefixes, ", "))
		fmt.Fprintf(os.Stdout, "[detect] pkg-config path: %s\n", strings.Join(candidate.PkgConfigPath, ", "))
		if len(candidate.ExistingPrefixes) == 0 {
			fmt.Fprintln(os.Stdout, "[detect] existing prefixes: <none>")
		} else {
			fmt.Fprintf(os.Stdout, "[detect] existing prefixes: %s\n", strings.Join(candidate.ExistingPrefixes, ", "))
		}
		if len(candidate.ExistingPkgConfigPath) == 0 {
			fmt.Fprintln(os.Stdout, "[detect] existing pkg-config path: <none>")
		} else {
			fmt.Fprintf(os.Stdout, "[detect] existing pkg-config path: %s\n", strings.Join(candidate.ExistingPkgConfigPath, ", "))
		}
		if candidate.SuggestedSetupCommand != "" {
			fmt.Fprintf(os.Stdout, "[next] prepare user-local prefix: %s\n", candidate.SuggestedSetupCommand)
		}
		if candidate.SuggestedSetupNote != "" {
			fmt.Fprintf(os.Stdout, "[next] setup note: %s\n", candidate.SuggestedSetupNote)
		}
		fmt.Fprintf(os.Stdout, "[next] preview template: rs toolchain template %s --check\n", candidate.Preset)
		fmt.Fprintf(os.Stdout, "[next] initialize project defaults: %s\n", candidate.SuggestedInitCommand)
	}
	return nil
}

func toolchainBootstrapCommand(args []string) error {
	fs := flag.NewFlagSet("toolchain bootstrap", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	jsonOutput := fs.Bool("json", false, "print the bootstrap plan as JSON")
	if err := fs.Parse(normalizeToolchainTemplateArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs toolchain bootstrap <preset|auto> [--json]")
	}

	home, err := cliUserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory for toolchain bootstrap: %w", err)
	}
	candidate, err := toolchainenv.DescribePreset(fs.Arg(0), home)
	if err != nil {
		return err
	}
	report := toolchainBootstrapReport{
		Candidate:            *candidate,
		TemplateCommand:      fmt.Sprintf("rs toolchain template %s", candidate.Preset),
		EnvTemplateCommand:   fmt.Sprintf("rs toolchain template %s --format env", candidate.Preset),
		InitCommand:          candidate.SuggestedInitCommand,
		DoctorCommand:        "rs doctor --toolchain-only",
		TemplateCheckCommand: fmt.Sprintf("rs toolchain template %s --check", candidate.Preset),
	}

	if *jsonOutput {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal toolchain bootstrap report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	}

	status := "template"
	if candidate.Complete {
		status = "detected complete layout"
	} else if len(candidate.ExistingPrefixes) > 0 || len(candidate.ExistingPkgConfigPath) > 0 {
		status = "detected partial layout"
	}
	if candidate.Recommended {
		status += ", recommended"
	}
	fmt.Fprintf(os.Stdout, "[bootstrap] preset: %s (%s)\n", candidate.Preset, status)
	fmt.Fprintf(os.Stdout, "[bootstrap] toolchain prefixes: %s\n", strings.Join(candidate.ToolchainPrefixes, ", "))
	fmt.Fprintf(os.Stdout, "[bootstrap] pkg-config path: %s\n", strings.Join(candidate.PkgConfigPath, ", "))
	if candidate.SuggestedSetupCommand != "" {
		fmt.Fprintf(os.Stdout, "[bootstrap] setup command: %s\n", candidate.SuggestedSetupCommand)
	}
	if candidate.SuggestedSetupNote != "" {
		fmt.Fprintf(os.Stdout, "[bootstrap] setup note: %s\n", candidate.SuggestedSetupNote)
	}
	fmt.Fprintf(os.Stdout, "[next] preview template: %s\n", report.TemplateCheckCommand)
	fmt.Fprintf(os.Stdout, "[next] export ad hoc environment: %s\n", report.EnvTemplateCommand)
	fmt.Fprintf(os.Stdout, "[next] initialize project defaults: %s\n", report.InitCommand)
	fmt.Fprintf(os.Stdout, "[next] validate toolchain configuration: %s\n", report.DoctorCommand)
	return nil
}

func toolchainPlanCommand(args []string) error {
	fs := flag.NewFlagSet("toolchain plan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	jsonOutput := fs.Bool("json", false, "print the toolchain plan as JSON")
	preset := fs.String("preset", "auto", "toolchain preset to plan for: auto, enva, micromamba, mamba, conda, homebrew, or spack")
	phase := fs.String("phase", "full", "package phase to plan: base or full")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs toolchain plan [--preset auto|enva|micromamba|mamba|conda|homebrew|spack] [--phase base|full] [--json] path/to/script.R")
	}

	report, err := buildToolchainPlanReport(fs.Arg(0), *preset, *phase)
	if err != nil {
		return err
	}

	if *jsonOutput {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal toolchain plan report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	}

	fmt.Fprintf(os.Stdout, "[plan] script: %s\n", report.DependencyPlan.Script)
	if report.DependencyPlan.ProjectConfig != "" {
		fmt.Fprintf(os.Stdout, "[plan] project config: %s\n", report.DependencyPlan.ProjectConfig)
	}
	fmt.Fprintf(os.Stdout, "[plan] preset: %s\n", report.Candidate.Preset)
	fmt.Fprintf(os.Stdout, "[plan] phase: %s\n", report.Phase)
	fmt.Fprintf(os.Stdout, "[plan] detected packages: %s\n", renderListOrNone(report.DependencyPlan.DetectedDeps))
	fmt.Fprintf(os.Stdout, "[plan] cran packages: %s\n", renderListOrNone(report.DependencyPlan.CRANDeps))
	fmt.Fprintf(os.Stdout, "[plan] bioconductor packages: %s\n", renderListOrNone(report.DependencyPlan.BiocDeps))
	fmt.Fprintf(os.Stdout, "[plan] base toolchain packages: %s\n", renderListOrNone(report.PackagePlan.BasePackages))
	if len(report.PackagePlan.Groups) == 0 {
		fmt.Fprintln(os.Stdout, "[plan] resolved system package groups: <none>")
	} else {
		for _, group := range report.PackagePlan.Groups {
			fmt.Fprintf(os.Stdout, "[plan] system package group %s: %s\n", group.Category, strings.Join(group.Packages, ", "))
		}
	}
	fmt.Fprintf(os.Stdout, "[plan] planned packages for phase %s: %s\n", report.Phase, renderListOrNone(report.PlannedPackages))
	fmt.Fprintf(os.Stdout, "[next] prepare rootless toolchain env: %s\n", report.SetupCommand)
	fmt.Fprintf(os.Stdout, "[next] initialize project defaults: %s\n", report.InitCommand)
	fmt.Fprintf(os.Stdout, "[next] validate toolchain configuration: %s\n", report.DoctorCommand)
	return nil
}

func toolchainInitCommand(args []string) error {
	fs := flag.NewFlagSet("toolchain init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	preset := fs.String("preset", "auto", "toolchain preset to initialize: auto, enva, micromamba, mamba, conda, homebrew, or spack")
	phase := fs.String("phase", "full", "package phase to initialize: base or full")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs toolchain init [--preset auto|enva|micromamba|mamba|conda|homebrew|spack] [--phase base|full] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}
	report, err := buildToolchainPlanReport(scriptPath, *preset, *phase)
	if err != nil {
		return err
	}
	home, err := cliUserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory for toolchain init: %w", err)
	}
	candidate, err := cliBootstrapToolchain(report.Candidate.Preset, home, os.Environ(), report.PlannedPackages, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[init] preset: %s\n", candidate.Preset)
	fmt.Fprintf(os.Stdout, "[init] phase: %s\n", report.Phase)
	fmt.Fprintf(os.Stdout, "[init] installed toolchain packages: %s\n", renderListOrNone(report.PlannedPackages))
	fmt.Fprintf(os.Stdout, "[init] toolchain prefixes: %s\n", renderListOrNone(candidate.ToolchainPrefixes))
	fmt.Fprintf(os.Stdout, "[init] pkg-config path: %s\n", renderListOrNone(candidate.PkgConfigPath))
	fmt.Fprintf(os.Stdout, "[next] initialize project defaults: %s\n", candidate.SuggestedInitCommand)
	fmt.Fprintf(os.Stdout, "[next] validate toolchain configuration: %s\n", report.DoctorCommand)
	return nil
}

func buildToolchainPlanReport(scriptArg, preset, phase string) (toolchainPlanReport, error) {
	scriptPath, err := filepath.Abs(scriptArg)
	if err != nil {
		return toolchainPlanReport{}, fmt.Errorf("resolve script path: %w", err)
	}
	home, err := cliUserHomeDir()
	if err != nil {
		return toolchainPlanReport{}, fmt.Errorf("resolve home directory for toolchain plan: %w", err)
	}
	candidate, err := resolveToolchainPlanningCandidate(home, preset)
	if err != nil {
		return toolchainPlanReport{}, err
	}
	dependencyPlan, err := cliPlanToolchain(runner.ToolchainPlanOptions{
		ScriptPath: scriptPath,
		Progress:   os.Stderr,
	})
	if err != nil {
		return toolchainPlanReport{}, err
	}
	categories := make([]string, 0, len(dependencyPlan.SystemHintDetails))
	for _, detail := range dependencyPlan.SystemHintDetails {
		categories = append(categories, detail.Category)
	}
	packagePlan, err := cliBuildToolchainPackages(candidate.Preset, categories)
	if err != nil {
		return toolchainPlanReport{}, err
	}
	normalizedPhase, err := normalizeToolchainPhase(phase)
	if err != nil {
		return toolchainPlanReport{}, err
	}
	plannedPackages, err := packagePlan.PackagesForPhase(normalizedPhase)
	if err != nil {
		return toolchainPlanReport{}, err
	}
	return toolchainPlanReport{
		Candidate:       *candidate,
		DependencyPlan:  dependencyPlan,
		Phase:           normalizedPhase,
		PackagePlan:     packagePlan,
		PlannedPackages: plannedPackages,
		SetupCommand:    cliToolchainSetupCommand(*candidate, plannedPackages),
		InitCommand:     candidate.SuggestedInitCommand,
		DoctorCommand:   fmt.Sprintf("rs doctor --toolchain-only %s", scriptPath),
	}, nil
}

func resolveToolchainPlanningCandidate(home, preset string) (*toolchainenv.Candidate, error) {
	normalized := strings.TrimSpace(strings.ToLower(preset))
	if normalized == "" || normalized == "auto" {
		if candidate, err := cliBootstrapCandidate("auto", home, os.Environ()); err == nil {
			return candidate, nil
		}
		if candidate, err := cliRecommendedToolchain(home); err == nil && candidate != nil {
			return candidate, nil
		}
		return cliDescribeToolchainPreset("enva", home)
	}
	return cliDescribeToolchainPreset(normalized, home)
}

func normalizeToolchainPhase(value string) (string, error) {
	switch normalized := strings.TrimSpace(strings.ToLower(value)); normalized {
	case "", "full":
		return "full", nil
	case "base":
		return "base", nil
	default:
		return "", fmt.Errorf("unsupported --phase %q; supported phases: base, full", value)
	}
}

func renderListOrNone(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}

func initCommand(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	repo := fs.String("repo", project.DefaultRepo, "default CRAN mirror written to rs.toml")
	cacheDir := fs.String("cache-dir", ".rs-cache", "managed library cache directory written to rs.toml")
	lockfile := fs.String("lockfile", "rs.lock.json", "lockfile path written to rs.toml")
	rscript := fs.String("rscript", "", "default Rscript binary or path written to rs.toml")
	rVersion := fs.String("r-version", "", "default R version selector written to rs.toml")
	toolchainPreset := fs.String("toolchain-preset", "", "seed toolchain_prefixes/pkg_config_path for auto, enva, micromamba, mamba, conda, homebrew, or spack")
	var toolchainPrefixes stringList
	var pkgConfigPath stringList
	var fromScripts stringList
	var fromDirs stringList
	var includePackages stringList
	var excludePackages stringList
	var biocPackages stringList
	writeScriptBlock := fs.Bool("write-script-block", false, "with one --from, write detected packages under a script-specific block")
	includeBundled := fs.Bool("include-bundled", false, "with --from or --from-dir, keep R bundled base/recommended packages in generated config")
	force := fs.Bool("force", false, "overwrite an existing rs.toml")
	fs.Var(&fromScripts, "from", "scan packages from an existing R script and seed rs.toml (repeatable)")
	fs.Var(&fromDirs, "from-dir", "scan all R scripts under a directory and seed rs.toml (repeatable)")
	fs.Var(&toolchainPrefixes, "toolchain-prefix", "user-local prefix to expose to PATH/CPPFLAGS/LDFLAGS/PKG_CONFIG_PATH during source builds (repeatable)")
	fs.Var(&pkgConfigPath, "pkg-config-path", "extra pkg-config search path for source builds (repeatable)")
	fs.Var(&includePackages, "include", "add an extra project-level dependency to generated config (repeatable)")
	fs.Var(&excludePackages, "exclude", "exclude a detected dependency from generated config (repeatable)")
	fs.Var(&biocPackages, "bioc-package", "add an extra project-level Bioconductor dependency to generated config (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs init [flags] [dir]")
	}

	targetDir := "."
	if fs.NArg() == 1 {
		targetDir = fs.Arg(0)
	}
	targetDir, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolve target dir: %w", err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	presetPrefixes, presetPkgConfig, err := resolveToolchainPreset(*toolchainPreset)
	if err != nil {
		return err
	}

	configPath := filepath.Join(targetDir, project.ConfigFileName)
	if _, err := os.Stat(configPath); err == nil && !*force {
		return fmt.Errorf("%s already exists\nrerun with --force to overwrite", configPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config: %w", err)
	}

	initOpts := project.InitOptions{
		Repo:              *repo,
		CacheDir:          *cacheDir,
		Lockfile:          *lockfile,
		Rscript:           *rscript,
		RVersion:          *rVersion,
		ToolchainPrefixes: mergeUniqueStrings(presetPrefixes, toolchainPrefixes),
		PkgConfigPath:     mergeUniqueStrings(presetPkgConfig, pkgConfigPath),
		BiocPackages:      biocPackages,
	}
	includeCRAN, includeBioc := rdeps.SplitBiocPackages(includePackages)
	initOpts.Packages = mergeInitDeps(initOpts.Packages, includeCRAN)
	initOpts.BiocPackages = mergeInitDeps(initOpts.BiocPackages, includeBioc)

	scriptConfigs := map[string]project.ScriptConfig{}
	for _, rawDir := range fromDirs {
		dirPath, err := filepath.Abs(rawDir)
		if err != nil {
			return fmt.Errorf("resolve --from-dir path: %w", err)
		}
		scriptPaths, err := collectInitScriptPaths(dirPath)
		if err != nil {
			return err
		}
		for _, path := range scriptPaths {
			fromScripts = append(fromScripts, path)
		}
	}
	for _, rawPath := range fromScripts {
		fromPath, err := filepath.Abs(rawPath)
		if err != nil {
			return fmt.Errorf("resolve --from script path: %w", err)
		}
		deps, err := runner.ScanScript(fromPath)
		if err != nil {
			return err
		}
		if !*includeBundled {
			deps = rdeps.FilterInstallable(deps)
		}
		deps = filterInitExcludedDeps(deps, excludePackages)
		cranDeps, detectedBioc := rdeps.SplitBiocPackages(deps)
		scriptCfg := scriptConfigs[fromPath]
		scriptCfg.Packages = mergeInitDeps(scriptCfg.Packages, cranDeps)
		scriptCfg.BiocPackages = mergeInitDeps(scriptCfg.BiocPackages, detectedBioc)
		scriptConfigs[fromPath] = scriptCfg
	}

	writeScripts := *writeScriptBlock || len(scriptConfigs) > 1
	if len(scriptConfigs) == 1 && !writeScripts {
		for _, scriptCfg := range scriptConfigs {
			initOpts.Packages = mergeInitDeps(initOpts.Packages, scriptCfg.Packages)
			initOpts.BiocPackages = mergeInitDeps(initOpts.BiocPackages, scriptCfg.BiocPackages)
		}
	}

	cfg, err := project.NewConfigFromScripts(initOpts, targetDir, scriptConfigs, writeScripts)
	if err != nil {
		return err
	}
	if err := project.Save(configPath, cfg); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "wrote %s\n", configPath)
	if len(scriptConfigs) > 0 {
		scriptPaths := make([]string, 0, len(scriptConfigs))
		for path := range scriptConfigs {
			scriptPaths = append(scriptPaths, path)
		}
		slices.Sort(scriptPaths)
		for _, path := range scriptPaths {
			total := len(scriptConfigs[path].Packages) + len(scriptConfigs[path].BiocPackages)
			fmt.Fprintf(os.Stdout, "seeded %d package(s) from %s\n", total, path)
		}
	}
	return nil
}

func resolveToolchainPreset(name string) ([]string, []string, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return nil, nil, nil
	}

	home, err := cliUserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve home directory for --toolchain-preset: %w", err)
	}
	return toolchainenv.ResolvePreset(name, home)
}

func mergeUniqueStrings(groups ...[]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, group := range groups {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func renderToolchainTemplateTOML(prefixes, pkgConfig []string) string {
	lines := []string{
		"toolchain_prefixes = " + renderCLIStringArray(prefixes),
		"pkg_config_path = " + renderCLIStringArray(pkgConfig),
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderToolchainTemplateEnv(prefixes, pkgConfig []string) string {
	lines := []string{
		"export RS_TOOLCHAIN_PREFIXES=" + shellQuote(strings.Join(prefixes, string(os.PathListSeparator))),
		"export RS_PKG_CONFIG_PATH=" + shellQuote(strings.Join(pkgConfig, string(os.PathListSeparator))),
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderCLIStringArray(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Quote(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func checkToolchainTemplatePaths(prefixes, pkgConfig []string) []string {
	issues := []string{}
	for _, path := range prefixes {
		if issue, ok := checkTemplatePath("toolchain prefix", path); ok {
			issues = append(issues, issue)
		}
	}
	for _, path := range pkgConfig {
		if issue, ok := checkTemplatePath("pkg-config path", path); ok {
			issues = append(issues, issue)
		}
	}
	return issues
}

func detectToolchainCandidates() ([]toolchainDetection, error) {
	home, err := cliUserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory for toolchain detect: %w", err)
	}
	return toolchainenv.DetectCandidates(home)
}

func checkTemplatePath(label, path string) (string, bool) {
	info, err := cliStat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("%s missing: %s", label, path), true
	}
	if err != nil {
		return fmt.Sprintf("%s unreadable: %s (%v)", label, path, err), true
	}
	if !info.IsDir() {
		return fmt.Sprintf("%s is not a directory: %s", label, path), true
	}
	return "", false
}

func mergeInitDeps(left, right []string) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	out := make([]string, 0, len(left)+len(right))
	for _, dep := range left {
		if _, ok := seen[dep]; ok {
			continue
		}
		seen[dep] = struct{}{}
		out = append(out, dep)
	}
	for _, dep := range right {
		if _, ok := seen[dep]; ok {
			continue
		}
		seen[dep] = struct{}{}
		out = append(out, dep)
	}
	slices.Sort(out)
	return out
}

func splitIncludedPackages(includePackages []string) ([]string, []string) {
	return rdeps.SplitBiocPackages(includePackages)
}

func filterInitExcludedDeps(deps []string, excludes []string) []string {
	if len(deps) == 0 || len(excludes) == 0 {
		return deps
	}
	excluded := make(map[string]struct{}, len(excludes))
	for _, dep := range excludes {
		excluded[dep] = struct{}{}
	}
	out := make([]string, 0, len(deps))
	for _, dep := range deps {
		if _, ok := excluded[dep]; ok {
			continue
		}
		out = append(out, dep)
	}
	return out
}

func collectInitScriptPaths(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat --from-dir path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}

	var out []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".rs-cache":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".r", ".rscript":
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk --from-dir scripts: %w", err)
	}
	slices.Sort(out)
	return out, nil
}

func addCommand(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	bioc := fs.Bool("bioc", false, "add packages to bioc_packages instead of packages")
	script := fs.String("script", "", "add packages only for the given script profile")
	projectDir := fs.String("project-dir", "", "start config discovery from this directory instead of the current working directory")
	sourceType := fs.String("source", "", "custom source type: github, git, or local")
	githubRepo := fs.String("github-repo", "", "GitHub repository for --source github (owner/name)")
	url := fs.String("url", "", "repository URL for --source git")
	path := fs.String("path", "", "local path for --source local")
	ref := fs.String("ref", "", "git or GitHub ref for custom source packages")
	subdir := fs.String("subdir", "", "package subdirectory inside the custom source")
	host := fs.String("host", "", "GitHub API host for GitHub Enterprise installs")
	tokenEnv := fs.String("token-env", "", "environment variable name holding a GitHub token")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: rs add [flags] <package> [package...]")
	}

	packages := fs.Args()
	if *sourceType != "" {
		if len(packages) != 1 {
			return errors.New("custom source packages must be added one at a time")
		}
		if *bioc {
			return errors.New("--bioc cannot be combined with --source")
		}
	}

	discoveryDir := *projectDir
	if discoveryDir == "" {
		discoveryDir = "."
	}
	if *script != "" {
		scriptPath := *script
		if !filepath.IsAbs(scriptPath) && *projectDir != "" {
			scriptPath = filepath.Join(*projectDir, scriptPath)
		}
		scriptPath, err := filepath.Abs(scriptPath)
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		*script = scriptPath
		if *projectDir == "" {
			discoveryDir = filepath.Dir(scriptPath)
		}
	}
	discoveryDir, err := filepath.Abs(discoveryDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}

	projectCfg, found, err := project.Discover(discoveryDir)
	if err != nil {
		return fmt.Errorf("discover project config: %w", err)
	}
	if !found || projectCfg.Path == "" {
		return fmt.Errorf("no %s found from %s\nrun `rs init` first", project.ConfigFileName, discoveryDir)
	}

	cfg, err := project.LoadEditable(projectCfg.Path)
	if err != nil {
		return err
	}

	var sourceSpec *project.SourceSpec
	if *sourceType != "" {
		sourceSpec = &project.SourceSpec{
			Type:     *sourceType,
			Host:     *host,
			Repo:     *githubRepo,
			URL:      *url,
			Ref:      *ref,
			Path:     *path,
			Subdir:   *subdir,
			TokenEnv: *tokenEnv,
		}
	}

	for _, pkg := range packages {
		if err := project.AddPackage(&cfg, project.AddPackageOptions{
			ScriptPath: *script,
			Package:    pkg,
			Bioc:       *bioc,
			Source:     sourceSpec,
		}); err != nil {
			return err
		}
	}

	if err := project.Save(cfg.Path, cfg); err != nil {
		return err
	}

	scope := "project"
	if *script != "" {
		scope = *script
	}
	fmt.Fprintf(os.Stdout, "updated %s (%s)\n", cfg.Path, scope)
	return nil
}

func removeCommand(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	bioc := fs.Bool("bioc", false, "remove packages from bioc_packages instead of packages")
	script := fs.String("script", "", "remove packages only from the given script profile")
	projectDir := fs.String("project-dir", "", "start config discovery from this directory instead of the current working directory")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: rs remove [flags] <package> [package...]")
	}

	discoveryDir := *projectDir
	if discoveryDir == "" {
		discoveryDir = "."
	}
	if *script != "" {
		scriptPath := *script
		if !filepath.IsAbs(scriptPath) && *projectDir != "" {
			scriptPath = filepath.Join(*projectDir, scriptPath)
		}
		scriptPath, err := filepath.Abs(scriptPath)
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		*script = scriptPath
		if *projectDir == "" {
			discoveryDir = filepath.Dir(scriptPath)
		}
	}
	discoveryDir, err := filepath.Abs(discoveryDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}

	projectCfg, found, err := project.Discover(discoveryDir)
	if err != nil {
		return fmt.Errorf("discover project config: %w", err)
	}
	if !found || projectCfg.Path == "" {
		return fmt.Errorf("no %s found from %s\nrun `rs init` first", project.ConfigFileName, discoveryDir)
	}

	cfg, err := project.LoadEditable(projectCfg.Path)
	if err != nil {
		return err
	}

	for _, pkg := range fs.Args() {
		if err := project.RemovePackage(&cfg, project.RemovePackageOptions{
			ScriptPath: *script,
			Package:    pkg,
			Bioc:       *bioc,
		}); err != nil {
			return err
		}
	}

	if err := project.Save(cfg.Path, cfg); err != nil {
		return err
	}

	scope := "project"
	if *script != "" {
		scope = *script
	}
	fmt.Fprintf(os.Stdout, "updated %s (%s)\n", cfg.Path, scope)
	return nil
}

func lockCommand(args []string) error {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to install and lock packages")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use while locking")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before locking")
	bootstrapToolchain := fs.Bool("bootstrap-toolchain", false, "when no rootless toolchain is configured, try to create one automatically with a detected external manager")
	fs.Var(&packages, "package", "extra R package to include before writing the lockfile (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to include before writing the lockfile (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include before writing the lockfile (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved lock plan (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs lock [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Lock(runner.LockOptions{
		ScriptPath:         scriptPath,
		ExtraDeps:          mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:      mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:        excludePackages,
		Repo:               *repo,
		CacheDir:           *cacheDir,
		RscriptPath:        *rscript,
		Verbose:            *verbose,
		BootstrapToolchain: *bootstrapToolchain,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
	})
}

func listCommand(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to resolve the dependency plan")
	cacheDir := fs.String("cache-dir", "", "cache directory used to compute the managed library path")
	rscript := fs.String("rscript", "", "Rscript binary or path to use while resolving the plan")
	jsonOutput := fs.Bool("json", false, "print the resolved dependency plan as JSON")
	fs.Var(&packages, "package", "extra CRAN package to include in the resolved plan (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to include in the resolved plan (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include in the resolved plan (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved plan (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs list [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.List(runner.ListOptions{
		ScriptPath:      scriptPath,
		ExtraDeps:       mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:   mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:     excludePackages,
		IncludeDeps:     includeCRAN,
		IncludeBiocDeps: includeBioc,
		Repo:            *repo,
		CacheDir:        *cacheDir,
		RscriptPath:     *rscript,
		JSON:            *jsonOutput,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
	})
}

func pruneCommand(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	projectDir := fs.String("project-dir", "", "project directory to prune when no script path is provided")
	dryRun := fs.Bool("dry-run", false, "show which managed libraries would be removed without deleting them")
	pkgstoreRetentionDays := fs.Int("pkgstore-retention-days", -1, "remove package-store entries unused for more than this many days; defaults to RS_PKGSTORE_RETENTION_DAYS or 30")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs prune [flags] [path/to/script.R]")
	}

	scriptPath := ""
	if fs.NArg() == 1 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		scriptPath = abs
	}

	return runner.Prune(runner.PruneOptions{
		ScriptPath:                scriptPath,
		ProjectDir:                *projectDir,
		DryRun:                    *dryRun,
		PackageStoreRetentionDays: *pkgstoreRetentionDays,
		Stdout:                    os.Stdout,
		Stderr:                    os.Stderr,
	})
}

func shellCommand(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to install missing packages")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use for the managed shell")
	noInstall := fs.Bool("no-install", false, "skip package installation and use current libraries only")
	locked := fs.Bool("locked", false, "require a valid lockfile but allow installing missing packages without updating it")
	frozen := fs.Bool("frozen", false, "require a valid lockfile and installed packages; do not modify dependencies")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before opening the shell")
	bootstrapToolchain := fs.Bool("bootstrap-toolchain", false, "when no rootless toolchain is configured, try to create one automatically with a detected external manager")
	fs.Var(&packages, "package", "extra R package to install before opening the shell (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to install before opening the shell (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include before opening the shell (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved shell environment (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs shell [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Shell(runner.ShellOptions{
		ScriptPath:         scriptPath,
		ExtraDeps:          mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:      mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:        excludePackages,
		Repo:               *repo,
		CacheDir:           *cacheDir,
		RscriptPath:        *rscript,
		SkipInstall:        *noInstall,
		Locked:             *locked,
		Frozen:             *frozen,
		Verbose:            *verbose,
		BootstrapToolchain: *bootstrapToolchain,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
	})
}

func execCommand(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to install missing packages")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use for the managed expression")
	noInstall := fs.Bool("no-install", false, "skip package installation and use current libraries only")
	locked := fs.Bool("locked", false, "require a valid lockfile but allow installing missing packages without updating it")
	frozen := fs.Bool("frozen", false, "require a valid lockfile and installed packages; do not modify dependencies")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before executing the expression")
	bootstrapToolchain := fs.Bool("bootstrap-toolchain", false, "when no rootless toolchain is configured, try to create one automatically with a detected external manager")
	expression := fs.String("e", "", "R expression to execute")
	fs.StringVar(expression, "expr", "", "R expression to execute")
	fs.Var(&packages, "package", "extra R package to install before executing the expression (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to install before executing the expression (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include before executing the expression (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved expression environment (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*expression) == "" {
		return errors.New("usage: rs exec [flags] -e 'expression' path/to/script.R")
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs exec [flags] -e 'expression' path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Exec(runner.ExecOptions{
		ScriptPath:         scriptPath,
		Expression:         *expression,
		ExtraDeps:          mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:      mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:        excludePackages,
		Repo:               *repo,
		CacheDir:           *cacheDir,
		RscriptPath:        *rscript,
		SkipInstall:        *noInstall,
		Locked:             *locked,
		Frozen:             *frozen,
		Verbose:            *verbose,
		BootstrapToolchain: *bootstrapToolchain,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
	})
}

func cacheCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rs cache <dir|ls|rm> [args]")
	}

	switch args[0] {
	case "dir":
		return cacheDirCommand(args[1:])
	case "ls":
		return cacheListCommand(args[1:])
	case "rm":
		return cacheRemoveCommand(args[1:])
	default:
		return fmt.Errorf("unknown cache subcommand %q\n\n%s", args[0], usageText())
	}
}

func cacheDirCommand(args []string) error {
	fs := flag.NewFlagSet("cache dir", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs cache dir [path/to/script.R]")
	}

	scriptPath := ""
	if fs.NArg() == 1 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		scriptPath = abs
	}

	return runner.CacheDir(runner.CacheDirOptions{
		ScriptPath: scriptPath,
		Stdout:     os.Stdout,
	})
}

func cacheListCommand(args []string) error {
	fs := flag.NewFlagSet("cache ls", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	projectDir := fs.String("project-dir", "", "project directory used to mark active libraries")
	jsonOutput := fs.Bool("json", false, "print cache libraries as JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs cache ls [flags] [path/to/script.R]")
	}

	scriptPath := ""
	if fs.NArg() == 1 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		scriptPath = abs
	}

	return runner.CacheList(runner.CacheListOptions{
		ScriptPath: scriptPath,
		ProjectDir: *projectDir,
		JSON:       *jsonOutput,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
}

func cacheRemoveCommand(args []string) error {
	fs := flag.NewFlagSet("cache rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	script := fs.String("script", "", "script path used to resolve the cache root when removing by hash")
	projectDir := fs.String("project-dir", "", "project directory used to resolve the cache root when removing by hash")
	cacheDir := fs.String("cache-dir", "", "explicit cache root used when removing by hash")
	dryRun := fs.Bool("dry-run", false, "show which managed library would be removed without deleting it")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs cache rm [flags] <hash|path>")
	}

	scriptPath := ""
	if *script != "" {
		abs, err := filepath.Abs(*script)
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		scriptPath = abs
	}

	return runner.CacheRemove(runner.CacheRemoveOptions{
		Target:     fs.Arg(0),
		ScriptPath: scriptPath,
		ProjectDir: *projectDir,
		CacheDir:   *cacheDir,
		DryRun:     *dryRun,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
}

func syncCommand(args []string) error {
	return lockCommand(args)
}

func rCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rs r <list|install|use|which> [args]")
	}

	switch args[0] {
	case "list":
		return rListCommand(args[1:])
	case "install":
		return rInstallCommand(args[1:])
	case "use":
		return rUseCommand(args[1:])
	case "which":
		return rWhichCommand(args[1:])
	default:
		return fmt.Errorf("unknown r subcommand %q\n\n%s", args[0], usageText())
	}
}

func rListCommand(args []string) error {
	fs := flag.NewFlagSet("r list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: rs r list")
	}
	return rmanager.List(os.Stdout, os.Stderr)
}

func rInstallCommand(args []string) error {
	fs := flag.NewFlagSet("r install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	method := fs.String("method", string(rmanager.InstallMethodAuto), "install method: auto, binary, or source")
	bootstrapToolchain := fs.Bool("bootstrap-toolchain", false, "when source-build prerequisites are missing, try to create a rootless toolchain automatically with a detected external manager")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs r install [--method auto|binary|source] [--bootstrap-toolchain] <version>")
	}
	return cliInstallRWithOptions(rmanager.InstallOptions{
		Version:            fs.Arg(0),
		Method:             rmanager.InstallMethod(strings.TrimSpace(*method)),
		BootstrapToolchain: *bootstrapToolchain,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
	})
}

func rUseCommand(args []string) error {
	fs := flag.NewFlagSet("r use", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectDir := fs.String("project-dir", ".", "directory or script path used to find rs.toml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs r use [--project-dir <dir>] <version|Rscript-path>")
	}

	startDir, _, err := resolveProjectTarget(*projectDir)
	if err != nil {
		return err
	}
	cfg, found, err := project.Discover(startDir)
	if err != nil {
		return fmt.Errorf("discover project config: %w", err)
	}
	if !found {
		return fmt.Errorf("no %s found from %s\nrun `rs init` first", project.ConfigFileName, startDir)
	}

	editable, err := project.LoadEditable(cfg.Path)
	if err != nil {
		return err
	}
	spec := fs.Arg(0)
	if rmanager.LooksLikeVersionSpec(spec) && !strings.Contains(strings.ToLower(spec), "rscript") {
		if err := cliValidateVersionSelector(spec); err != nil {
			return err
		}
		if _, err := cliResolveVersionOrPath(spec); err != nil {
			if _, resolveErr := cliResolveVersionSelector(spec); resolveErr != nil {
				return resolveErr
			}
		}
		editable.Defaults.RVersion = spec
		editable.Defaults.Rscript = ""
	} else {
		rscriptPath, err := cliResolveVersionOrPath(spec)
		if err != nil {
			return err
		}
		editable.Defaults.Rscript = rscriptPath
		editable.Defaults.RVersion = ""
	}
	if err := project.Save(editable.Path, editable); err != nil {
		return err
	}

	if editable.Defaults.RVersion != "" {
		fmt.Fprintf(os.Stdout, "updated %s (r_version = %s)\n", editable.Path, editable.Defaults.RVersion)
	} else {
		fmt.Fprintf(os.Stdout, "updated %s (rscript = %s)\n", editable.Path, editable.Defaults.Rscript)
	}
	return nil
}

func rWhichCommand(args []string) error {
	fs := flag.NewFlagSet("r which", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	script := fs.String("script", "", "script path used to resolve a script-specific rscript setting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs r which [--script <path>] [dir|script]")
	}

	target := "."
	if fs.NArg() == 1 {
		target = fs.Arg(0)
	}
	startDir, inferredScript, err := resolveProjectTarget(target)
	if err != nil {
		return err
	}
	scriptPath := inferredScript
	if *script != "" {
		scriptPath, err = filepath.Abs(*script)
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
	}

	cfg, found, err := project.Discover(startDir)
	if err != nil {
		return fmt.Errorf("discover project config: %w", err)
	}
	configured := ""
	if found {
		if scriptPath != "" {
			resolved, err := cfg.ResolveForScript(scriptPath)
			if err != nil {
				return fmt.Errorf("resolve script config: %w", err)
			}
			if resolved.Rscript != "" {
				configured = resolved.Rscript
			} else {
				configured = resolved.RVersion
			}
		} else {
			if cfg.Defaults.Rscript != "" {
				configured = cfg.Defaults.Rscript
			} else {
				configured = cfg.Defaults.RVersion
			}
		}
	}

	if configured == "" {
		if managed, err := cliCurrentManagedRscript(); err == nil {
			fmt.Fprintln(os.Stdout, managed)
			return nil
		}
		configured = "Rscript"
	}
	rscriptPath, err := cliResolveVersionOrPath(configured)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, rscriptPath)
	return nil
}

func checkCommand(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to resolve the lockfile environment")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use during validation")
	jsonOutput := fs.Bool("json", false, "print the validation report as JSON")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before validation")
	bootstrapToolchain := fs.Bool("bootstrap-toolchain", false, "when no rootless toolchain is configured, try to create one automatically with a detected external manager")
	fs.Var(&packages, "package", "extra CRAN package to include in validation (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to include in validation (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include in validation (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from validation (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs check [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Check(runner.CheckOptions{
		ScriptPath:         scriptPath,
		ExtraDeps:          mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:      mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:        excludePackages,
		IncludeDeps:        includeCRAN,
		IncludeBiocDeps:    includeBioc,
		Repo:               *repo,
		CacheDir:           *cacheDir,
		RscriptPath:        *rscript,
		JSON:               *jsonOutput,
		Verbose:            *verbose,
		BootstrapToolchain: *bootstrapToolchain,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
	})
}

func doctorCommand(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to resolve the environment")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use during diagnosis")
	jsonOutput := fs.Bool("json", false, "print the diagnostic report as JSON")
	strict := fs.Bool("strict", false, "exit non-zero unless doctor status is ok")
	quiet := fs.Bool("quiet", false, "hide informational lines and print only diagnostics plus summary")
	summaryOnly := fs.Bool("summary-only", false, "print only the final doctor summary line")
	toolchainOnly := fs.Bool("toolchain-only", false, "only validate rootless toolchain prefixes and pkg-config paths")
	verbose := fs.Bool("verbose", false, "print additional environment details")
	bootstrapToolchain := fs.Bool("bootstrap-toolchain", false, "when no rootless toolchain is configured, try to create one automatically with a detected external manager before diagnosing")
	fs.Var(&packages, "package", "extra CRAN package to include in diagnosis (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to include in diagnosis (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include in diagnosis (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from diagnosis (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*toolchainOnly && fs.NArg() != 1 {
		return errors.New("usage: rs doctor [flags] path/to/script.R")
	}
	if *toolchainOnly && fs.NArg() > 1 {
		return errors.New("usage: rs doctor --toolchain-only [path/to/script.R|path/to/project]")
	}

	scriptPath := ""
	projectDir := ""
	var err error
	if *toolchainOnly {
		target := "."
		if fs.NArg() == 1 {
			target = fs.Arg(0)
		}
		projectDir, scriptPath, err = resolveProjectTarget(target)
		if err != nil {
			return err
		}
	} else {
		scriptPath, err = filepath.Abs(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return cliDoctor(runner.DoctorOptions{
		ScriptPath:         scriptPath,
		ProjectDir:         projectDir,
		ExtraDeps:          mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:      mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:        excludePackages,
		IncludeDeps:        includeCRAN,
		IncludeBiocDeps:    includeBioc,
		Repo:               *repo,
		CacheDir:           *cacheDir,
		RscriptPath:        *rscript,
		JSON:               *jsonOutput,
		Strict:             *strict,
		Quiet:              *quiet,
		SummaryOnly:        *summaryOnly,
		ToolchainOnly:      *toolchainOnly,
		Verbose:            *verbose,
		BootstrapToolchain: *bootstrapToolchain,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
	})
}

func scanCommand(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print detected package dependencies as JSON")
	installable := fs.Bool("installable", false, "filter out R bundled base/recommended packages")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs scan [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	deps, err := runner.ScanScript(scriptPath)
	if err != nil {
		return err
	}
	if *installable {
		deps = rdeps.FilterInstallable(deps)
	}

	if *jsonOutput {
		cranDeps, biocDeps := rdeps.SplitBiocPackages(deps)
		report := scanReport{
			Script:          scriptPath,
			Packages:        deps,
			CRANPackages:    cranDeps,
			BiocPackages:    biocDeps,
			InstallableOnly: *installable,
		}
		if report.Packages == nil {
			report.Packages = []string{}
		}
		if report.CRANPackages == nil {
			report.CRANPackages = []string{}
		}
		if report.BiocPackages == nil {
			report.BiocPackages = []string{}
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal scan report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	}

	for _, dep := range deps {
		fmt.Println(dep)
	}

	return nil
}

func resolveProjectTarget(raw string) (string, string, error) {
	target := raw
	if strings.TrimSpace(target) == "" {
		target = "."
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve target path: %w", err)
	}
	info, err := os.Stat(absTarget)
	if err != nil {
		return "", "", fmt.Errorf("stat target path: %w", err)
	}
	if info.IsDir() {
		return absTarget, "", nil
	}
	scriptPath := ""
	ext := strings.ToLower(filepath.Ext(absTarget))
	if ext == ".r" || ext == ".rscript" {
		scriptPath = absTarget
	}
	return filepath.Dir(absTarget), scriptPath, nil
}

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText())
}

func usageError() error {
	return errors.New(usageText())
}

func usageText() string {
	return `rs manages a lightweight per-script R library and runs R scripts with automatic dependency installation.

Usage:
  rs version
  rs init [flags] [dir]
  rs add [flags] <package> [package...]
  rs remove [flags] <package> [package...]
  rs lock [flags] path/to/script.R
  rs r <list|install|use|which> [args]
  rs list [flags] path/to/script.R
  rs prune [flags] [path/to/script.R]
  rs shell [flags] path/to/script.R
  rs exec [flags] -e 'expression' path/to/script.R
  rs cache <dir|ls|rm> [args]
  rs run [flags] path/to/script.R [script args...]
  rs scan [flags] path/to/script.R
  rs sync [flags] path/to/script.R
  rs check [flags] path/to/script.R
  rs doctor [flags] path/to/script.R
  rs diagnose-install-error [--json] [--file path/to/error.log] [error text]
  rs toolchain template <preset|auto> [--format toml|env] [--check]
  rs toolchain detect [--json]
  rs toolchain bootstrap <preset|auto> [--json]
  rs toolchain plan [--preset auto|enva|micromamba|mamba|conda|homebrew|spack] [--phase base|full] [--json] path/to/script.R
  rs toolchain init [--preset auto|enva|micromamba|mamba|conda|homebrew|spack] [--phase base|full] path/to/script.R
  rs doctor --toolchain-only [path/to/script.R|path/to/project]

Commands:
  version print version, commit, and build metadata
  init   create a starter rs.toml for an R project, optionally seeded from a script
  add    add CRAN, Bioconductor, or custom source packages to rs.toml
  remove remove packages from rs.toml
  lock   install resolved dependencies and write or refresh the lock file
  r      list, install, select, or inspect managed R interpreters
  list   print the resolved dependency plan for a script without installing
  prune  remove stale managed library directories from the cache
  shell  open an interactive R shell in the script's managed environment
  exec   execute an inline R expression in the script's managed environment
  cache  inspect cache locations and managed library directories
  run    scan dependencies, install missing packages into rs cache, then execute the script
  scan   print detected package dependencies for a script
  sync   alias for lock
  check  validate the current environment against the lock file
  doctor inspect local prerequisites, source configuration, and lockfile presence
  diagnose-install-error  classify native-library install failures and emit structured details
  toolchain print, discover, plan, or bootstrap rootless toolchain environments without writing rs.toml

Flags for "init":
  --repo <url>              default CRAN mirror to write into rs.toml
  --cache-dir <dir>         cache directory to write into rs.toml
  --lockfile <path>         lockfile path to write into rs.toml
  --rscript <path|cmd>      default Rscript binary or path to write into rs.toml
  --r-version <version>     default R version selector to write into rs.toml
  --toolchain-preset <id>   seed rootless toolchain config for auto, enva, micromamba, mamba, conda, homebrew, or spack
  --toolchain-prefix <dir>  user-local prefix used for source-build toolchains and headers (repeatable)
  --pkg-config-path <dir>   extra pkg-config search path used during source builds (repeatable)
  --from <path>             scan an existing R script and seed rs.toml (repeatable)
  --from-dir <dir>          scan all .R/.Rscript files under a directory (repeatable)
  --include <pkg>           add an extra project-level dependency (repeatable)
  --exclude <pkg>           exclude a detected dependency from generated config (repeatable)
  --bioc-package <pkg>      add an extra project-level Bioconductor dependency (repeatable)
  --write-script-block      with one --from, write detected packages under [scripts."..."]
  --include-bundled         keep R bundled base/recommended packages in generated config
  --force                   overwrite an existing rs.toml

Flags for "toolchain template":
  --format <kind>           output format: toml or env
  --check                   verify whether the preset paths exist on this machine

Flags for "toolchain detect":
  --json                    print detected toolchain candidates as JSON

Flags for "toolchain bootstrap":
  --json                    print the bootstrap plan as JSON

Flags for "toolchain plan":
  --preset <id>             choose auto, enva, micromamba, mamba, conda, homebrew, or spack
  --phase <kind>            choose base or full package planning
  --json                    print the toolchain plan as JSON

Flags for "toolchain init":
  --preset <id>             choose auto, enva, micromamba, mamba, conda, homebrew, or spack
  --phase <kind>            choose base or full package initialization

Flags for "add":
  --bioc                    add packages to bioc_packages instead of packages
  --script <path>           add packages to one script profile instead of project defaults
  --project-dir <dir>       start config discovery from this directory
  --source <type>           set a custom source type: github, git, or local
  --github-repo <owner/repo> repository for --source github
  --url <git-url>           repository URL for --source git
  --path <local-path>       local path for --source local
  --ref <ref>               git or GitHub ref for custom source packages
  --subdir <path>           package subdirectory inside a custom source
  --host <host>             GitHub API host for GitHub Enterprise installs
  --token-env <env>         environment variable name holding a GitHub token

Flags for "remove":
  --bioc                    remove packages from bioc_packages instead of packages
  --script <path>           remove packages from one script profile instead of project defaults
  --project-dir <dir>       start config discovery from this directory

Flags for "lock":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved lock plan (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used while locking
  --bootstrap-toolchain     if no rootless toolchain is configured, try to create one automatically with a detected external manager
  --verbose                 print dependency and cache details before locking

Flags for "r":
  list                      list managed and discovered external R installations
  install <version>         install an R version with the selected manager backend; supports --method and --bootstrap-toolchain
  use <version|path>        write r_version or a resolved Rscript path to rs.toml
  which [dir|script]        print the currently selected Rscript path

Flags for "list":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved plan (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used while resolving
  --json                    print the resolved dependency plan as JSON

Flags for "prune":
  --project-dir <dir>       prune a project by scanning script files under this directory
  --dry-run                 show which managed libraries would be removed without deleting them
  --pkgstore-retention-days remove package-store entries unused for more than this many days

Flags for "shell":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved shell environment (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used for the shell
  --no-install              do not install missing packages
  --locked                  require a valid lockfile but allow installing missing packages without updating it
  --frozen                  require a valid lockfile and installed packages; do not modify dependencies
  --bootstrap-toolchain     if no rootless toolchain is configured, try to create one automatically with a detected external manager
  --verbose                 print dependency and cache details before opening the shell

Flags for "exec":
  -e, --expr <code>         R expression to execute
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved expression environment (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used for the expression
  --no-install              do not install missing packages
  --locked                  require a valid lockfile but allow installing missing packages without updating it
  --frozen                  require a valid lockfile and installed packages; do not modify dependencies
  --bootstrap-toolchain     if no rootless toolchain is configured, try to create one automatically with a detected external manager
  --verbose                 print dependency and cache details before executing the expression

Flags for "cache dir":
  accepts an optional path/to/script.R to print the cache root for that script

Flags for "cache ls":
  --project-dir <dir>       project directory used to mark active libraries
  --json                    print cache libraries as JSON
  accepts an optional path/to/script.R to mark active libraries for one script

Flags for "cache rm":
  --script <path>           script path used to resolve the cache root when removing by hash
  --project-dir <dir>       project directory used to resolve the cache root when removing by hash
  --cache-dir <dir>         explicit cache root used when removing by hash
  --dry-run                 show which managed library would be removed without deleting it
  accepts one managed library hash, package-store hash, or matching path under <cache>/

Flags for "scan":
  --json                    print detected package dependencies as JSON
  --installable             filter out R bundled base/recommended packages

Flags for "diagnose-install-error":
  --json                    print the install failure diagnostic as JSON
  --file <path>             read install error text from a file instead of a positional argument

Flags for "run":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved environment (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used for execution
  --no-install              do not install missing packages
  --locked                  require a valid lockfile but allow installing missing packages without updating it
  --frozen                  require a valid lockfile and installed packages; do not modify dependencies
  --bootstrap-toolchain     if no rootless toolchain is configured, try to create one automatically with a detected external manager
  --verbose                 print dependency and cache details before execution

Flags for "sync":
  same as "lock" (kept as a compatibility alias)

Flags for "check":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from validation (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used during validation
  --json                    print the validation report as JSON
  --bootstrap-toolchain     if no rootless toolchain is configured, try to create one automatically with a detected external manager
  --verbose                 print dependency and cache details before validation

Flags for "doctor":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN package manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor package manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from diagnosis (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used during diagnosis
  --json                    print the diagnostic report as JSON
  --bootstrap-toolchain     if no rootless toolchain is configured, try to create one automatically with a detected external manager before diagnosing
  --verbose                 print additional environment details
`
}
