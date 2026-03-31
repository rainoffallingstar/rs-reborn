package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"gr/internal/installer"
	"gr/internal/lockfile"
	"gr/internal/progresscmd"
	"gr/internal/project"
	"gr/internal/rdeps"
	"gr/internal/rmanager"
	"gr/internal/toolchainenv"
)

type RunOptions struct {
	ScriptPath         string
	ScriptArgs         []string
	ExtraDeps          []string
	ExtraBiocDeps      []string
	ExcludeDeps        []string
	Repo               string
	CacheDir           string
	RscriptPath        string
	SkipInstall        bool
	Locked             bool
	Frozen             bool
	Verbose            bool
	BootstrapToolchain bool
	Stdout             io.Writer
	Stderr             io.Writer
	AutoInstallR       bool
}

type ShellOptions struct {
	ScriptPath         string
	ExtraDeps          []string
	ExtraBiocDeps      []string
	ExcludeDeps        []string
	Repo               string
	CacheDir           string
	RscriptPath        string
	SkipInstall        bool
	Locked             bool
	Frozen             bool
	Verbose            bool
	BootstrapToolchain bool
	Stdout             io.Writer
	Stderr             io.Writer
}

type ExecOptions struct {
	ScriptPath         string
	Expression         string
	ExtraDeps          []string
	ExtraBiocDeps      []string
	ExcludeDeps        []string
	Repo               string
	CacheDir           string
	RscriptPath        string
	SkipInstall        bool
	Locked             bool
	Frozen             bool
	Verbose            bool
	BootstrapToolchain bool
	Stdout             io.Writer
	Stderr             io.Writer
}

type SyncOptions struct {
	ScriptPath         string
	ExtraDeps          []string
	ExtraBiocDeps      []string
	ExcludeDeps        []string
	Repo               string
	CacheDir           string
	RscriptPath        string
	Verbose            bool
	BootstrapToolchain bool
	Stdout             io.Writer
	Stderr             io.Writer
}

type LockOptions = SyncOptions

type CheckOptions struct {
	ScriptPath         string
	ExtraDeps          []string
	ExtraBiocDeps      []string
	ExcludeDeps        []string
	IncludeDeps        []string
	IncludeBiocDeps    []string
	Repo               string
	CacheDir           string
	RscriptPath        string
	JSON               bool
	Verbose            bool
	BootstrapToolchain bool
	Stdout             io.Writer
	Stderr             io.Writer
}

type DoctorOptions struct {
	ScriptPath         string
	ProjectDir         string
	ExtraDeps          []string
	ExtraBiocDeps      []string
	ExcludeDeps        []string
	IncludeDeps        []string
	IncludeBiocDeps    []string
	Repo               string
	CacheDir           string
	RscriptPath        string
	JSON               bool
	Strict             bool
	Quiet              bool
	SummaryOnly        bool
	ToolchainOnly      bool
	Verbose            bool
	BootstrapToolchain bool
	Stdout             io.Writer
	Stderr             io.Writer
}

type ListOptions struct {
	ScriptPath      string
	ExtraDeps       []string
	ExtraBiocDeps   []string
	ExcludeDeps     []string
	IncludeDeps     []string
	IncludeBiocDeps []string
	Repo            string
	CacheDir        string
	RscriptPath     string
	JSON            bool
	Stdout          io.Writer
	Stderr          io.Writer
}

type PruneOptions struct {
	ScriptPath string
	ProjectDir string
	DryRun     bool
	Stdout     io.Writer
	Stderr     io.Writer
}

type CacheDirOptions struct {
	ScriptPath string
	Stdout     io.Writer
}

type CacheListOptions struct {
	ScriptPath string
	ProjectDir string
	JSON       bool
	Stdout     io.Writer
	Stderr     io.Writer
}

type CacheRemoveOptions struct {
	Target     string
	ScriptPath string
	ProjectDir string
	CacheDir   string
	DryRun     bool
	Stdout     io.Writer
	Stderr     io.Writer
}

type ExitError struct {
	Code int
}

func (e ExitError) Error() string {
	return fmt.Sprintf("Rscript exited with code %d", e.Code)
}

type ReportedError struct {
	Err error
}

func (e ReportedError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e ReportedError) Unwrap() error {
	return e.Err
}

type ResolvedEnvironment struct {
	ScriptPath        string
	ScriptArgs        []string
	Repo              string
	CacheRoot         string
	LibraryPath       string
	BootstrapPath     string
	LockfilePath      string
	Interpreter       string
	Runtime           RuntimeMetadata
	DetectedDeps      []string
	CRANDeps          []string
	BiocDeps          []string
	SourceDeps        map[string]project.SourceSpec
	ToolchainPrefixes []string
	PkgConfigPath     []string
	ProjectConfig     project.Config
	ScriptConfig      project.ResolvedScriptConfig
	Verbose           bool
	Stdout            io.Writer
	Stderr            io.Writer
}

type RuntimeMetadata struct {
	Interpreter     string
	RVersion        string
	Platform        string
	Arch            string
	OS              string
	PackageType     string
	InterpreterKind string
}

type sourceMetadata struct {
	Source                string
	SourceHost            string
	SourceLocation        string
	SourceRef             string
	SourceCommit          string
	SourceSubdir          string
	SourceFingerprint     string
	SourceFingerprintKind string
}

const (
	localSourceFingerprintKindFile = "file_sha256"
	localSourceFingerprintKindDir  = "dir_tree_sha256"
	localSourceFingerprintMissing  = "missing"
	localSourceFingerprintError    = "unavailable"
)

type ValidationError struct {
	Mode         ValidationMode
	Kind         ValidationKind
	ScriptPath   string
	LockfilePath string
	LibraryPath  string
	Issues       []string
}

type ValidationMode string

var nativeInstall = func(req installer.Request) error {
	return installer.Install(req)
}

var nativeValidatePlan = func(req installer.Request) error {
	return installer.Validate(req)
}

var resolveManagedRscript = rmanager.ResolveVersionOrPath
var resolveCurrentManagedRscript = rmanager.CurrentManagedRscript
var resolveSelectedRscript = resolveConfiguredInterpreterPath

var ensureManagedRscript = func(selected string, stderr io.Writer) (string, error) {
	return rmanager.EnsureInstalledRscript(selected, io.Discard, stderr)
}

var rManagerBootstrapAdvice = rmanager.BootstrapAdvice
var rManagerBootstrapAdviceFor = rmanager.BootstrapAdviceFor
var autoInstallR = rmanager.AutoInstallREnabled

type interpreterSelection struct {
	Selected     string
	Requested    string
	RequestedVer string
	Interpreter  string
	Runtime      RuntimeMetadata
	Issue        error
}

var bootstrapInstall = func(env ResolvedEnvironment, backend string) error {
	cmd := exec.Command(env.Interpreter, "-e", `source(Sys.getenv("RS_BOOTSTRAP_FILE")); rs_bootstrap()`)
	cmd.Stdin = os.Stdin
	cmd.Dir = filepath.Dir(env.ScriptPath)
	cmd.Env = append(runtimeEnv(env, true), "RS_BOOTSTRAP_AUTORUN=false", "RS_INSTALL_BACKEND="+backend)

	label := fmt.Sprintf("installing packages via %s backend", backend)
	if err := progresscmd.Run(cmd, label, env.Stderr, env.Stderr); err != nil {
		return fmt.Errorf("install packages: %w", err)
	}
	return nil
}

const (
	ValidationModeGeneric ValidationMode = ""
	ValidationModeLocked  ValidationMode = "locked"
	ValidationModeFrozen  ValidationMode = "frozen"
	ValidationModeCheck   ValidationMode = "check"
)

type ValidationKind string

const (
	ValidationKindGeneric   ValidationKind = ""
	ValidationKindMissing   ValidationKind = "missing_lockfile"
	ValidationKindInputs    ValidationKind = "inputs"
	ValidationKindInstalled ValidationKind = "installed"
)

func (e ValidationError) Error() string {
	header := "lockfile validation failed"
	switch e.Mode {
	case ValidationModeLocked:
		header = "locked mode validation failed"
	case ValidationModeFrozen:
		header = "frozen mode validation failed"
	case ValidationModeCheck:
		header = "check failed"
	}

	if len(e.Issues) == 0 {
		return fmt.Sprintf("%s: %s", header, e.LockfilePath)
	}

	lines := []string{fmt.Sprintf("%s: %s", header, e.LockfilePath)}
	if context := e.contextLine(); context != "" {
		lines = append(lines, context)
	}
	for _, issue := range e.Issues {
		lines = append(lines, "  - "+issue)
	}
	lines = append(lines, e.summaryLines()...)
	if hint := e.hintLine(); hint != "" {
		lines = append(lines, hint)
	}
	return strings.Join(lines, "\n")
}

func (e ValidationError) summaryLines() []string {
	if len(e.Issues) == 0 {
		return nil
	}
	switch e.Kind {
	case ValidationKindInputs:
		return inputSummaryLines(e.Issues)
	case ValidationKindInstalled:
		return installedSummaryLines(e.Issues)
	default:
		return nil
	}
}

func (e ValidationError) contextLine() string {
	switch e.Kind {
	case ValidationKindMissing:
		switch e.Mode {
		case ValidationModeLocked, ValidationModeFrozen:
			return "the requested execution mode requires an existing lockfile"
		case ValidationModeCheck:
			return "check requires an existing lockfile to validate against"
		}
	case ValidationKindInputs:
		switch e.Mode {
		case ValidationModeLocked:
			return "the current script, config, or runtime no longer matches the lockfile inputs required by --locked"
		case ValidationModeFrozen:
			return "the current script, config, or runtime no longer matches the frozen lockfile inputs"
		case ValidationModeCheck:
			return "the current script, config, or runtime does not match the lockfile inputs"
		}
	case ValidationKindInstalled:
		switch e.Mode {
		case ValidationModeLocked:
			return "the managed library does not match the locked dependency set"
		case ValidationModeFrozen:
			return "the managed library does not match the frozen dependency set"
		case ValidationModeCheck:
			return "the managed library does not match the lockfile"
		}
	}
	return ""
}

func (e ValidationError) hintLine() string {
	if e.ScriptPath == "" {
		return ""
	}

	switch e.Kind {
	case ValidationKindMissing, ValidationKindInputs:
		return fmt.Sprintf("hint: run `rs lock %s` to refresh the lockfile, or run without --locked/--frozen if you intend to change dependencies", e.ScriptPath)
	case ValidationKindInstalled:
		if e.LibraryPath != "" {
			return fmt.Sprintf("hint: run `rs cache rm %s` to drop the stale managed library, then rerun the command or run `rs lock %s` if the dependency state intentionally changed", e.LibraryPath, e.ScriptPath)
		}
		return fmt.Sprintf("hint: remove the stale managed library, then rerun the command or run `rs lock %s` if the dependency state intentionally changed", e.ScriptPath)
	}
	return ""
}

func installedSummaryLines(issues []string) []string {
	details := buildInstalledIssueDetails(issues)
	if len(details) == 0 {
		return nil
	}

	missing := []string{}
	version := []string{}
	source := []string{}
	other := []string{}

	for _, detail := range details {
		switch detail.Kind {
		case "missing_package":
			if detail.Package != "" {
				missing = append(missing, detail.Package)
			}
		case "version_mismatch":
			if detail.Package != "" {
				version = append(version, detail.Package)
			}
		case "source_mismatch":
			label := detail.Package
			if detail.Field != "" {
				if label == "" {
					label = detail.Field
				} else {
					label += "(" + detail.Field + ")"
				}
			}
			if label != "" {
				source = append(source, label)
			}
		default:
			other = append(other, detail.Message)
		}
	}

	lines := []string{}
	if values := compactStrings(missing); len(values) > 0 {
		lines = append(lines, "summary: missing packages = "+strings.Join(values, ", "))
	}
	if values := compactStrings(version); len(values) > 0 {
		lines = append(lines, "summary: version mismatches = "+strings.Join(values, ", "))
	}
	if values := compactStrings(source); len(values) > 0 {
		lines = append(lines, "summary: source mismatches = "+strings.Join(values, ", "))
	}
	if values := compactStrings(other); len(values) > 0 {
		if len(values) == 1 {
			lines = append(lines, "summary: other installed mismatch = "+values[0])
		} else {
			lines = append(lines, fmt.Sprintf("summary: other installed mismatches = %d", len(values)))
		}
	}
	return lines
}

func inputSummaryLines(issues []string) []string {
	scriptConfig := []string{}
	runtime := []string{}
	deps := []string{}
	sources := []string{}
	other := []string{}

	for _, issue := range issues {
		switch {
		case strings.HasPrefix(issue, "script mismatch:"):
			scriptConfig = append(scriptConfig, "script path")
		case strings.HasPrefix(issue, "script changed after lockfile"):
			scriptConfig = append(scriptConfig, "script timestamp")
		case strings.HasPrefix(issue, "project config changed after lockfile"):
			scriptConfig = append(scriptConfig, "project config timestamp")
		case strings.HasPrefix(issue, "repository mismatch:"):
			runtime = append(runtime, "repository")
		case strings.HasPrefix(issue, "library mismatch:"):
			runtime = append(runtime, "managed library")
		case strings.HasPrefix(issue, "interpreter mismatch:"):
			runtime = append(runtime, "interpreter")
		case strings.HasPrefix(issue, "R version mismatch:"):
			runtime = append(runtime, "R version")
		case strings.HasPrefix(issue, "platform mismatch:"):
			runtime = append(runtime, "platform")
		case strings.HasPrefix(issue, "arch mismatch:"):
			runtime = append(runtime, "arch")
		case strings.HasPrefix(issue, "os mismatch:"):
			runtime = append(runtime, "os")
		case strings.HasPrefix(issue, "package type mismatch:"):
			runtime = append(runtime, "package type")
		case strings.HasPrefix(issue, "missing package in lockfile: "):
			deps = append(deps, strings.TrimPrefix(issue, "missing package in lockfile: "))
		case strings.HasPrefix(issue, "lockfile contains unexpected package: "):
			deps = append(deps, strings.TrimPrefix(issue, "lockfile contains unexpected package: "))
		case strings.HasPrefix(issue, "source type mismatch for "),
			strings.HasPrefix(issue, "source host mismatch for "),
			strings.HasPrefix(issue, "source location mismatch for "),
			strings.HasPrefix(issue, "source ref mismatch for "),
			strings.HasPrefix(issue, "source subdir mismatch for "),
			strings.HasPrefix(issue, "source fingerprint kind mismatch for "),
			strings.HasPrefix(issue, "source fingerprint mismatch for "):
			if pkg, field := inputSourceMismatchLabel(issue); pkg != "" {
				label := pkg
				if field != "" {
					label += "(" + field + ")"
				}
				sources = append(sources, label)
			} else {
				sources = append(sources, issue)
			}
		default:
			other = append(other, issue)
		}
	}

	lines := []string{}
	if values := compactStrings(scriptConfig); len(values) > 0 {
		lines = append(lines, "summary: script/config drift = "+strings.Join(values, ", "))
	}
	if values := compactStrings(runtime); len(values) > 0 {
		lines = append(lines, "summary: runtime drift = "+strings.Join(values, ", "))
	}
	if values := compactStrings(deps); len(values) > 0 {
		lines = append(lines, "summary: dependency set drift = "+strings.Join(values, ", "))
	}
	if values := compactStrings(sources); len(values) > 0 {
		lines = append(lines, "summary: source config drift = "+strings.Join(values, ", "))
	}
	if values := compactStrings(other); len(values) > 0 {
		if len(values) == 1 {
			lines = append(lines, "summary: other input mismatch = "+values[0])
		} else {
			lines = append(lines, fmt.Sprintf("summary: other input mismatches = %d", len(values)))
		}
	}
	return lines
}

func inputSourceMismatchLabel(issue string) (string, string) {
	prefixes := []struct {
		prefix string
		field  string
	}{
		{prefix: "source type mismatch for ", field: "source_type"},
		{prefix: "source host mismatch for ", field: "source_host"},
		{prefix: "source location mismatch for ", field: "source_location"},
		{prefix: "source ref mismatch for ", field: "source_ref"},
		{prefix: "source subdir mismatch for ", field: "source_subdir"},
		{prefix: "source fingerprint kind mismatch for ", field: "source_fingerprint_kind"},
		{prefix: "source fingerprint mismatch for ", field: "source_fingerprint"},
	}
	for _, candidate := range prefixes {
		if rest, ok := strings.CutPrefix(issue, candidate.prefix); ok {
			pkg, _, found := strings.Cut(rest, ":")
			if found {
				return pkg, candidate.field
			}
		}
	}
	return "", ""
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

type DoctorError struct {
	Issues []string
	Code   int
}

func (e DoctorError) Error() string {
	header := "doctor found blocking issues:"
	if e.Code == 2 {
		header = "doctor strict mode failed:"
	}
	lines := []string{header}
	for _, issue := range e.Issues {
		lines = append(lines, "  - "+issue)
	}
	return strings.Join(lines, "\n")
}

type validationContext struct {
	Lockfile lockfile.File
	Runtime  RuntimeMetadata
}

type dependencyPlan struct {
	ScriptPath        string
	ProjectPath       string
	ScriptKey         string
	RequestedR        string
	RequestedRVersion string
	RscriptPath       string
	RscriptIssue      string
	Repo              string
	CacheRoot         string
	LockfilePath      string
	LibraryPath       string
	DetectedDeps      []string
	CRANDeps          []string
	BiocDeps          []string
	IncludedCRAN      []string
	IncludedBioc      []string
	ExcludedDeps      []string
	SourceDeps        map[string]project.SourceSpec
	ToolchainPrefixes []string
	PkgConfigPath     []string
	Runtime           RuntimeMetadata
}

type ListReport struct {
	Script         string       `json:"script"`
	ProjectConfig  string       `json:"project_config,omitempty"`
	ScriptProfile  string       `json:"script_profile,omitempty"`
	RscriptPath    string       `json:"rscript_path,omitempty"`
	RscriptIssue   string       `json:"rscript_issue,omitempty"`
	Repo           string       `json:"repo"`
	Lockfile       string       `json:"lockfile"`
	ManagedLibrary string       `json:"managed_library"`
	CacheRoot      string       `json:"cache_root"`
	DetectedDeps   []string     `json:"detected_packages"`
	CRANDeps       []string     `json:"cran_packages"`
	BiocDeps       []string     `json:"bioc_packages"`
	IncludedCRAN   []string     `json:"included_cran_packages"`
	IncludedBioc   []string     `json:"included_bioc_packages"`
	ExcludedDeps   []string     `json:"excluded_packages"`
	CustomSources  []ListSource `json:"custom_sources"`
}

type ListSource struct {
	Package  string `json:"package"`
	Type     string `json:"type"`
	Host     string `json:"host,omitempty"`
	Repo     string `json:"repo,omitempty"`
	URL      string `json:"url,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
	Subdir   string `json:"subdir,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

type pruneSummary struct {
	CacheRoot string
	Kept      []string
	Removed   []string
}

type CacheListReport struct {
	CacheRoot  string         `json:"cache_root"`
	Libraries  []CacheLibrary `json:"libraries"`
	Scope      string         `json:"scope,omitempty"`
	ProjectDir string         `json:"project_dir,omitempty"`
}

type CacheLibrary struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type CheckReport struct {
	Script                   string                 `json:"script"`
	ProjectConfig            string                 `json:"project_config,omitempty"`
	ScriptProfile            string                 `json:"script_profile,omitempty"`
	RscriptPath              string                 `json:"rscript_path,omitempty"`
	Repo                     string                 `json:"repo"`
	Lockfile                 string                 `json:"lockfile"`
	ManagedLibrary           string                 `json:"managed_library"`
	CacheRoot                string                 `json:"cache_root"`
	DetectedDeps             []string               `json:"detected_packages"`
	CRANDeps                 []string               `json:"cran_packages"`
	BiocDeps                 []string               `json:"bioc_packages"`
	IncludedCRAN             []string               `json:"included_cran_packages"`
	IncludedBioc             []string               `json:"included_bioc_packages"`
	ExcludedDeps             []string               `json:"excluded_packages"`
	PlanningIssues           []string               `json:"planning_issues"`
	InputIssues              []string               `json:"input_issues"`
	InstalledIssues          []string               `json:"installed_issues"`
	InstalledMissingPackages []string               `json:"installed_missing_packages"`
	InstalledVersionIssues   []string               `json:"installed_version_issues"`
	InstalledSourceIssues    []string               `json:"installed_source_issues"`
	InstalledOtherIssues     []string               `json:"installed_other_issues"`
	PlanningIssueDetails     []InstalledIssueDetail `json:"planning_issue_details"`
	InstalledIssueDetails    []InstalledIssueDetail `json:"installed_issue_details"`
	Valid                    bool                   `json:"valid"`
	Issues                   []string               `json:"issues"`
}

type InstalledIssueDetail struct {
	Kind           string   `json:"kind"`
	Package        string   `json:"package,omitempty"`
	Field          string   `json:"field,omitempty"`
	Message        string   `json:"message"`
	DependencyPath []string `json:"dependency_path,omitempty"`
	Constraint     string   `json:"constraint,omitempty"`
	Selected       string   `json:"selected_version,omitempty"`
	RequiredBy     string   `json:"required_by,omitempty"`
}

type DoctorReport struct {
	Script            string              `json:"script"`
	ProjectConfig     string              `json:"project_config,omitempty"`
	ScriptProfile     string              `json:"script_profile,omitempty"`
	RscriptPath       string              `json:"rscript_path,omitempty"`
	GitPath           string              `json:"git_path,omitempty"`
	NeedsGit          bool                `json:"needs_git"`
	Repo              string              `json:"repo"`
	Lockfile          string              `json:"lockfile"`
	ManagedLibrary    string              `json:"managed_library"`
	CacheRoot         string              `json:"cache_root"`
	DetectedDeps      []string            `json:"detected_packages"`
	CRANDeps          []string            `json:"cran_packages"`
	BiocDeps          []string            `json:"bioc_packages"`
	IncludedCRAN      []string            `json:"included_cran_packages"`
	IncludedBioc      []string            `json:"included_bioc_packages"`
	ExcludedDeps      []string            `json:"excluded_packages"`
	ToolchainPrefixes []string            `json:"toolchain_prefixes"`
	PkgConfigPath     []string            `json:"pkg_config_path"`
	ToolchainPath     []string            `json:"toolchain_path"`
	ToolchainCPPFLAGS []string            `json:"toolchain_cppflags"`
	ToolchainLDFLAGS  []string            `json:"toolchain_ldflags"`
	ToolchainPkgPath  []string            `json:"toolchain_pkg_config_path"`
	CustomSources     []string            `json:"custom_sources"`
	Warnings          []string            `json:"warnings"`
	Errors            []string            `json:"errors"`
	SetupErrors       []string            `json:"setup_errors"`
	SourceErrors      []string            `json:"source_errors"`
	NetworkErrors     []string            `json:"network_errors"`
	RuntimeErrors     []string            `json:"runtime_errors"`
	OtherErrors       []string            `json:"other_errors"`
	LockWarnings      []string            `json:"lock_warnings"`
	CacheWarnings     []string            `json:"cache_warnings"`
	OtherWarnings     []string            `json:"other_warnings"`
	ErrorDetails      []DoctorIssueDetail `json:"error_details"`
	WarningDetails    []DoctorIssueDetail `json:"warning_details"`
	SystemHints       []string            `json:"system_hints"`
	SystemHintDetails []SystemHintDetail  `json:"system_hint_details"`
	NextSteps         []NextStepDetail    `json:"next_steps"`
	Status            string              `json:"status"`
	Summary           DoctorSummary       `json:"summary"`
	OK                bool                `json:"ok"`
}

type DoctorSummary struct {
	ErrorCount            int `json:"error_count"`
	WarningCount          int `json:"warning_count"`
	SystemHintCount       int `json:"system_hint_count"`
	NextStepCount         int `json:"next_step_count"`
	BlockingNextStepCount int `json:"blocking_next_step_count"`
	SetupErrorCount       int `json:"setup_error_count"`
	SourceErrorCount      int `json:"source_error_count"`
	NetworkErrorCount     int `json:"network_error_count"`
	RuntimeErrorCount     int `json:"runtime_error_count"`
	OtherErrorCount       int `json:"other_error_count"`
	LockWarningCount      int `json:"lock_warning_count"`
	CacheWarningCount     int `json:"cache_warning_count"`
	OtherWarningCount     int `json:"other_warning_count"`
}

type DoctorIssueDetail struct {
	Category       string   `json:"category"`
	Kind           string   `json:"kind"`
	Message        string   `json:"message"`
	Package        string   `json:"package,omitempty"`
	Path           string   `json:"path,omitempty"`
	EnvVar         string   `json:"env_var,omitempty"`
	DependencyPath []string `json:"dependency_path,omitempty"`
	Constraint     string   `json:"constraint,omitempty"`
	Selected       string   `json:"selected_version,omitempty"`
	RequiredBy     string   `json:"required_by,omitempty"`
}

type SystemHintDetail struct {
	Category string   `json:"category"`
	Packages []string `json:"packages"`
	Message  string   `json:"message"`
}

type NextStepDetail struct {
	Category string `json:"category"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Command  string `json:"command,omitempty"`
	Note     string `json:"note,omitempty"`
	Preset   string `json:"preset,omitempty"`
	Blocking bool   `json:"blocking"`
}

func Run(opts RunOptions) error {
	env, err := resolveEnvironment(RunOptions{
		ScriptPath:    opts.ScriptPath,
		ScriptArgs:    opts.ScriptArgs,
		ExtraDeps:     opts.ExtraDeps,
		ExtraBiocDeps: opts.ExtraBiocDeps,
		ExcludeDeps:   opts.ExcludeDeps,
		Repo:          opts.Repo,
		CacheDir:      opts.CacheDir,
		RscriptPath:   opts.RscriptPath,
		Frozen:        opts.Frozen,
		Locked:        opts.Locked,
		Verbose:       opts.Verbose,
		Stdout:        opts.Stdout,
		Stderr:        opts.Stderr,
		AutoInstallR:  true,
	})
	if err != nil {
		return err
	}
	defer os.Remove(env.BootstrapPath)
	if opts.BootstrapToolchain {
		if err := maybeBootstrapResolvedEnvironment(&env); err != nil {
			return err
		}
	}

	if opts.Frozen {
		if err := ValidateLockfile(env, ValidationModeFrozen); err != nil {
			return err
		}
	} else if opts.Locked {
		validation, err := ValidateLockfileInputs(env, ValidationModeLocked)
		if err != nil {
			return err
		}
		if !opts.SkipInstall {
			if err := EnsureInstalled(env); err != nil {
				return err
			}
		}
		if err := ValidateInstalledPackages(env, validation.Lockfile, ValidationModeLocked); err != nil {
			return err
		}
	} else if !opts.SkipInstall {
		if err := EnsureInstalled(env); err != nil {
			return err
		}
		if err := WriteLockfile(env); err != nil {
			return err
		}
	}

	cmdArgs := append([]string{env.ScriptPath}, env.ScriptArgs...)
	cmd := exec.Command(env.Interpreter, cmdArgs...)
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	cmd.Stdin = os.Stdin
	cmd.Dir = filepath.Dir(env.ScriptPath)
	cmd.Env = runtimeEnv(env, false)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitError{Code: exitErr.ExitCode()}
		}
		return fmt.Errorf("run Rscript: %w", err)
	}

	return nil
}

func Shell(opts ShellOptions) error {
	env, err := resolveEnvironment(RunOptions{
		ScriptPath:    opts.ScriptPath,
		ExtraDeps:     opts.ExtraDeps,
		ExtraBiocDeps: opts.ExtraBiocDeps,
		ExcludeDeps:   opts.ExcludeDeps,
		Repo:          opts.Repo,
		CacheDir:      opts.CacheDir,
		RscriptPath:   opts.RscriptPath,
		Frozen:        opts.Frozen,
		Locked:        opts.Locked,
		Verbose:       opts.Verbose,
		Stdout:        opts.Stdout,
		Stderr:        opts.Stderr,
		AutoInstallR:  true,
	})
	if err != nil {
		return err
	}
	defer os.Remove(env.BootstrapPath)
	if opts.BootstrapToolchain {
		if err := maybeBootstrapResolvedEnvironment(&env); err != nil {
			return err
		}
	}

	if opts.Frozen {
		if err := ValidateLockfile(env, ValidationModeFrozen); err != nil {
			return err
		}
	} else if opts.Locked {
		validation, err := ValidateLockfileInputs(env, ValidationModeLocked)
		if err != nil {
			return err
		}
		if !opts.SkipInstall {
			if err := EnsureInstalled(env); err != nil {
				return err
			}
		}
		if err := ValidateInstalledPackages(env, validation.Lockfile, ValidationModeLocked); err != nil {
			return err
		}
	} else if !opts.SkipInstall {
		if err := EnsureInstalled(env); err != nil {
			return err
		}
		if err := WriteLockfile(env); err != nil {
			return err
		}
	}

	interpreter, err := resolveRShellPath(env.Interpreter)
	if err != nil {
		return err
	}

	cmd := exec.Command(interpreter, "--quiet", "--no-save")
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	cmd.Stdin = os.Stdin
	cmd.Dir = filepath.Dir(env.ScriptPath)
	cmd.Env = runtimeEnv(env, false)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitError{Code: exitErr.ExitCode()}
		}
		return fmt.Errorf("run R shell: %w", err)
	}
	return nil
}

func Exec(opts ExecOptions) error {
	if strings.TrimSpace(opts.Expression) == "" {
		return fmt.Errorf("R expression is required")
	}

	env, err := resolveEnvironment(RunOptions{
		ScriptPath:    opts.ScriptPath,
		ExtraDeps:     opts.ExtraDeps,
		ExtraBiocDeps: opts.ExtraBiocDeps,
		ExcludeDeps:   opts.ExcludeDeps,
		Repo:          opts.Repo,
		CacheDir:      opts.CacheDir,
		RscriptPath:   opts.RscriptPath,
		Frozen:        opts.Frozen,
		Locked:        opts.Locked,
		Verbose:       opts.Verbose,
		Stdout:        opts.Stdout,
		Stderr:        opts.Stderr,
		AutoInstallR:  true,
	})
	if err != nil {
		return err
	}
	defer os.Remove(env.BootstrapPath)
	if opts.BootstrapToolchain {
		if err := maybeBootstrapResolvedEnvironment(&env); err != nil {
			return err
		}
	}

	if opts.Frozen {
		if err := ValidateLockfile(env, ValidationModeFrozen); err != nil {
			return err
		}
	} else if opts.Locked {
		validation, err := ValidateLockfileInputs(env, ValidationModeLocked)
		if err != nil {
			return err
		}
		if !opts.SkipInstall {
			if err := EnsureInstalled(env); err != nil {
				return err
			}
		}
		if err := ValidateInstalledPackages(env, validation.Lockfile, ValidationModeLocked); err != nil {
			return err
		}
	} else if !opts.SkipInstall {
		if err := EnsureInstalled(env); err != nil {
			return err
		}
		if err := WriteLockfile(env); err != nil {
			return err
		}
	}

	cmd := exec.Command(env.Interpreter, "-e", opts.Expression)
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	cmd.Stdin = os.Stdin
	cmd.Dir = filepath.Dir(env.ScriptPath)
	cmd.Env = runtimeEnv(env, false)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitError{Code: exitErr.ExitCode()}
		}
		return fmt.Errorf("run R expression: %w", err)
	}
	return nil
}

func Sync(opts SyncOptions) error {
	env, err := resolveEnvironment(RunOptions{
		ScriptPath:    opts.ScriptPath,
		ExtraDeps:     opts.ExtraDeps,
		ExtraBiocDeps: opts.ExtraBiocDeps,
		ExcludeDeps:   opts.ExcludeDeps,
		Repo:          opts.Repo,
		CacheDir:      opts.CacheDir,
		RscriptPath:   opts.RscriptPath,
		Verbose:       opts.Verbose,
		Stdout:        opts.Stdout,
		Stderr:        opts.Stderr,
		AutoInstallR:  true,
	})
	if err != nil {
		return err
	}
	defer os.Remove(env.BootstrapPath)
	if opts.BootstrapToolchain {
		if err := maybeBootstrapResolvedEnvironment(&env); err != nil {
			return err
		}
	}

	if err := EnsureInstalled(env); err != nil {
		return err
	}
	if err := WriteLockfile(env); err != nil {
		return err
	}
	return nil
}

func Lock(opts LockOptions) error {
	return Sync(SyncOptions(opts))
}

func List(opts ListOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	plan, err := resolveDependencyPlanWithProgress(opts.ScriptPath, opts.ExtraDeps, opts.ExtraBiocDeps, opts.ExcludeDeps, opts.Repo, opts.CacheDir, opts.RscriptPath, opts.Stderr)
	if err != nil {
		return err
	}
	if plan.RscriptIssue != "" && !listCanProceedWithoutRuntime(plan.RscriptIssue, plan.RequestedRVersion) {
		return fmt.Errorf("%s", plan.RscriptIssue)
	}
	report := buildListReport(plan, opts)
	normalizeListReport(&report)

	if opts.JSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal list report: %w", err)
		}
		fmt.Fprintln(opts.Stdout, string(data))
		return nil
	}

	fmt.Fprintf(opts.Stdout, "script: %s\n", report.Script)
	if report.ProjectConfig != "" {
		fmt.Fprintf(opts.Stdout, "project config: %s\n", report.ProjectConfig)
	}
	if report.ScriptProfile != "" {
		fmt.Fprintf(opts.Stdout, "script profile: %s\n", report.ScriptProfile)
	}
	fmt.Fprintf(opts.Stdout, "repo: %s\n", report.Repo)
	fmt.Fprintf(opts.Stdout, "lockfile: %s\n", report.Lockfile)
	fmt.Fprintf(opts.Stdout, "managed library: %s\n", report.ManagedLibrary)
	fmt.Fprintf(opts.Stdout, "cache root: %s\n", report.CacheRoot)
	if len(report.DetectedDeps) == 0 {
		fmt.Fprintln(opts.Stdout, "detected packages: <none>")
	} else {
		fmt.Fprintf(opts.Stdout, "detected packages: %s\n", strings.Join(report.DetectedDeps, ", "))
	}
	if len(report.CRANDeps) == 0 {
		fmt.Fprintln(opts.Stdout, "cran packages: <none>")
	} else {
		fmt.Fprintf(opts.Stdout, "cran packages: %s\n", strings.Join(report.CRANDeps, ", "))
	}
	if len(report.BiocDeps) == 0 {
		fmt.Fprintln(opts.Stdout, "bioconductor packages: <none>")
	} else {
		fmt.Fprintf(opts.Stdout, "bioconductor packages: %s\n", strings.Join(report.BiocDeps, ", "))
	}
	if len(report.IncludedCRAN) == 0 && len(report.IncludedBioc) == 0 {
		fmt.Fprintln(opts.Stdout, "included packages: <none>")
	} else {
		parts := []string{}
		if len(report.IncludedCRAN) > 0 {
			parts = append(parts, "CRAN="+strings.Join(report.IncludedCRAN, ", "))
		}
		if len(report.IncludedBioc) > 0 {
			parts = append(parts, "Bioconductor="+strings.Join(report.IncludedBioc, ", "))
		}
		fmt.Fprintf(opts.Stdout, "included packages: %s\n", strings.Join(parts, " | "))
	}
	if len(report.ExcludedDeps) == 0 {
		fmt.Fprintln(opts.Stdout, "excluded packages: <none>")
	} else {
		fmt.Fprintf(opts.Stdout, "excluded packages: %s\n", strings.Join(report.ExcludedDeps, ", "))
	}
	if len(report.CustomSources) == 0 {
		fmt.Fprintln(opts.Stdout, "custom sources: <none>")
		return nil
	}

	fmt.Fprintf(opts.Stdout, "custom sources: %s\n", strings.Join(sourceSummary(plan.SourceDeps), ", "))
	for _, source := range report.CustomSources {
		fmt.Fprintf(opts.Stdout, "source %s: type=%s", source.Package, source.Type)
		switch source.Type {
		case "github":
			if source.Repo != "" {
				fmt.Fprintf(opts.Stdout, " repo=%s", source.Repo)
			}
			if source.Host != "" {
				fmt.Fprintf(opts.Stdout, " host=%s", source.Host)
			}
			if source.Ref != "" {
				fmt.Fprintf(opts.Stdout, " ref=%s", source.Ref)
			}
			if source.Subdir != "" {
				fmt.Fprintf(opts.Stdout, " subdir=%s", source.Subdir)
			}
			if source.TokenEnv != "" {
				fmt.Fprintf(opts.Stdout, " token_env=%s", source.TokenEnv)
			}
		case "git":
			if source.URL != "" {
				fmt.Fprintf(opts.Stdout, " url=%s", source.URL)
			}
			if source.Ref != "" {
				fmt.Fprintf(opts.Stdout, " ref=%s", source.Ref)
			}
			if source.Subdir != "" {
				fmt.Fprintf(opts.Stdout, " subdir=%s", source.Subdir)
			}
		case "local":
			if source.Path != "" {
				fmt.Fprintf(opts.Stdout, " path=%s", source.Path)
			}
		}
		fmt.Fprintln(opts.Stdout)
	}
	return nil
}

func listCanProceedWithoutRuntime(issue, requestedVersion string) bool {
	if strings.Contains(issue, "is not available") {
		return true
	}
	if requestedVersion != "" {
		return false
	}
	return strings.Contains(issue, "inspect R runtime:")
}

func Prune(opts PruneOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	keepByCacheRoot, scopeLabel, projectRoot, projectConfigPath, scriptCount, err := collectKeepByCacheRoot(opts.ScriptPath, opts.ProjectDir)
	if err != nil {
		return err
	}
	if opts.ScriptPath != "" {
		fmt.Fprintf(opts.Stdout, "[info] prune scope: %s\n", scopeLabel)
	} else {
		fmt.Fprintf(opts.Stdout, "[info] prune scope: %s\n", scopeLabel)
		fmt.Fprintf(opts.Stdout, "[info] project config: %s\n", projectConfigPath)
		fmt.Fprintf(opts.Stdout, "[info] discovered scripts: %d\n", scriptCount)
		_ = projectRoot
	}

	if len(keepByCacheRoot) == 0 {
		fmt.Fprintln(opts.Stdout, "[ok] no managed libraries to prune")
		return nil
	}

	cacheRoots := make([]string, 0, len(keepByCacheRoot))
	for cacheRoot := range keepByCacheRoot {
		cacheRoots = append(cacheRoots, cacheRoot)
	}
	slices.Sort(cacheRoots)

	totalRemoved := 0
	for _, cacheRoot := range cacheRoots {
		summary, err := pruneCacheRoot(cacheRoot, keepByCacheRoot[cacheRoot], opts.DryRun)
		if err != nil {
			return err
		}
		fmt.Fprintf(opts.Stdout, "[info] cache root: %s\n", summary.CacheRoot)
		for _, kept := range summary.Kept {
			fmt.Fprintf(opts.Stdout, "[keep] %s\n", kept)
		}
		for _, removed := range summary.Removed {
			if opts.DryRun {
				fmt.Fprintf(opts.Stdout, "[dry-run] would remove %s\n", removed)
			} else {
				fmt.Fprintf(opts.Stdout, "[remove] %s\n", removed)
			}
			totalRemoved++
		}
	}

	if totalRemoved == 0 {
		if opts.DryRun {
			fmt.Fprintln(opts.Stdout, "[ok] prune found nothing to remove")
		} else {
			fmt.Fprintln(opts.Stdout, "[ok] prune removed nothing")
		}
		return nil
	}
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[ok] prune would remove %d managed librar", totalRemoved)
	} else {
		fmt.Fprintf(opts.Stdout, "[ok] prune removed %d managed librar", totalRemoved)
	}
	if totalRemoved == 1 {
		fmt.Fprintln(opts.Stdout, "y")
	} else {
		fmt.Fprintln(opts.Stdout, "ies")
	}
	return nil
}

func CacheDir(opts CacheDirOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.ScriptPath != "" {
		plan, err := resolveDependencyPlan(opts.ScriptPath, nil, nil, nil, "", "", "")
		if err != nil {
			return err
		}
		fmt.Fprintln(opts.Stdout, plan.CacheRoot)
		return nil
	}
	fmt.Fprintln(opts.Stdout, predictedCacheRoot(""))
	return nil
}

func CacheList(opts CacheListOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	var (
		cacheRoot     string
		active        map[string]struct{}
		scopeLabel    string
		projectRoot   string
		projectConfig string
	)

	if opts.ScriptPath != "" || opts.ProjectDir != "" {
		keepByCacheRoot, scope, root, cfgPath, _, err := collectKeepByCacheRoot(opts.ScriptPath, opts.ProjectDir)
		if err != nil {
			return err
		}
		scopeLabel = scope
		projectRoot = root
		projectConfig = cfgPath
		if len(keepByCacheRoot) == 0 {
			cacheRoot = predictedCacheRoot("")
			active = map[string]struct{}{}
		} else {
			cacheRoots := make([]string, 0, len(keepByCacheRoot))
			for root := range keepByCacheRoot {
				cacheRoots = append(cacheRoots, root)
			}
			slices.Sort(cacheRoots)
			cacheRoot = cacheRoots[0]
			active = keepByCacheRoot[cacheRoot]
		}
	} else {
		cacheRoot = predictedCacheRoot("")
		active = nil
	}

	libs, err := listManagedLibraries(cacheRoot, active)
	if err != nil {
		return err
	}
	report := CacheListReport{
		CacheRoot: cacheRoot,
		Libraries: libs,
	}
	if scopeLabel != "" {
		report.Scope = scopeLabel
	}
	if projectRoot != "" {
		report.ProjectDir = projectRoot
	}

	if opts.JSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal cache report: %w", err)
		}
		fmt.Fprintln(opts.Stdout, string(data))
		return nil
	}

	fmt.Fprintf(opts.Stdout, "cache root: %s\n", report.CacheRoot)
	if projectConfig != "" {
		fmt.Fprintf(opts.Stdout, "project config: %s\n", projectConfig)
	}
	if report.Scope != "" {
		fmt.Fprintf(opts.Stdout, "scope: %s\n", report.Scope)
	}
	if len(report.Libraries) == 0 {
		fmt.Fprintln(opts.Stdout, "libraries: <none>")
		return nil
	}
	fmt.Fprintln(opts.Stdout, "libraries:")
	for _, lib := range report.Libraries {
		status := ""
		if active != nil {
			if lib.Active {
				status = " [active]"
			} else {
				status = " [stale]"
			}
		}
		fmt.Fprintf(opts.Stdout, "- %s%s\n", lib.Path, status)
	}
	return nil
}

func CacheRemove(opts CacheRemoveOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if strings.TrimSpace(opts.Target) == "" {
		return fmt.Errorf("cache remove target is required")
	}

	targetPath, cacheRoot, err := resolveManagedLibraryTarget(opts)
	if err != nil {
		return err
	}
	if _, err := os.Stat(targetPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("managed library not found: %s", targetPath)
	} else if err != nil {
		return fmt.Errorf("stat managed library: %w", err)
	}

	fmt.Fprintf(opts.Stdout, "[info] cache root: %s\n", cacheRoot)
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[dry-run] would remove %s\n", targetPath)
		fmt.Fprintln(opts.Stdout, "[ok] cache rm would remove 1 managed library")
		return nil
	}
	if err := os.RemoveAll(targetPath); err != nil {
		return fmt.Errorf("remove managed library %s: %w", targetPath, err)
	}
	fmt.Fprintf(opts.Stdout, "[remove] %s\n", targetPath)
	fmt.Fprintln(opts.Stdout, "[ok] cache rm removed 1 managed library")
	return nil
}

func Check(opts CheckOptions) error {
	env, err := resolveEnvironment(RunOptions{
		ScriptPath:    opts.ScriptPath,
		ExtraDeps:     opts.ExtraDeps,
		ExtraBiocDeps: opts.ExtraBiocDeps,
		ExcludeDeps:   opts.ExcludeDeps,
		Repo:          opts.Repo,
		CacheDir:      opts.CacheDir,
		RscriptPath:   opts.RscriptPath,
		Verbose:       opts.Verbose,
		Stdout:        opts.Stdout,
		Stderr:        opts.Stderr,
	})
	if err != nil {
		return err
	}
	defer os.Remove(env.BootstrapPath)
	if opts.BootstrapToolchain {
		if err := maybeBootstrapResolvedEnvironment(&env); err != nil {
			return err
		}
	}

	if opts.JSON {
		report, resultErr := buildCheckReport(env, opts)
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal check report: %w", err)
		}
		fmt.Fprintln(opts.Stdout, string(data))
		if resultErr != nil {
			return ReportedError{Err: resultErr}
		}
		return nil
	}

	if err := ValidateLockfile(env, ValidationModeFrozen); err != nil {
		return err
	}
	if env.Verbose {
		printAppliedAdjustments(env.Stderr, "[rs] ", opts.IncludeDeps, opts.IncludeBiocDeps, opts.ExcludeDeps)
		fmt.Fprintf(env.Stderr, "[rs] lockfile validated: %s\n", env.LockfilePath)
	}
	return nil
}

func Doctor(opts DoctorOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	var (
		plan dependencyPlan
		err  error
	)
	if opts.ToolchainOnly {
		plan, err = resolveToolchainOnlyPlan(opts)
		if err != nil {
			return err
		}
	} else {
		info, err := os.Stat(opts.ScriptPath)
		if err != nil {
			return fmt.Errorf("stat script: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory, expected an R script", opts.ScriptPath)
		}

		plan, err = resolveDependencyPlanWithProgress(opts.ScriptPath, opts.ExtraDeps, opts.ExtraBiocDeps, opts.ExcludeDeps, opts.Repo, opts.CacheDir, opts.RscriptPath, opts.Stderr)
		if err != nil {
			return err
		}
	}
	if opts.BootstrapToolchain {
		if err := maybeBootstrapPlanToolchain(&plan, opts.Stdout, opts.Stderr); err != nil {
			return err
		}
	}

	var errorsList []string
	var warnings []string
	toolchainPreview := toolchainenv.BuildPreview(plan.ToolchainPrefixes, plan.PkgConfigPath)
	systemHintDetails := collectSystemDependencyHintDetails(plan.CRANDeps, plan.BiocDeps, plan.SourceDeps)
	if opts.ToolchainOnly {
		systemHintDetails = []SystemHintDetail{}
	}
	systemHints := renderSystemHints(systemHintDetails)

	rscriptPath := plan.RscriptPath
	var rscriptErr error
	if !opts.ToolchainOnly && plan.RscriptIssue != "" {
		rscriptErr = fmt.Errorf(plan.RscriptIssue)
		errorsList = append(errorsList, plan.RscriptIssue)
	}

	needsGit := hasSourceType(plan.SourceDeps, "git")
	if opts.ToolchainOnly {
		needsGit = false
	}
	gitPath := ""
	if needsGit {
		if foundGitPath, err := exec.LookPath("git"); err != nil {
			errorsList = append(errorsList, "git is required for git sources but is not available on PATH")
		} else {
			gitPath = foundGitPath
		}
	}

	errorsList = append(errorsList, collectSourceDefinitionIssues(plan.SourceDeps)...)
	errorsList = append(errorsList, collectSourceAvailabilityIssues(plan.SourceDeps)...)
	toolchainIssues := toolchainenv.Validate(
		plan.ToolchainPrefixes,
		plan.PkgConfigPath,
		toolchainenv.Apply(os.Environ(), plan.ToolchainPrefixes, plan.PkgConfigPath),
	)
	errorsList = append(errorsList, toolchainIssues.Errors...)
	warnings = append(warnings, toolchainIssues.Warnings...)
	if !opts.ToolchainOnly && rscriptErr == nil {
		backend := installBackend()
		if backend == "native" || backend == "auto" {
			env := ResolvedEnvironment{
				ScriptPath:   plan.ScriptPath,
				Repo:         plan.Repo,
				CacheRoot:    plan.CacheRoot,
				LibraryPath:  plan.LibraryPath,
				LockfilePath: plan.LockfilePath,
				Interpreter:  plan.RscriptPath,
				DetectedDeps: copyStrings(plan.DetectedDeps),
				CRANDeps:     copyStrings(plan.CRANDeps),
				BiocDeps:     copyStrings(plan.BiocDeps),
				SourceDeps:   cloneSourceSpecMap(plan.SourceDeps),
			}
			req, err := installerRequestFromEnvironment(env, io.Discard, io.Discard)
			if err != nil {
				errorsList = append(errorsList, errorLines(err)...)
			} else if err := nativeValidatePlan(req); err != nil {
				errorsList = append(errorsList, errorLines(err)...)
			}
		}
	}

	if !opts.ToolchainOnly && plan.LockfilePath != "" {
		if _, err := os.Stat(plan.LockfilePath); errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, fmt.Sprintf("lockfile not found: %s", plan.LockfilePath))
		}
	}
	if !opts.ToolchainOnly && plan.LibraryPath != "" {
		if _, err := os.Stat(plan.LibraryPath); errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, fmt.Sprintf("managed library directory does not exist yet: %s", plan.LibraryPath))
		}
	}
	if !opts.ToolchainOnly && plan.Runtime.InterpreterKind == "external-conda" {
		warnings = append(warnings, fmt.Sprintf("selected interpreter %s is an external Conda-style R installation; source package compilation may be less reliable than a managed rs R", plan.RscriptPath))
	}

	nextSteps := buildDoctorNextSteps(plan, rscriptErr, needsGit, warnings, errorsList, systemHintDetails)
	report := buildDoctorReport(plan, opts, rscriptPath, rscriptErr, gitPath, needsGit, warnings, errorsList, systemHints, systemHintDetails, nextSteps, toolchainPreview)

	if opts.JSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal doctor report: %w", err)
		}
		fmt.Fprintln(opts.Stdout, string(data))
		if len(errorsList) > 0 {
			return DoctorError{Issues: errorsList, Code: 1}
		}
		if opts.Strict && report.Status != "ok" {
			return doctorStrictError(report)
		}
		return nil
	}
	if opts.SummaryOnly {
		fmt.Fprintf(opts.Stdout, "[summary] %s\n", formatDoctorSummary(report))
		if len(errorsList) > 0 {
			return DoctorError{Issues: errorsList, Code: 1}
		}
		if opts.Strict && report.Status != "ok" {
			return doctorStrictError(report)
		}
		return nil
	}

	if !opts.Quiet {
		if opts.ToolchainOnly {
			target := firstNonEmpty(plan.ScriptPath, plan.ProjectPath, plan.CacheRoot)
			fmt.Fprintf(opts.Stdout, "[info] toolchain target: %s\n", target)
			if plan.ProjectPath != "" {
				fmt.Fprintf(opts.Stdout, "[info] project config: %s\n", plan.ProjectPath)
			}
			if len(plan.ToolchainPrefixes) == 0 {
				fmt.Fprintln(opts.Stdout, "[info] toolchain prefixes: <none>")
			} else {
				fmt.Fprintf(opts.Stdout, "[info] toolchain prefixes: %s\n", strings.Join(plan.ToolchainPrefixes, ", "))
			}
			if len(plan.PkgConfigPath) == 0 {
				fmt.Fprintln(opts.Stdout, "[info] pkg-config path: <none>")
			} else {
				fmt.Fprintf(opts.Stdout, "[info] pkg-config path: %s\n", strings.Join(plan.PkgConfigPath, ", "))
			}
			printDoctorToolchainPreview(opts.Stdout, toolchainPreview)
		} else {
			fmt.Fprintf(opts.Stdout, "[info] script: %s\n", plan.ScriptPath)
			if plan.ProjectPath != "" {
				fmt.Fprintf(opts.Stdout, "[info] project config: %s\n", plan.ProjectPath)
			}
			if plan.ScriptKey != "" {
				fmt.Fprintf(opts.Stdout, "[info] script profile: %s\n", plan.ScriptKey)
			}
			if rscriptErr == nil {
				fmt.Fprintf(opts.Stdout, "[info] Rscript: %s\n", rscriptPath)
			}
			fmt.Fprintf(opts.Stdout, "[info] repo: %s\n", plan.Repo)
			fmt.Fprintf(opts.Stdout, "[info] lockfile: %s\n", plan.LockfilePath)
			fmt.Fprintf(opts.Stdout, "[info] managed library: %s\n", plan.LibraryPath)
			if opts.Verbose {
				fmt.Fprintf(opts.Stdout, "[info] cache root: %s\n", plan.CacheRoot)
				if needsGit && gitPath != "" {
					fmt.Fprintf(opts.Stdout, "[info] git: %s\n", gitPath)
				}
			}
			if len(plan.DetectedDeps) == 0 {
				fmt.Fprintln(opts.Stdout, "[info] detected packages: <none>")
			} else {
				fmt.Fprintf(opts.Stdout, "[info] detected packages: %s\n", strings.Join(plan.DetectedDeps, ", "))
			}
			if len(plan.CRANDeps) == 0 {
				fmt.Fprintln(opts.Stdout, "[info] resolved CRAN packages: <none>")
			} else {
				fmt.Fprintf(opts.Stdout, "[info] resolved CRAN packages: %s\n", strings.Join(plan.CRANDeps, ", "))
			}
			if len(plan.BiocDeps) == 0 {
				fmt.Fprintln(opts.Stdout, "[info] resolved Bioconductor packages: <none>")
			} else {
				fmt.Fprintf(opts.Stdout, "[info] resolved Bioconductor packages: %s\n", strings.Join(plan.BiocDeps, ", "))
			}
			if len(plan.ToolchainPrefixes) == 0 {
				fmt.Fprintln(opts.Stdout, "[info] toolchain prefixes: <none>")
			} else {
				fmt.Fprintf(opts.Stdout, "[info] toolchain prefixes: %s\n", strings.Join(plan.ToolchainPrefixes, ", "))
			}
			if len(plan.PkgConfigPath) == 0 {
				fmt.Fprintln(opts.Stdout, "[info] pkg-config path: <none>")
			} else {
				fmt.Fprintf(opts.Stdout, "[info] pkg-config path: %s\n", strings.Join(plan.PkgConfigPath, ", "))
			}
			if opts.Verbose {
				printDoctorToolchainPreview(opts.Stdout, toolchainPreview)
			}
			printAppliedAdjustments(opts.Stdout, "[info] ", opts.IncludeDeps, opts.IncludeBiocDeps, opts.ExcludeDeps)
			if len(plan.SourceDeps) == 0 {
				fmt.Fprintln(opts.Stdout, "[info] resolved custom sources: <none>")
			} else {
				fmt.Fprintf(opts.Stdout, "[info] resolved custom sources: %s\n", strings.Join(sourceSummary(plan.SourceDeps), ", "))
				if opts.Verbose {
					for _, name := range sourceDepNames(plan.SourceDeps) {
						spec := plan.SourceDeps[name]
						fmt.Fprintf(opts.Stdout, "[info] source %s: type=%s", name, spec.Type)
						switch spec.Type {
						case "github":
							fmt.Fprintf(opts.Stdout, " repo=%s", spec.Repo)
							if spec.Host != "" {
								fmt.Fprintf(opts.Stdout, " host=%s", spec.Host)
							}
							if spec.Ref != "" {
								fmt.Fprintf(opts.Stdout, " ref=%s", spec.Ref)
							}
							if spec.Subdir != "" {
								fmt.Fprintf(opts.Stdout, " subdir=%s", spec.Subdir)
							}
							if spec.TokenEnv != "" {
								fmt.Fprintf(opts.Stdout, " token_env=%s", spec.TokenEnv)
							}
						case "git":
							fmt.Fprintf(opts.Stdout, " url=%s", spec.URL)
							if spec.Ref != "" {
								fmt.Fprintf(opts.Stdout, " ref=%s", spec.Ref)
							}
							if spec.Subdir != "" {
								fmt.Fprintf(opts.Stdout, " subdir=%s", spec.Subdir)
							}
						case "local":
							fmt.Fprintf(opts.Stdout, " path=%s", spec.Path)
						}
						fmt.Fprintln(opts.Stdout)
					}
				}
			}
		}
	}
	for _, warning := range warnings {
		fmt.Fprintf(opts.Stdout, "[warn] %s\n", warning)
	}
	for _, issue := range errorsList {
		fmt.Fprintf(opts.Stdout, "[error] %s\n", issue)
	}
	for _, hint := range systemHints {
		fmt.Fprintf(opts.Stdout, "[hint] %s\n", hint)
	}
	for _, step := range nextSteps {
		fmt.Fprintf(opts.Stdout, "[next] %s", step.Message)
		if step.Command != "" {
			fmt.Fprintf(opts.Stdout, ": %s", step.Command)
		}
		fmt.Fprintln(opts.Stdout)
	}
	fmt.Fprintf(opts.Stdout, "[summary] %s\n", formatDoctorSummary(report))
	if len(errorsList) == 0 && len(warnings) == 0 {
		fmt.Fprintln(opts.Stdout, "[ok] doctor checks passed")
	}

	if len(errorsList) > 0 {
		return DoctorError{Issues: errorsList, Code: 1}
	}
	if opts.Strict && report.Status != "ok" {
		return doctorStrictError(report)
	}
	return nil
}

func resolveDependencyPlan(scriptPath string, extraDeps, extraBiocDeps, excludeDeps []string, repoOverride, cacheDirOverride, rscriptOverride string) (dependencyPlan, error) {
	return resolveDependencyPlanWithProgress(scriptPath, extraDeps, extraBiocDeps, excludeDeps, repoOverride, cacheDirOverride, rscriptOverride, io.Discard)
}

func resolveToolchainOnlyPlan(opts DoctorOptions) (dependencyPlan, error) {
	baseDir := opts.ProjectDir
	if strings.TrimSpace(baseDir) == "" && strings.TrimSpace(opts.ScriptPath) == "" {
		baseDir = "."
	}

	plan := dependencyPlan{
		ScriptPath: strings.TrimSpace(opts.ScriptPath),
	}
	if plan.ScriptPath != "" {
		info, err := os.Stat(plan.ScriptPath)
		if err != nil {
			return dependencyPlan{}, fmt.Errorf("stat script: %w", err)
		}
		if info.IsDir() {
			return dependencyPlan{}, fmt.Errorf("%s is a directory, expected an R script", plan.ScriptPath)
		}
		baseDir = filepath.Dir(plan.ScriptPath)
	}

	projectCfg, found, err := project.Discover(baseDir)
	if err != nil {
		return dependencyPlan{}, fmt.Errorf("discover project config: %w", err)
	}

	resolvedCfg := project.ResolvedScriptConfig{}
	if found {
		plan.ProjectPath = projectCfg.Path
		if plan.ScriptPath != "" {
			resolvedCfg, err = projectCfg.ResolveForScript(plan.ScriptPath)
			if err != nil {
				return dependencyPlan{}, fmt.Errorf("resolve script config: %w", err)
			}
			plan.ScriptKey = resolvedCfg.ScriptKey
		} else {
			resolvedCfg = project.ResolvedScriptConfig{
				Repo:              projectCfg.Defaults.Repo,
				CacheDir:          projectCfg.Defaults.CacheDir,
				Lockfile:          projectCfg.Defaults.Lockfile,
				Rscript:           projectCfg.Defaults.Rscript,
				RVersion:          projectCfg.Defaults.RVersion,
				ToolchainPrefixes: append([]string(nil), projectCfg.Defaults.ToolchainPrefixes...),
				PkgConfigPath:     append([]string(nil), projectCfg.Defaults.PkgConfigPath...),
			}
		}
	}

	plan.Repo = firstNonEmpty(opts.Repo, resolvedCfg.Repo, "https://cloud.r-project.org")
	plan.CacheRoot = predictedCacheRoot(firstNonEmpty(opts.CacheDir, resolvedCfg.CacheDir))
	if resolvedCfg.Lockfile != "" {
		plan.LockfilePath = resolvedCfg.Lockfile
	}
	if plan.LockfilePath == "" && found && projectCfg.RootDir != "" && plan.ScriptPath != "" {
		plan.LockfilePath = defaultLockfilePath(projectCfg.RootDir, plan.ScriptPath)
	}
	if plan.ScriptPath != "" {
		plan.LibraryPath = predictedLibraryPath(plan.CacheRoot, plan.ScriptPath, nil, nil, nil, plan.Repo, RuntimeMetadata{})
	}
	plan.ToolchainPrefixes = mergeUniqueStringLists(resolvedCfg.ToolchainPrefixes, toolchainenv.PrefixesFromEnv(os.Environ()))
	plan.PkgConfigPath = mergeUniqueStringLists(resolvedCfg.PkgConfigPath, toolchainenv.PkgConfigPathsFromEnv(os.Environ()))
	return plan, nil
}

func resolveDependencyPlanWithProgress(scriptPath string, extraDeps, extraBiocDeps, excludeDeps []string, repoOverride, cacheDirOverride, rscriptOverride string, progress io.Writer) (dependencyPlan, error) {
	info, err := os.Stat(scriptPath)
	if err != nil {
		return dependencyPlan{}, fmt.Errorf("stat script: %w", err)
	}
	if info.IsDir() {
		return dependencyPlan{}, fmt.Errorf("%s is a directory, expected an R script", scriptPath)
	}

	progresscmd.Stage(progress, "discovering project config")
	projectCfg, _, err := project.Discover(filepath.Dir(scriptPath))
	if err != nil {
		return dependencyPlan{}, fmt.Errorf("discover project config: %w", err)
	}
	progresscmd.Stage(progress, "resolving script configuration")
	scriptCfg, err := projectCfg.ResolveForScript(scriptPath)
	if err != nil {
		return dependencyPlan{}, fmt.Errorf("resolve script config: %w", err)
	}

	repo := firstNonEmpty(repoOverride, scriptCfg.Repo, "https://cloud.r-project.org")
	progresscmd.Stage(progress, "scanning script dependencies")
	selection := resolveInterpreterSelection(rscriptOverride, scriptCfg.Rscript, scriptCfg.RVersion, filepath.Dir(scriptPath), io.Discard, false)
	detectedDeps, err := ScanScript(scriptPath)
	if err != nil {
		return dependencyPlan{}, fmt.Errorf("scan script: %w", err)
	}
	progresscmd.Stage(progress, "resolving interpreter and managed library plan")
	cranDeps, biocDeps := resolveManagedPackageSets(detectedDeps, scriptCfg.Packages, scriptCfg.BiocPackages, extraDeps, extraBiocDeps, excludeDeps)
	sourceDeps := selectSourceDeps(scriptCfg.Sources, cranDeps, biocDeps)
	cranDeps = filterManagedDeps(cranDeps, sourceDeps, biocDeps)
	biocDeps = filterBiocDeps(biocDeps, sourceDeps)

	cacheRoot := predictedCacheRoot(firstNonEmpty(cacheDirOverride, scriptCfg.CacheDir))
	lockfilePath := scriptCfg.Lockfile
	if lockfilePath == "" {
		lockfilePath = defaultLockfilePath(projectCfg.RootDir, scriptPath)
	}
	runtime := RuntimeMetadata{}
	if selection.Issue == nil {
		runtime = selection.Runtime
	}
	libraryPath := predictedLibraryPath(cacheRoot, scriptPath, cranDeps, biocDeps, sourceDeps, repo, runtime)

	return dependencyPlan{
		ScriptPath:        scriptPath,
		ProjectPath:       projectCfg.Path,
		ScriptKey:         scriptCfg.ScriptKey,
		RequestedR:        selection.Requested,
		RequestedRVersion: selection.RequestedVer,
		RscriptPath:       selection.Interpreter,
		RscriptIssue:      errorString(selection.Issue),
		Repo:              repo,
		CacheRoot:         cacheRoot,
		LockfilePath:      lockfilePath,
		LibraryPath:       libraryPath,
		DetectedDeps:      detectedDeps,
		CRANDeps:          cranDeps,
		BiocDeps:          biocDeps,
		ExcludedDeps:      copyStrings(excludeDeps),
		SourceDeps:        sourceDeps,
		ToolchainPrefixes: copyStrings(scriptCfg.ToolchainPrefixes),
		PkgConfigPath:     copyStrings(scriptCfg.PkgConfigPath),
		Runtime:           runtime,
	}, nil
}

func buildListReport(plan dependencyPlan, opts ListOptions) ListReport {
	report := ListReport{
		Script:         plan.ScriptPath,
		ProjectConfig:  plan.ProjectPath,
		ScriptProfile:  plan.ScriptKey,
		RscriptPath:    plan.RscriptPath,
		RscriptIssue:   plan.RscriptIssue,
		Repo:           plan.Repo,
		Lockfile:       plan.LockfilePath,
		ManagedLibrary: plan.LibraryPath,
		CacheRoot:      plan.CacheRoot,
		DetectedDeps:   copyStrings(plan.DetectedDeps),
		CRANDeps:       copyStrings(plan.CRANDeps),
		BiocDeps:       copyStrings(plan.BiocDeps),
		IncludedCRAN:   copyStrings(opts.IncludeDeps),
		IncludedBioc:   copyStrings(opts.IncludeBiocDeps),
		ExcludedDeps:   copyStrings(plan.ExcludedDeps),
		CustomSources:  []ListSource{},
	}

	for _, name := range sourceDepNames(plan.SourceDeps) {
		spec := plan.SourceDeps[name]
		report.CustomSources = append(report.CustomSources, ListSource{
			Package:  name,
			Type:     spec.Type,
			Host:     spec.Host,
			Repo:     spec.Repo,
			URL:      spec.URL,
			Ref:      spec.Ref,
			Path:     spec.Path,
			Subdir:   spec.Subdir,
			TokenEnv: spec.TokenEnv,
		})
	}
	normalizeListReport(&report)
	return report
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func mergeUniqueStringLists(groups ...[]string) []string {
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

func cloneSourceSpecMap(values map[string]project.SourceSpec) map[string]project.SourceSpec {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]project.SourceSpec, len(values))
	for name, spec := range values {
		out[name] = spec
	}
	return out
}

func normalizeListReport(report *ListReport) {
	if report == nil {
		return
	}
	if report.DetectedDeps == nil {
		report.DetectedDeps = []string{}
	}
	if report.CRANDeps == nil {
		report.CRANDeps = []string{}
	}
	if report.BiocDeps == nil {
		report.BiocDeps = []string{}
	}
	if report.IncludedCRAN == nil {
		report.IncludedCRAN = []string{}
	}
	if report.IncludedBioc == nil {
		report.IncludedBioc = []string{}
	}
	if report.ExcludedDeps == nil {
		report.ExcludedDeps = []string{}
	}
	if report.CustomSources == nil {
		report.CustomSources = []ListSource{}
	}
}

func buildCheckReport(env ResolvedEnvironment, opts CheckOptions) (CheckReport, error) {
	report := CheckReport{
		Script:                   env.ScriptPath,
		ProjectConfig:            env.ProjectConfig.Path,
		ScriptProfile:            env.ScriptConfig.ScriptKey,
		RscriptPath:              env.Interpreter,
		Repo:                     env.Repo,
		Lockfile:                 env.LockfilePath,
		ManagedLibrary:           env.LibraryPath,
		CacheRoot:                env.CacheRoot,
		DetectedDeps:             copyStrings(env.DetectedDeps),
		CRANDeps:                 copyStrings(env.CRANDeps),
		BiocDeps:                 copyStrings(env.BiocDeps),
		IncludedCRAN:             copyStrings(opts.IncludeDeps),
		IncludedBioc:             copyStrings(opts.IncludeBiocDeps),
		ExcludedDeps:             copyStrings(opts.ExcludeDeps),
		PlanningIssues:           []string{},
		InputIssues:              []string{},
		InstalledIssues:          []string{},
		InstalledMissingPackages: []string{},
		InstalledVersionIssues:   []string{},
		InstalledSourceIssues:    []string{},
		InstalledOtherIssues:     []string{},
		PlanningIssueDetails:     []InstalledIssueDetail{},
		InstalledIssueDetails:    []InstalledIssueDetail{},
		Issues:                   []string{},
	}

	if backend := installBackend(); backend == "native" || backend == "auto" {
		req, err := installerRequestFromEnvironment(env, io.Discard, io.Discard)
		if err != nil {
			report.Valid = false
			report.Issues = errorLines(err)
			report.PlanningIssues = copyStrings(report.Issues)
			report.PlanningIssueDetails = buildInstalledIssueDetails(report.Issues)
			normalizeCheckReport(&report)
			return report, err
		}
		if err := nativeValidatePlan(req); err != nil {
			report.Valid = false
			report.Issues = errorLines(err)
			report.PlanningIssues = copyStrings(report.Issues)
			report.PlanningIssueDetails = buildInstalledIssueDetails(report.Issues)
			normalizeCheckReport(&report)
			return report, err
		}
	}

	validation, err := ValidateLockfileInputs(env, ValidationModeCheck)
	if err != nil {
		report.Valid = false
		report.Issues, report.InputIssues, report.InstalledIssues = validationIssueBreakdown(err)
		normalizeCheckReport(&report)
		return report, err
	}

	actualPkgs, err := InstalledPackages(env)
	if err != nil {
		report.Valid = false
		report.Issues = errorLines(err)
		normalizeCheckReport(&report)
		return report, err
	}

	issues := compareInstalledPackages(validation.Lockfile.Packages, actualPkgs)
	report.Valid = len(issues) == 0
	report.InstalledIssues = copyStrings(issues)
	report.InstalledMissingPackages, report.InstalledVersionIssues, report.InstalledSourceIssues, report.InstalledOtherIssues = categorizeInstalledIssues(issues)
	report.InstalledIssueDetails = buildInstalledIssueDetails(issues)
	report.Issues = issues
	normalizeCheckReport(&report)
	if len(issues) > 0 {
		return report, ValidationError{
			Mode:         ValidationModeCheck,
			Kind:         ValidationKindInstalled,
			ScriptPath:   env.ScriptPath,
			LockfilePath: env.LockfilePath,
			LibraryPath:  env.LibraryPath,
			Issues:       issues,
		}
	}
	return report, nil
}

func normalizeCheckReport(report *CheckReport) {
	if report == nil {
		return
	}
	if report.DetectedDeps == nil {
		report.DetectedDeps = []string{}
	}
	if report.CRANDeps == nil {
		report.CRANDeps = []string{}
	}
	if report.BiocDeps == nil {
		report.BiocDeps = []string{}
	}
	if report.IncludedCRAN == nil {
		report.IncludedCRAN = []string{}
	}
	if report.IncludedBioc == nil {
		report.IncludedBioc = []string{}
	}
	if report.ExcludedDeps == nil {
		report.ExcludedDeps = []string{}
	}
	if report.PlanningIssues == nil {
		report.PlanningIssues = []string{}
	}
	if report.InputIssues == nil {
		report.InputIssues = []string{}
	}
	if report.InstalledIssues == nil {
		report.InstalledIssues = []string{}
	}
	if report.InstalledMissingPackages == nil {
		report.InstalledMissingPackages = []string{}
	}
	if report.InstalledVersionIssues == nil {
		report.InstalledVersionIssues = []string{}
	}
	if report.InstalledSourceIssues == nil {
		report.InstalledSourceIssues = []string{}
	}
	if report.InstalledOtherIssues == nil {
		report.InstalledOtherIssues = []string{}
	}
	if report.PlanningIssueDetails == nil {
		report.PlanningIssueDetails = []InstalledIssueDetail{}
	}
	if report.InstalledIssueDetails == nil {
		report.InstalledIssueDetails = []InstalledIssueDetail{}
	}
	if report.Issues == nil {
		report.Issues = []string{}
	}
}

func buildDoctorReport(plan dependencyPlan, opts DoctorOptions, rscriptPath string, rscriptErr error, gitPath string, needsGit bool, warnings, errorsList, systemHints []string, systemHintDetails []SystemHintDetail, nextSteps []NextStepDetail, toolchainPreview toolchainenv.Preview) DoctorReport {
	setupErrors, sourceErrors, networkErrors, runtimeErrors, otherErrors := categorizeDoctorErrors(errorsList)
	lockWarnings, cacheWarnings, otherWarnings := categorizeDoctorWarnings(warnings)
	errorDetails := buildDoctorIssueDetails(errorsList, false)
	warningDetails := buildDoctorIssueDetails(warnings, true)
	status := doctorStatus(errorsList, warnings, systemHintDetails)
	summary := buildDoctorSummary(errorsList, warnings, systemHintDetails, nextSteps, setupErrors, sourceErrors, networkErrors, runtimeErrors, otherErrors, lockWarnings, cacheWarnings, otherWarnings)

	report := DoctorReport{
		Script:            plan.ScriptPath,
		ProjectConfig:     plan.ProjectPath,
		ScriptProfile:     plan.ScriptKey,
		NeedsGit:          needsGit,
		Repo:              plan.Repo,
		Lockfile:          plan.LockfilePath,
		ManagedLibrary:    plan.LibraryPath,
		CacheRoot:         plan.CacheRoot,
		DetectedDeps:      copyStrings(plan.DetectedDeps),
		CRANDeps:          copyStrings(plan.CRANDeps),
		BiocDeps:          copyStrings(plan.BiocDeps),
		IncludedCRAN:      copyStrings(opts.IncludeDeps),
		IncludedBioc:      copyStrings(opts.IncludeBiocDeps),
		ExcludedDeps:      copyStrings(opts.ExcludeDeps),
		ToolchainPrefixes: copyStrings(plan.ToolchainPrefixes),
		PkgConfigPath:     copyStrings(plan.PkgConfigPath),
		ToolchainPath:     copyStrings(toolchainPreview.Path),
		ToolchainCPPFLAGS: copyStrings(toolchainPreview.CPPFLAGS),
		ToolchainLDFLAGS:  copyStrings(toolchainPreview.LDFLAGS),
		ToolchainPkgPath:  copyStrings(toolchainPreview.PkgConfigPath),
		CustomSources:     sourceSummary(plan.SourceDeps),
		Warnings:          copyStrings(warnings),
		Errors:            copyStrings(errorsList),
		SetupErrors:       setupErrors,
		SourceErrors:      sourceErrors,
		NetworkErrors:     networkErrors,
		RuntimeErrors:     runtimeErrors,
		OtherErrors:       otherErrors,
		LockWarnings:      lockWarnings,
		CacheWarnings:     cacheWarnings,
		OtherWarnings:     otherWarnings,
		ErrorDetails:      errorDetails,
		WarningDetails:    warningDetails,
		SystemHints:       copyStrings(systemHints),
		SystemHintDetails: copySystemHintDetails(systemHintDetails),
		NextSteps:         copyNextStepDetails(nextSteps),
		Status:            status,
		Summary:           summary,
		OK:                len(warnings) == 0 && len(errorsList) == 0,
	}
	if rscriptErr == nil {
		report.RscriptPath = rscriptPath
	}
	if gitPath != "" {
		report.GitPath = gitPath
	}
	normalizeDoctorReport(&report)
	return report
}

func normalizeDoctorReport(report *DoctorReport) {
	if report == nil {
		return
	}
	if report.DetectedDeps == nil {
		report.DetectedDeps = []string{}
	}
	if report.CRANDeps == nil {
		report.CRANDeps = []string{}
	}
	if report.BiocDeps == nil {
		report.BiocDeps = []string{}
	}
	if report.IncludedCRAN == nil {
		report.IncludedCRAN = []string{}
	}
	if report.IncludedBioc == nil {
		report.IncludedBioc = []string{}
	}
	if report.ExcludedDeps == nil {
		report.ExcludedDeps = []string{}
	}
	if report.ToolchainPrefixes == nil {
		report.ToolchainPrefixes = []string{}
	}
	if report.PkgConfigPath == nil {
		report.PkgConfigPath = []string{}
	}
	if report.ToolchainPath == nil {
		report.ToolchainPath = []string{}
	}
	if report.ToolchainCPPFLAGS == nil {
		report.ToolchainCPPFLAGS = []string{}
	}
	if report.ToolchainLDFLAGS == nil {
		report.ToolchainLDFLAGS = []string{}
	}
	if report.ToolchainPkgPath == nil {
		report.ToolchainPkgPath = []string{}
	}
	if report.CustomSources == nil {
		report.CustomSources = []string{}
	}
	if report.Warnings == nil {
		report.Warnings = []string{}
	}
	if report.Errors == nil {
		report.Errors = []string{}
	}
	if report.SetupErrors == nil {
		report.SetupErrors = []string{}
	}
	if report.SourceErrors == nil {
		report.SourceErrors = []string{}
	}
	if report.NetworkErrors == nil {
		report.NetworkErrors = []string{}
	}
	if report.RuntimeErrors == nil {
		report.RuntimeErrors = []string{}
	}
	if report.OtherErrors == nil {
		report.OtherErrors = []string{}
	}
	if report.LockWarnings == nil {
		report.LockWarnings = []string{}
	}
	if report.CacheWarnings == nil {
		report.CacheWarnings = []string{}
	}
	if report.OtherWarnings == nil {
		report.OtherWarnings = []string{}
	}
	if report.ErrorDetails == nil {
		report.ErrorDetails = []DoctorIssueDetail{}
	}
	if report.WarningDetails == nil {
		report.WarningDetails = []DoctorIssueDetail{}
	}
	if report.SystemHints == nil {
		report.SystemHints = []string{}
	}
	if report.SystemHintDetails == nil {
		report.SystemHintDetails = []SystemHintDetail{}
	}
	if report.NextSteps == nil {
		report.NextSteps = []NextStepDetail{}
	}
	if report.Status == "" {
		report.Status = "ok"
	}
}

func printDoctorToolchainPreview(w io.Writer, preview toolchainenv.Preview) {
	if len(preview.Path) == 0 {
		fmt.Fprintln(w, "[info] toolchain PATH: <none>")
	} else {
		fmt.Fprintf(w, "[info] toolchain PATH: %s\n", strings.Join(preview.Path, string(os.PathListSeparator)))
	}
	if len(preview.CPPFLAGS) == 0 {
		fmt.Fprintln(w, "[info] toolchain CPPFLAGS: <none>")
	} else {
		fmt.Fprintf(w, "[info] toolchain CPPFLAGS: %s\n", strings.Join(preview.CPPFLAGS, " "))
	}
	if len(preview.LDFLAGS) == 0 {
		fmt.Fprintln(w, "[info] toolchain LDFLAGS: <none>")
	} else {
		fmt.Fprintf(w, "[info] toolchain LDFLAGS: %s\n", strings.Join(preview.LDFLAGS, " "))
	}
	if len(preview.PkgConfigPath) == 0 {
		fmt.Fprintln(w, "[info] toolchain PKG_CONFIG_PATH: <none>")
	} else {
		fmt.Fprintf(w, "[info] toolchain PKG_CONFIG_PATH: %s\n", strings.Join(preview.PkgConfigPath, string(os.PathListSeparator)))
	}
}

func errorLines(err error) []string {
	if err == nil {
		return []string{}
	}
	lines := strings.Split(err.Error(), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{err.Error()}
	}
	return out
}

func validationIssues(err error) []string {
	var validationErr ValidationError
	if errors.As(err, &validationErr) {
		return copyStrings(validationErr.Issues)
	}
	return errorLines(err)
}

func validationIssueBreakdown(err error) ([]string, []string, []string) {
	var validationErr ValidationError
	if errors.As(err, &validationErr) {
		issues := copyStrings(validationErr.Issues)
		switch validationErr.Kind {
		case ValidationKindMissing, ValidationKindInputs:
			return issues, issues, []string{}
		case ValidationKindInstalled:
			return issues, []string{}, issues
		default:
			return issues, []string{}, []string{}
		}
	}

	issues := errorLines(err)
	return issues, []string{}, []string{}
}

func categorizeInstalledIssues(issues []string) ([]string, []string, []string, []string) {
	missing := []string{}
	version := []string{}
	source := []string{}
	other := []string{}

	for _, issue := range issues {
		switch {
		case strings.HasPrefix(issue, "package not installed in managed library: "):
			missing = append(missing, strings.TrimPrefix(issue, "package not installed in managed library: "))
		case strings.HasPrefix(issue, "version mismatch for "):
			version = append(version, issue)
		case strings.HasPrefix(issue, "source mismatch for "),
			strings.HasPrefix(issue, "source host mismatch for "),
			strings.HasPrefix(issue, "source location mismatch for "),
			strings.HasPrefix(issue, "source ref mismatch for "),
			strings.HasPrefix(issue, "source commit mismatch for "),
			strings.HasPrefix(issue, "source subdir mismatch for "),
			strings.HasPrefix(issue, "source fingerprint kind mismatch for "),
			strings.HasPrefix(issue, "source fingerprint mismatch for "):
			source = append(source, issue)
		default:
			other = append(other, issue)
		}
	}

	return missing, version, source, other
}

func buildInstalledIssueDetails(issues []string) []InstalledIssueDetail {
	if len(issues) == 0 {
		return []InstalledIssueDetail{}
	}

	details := make([]InstalledIssueDetail, 0, len(issues))
	for _, issue := range issues {
		details = append(details, installedIssueDetail(issue))
	}
	return details
}

func installedIssueDetail(issue string) InstalledIssueDetail {
	detail := InstalledIssueDetail{
		Kind:    "other",
		Message: issue,
	}

	if conflict, ok := parseDependencyConflictIssue(issue); ok {
		detail.Kind = "dependency_conflict"
		detail.Package = conflict.Package
		detail.DependencyPath = copyStrings(conflict.DependencyPath)
		detail.Constraint = conflict.Constraint
		detail.Selected = conflict.SelectedVersion
		detail.RequiredBy = conflict.RequiredBy
		return detail
	}

	if pkg, ok := strings.CutPrefix(issue, "package not installed in managed library: "); ok {
		detail.Kind = "missing_package"
		detail.Package = pkg
		return detail
	}

	prefixes := []struct {
		prefix string
		kind   string
		field  string
	}{
		{prefix: "version mismatch for ", kind: "version_mismatch", field: "version"},
		{prefix: "source mismatch for ", kind: "source_mismatch", field: "source"},
		{prefix: "source host mismatch for ", kind: "source_mismatch", field: "source_host"},
		{prefix: "source location mismatch for ", kind: "source_mismatch", field: "source_location"},
		{prefix: "source ref mismatch for ", kind: "source_mismatch", field: "source_ref"},
		{prefix: "source commit mismatch for ", kind: "source_mismatch", field: "source_commit"},
		{prefix: "source subdir mismatch for ", kind: "source_mismatch", field: "source_subdir"},
		{prefix: "source fingerprint kind mismatch for ", kind: "source_mismatch", field: "source_fingerprint_kind"},
		{prefix: "source fingerprint mismatch for ", kind: "source_mismatch", field: "source_fingerprint"},
		{prefix: "priority mismatch for ", kind: "priority_mismatch", field: "priority"},
	}
	for _, candidate := range prefixes {
		if rest, ok := strings.CutPrefix(issue, candidate.prefix); ok {
			pkg, _, found := strings.Cut(rest, ":")
			if found {
				detail.Kind = candidate.kind
				detail.Package = pkg
				detail.Field = candidate.field
				return detail
			}
		}
	}

	return detail
}

func categorizeDoctorErrors(issues []string) ([]string, []string, []string, []string, []string) {
	setup := []string{}
	source := []string{}
	network := []string{}
	runtime := []string{}
	other := []string{}

	for _, issue := range issues {
		detail := doctorIssueDetail(issue, false)
		switch detail.Category {
		case "setup":
			setup = append(setup, issue)
		case "source":
			source = append(source, issue)
		case "network":
			network = append(network, issue)
		case "runtime":
			runtime = append(runtime, issue)
		default:
			other = append(other, issue)
		}
	}

	return setup, source, network, runtime, other
}

func categorizeDoctorWarnings(issues []string) ([]string, []string, []string) {
	lockWarnings := []string{}
	cacheWarnings := []string{}
	other := []string{}

	for _, issue := range issues {
		switch {
		case strings.HasPrefix(issue, "lockfile not found: "):
			lockWarnings = append(lockWarnings, issue)
		case strings.HasPrefix(issue, "managed library directory does not exist yet: "):
			cacheWarnings = append(cacheWarnings, issue)
		default:
			other = append(other, issue)
		}
	}

	return lockWarnings, cacheWarnings, other
}

func buildDoctorIssueDetails(issues []string, warning bool) []DoctorIssueDetail {
	if len(issues) == 0 {
		return []DoctorIssueDetail{}
	}

	details := make([]DoctorIssueDetail, 0, len(issues))
	for _, issue := range issues {
		details = append(details, doctorIssueDetail(issue, warning))
	}
	return details
}

func doctorIssueDetail(issue string, warning bool) DoctorIssueDetail {
	detail := DoctorIssueDetail{
		Category: "other",
		Kind:     "generic",
		Message:  issue,
	}

	if warning {
		switch {
		case strings.HasPrefix(issue, "lockfile not found: "):
			detail.Category = "lock"
			detail.Kind = "missing_lockfile"
			detail.Path = strings.TrimPrefix(issue, "lockfile not found: ")
		case strings.HasPrefix(issue, "managed library directory does not exist yet: "):
			detail.Category = "cache"
			detail.Kind = "missing_managed_library"
			detail.Path = strings.TrimPrefix(issue, "managed library directory does not exist yet: ")
		case strings.HasPrefix(issue, "pkg-config is not available on PATH; "):
			detail.Category = "setup"
			detail.Kind = "missing_pkg_config_binary"
		default:
			detail.Kind = "warning"
		}
		return detail
	}

	switch {
	case issue == "Rscript is not available on PATH":
		detail.Category = "setup"
		detail.Kind = "missing_rscript"
	case issue == "git is required for git sources but is not available on PATH":
		detail.Category = "setup"
		detail.Kind = "missing_git"
	case strings.HasPrefix(issue, "toolchain prefix does not exist: "):
		detail.Category = "setup"
		detail.Kind = "missing_toolchain_prefix"
		detail.Path = strings.TrimPrefix(issue, "toolchain prefix does not exist: ")
	case strings.HasPrefix(issue, "toolchain prefix is not a directory: "):
		detail.Category = "setup"
		detail.Kind = "invalid_toolchain_prefix"
		detail.Path = strings.TrimPrefix(issue, "toolchain prefix is not a directory: ")
	case strings.HasPrefix(issue, "pkg-config path does not exist: "):
		detail.Category = "setup"
		detail.Kind = "missing_pkg_config_path"
		detail.Path = strings.TrimPrefix(issue, "pkg-config path does not exist: ")
	case strings.HasPrefix(issue, "pkg-config path is not a directory: "):
		detail.Category = "setup"
		detail.Kind = "invalid_pkg_config_path"
		detail.Path = strings.TrimPrefix(issue, "pkg-config path is not a directory: ")
	case strings.HasPrefix(issue, "could not inspect toolchain prefix "):
		detail.Category = "setup"
		detail.Kind = "unreadable_toolchain_prefix"
	case strings.HasPrefix(issue, "could not inspect pkg-config path "):
		detail.Category = "setup"
		detail.Kind = "unreadable_pkg_config_path"
	case strings.HasPrefix(issue, "source \"") && strings.Contains(issue, "\" requires environment variable "):
		detail.Category = "network"
		detail.Kind = "missing_token_env"
		if pkg, envVar, ok := parseDoctorSourceEnvIssue(issue); ok {
			detail.Package = pkg
			detail.EnvVar = envVar
		}
	case strings.HasPrefix(issue, "source \"") && strings.Contains(issue, "\" is type github but missing repo"):
		detail.Category = "source"
		detail.Kind = "missing_repo"
		detail.Package = parseDoctorQuotedName(issue, `source "`)
	case strings.HasPrefix(issue, "source \"") && strings.Contains(issue, "\" is type git but missing url"):
		detail.Category = "source"
		detail.Kind = "missing_url"
		detail.Package = parseDoctorQuotedName(issue, `source "`)
	case strings.HasPrefix(issue, "source \"") && strings.Contains(issue, "\" is type local but missing path"):
		detail.Category = "source"
		detail.Kind = "missing_path"
		detail.Package = parseDoctorQuotedName(issue, `source "`)
	case strings.HasPrefix(issue, "source \"") && strings.Contains(issue, "\" is missing type"):
		detail.Category = "source"
		detail.Kind = "missing_type"
		detail.Package = parseDoctorQuotedName(issue, `source "`)
	case strings.HasPrefix(issue, "source \"") && strings.Contains(issue, "\" has unsupported type "):
		detail.Category = "source"
		detail.Kind = "unsupported_type"
		detail.Package = parseDoctorQuotedName(issue, `source "`)
	case strings.HasPrefix(issue, "local source \"") && strings.Contains(issue, "\" does not exist: "):
		detail.Category = "source"
		detail.Kind = "missing_local_source"
		if pkg, path, ok := parseDoctorPathIssue(issue, `local source "`); ok {
			detail.Package = pkg
			detail.Path = path
		}
	case strings.HasPrefix(issue, "git source \"") && strings.Contains(issue, "\" does not exist: "):
		detail.Category = "source"
		detail.Kind = "missing_git_source"
		if pkg, path, ok := parseDoctorPathIssue(issue, `git source "`); ok {
			detail.Package = pkg
			detail.Path = path
		}
	case strings.HasPrefix(issue, "dependency constraint conflict for "):
		detail.Kind = "dependency_conflict"
		if conflict, ok := parseDependencyConflictIssue(issue); ok {
			detail.Package = conflict.Package
			detail.DependencyPath = copyStrings(conflict.DependencyPath)
			detail.Constraint = conflict.Constraint
			detail.Selected = conflict.SelectedVersion
			detail.RequiredBy = conflict.RequiredBy
		}
	default:
		detail.Kind = "error"
	}

	return detail
}

func parseDoctorQuotedName(issue, prefix string) string {
	rest, ok := strings.CutPrefix(issue, prefix)
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, `"`)
	return name
}

func parseDoctorPathIssue(issue, prefix string) (string, string, bool) {
	rest, ok := strings.CutPrefix(issue, prefix)
	if !ok {
		return "", "", false
	}
	name, afterName, found := strings.Cut(rest, `" does not exist: `)
	if !found {
		return "", "", false
	}
	return name, afterName, true
}

func parseDoctorSourceEnvIssue(issue string) (string, string, bool) {
	rest, ok := strings.CutPrefix(issue, `source "`)
	if !ok {
		return "", "", false
	}
	name, afterName, found := strings.Cut(rest, `" requires environment variable `)
	if !found {
		return "", "", false
	}
	envVar, _, _ := strings.Cut(afterName, ",")
	return name, strings.TrimSpace(envVar), true
}

type dependencyConflictDetail struct {
	Package         string
	SelectedVersion string
	Constraint      string
	RequiredBy      string
	DependencyPath  []string
}

func parseDependencyConflictIssue(issue string) (dependencyConflictDetail, bool) {
	const prefix = "dependency constraint conflict for "
	rest, ok := strings.CutPrefix(issue, prefix)
	if !ok {
		return dependencyConflictDetail{}, false
	}
	name, message, found := strings.Cut(rest, ":")
	if !found {
		return dependencyConflictDetail{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return dependencyConflictDetail{}, false
	}
	detail := dependencyConflictDetail{Package: name}
	if body, ok := strings.CutPrefix(strings.TrimSpace(message), "selected version "); ok {
		selected, remainder, found := strings.Cut(body, " does not satisfy ")
		if found {
			detail.SelectedVersion = strings.TrimSpace(selected)
			if beforePath, pathPart, foundPath := strings.Cut(remainder, " (dependency path: "); foundPath {
				remainder = beforePath
				pathPart = strings.TrimSuffix(pathPart, ")")
				if pathPart != "" {
					detail.DependencyPath = strings.Split(pathPart, " -> ")
				}
			}
			if constraint, requiredByPart, foundRequiredBy := strings.Cut(remainder, " required by "); foundRequiredBy {
				detail.Constraint = strings.TrimSpace(constraint)
				detail.RequiredBy = strings.TrimSpace(requiredByPart)
			} else {
				detail.Constraint = strings.TrimSpace(remainder)
			}
		}
	}
	return detail, true
}

func collectSystemDependencyHintDetails(cranDeps, biocDeps []string, sourceDeps map[string]project.SourceSpec) []SystemHintDetail {
	all := mergeDeps(cranDeps, biocDeps, sourceDepNames(sourceDeps))
	if len(all) == 0 {
		return []SystemHintDetail{}
	}

	depSet := make(map[string]struct{}, len(all))
	for _, dep := range all {
		depSet[dep] = struct{}{}
	}

	type rule struct {
		category string
		packages []string
		message  string
	}
	rules := []rule{
		{
			category: "network",
			packages: []string{"curl", "openssl", "gert", "git2r", "httr", "httr2", "gitcreds", "gh"},
			message:  "commonly need libcurl and OpenSSL development headers, especially when source installs are required",
		},
		{
			category: "icu",
			packages: []string{"stringi"},
			message:  "commonly needs ICU development libraries when binaries are unavailable",
		},
		{
			category: "xml",
			packages: []string{"xml2", "XML"},
			message:  "commonly need libxml2 development headers",
		},
		{
			category: "geospatial",
			packages: []string{"sf", "terra", "units", "lwgeom", "s2", "rgdal", "rgeos"},
			message:  "commonly need geospatial system libraries such as GDAL, GEOS, PROJ, and sometimes udunits2",
		},
		{
			category: "java",
			packages: []string{"rJava"},
			message:  "needs a working Java/JDK toolchain and JVM headers",
		},
		{
			category: "database",
			packages: []string{"odbc", "RPostgres", "RMySQL", "RMariaDB"},
			message:  "commonly need database client development libraries such as unixODBC, libpq, or MariaDB/MySQL client headers",
		},
		{
			category: "javascript",
			packages: []string{"V8"},
			message:  "may need V8 or Node.js development libraries when binaries are unavailable",
		},
		{
			category: "imaging",
			packages: []string{"magick"},
			message:  "commonly needs ImageMagick or Magick++ development libraries",
		},
		{
			category: "fonts",
			packages: []string{"textshaping", "ragg", "systemfonts", "gdtools", "svglite"},
			message:  "commonly need font and text rendering libraries such as freetype, harfbuzz, fribidi, and cairo",
		},
		{
			category: "pdf",
			packages: []string{"pdftools", "qpdf"},
			message:  "commonly need poppler and qpdf system libraries",
		},
		{
			category: "cpp",
			packages: []string{"arrow"},
			message:  "may need a C++ toolchain and Arrow/Parquet build dependencies if a matching binary is unavailable",
		},
	}

	hints := make([]SystemHintDetail, 0, len(rules))
	for _, rule := range rules {
		matched := make([]string, 0, len(rule.packages))
		for _, pkg := range rule.packages {
			if _, ok := depSet[pkg]; ok {
				matched = append(matched, pkg)
			}
		}
		if len(matched) == 0 {
			continue
		}
		hints = append(hints, SystemHintDetail{
			Category: rule.category,
			Packages: append([]string(nil), matched...),
			Message:  rule.message,
		})
	}
	if runtime.GOOS == "windows" && len(sourceDeps) > 0 {
		packages := sourceDepNames(sourceDeps)
		if len(packages) > 0 {
			hints = append(hints, SystemHintDetail{
				Category: "toolchain",
				Packages: packages,
				Message:  "Windows source-based custom packages may require Rtools; install it and ensure make.exe and gcc.exe are on PATH before retrying compilation-heavy installs",
			})
		}
	}
	return hints
}

func renderSystemHints(details []SystemHintDetail) []string {
	if len(details) == 0 {
		return []string{}
	}

	hints := make([]string, 0, len(details))
	for _, detail := range details {
		label := "packages " + strings.Join(detail.Packages, ", ")
		if len(detail.Packages) == 1 {
			label = "package " + detail.Packages[0]
		}
		hints = append(hints, label+" "+detail.Message)
	}
	return hints
}

func copySystemHintDetails(details []SystemHintDetail) []SystemHintDetail {
	if len(details) == 0 {
		return []SystemHintDetail{}
	}

	out := make([]SystemHintDetail, 0, len(details))
	for _, detail := range details {
		out = append(out, SystemHintDetail{
			Category: detail.Category,
			Packages: append([]string(nil), detail.Packages...),
			Message:  detail.Message,
		})
	}
	return out
}

func copyNextStepDetails(details []NextStepDetail) []NextStepDetail {
	if len(details) == 0 {
		return []NextStepDetail{}
	}

	out := make([]NextStepDetail, 0, len(details))
	for _, detail := range details {
		out = append(out, NextStepDetail{
			Category: detail.Category,
			Kind:     detail.Kind,
			Message:  detail.Message,
			Command:  detail.Command,
			Note:     detail.Note,
			Preset:   detail.Preset,
			Blocking: detail.Blocking,
		})
	}
	return out
}

func doctorStatus(errorsList, warnings []string, systemHintDetails []SystemHintDetail) string {
	if len(errorsList) > 0 {
		return "error"
	}
	if len(warnings) > 0 || len(systemHintDetails) > 0 {
		return "warning"
	}
	return "ok"
}

func buildDoctorSummary(errorsList, warnings []string, systemHintDetails []SystemHintDetail, nextSteps []NextStepDetail, setupErrors, sourceErrors, networkErrors, runtimeErrors, otherErrors, lockWarnings, cacheWarnings, otherWarnings []string) DoctorSummary {
	blockingCount := 0
	for _, step := range nextSteps {
		if step.Blocking {
			blockingCount++
		}
	}

	return DoctorSummary{
		ErrorCount:            len(errorsList),
		WarningCount:          len(warnings),
		SystemHintCount:       len(systemHintDetails),
		NextStepCount:         len(nextSteps),
		BlockingNextStepCount: blockingCount,
		SetupErrorCount:       len(setupErrors),
		SourceErrorCount:      len(sourceErrors),
		NetworkErrorCount:     len(networkErrors),
		RuntimeErrorCount:     len(runtimeErrors),
		OtherErrorCount:       len(otherErrors),
		LockWarningCount:      len(lockWarnings),
		CacheWarningCount:     len(cacheWarnings),
		OtherWarningCount:     len(otherWarnings),
	}
}

func formatDoctorSummary(report DoctorReport) string {
	return fmt.Sprintf(
		"status=%s | errors=%d | warnings=%d | hints=%d | next=%d | blocking_next=%d",
		report.Status,
		report.Summary.ErrorCount,
		report.Summary.WarningCount,
		report.Summary.SystemHintCount,
		report.Summary.NextStepCount,
		report.Summary.BlockingNextStepCount,
	)
}

func doctorStrictError(report DoctorReport) DoctorError {
	issues := []string{
		fmt.Sprintf("strict mode requires doctor status=ok, got %s", report.Status),
	}
	if report.Summary.ErrorCount > 0 {
		issues = append(issues, fmt.Sprintf("errors: %d", report.Summary.ErrorCount))
	}
	if report.Summary.WarningCount > 0 {
		issues = append(issues, fmt.Sprintf("warnings: %d", report.Summary.WarningCount))
	}
	if report.Summary.SystemHintCount > 0 {
		issues = append(issues, fmt.Sprintf("system hints: %d", report.Summary.SystemHintCount))
	}
	if report.Summary.BlockingNextStepCount > 0 {
		issues = append(issues, fmt.Sprintf("blocking next steps: %d", report.Summary.BlockingNextStepCount))
	}
	return DoctorError{Issues: issues, Code: 2}
}

func buildDoctorNextSteps(plan dependencyPlan, rscriptErr error, needsGit bool, warnings, errorsList []string, systemHintDetails []SystemHintDetail) []NextStepDetail {
	steps := []NextStepDetail{}
	seen := map[string]struct{}{}
	add := func(step NextStepDetail) {
		if step.Kind == "" || step.Message == "" {
			return
		}
		key := step.Kind + "\x00" + step.Message + "\x00" + step.Command + "\x00" + step.Preset + "\x00" + strconv.FormatBool(step.Blocking)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		steps = append(steps, step)
	}

	addToolchainFollowups := func(blocking bool) {
		add(NextStepDetail{
			Category: "setup",
			Kind:     "detect_toolchain",
			Message:  "detect common rootless toolchain layouts on this machine before choosing prefixes to wire into rs",
			Command:  "rs toolchain detect",
			Blocking: false,
		})
		add(NextStepDetail{
			Category: "setup",
			Kind:     "validate_toolchain_only",
			Message:  "re-run the toolchain-only doctor after updating toolchain_prefixes/pkg_config_path or exporting RS_TOOLCHAIN_PREFIXES/RS_PKG_CONFIG_PATH",
			Command:  doctorToolchainOnlyCommand(plan),
			Blocking: false,
		})
		addRecommendedToolchainFollowups(plan, blocking, add)
	}

	if rscriptErr != nil {
		advice := rManagerBootstrapAdviceFor(plan.RequestedR)
		if shouldSuggestNativeBootstrap(plan.RequestedR) {
			add(NextStepDetail{
				Category: "setup",
				Kind:     "install_r",
				Message:  advice.ManualMessageWithCommand(),
				Blocking: true,
			})
			add(NextStepDetail{
				Category: "setup",
				Kind:     "auto_install_r",
				Message:  "explicitly allow rs to install R automatically and rerun",
				Command:  fmt.Sprintf("%s=1 rs run %s", advice.AutoEnableEnv, plan.ScriptPath),
				Blocking: true,
			})
		}
		add(NextStepDetail{
			Category: "setup",
			Kind:     "configure_r",
			Message:  "install R and make sure Rscript is available on PATH, or set rs.toml rscript manually before rerunning rs doctor or rs run",
			Blocking: true,
		})
	}

	if needsGit {
		for _, issue := range errorsList {
			if issue == "git is required for git sources but is not available on PATH" {
				add(NextStepDetail{
					Category: "setup",
					Kind:     "install_git",
					Message:  "install git and make sure it is available on PATH before resolving git-based package sources",
					Blocking: true,
				})
				break
			}
		}
	}

	for _, issue := range errorsList {
		switch {
		case strings.HasPrefix(issue, "toolchain prefix "),
			strings.HasPrefix(issue, "pkg-config path "),
			strings.HasPrefix(issue, "could not inspect toolchain prefix "),
			strings.HasPrefix(issue, "could not inspect pkg-config path "):
			add(NextStepDetail{
				Category: "setup",
				Kind:     "fix_toolchain_config",
				Message:  "fix toolchain_prefixes/pkg_config_path so they point at real user-local dependency directories, or export RS_TOOLCHAIN_PREFIXES/RS_PKG_CONFIG_PATH before retrying native builds",
				Blocking: true,
			})
			addToolchainFollowups(true)
		case strings.Contains(issue, "requires environment variable "):
			add(NextStepDetail{
				Category: "network",
				Kind:     "set_env_var",
				Message:  "set the required source authentication environment variable and rerun rs doctor",
				Blocking: true,
			})
		case strings.Contains(issue, "missing repo"),
			strings.Contains(issue, "missing url"),
			strings.Contains(issue, "missing path"),
			strings.Contains(issue, "missing type"),
			strings.Contains(issue, "unsupported type"):
			add(NextStepDetail{
				Category: "source",
				Kind:     "fix_source_config",
				Message:  "fix the custom source definition in rs.toml before retrying dependency resolution",
				Blocking: true,
			})
		case strings.HasPrefix(issue, "local source "),
			strings.HasPrefix(issue, "git source "):
			add(NextStepDetail{
				Category: "source",
				Kind:     "restore_source_path",
				Message:  "restore the referenced local source path or update rs.toml to point at an existing location",
				Blocking: true,
			})
		}
	}

	for _, warning := range warnings {
		switch {
		case strings.HasPrefix(warning, "lockfile not found: "):
			add(NextStepDetail{
				Category: "lock",
				Kind:     "create_lockfile",
				Message:  "create a lockfile and install the resolved dependencies",
				Command:  fmt.Sprintf("rs lock %s", plan.ScriptPath),
				Blocking: false,
			})
		case strings.HasPrefix(warning, "managed library directory does not exist yet: "):
			add(NextStepDetail{
				Category: "cache",
				Kind:     "materialize_library",
				Message:  "materialize the managed library for this script",
				Command:  fmt.Sprintf("rs run %s", plan.ScriptPath),
				Blocking: false,
			})
		case strings.HasPrefix(warning, "pkg-config is not available on PATH; "):
			add(NextStepDetail{
				Category: "setup",
				Kind:     "install_pkg_config",
				Message:  "install pkg-config in a user-local prefix such as enva, Homebrew-in-home, micromamba, mamba, conda, or Spack, then expose it through toolchain_prefixes or PATH",
				Blocking: false,
			})
			addToolchainFollowups(false)
		}
	}

	if len(systemHintDetails) > 0 && len(plan.ToolchainPrefixes) == 0 && len(plan.PkgConfigPath) == 0 {
		addToolchainFollowups(false)
	}

	for _, detail := range systemHintDetails {
		pkgLabel := strings.Join(detail.Packages, ", ")
		add(NextStepDetail{
			Category: "system_dependency",
			Kind:     "install_system_dependency",
			Message:  fmt.Sprintf("install the OS libraries or toolchain needed for %s packages (%s) before retrying source installs", detail.Category, pkgLabel),
			Blocking: false,
		})
	}
	if plan.Runtime.InterpreterKind == "external-conda" {
		target := firstNonEmpty(plan.Runtime.RVersion, plan.RequestedRVersion, "4.4")
		add(NextStepDetail{
			Category: "setup",
			Kind:     "switch_to_managed_r",
			Message:  "switch to a managed rs R if source package installs fail under the current external Conda-style interpreter",
			Command:  fmt.Sprintf("rs r install %s && rs r use %s", target, target),
			Blocking: false,
		})
	}

	if len(steps) == 0 && len(errorsList) == 0 && len(warnings) == 0 && strings.TrimSpace(plan.ScriptPath) != "" {
		add(NextStepDetail{
			Category: "run",
			Kind:     "run_script",
			Message:  "the environment looks healthy; run the script in its managed library",
			Command:  fmt.Sprintf("rs run %s", plan.ScriptPath),
			Blocking: false,
		})
	}

	return steps
}

func doctorToolchainOnlyCommand(plan dependencyPlan) string {
	switch {
	case strings.TrimSpace(plan.ScriptPath) != "":
		return fmt.Sprintf("rs doctor --toolchain-only %s", plan.ScriptPath)
	case strings.TrimSpace(plan.ProjectPath) != "":
		return fmt.Sprintf("rs doctor --toolchain-only %s", filepath.Dir(plan.ProjectPath))
	default:
		return "rs doctor --toolchain-only"
	}
}

func addRecommendedToolchainFollowups(plan dependencyPlan, blocking bool, add func(NextStepDetail)) {
	candidate, err := toolchainenv.RecommendedCandidate("")
	if err != nil || candidate == nil {
		return
	}
	setupMessage := candidate.SuggestedSetupNote
	if strings.TrimSpace(setupMessage) == "" {
		setupMessage = fmt.Sprintf("prepare the recommended %s toolchain prefix on this machine", candidate.Preset)
	}
	add(NextStepDetail{
		Category: "setup",
		Kind:     "setup_detected_toolchain",
		Message:  setupMessage,
		Command:  candidate.SuggestedSetupCommand,
		Note:     candidate.SuggestedSetupNote,
		Preset:   candidate.Preset,
		Blocking: blocking,
	})
	add(NextStepDetail{
		Category: "setup",
		Kind:     "init_detected_toolchain",
		Message:  fmt.Sprintf("write the recommended %s toolchain preset into project config", candidate.Preset),
		Command:  candidate.SuggestedInitCommand,
		Note:     fmt.Sprintf("detected recommended preset on this machine: %s", candidate.Preset),
		Preset:   candidate.Preset,
		Blocking: blocking,
	})
}

func shouldSuggestNativeBootstrap(requestedR string) bool {
	requestedR = strings.TrimSpace(requestedR)
	if requestedR == "" || strings.EqualFold(requestedR, "Rscript") || strings.EqualFold(requestedR, "Rscript.exe") {
		return true
	}
	return rmanager.LooksLikeVersionSpec(requestedR)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func collectProjectScriptPaths(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		return nil, fmt.Errorf("walk project scripts: %w", err)
	}
	slices.Sort(out)
	return out, nil
}

func collectKeepByCacheRoot(scriptPath, projectDir string) (map[string]map[string]struct{}, string, string, string, int, error) {
	keepByCacheRoot := map[string]map[string]struct{}{}
	if scriptPath != "" {
		plan, err := resolveDependencyPlan(scriptPath, nil, nil, nil, "", "", "")
		if err != nil {
			return nil, "", "", "", 0, err
		}
		keepByCacheRoot[plan.CacheRoot] = map[string]struct{}{
			plan.LibraryPath: {},
		}
		return keepByCacheRoot, "script " + plan.ScriptPath, filepath.Dir(plan.ProjectPath), plan.ProjectPath, 1, nil
	}

	if projectDir == "" {
		projectDir = "."
	}
	projectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, "", "", "", 0, fmt.Errorf("resolve project dir: %w", err)
	}
	projectCfg, found, err := project.Discover(projectDir)
	if err != nil {
		return nil, "", "", "", 0, fmt.Errorf("discover project config: %w", err)
	}
	if !found || projectCfg.Path == "" {
		return nil, "", "", "", 0, fmt.Errorf("no %s found from %s\npass a script path or run from a project with rs.toml", project.ConfigFileName, projectDir)
	}
	scriptPaths, err := collectProjectScriptPaths(projectCfg.RootDir)
	if err != nil {
		return nil, "", "", "", 0, err
	}
	for _, path := range scriptPaths {
		plan, err := resolveDependencyPlan(path, nil, nil, nil, "", "", "")
		if err != nil {
			return nil, "", "", "", 0, err
		}
		if keepByCacheRoot[plan.CacheRoot] == nil {
			keepByCacheRoot[plan.CacheRoot] = map[string]struct{}{}
		}
		keepByCacheRoot[plan.CacheRoot][plan.LibraryPath] = struct{}{}
	}
	return keepByCacheRoot, "project " + projectCfg.RootDir, projectCfg.RootDir, projectCfg.Path, len(scriptPaths), nil
}

func pruneCacheRoot(cacheRoot string, keep map[string]struct{}, dryRun bool) (pruneSummary, error) {
	summary := pruneSummary{
		CacheRoot: cacheRoot,
		Kept:      []string{},
		Removed:   []string{},
	}
	libRoot := filepath.Join(cacheRoot, "lib")
	entries, err := os.ReadDir(libRoot)
	if errors.Is(err, os.ErrNotExist) {
		return summary, nil
	}
	if err != nil {
		return summary, fmt.Errorf("read cache libraries: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || !isManagedLibraryDir(entry.Name()) {
			continue
		}
		path := filepath.Join(libRoot, entry.Name())
		if _, ok := keep[path]; ok {
			summary.Kept = append(summary.Kept, path)
			continue
		}
		if !dryRun {
			if err := os.RemoveAll(path); err != nil {
				return summary, fmt.Errorf("remove managed library %s: %w", path, err)
			}
		}
		summary.Removed = append(summary.Removed, path)
	}

	slices.Sort(summary.Kept)
	slices.Sort(summary.Removed)
	return summary, nil
}

func listManagedLibraries(cacheRoot string, active map[string]struct{}) ([]CacheLibrary, error) {
	libRoot := filepath.Join(cacheRoot, "lib")
	entries, err := os.ReadDir(libRoot)
	if errors.Is(err, os.ErrNotExist) {
		return []CacheLibrary{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cache libraries: %w", err)
	}

	out := make([]CacheLibrary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !isManagedLibraryDir(entry.Name()) {
			continue
		}
		path := filepath.Join(libRoot, entry.Name())
		_, isActive := active[path]
		out = append(out, CacheLibrary{
			Path:   path,
			Name:   entry.Name(),
			Active: isActive,
		})
	}
	slices.SortFunc(out, func(a, b CacheLibrary) int {
		return strings.Compare(a.Path, b.Path)
	})
	return out, nil
}

func isManagedLibraryDir(name string) bool {
	if len(name) != 16 {
		return false
	}
	for _, ch := range name {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func resolveManagedLibraryTarget(opts CacheRemoveOptions) (string, string, error) {
	target := strings.TrimSpace(opts.Target)
	if looksLikePath(target) {
		path, cacheRoot, err := validateManagedLibraryPath(target)
		if err != nil {
			return "", "", err
		}
		return path, cacheRoot, nil
	}
	if !isManagedLibraryDir(target) {
		return "", "", fmt.Errorf("cache target %q is not a managed library hash", target)
	}

	cacheRoot, err := resolveCacheRootForLookup(opts.CacheDir, opts.ScriptPath, opts.ProjectDir)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(cacheRoot, "lib", target), cacheRoot, nil
}

func resolveCacheRootForLookup(cacheDirOverride, scriptPath, projectDir string) (string, error) {
	if cacheDirOverride != "" {
		cacheRoot, err := filepath.Abs(cacheDirOverride)
		if err != nil {
			return "", fmt.Errorf("resolve cache dir: %w", err)
		}
		return cacheRoot, nil
	}
	if scriptPath != "" {
		plan, err := resolveDependencyPlan(scriptPath, nil, nil, nil, "", "", "")
		if err != nil {
			return "", err
		}
		return plan.CacheRoot, nil
	}
	if projectDir != "" {
		projectDir, err := filepath.Abs(projectDir)
		if err != nil {
			return "", fmt.Errorf("resolve project dir: %w", err)
		}
		projectCfg, found, err := project.Discover(projectDir)
		if err != nil {
			return "", fmt.Errorf("discover project config: %w", err)
		}
		if !found || projectCfg.Path == "" {
			return "", fmt.Errorf("no %s found from %s", project.ConfigFileName, projectDir)
		}
		if projectCfg.Defaults.CacheDir != "" {
			return projectCfg.Defaults.CacheDir, nil
		}
	}
	return predictedCacheRoot(""), nil
}

func validateManagedLibraryPath(target string) (string, string, error) {
	path, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve target path: %w", err)
	}
	path = filepath.Clean(path)
	if !isManagedLibraryDir(filepath.Base(path)) {
		return "", "", fmt.Errorf("cache target %q is not a managed library path", target)
	}
	libRoot := filepath.Dir(path)
	if filepath.Base(libRoot) != "lib" {
		return "", "", fmt.Errorf("cache target %q is not inside a managed lib directory", target)
	}
	return path, filepath.Dir(libRoot), nil
}

func looksLikePath(target string) bool {
	return filepath.IsAbs(target) || strings.ContainsRune(target, filepath.Separator)
}

func ResolveRscriptPath(override, configValue string) (string, error) {
	selected := firstNonEmpty(override, configValue, "Rscript")
	if looksLikePath(selected) {
		path := selected
		if !filepath.IsAbs(path) {
			absPath, err := filepath.Abs(path)
			if err != nil {
				return "", fmt.Errorf("resolve Rscript path %q: %w", selected, err)
			}
			path = absPath
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("%s: %w", missingRscriptMessage(override, selected), err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s: %s is a directory", missingRscriptMessage(override, selected), path)
		}
		return path, nil
	}

	path, err := exec.LookPath(selected)
	if err != nil {
		return "", fmt.Errorf("%s: %w", missingRscriptMessage(override, selected), err)
	}
	return path, nil
}

func resolveConfiguredInterpreterPath(override, configValue string) (string, error) {
	if strings.TrimSpace(override) == "" && strings.TrimSpace(configValue) == "" {
		if current, err := resolveCurrentManagedRscript(); err == nil {
			return current, nil
		}
	}
	selected := firstNonEmpty(override, configValue, "Rscript")
	if !looksLikePath(selected) &&
		!strings.EqualFold(selected, "Rscript") &&
		!strings.EqualFold(selected, "Rscript.exe") &&
		rmanager.LooksLikeVersionSpec(selected) {
		return resolveManagedRscript(selected)
	}
	return ResolveRscriptPath(override, configValue)
}

func resolveRunnableRscriptPath(override, configValue string, stderr io.Writer) (string, error) {
	interpreter, err := resolveSelectedRscript(override, configValue)
	if err == nil {
		return interpreter, nil
	}

	selected := firstNonEmpty(override, configValue, "Rscript")
	if looksLikePath(selected) {
		return "", err
	}
	if !strings.EqualFold(selected, "Rscript") && !strings.EqualFold(selected, "Rscript.exe") && !rmanager.LooksLikeVersionSpec(selected) {
		return "", err
	}
	if !autoInstallR() {
		advice := rManagerBootstrapAdviceFor(selected)
		return "", fmt.Errorf("%v\nnext step: %s\nexplicit auto-install: set %s=1 and retry", err, advice.ManualMessageWithCommand(), advice.AutoEnableEnv)
	}

	managed, managedErr := ensureManagedRscript(selected, stderr)
	if managedErr != nil {
		return "", fmt.Errorf("%v; automatic R installation failed: %w", err, managedErr)
	}
	return managed, nil
}

func selectInterpreterTarget(override, configuredPath, configuredVersion string) (string, string) {
	switch {
	case override != "":
		requestedVersion := ""
		if !looksLikePath(override) && rmanager.LooksLikeVersionSpec(override) {
			requestedVersion = override
		}
		return override, requestedVersion
	case configuredPath != "":
		return configuredPath, configuredVersion
	case configuredVersion != "":
		return configuredVersion, configuredVersion
	default:
		return "", ""
	}
}

func resolveInterpreterSelection(override, configuredPath, configuredVersion, workDir string, stderr io.Writer, autoInstall bool) interpreterSelection {
	selected, requestedVersion := selectInterpreterTarget(override, configuredPath, configuredVersion)
	result := interpreterSelection{
		Selected:     selected,
		Requested:    selected,
		RequestedVer: requestedVersion,
	}

	var (
		interpreter string
		err         error
	)
	if autoInstall {
		interpreter, err = resolveRunnableRscriptPath("", selected, stderr)
	} else {
		interpreter, err = resolveSelectedRscript("", selected)
	}
	if err != nil {
		result.Issue = err
		return result
	}
	result.Interpreter = interpreter

	runtime, err := inspectRuntimeWithInterpreter(interpreter, workDir, stderr)
	if err != nil {
		result.Issue = err
		return result
	}
	result.Runtime = runtime
	if requestedVersion != "" && !rmanager.VersionMatchesSpec(requestedVersion, runtime.RVersion) {
		result.Issue = fmt.Errorf("configured r_version %q does not match selected interpreter runtime %s", requestedVersion, runtime.RVersion)
	}
	return result
}

func resolveRShellPath(rscriptPath string) (string, error) {
	if looksLikePath(rscriptPath) {
		dir := filepath.Dir(rscriptPath)
		base := filepath.Base(rscriptPath)
		switch base {
		case "Rscript":
			candidate := filepath.Join(dir, "R")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		case "Rscript.exe":
			for _, candidate := range []string{
				filepath.Join(dir, "Rterm.exe"),
				filepath.Join(dir, "R.exe"),
				filepath.Join(dir, "x64", "Rterm.exe"),
				filepath.Join(dir, "x64", "R.exe"),
			} {
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate, nil
				}
			}
		}
	}

	interpreter, err := exec.LookPath("R")
	if err != nil {
		return "", fmt.Errorf("find R: %w", err)
	}
	return interpreter, nil
}

func missingRscriptMessage(override, selected string) string {
	switch {
	case override != "":
		return fmt.Sprintf("requested Rscript %q is not available", override)
	case selected != "" && selected != "Rscript":
		return fmt.Sprintf("configured Rscript %q is not available", selected)
	default:
		return "Rscript is not available on PATH"
	}
}

func ScanScript(path string) ([]string, error) {
	return rdeps.FromFile(path)
}

func resolveEnvironment(opts RunOptions) (ResolvedEnvironment, error) {
	info, err := os.Stat(opts.ScriptPath)
	if err != nil {
		return ResolvedEnvironment{}, fmt.Errorf("stat script: %w", err)
	}
	if info.IsDir() {
		return ResolvedEnvironment{}, fmt.Errorf("%s is a directory, expected an R script", opts.ScriptPath)
	}

	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	progresscmd.Stage(opts.Stderr, "discovering project config")
	projectCfg, _, err := project.Discover(filepath.Dir(opts.ScriptPath))
	if err != nil {
		return ResolvedEnvironment{}, fmt.Errorf("discover project config: %w", err)
	}

	progresscmd.Stage(opts.Stderr, "resolving script configuration")
	scriptCfg, err := projectCfg.ResolveForScript(opts.ScriptPath)
	if err != nil {
		return ResolvedEnvironment{}, fmt.Errorf("resolve script config: %w", err)
	}

	repo := firstNonEmpty(opts.Repo, scriptCfg.Repo, "https://cloud.r-project.org")

	progresscmd.Stage(opts.Stderr, "scanning script dependencies")
	detectedDeps, err := ScanScript(opts.ScriptPath)
	if err != nil {
		return ResolvedEnvironment{}, fmt.Errorf("scan script: %w", err)
	}
	cranDeps, biocDeps := resolveManagedPackageSets(detectedDeps, scriptCfg.Packages, scriptCfg.BiocPackages, opts.ExtraDeps, opts.ExtraBiocDeps, opts.ExcludeDeps)
	sourceDeps := selectSourceDeps(scriptCfg.Sources, cranDeps, biocDeps)
	if err := validateSourceDeps(sourceDeps); err != nil {
		return ResolvedEnvironment{}, err
	}
	cranDeps = filterManagedDeps(cranDeps, sourceDeps, biocDeps)
	biocDeps = filterBiocDeps(biocDeps, sourceDeps)

	cacheOverride := firstNonEmpty(opts.CacheDir, scriptCfg.CacheDir)
	cacheRoot, err := resolveCacheRoot(cacheOverride)
	if err != nil {
		return ResolvedEnvironment{}, err
	}

	lockfilePath := scriptCfg.Lockfile
	if lockfilePath == "" {
		lockfilePath = defaultLockfilePath(projectCfg.RootDir, opts.ScriptPath)
	}

	progresscmd.Stage(opts.Stderr, "resolving interpreter and managed library")
	selection := resolveInterpreterSelection(opts.RscriptPath, scriptCfg.Rscript, scriptCfg.RVersion, filepath.Dir(opts.ScriptPath), opts.Stderr, opts.AutoInstallR)
	if selection.Issue != nil {
		return ResolvedEnvironment{}, selection.Issue
	}
	interpreter := selection.Interpreter
	runtime := selection.Runtime
	libPath, err := resolveLibraryPath(cacheRoot, opts.ScriptPath, cranDeps, biocDeps, sourceDeps, repo, runtime)
	if err != nil {
		return ResolvedEnvironment{}, err
	}

	bootstrapPath, err := writeBootstrap(cacheRoot)
	if err != nil {
		return ResolvedEnvironment{}, err
	}

	env := ResolvedEnvironment{
		ScriptPath:        opts.ScriptPath,
		ScriptArgs:        opts.ScriptArgs,
		Repo:              repo,
		CacheRoot:         cacheRoot,
		LibraryPath:       libPath,
		BootstrapPath:     bootstrapPath,
		LockfilePath:      lockfilePath,
		Interpreter:       interpreter,
		Runtime:           runtime,
		DetectedDeps:      detectedDeps,
		CRANDeps:          cranDeps,
		BiocDeps:          biocDeps,
		SourceDeps:        sourceDeps,
		ToolchainPrefixes: append([]string(nil), scriptCfg.ToolchainPrefixes...),
		PkgConfigPath:     append([]string(nil), scriptCfg.PkgConfigPath...),
		ProjectConfig:     projectCfg,
		ScriptConfig:      scriptCfg,
		Verbose:           opts.Verbose,
		Stdout:            opts.Stdout,
		Stderr:            opts.Stderr,
	}

	if opts.Verbose {
		printEnvironment(env)
	}

	return env, nil
}

func resolveCacheRoot(override string) (string, error) {
	if override != "" {
		if err := os.MkdirAll(override, 0o755); err != nil {
			return "", fmt.Errorf("create cache dir: %w", err)
		}
		return override, nil
	}

	if home := os.Getenv("RS_HOME"); home != "" {
		cacheDir := filepath.Join(home, "cache")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return "", fmt.Errorf("create cache dir: %w", err)
		}
		return cacheDir, nil
	}

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}

	cacheDir := filepath.Join(userCacheDir, "rs")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}
	return cacheDir, nil
}

func predictedCacheRoot(override string) string {
	if override != "" {
		return override
	}
	if home := os.Getenv("RS_HOME"); home != "" {
		return filepath.Join(home, "cache")
	}
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userCacheDir, "rs")
}

func resolveLibraryPath(cacheRoot, scriptPath string, cranDeps, biocDeps []string, sourceDeps map[string]project.SourceSpec, repo string, runtime RuntimeMetadata) (string, error) {
	libPath := predictedLibraryPath(cacheRoot, scriptPath, cranDeps, biocDeps, sourceDeps, repo, runtime)
	if err := os.MkdirAll(libPath, 0o755); err != nil {
		return "", fmt.Errorf("create library path: %w", err)
	}
	return libPath, nil
}

func predictedLibraryPath(cacheRoot, scriptPath string, cranDeps, biocDeps []string, sourceDeps map[string]project.SourceSpec, repo string, runtime RuntimeMetadata) string {
	scriptHash := sha256.Sum256([]byte(strings.Join([]string{
		"script=" + scriptPath,
		"repo=" + repo,
		"cran=" + strings.Join(cranDeps, ","),
		"bioc=" + strings.Join(biocDeps, ","),
		"sources=" + fingerprintSourceDeps(sourceDeps),
		"interpreter=" + runtime.Interpreter,
		"r_version=" + runtime.RVersion,
		"platform=" + runtime.Platform,
		"arch=" + runtime.Arch,
		"os=" + runtime.OS,
		"package_type=" + runtime.PackageType,
	}, "\n")))
	scope := hex.EncodeToString(scriptHash[:])[:16]
	return filepath.Join(cacheRoot, "lib", scope)
}

func mergeDeps(groups ...[]string) []string {
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, dep := range group {
			if dep == "" {
				continue
			}
			seen[dep] = struct{}{}
		}
	}

	deps := make([]string, 0, len(seen))
	for dep := range seen {
		deps = append(deps, dep)
	}
	slices.Sort(deps)
	return deps
}

func resolveManagedPackageSets(detectedDeps, configuredCRAN, configuredBioc, extraCRAN, extraBioc, excludeDeps []string) ([]string, []string) {
	requested := mergeDeps(detectedDeps, configuredCRAN, extraCRAN)
	autoCRAN, autoBioc := rdeps.SplitBiocPackages(requested)
	bioc := mergeDeps(autoBioc, configuredBioc, extraBioc)
	return filterDeps(autoCRAN, excludeDeps), filterDeps(bioc, excludeDeps)
}

func filterDeps(deps, excluded []string) []string {
	if len(deps) == 0 || len(excluded) == 0 {
		return deps
	}
	excludeSet := make(map[string]struct{}, len(excluded))
	for _, dep := range excluded {
		excludeSet[dep] = struct{}{}
	}
	filtered := make([]string, 0, len(deps))
	for _, dep := range deps {
		if _, ok := excludeSet[dep]; ok {
			continue
		}
		filtered = append(filtered, dep)
	}
	return filtered
}

func printAppliedAdjustments(w io.Writer, prefix string, includeCRAN, includeBioc, excluded []string) {
	if w == nil {
		return
	}
	if len(includeCRAN) == 0 && len(includeBioc) == 0 {
		fmt.Fprintf(w, "%sincluded packages: <none>\n", prefix)
	} else {
		parts := []string{}
		if len(includeCRAN) > 0 {
			parts = append(parts, "CRAN="+strings.Join(includeCRAN, ", "))
		}
		if len(includeBioc) > 0 {
			parts = append(parts, "Bioconductor="+strings.Join(includeBioc, ", "))
		}
		fmt.Fprintf(w, "%sincluded packages: %s\n", prefix, strings.Join(parts, " | "))
	}
	if len(excluded) == 0 {
		fmt.Fprintf(w, "%sexcluded packages: <none>\n", prefix)
		return
	}
	fmt.Fprintf(w, "%sexcluded packages: %s\n", prefix, strings.Join(excluded, ", "))
}

func EnsureInstalled(env ResolvedEnvironment) error {
	backend := installBackend()
	switch backend {
	case "native":
		return ensureInstalledNative(env)
	case "pak":
		return bootstrapInstall(env, backend)
	case "auto":
		return ensureInstalledNative(env)
	default:
		return fmt.Errorf("unsupported install backend %s", backend)
	}
}

func ensureInstalledNative(env ResolvedEnvironment) error {
	req, err := installerRequestFromEnvironment(env, env.Stdout, env.Stderr)
	if err != nil {
		return err
	}
	if err := nativeInstall(req); err != nil {
		return wrapExternalInterpreterInstallError(err, env.Runtime)
	}
	return nil
}

func installerRequestFromEnvironment(env ResolvedEnvironment, stdout, stderr io.Writer) (installer.Request, error) {
	runtime, err := InspectRuntime(env)
	if err != nil {
		return installer.Request{}, err
	}
	effectivePrefixes, effectivePkgConfig, detectedToolchain, err := effectiveToolchainConfig(env.ToolchainPrefixes, env.PkgConfigPath)
	if err != nil {
		return installer.Request{}, err
	}
	if detectedToolchain != nil && stderr != nil {
		fmt.Fprintf(stderr, "[rs] auto-detected rootless toolchain preset: %s\n", detectedToolchain.Preset)
	}
	sourceDeps := make(map[string]project.SourceSpec, len(env.SourceDeps))
	for name, spec := range env.SourceDeps {
		sourceDeps[name] = spec
	}
	return installer.Request{
		Interpreter: env.Interpreter,
		WorkDir:     filepath.Dir(env.ScriptPath),
		LibraryPath: env.LibraryPath,
		Repo:        env.Repo,
		Environment: toolchainenv.Apply(os.Environ(), effectivePrefixes, effectivePkgConfig),
		Runtime: installer.Runtime{
			RVersion: runtime.RVersion,
		},
		CRANDeps:   copyStrings(env.CRANDeps),
		BiocDeps:   copyStrings(env.BiocDeps),
		SourceDeps: sourceDeps,
		Stdout:     stdout,
		Stderr:     stderr,
	}, nil
}

func WriteLockfile(env ResolvedEnvironment) error {
	pkgs, err := InstalledPackages(env)
	if err != nil {
		return err
	}

	file := lockfile.File{
		Version:     1,
		GeneratedAt: time.Now().UTC(),
		Script:      env.ScriptPath,
		Repo:        env.Repo,
		Library:     env.LibraryPath,
		Metadata:    lockfile.Metadata{},
		Packages:    pkgs,
	}
	meta, err := InspectRuntime(env)
	if err != nil {
		return err
	}
	file.Metadata = lockfile.Metadata{
		Interpreter: meta.Interpreter,
		RVersion:    meta.RVersion,
		Platform:    meta.Platform,
		Arch:        meta.Arch,
		OS:          meta.OS,
		PackageType: meta.PackageType,
	}
	enrichLockedPackages(env, file.Packages)
	return lockfile.Write(env.LockfilePath, file)
}

func ValidateLockfile(env ResolvedEnvironment, mode ValidationMode) error {
	validation, err := ValidateLockfileInputs(env, mode)
	if err != nil {
		return err
	}
	return ValidateInstalledPackages(env, validation.Lockfile, mode)
}

func InstalledPackages(env ResolvedEnvironment) ([]lockfile.Package, error) {
	script := `
pkgs <- Filter(nzchar, strsplit(Sys.getenv("RS_ALL_DEPS", ""), ",", fixed = TRUE)[[1]])
ip <- installed.packages(
  lib.loc = .libPaths(),
  fields = c("Priority", "Repository", "RemoteType", "RemoteUsername", "RemoteRepo", "RemoteRef", "RemoteSha", "RemoteSubdir", "RemoteHost")
)
for (pkg in pkgs) {
  if (pkg %in% rownames(ip)) {
    priority <- if ("Priority" %in% colnames(ip)) ip[pkg, "Priority"] else ""
    source <- if ("Repository" %in% colnames(ip)) ip[pkg, "Repository"] else ""
    remote_type <- if ("RemoteType" %in% colnames(ip)) ip[pkg, "RemoteType"] else ""
    remote_username <- if ("RemoteUsername" %in% colnames(ip)) ip[pkg, "RemoteUsername"] else ""
    remote_repo <- if ("RemoteRepo" %in% colnames(ip)) ip[pkg, "RemoteRepo"] else ""
    remote_ref <- if ("RemoteRef" %in% colnames(ip)) ip[pkg, "RemoteRef"] else ""
    remote_sha <- if ("RemoteSha" %in% colnames(ip)) ip[pkg, "RemoteSha"] else ""
    remote_subdir <- if ("RemoteSubdir" %in% colnames(ip)) ip[pkg, "RemoteSubdir"] else ""
    remote_host <- if ("RemoteHost" %in% colnames(ip)) ip[pkg, "RemoteHost"] else ""
    cat(pkg, "\t", ip[pkg, "Version"], "\t", priority, "\t", source, "\t", remote_type, "\t", remote_username, "\t", remote_repo, "\t", remote_ref, "\t", remote_sha, "\t", remote_subdir, "\t", remote_host, "\n", sep = "")
  }
}`

	cmd := exec.Command(env.Interpreter, "-e", script)
	cmd.Stderr = env.Stderr
	cmd.Dir = filepath.Dir(env.ScriptPath)
	cmd.Env = append(runtimeEnv(env, false), "RS_ALL_DEPS="+strings.Join(allDeps(env), ","))

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect installed packages: %w", err)
	}
	metaByName, err := readInstalledSourceMetadata(env.LibraryPath)
	if err != nil {
		return nil, err
	}

	sourceByName := map[string]string{}
	for _, name := range env.CRANDeps {
		sourceByName[name] = "cran"
	}
	for _, name := range env.BiocDeps {
		sourceByName[name] = "bioconductor"
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}

	packages := make([]lockfile.Package, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		source := sourceByName[fields[0]]
		priority := ""
		if len(fields) >= 3 {
			priority = fields[2]
			if priority == "NA" {
				priority = ""
			}
			if priority == "base" || priority == "recommended" {
				source = priority
			}
		}
		if len(fields) >= 4 && fields[3] != "" && source != "base" && source != "recommended" && source != "cran" && source != "bioconductor" {
			source = fields[3]
		}
		pkg := lockfile.Package{
			Name:     fields[0],
			Version:  fields[1],
			Source:   source,
			Priority: priority,
		}
		if len(fields) >= 11 {
			remoteType := fields[4]
			remoteUsername := fields[5]
			remoteRepo := fields[6]
			remoteRef := fields[7]
			remoteSHA := fields[8]
			remoteSubdir := fields[9]
			remoteHost := fields[10]
			if remoteType == "github" {
				pkg.Source = "github"
				if remoteHost != "" && remoteHost != "NA" {
					pkg.SourceHost = remoteHost
				}
				if remoteUsername != "" && remoteRepo != "" {
					pkg.SourceLocation = remoteUsername + "/" + remoteRepo
				}
				if remoteRef != "" && remoteRef != "NA" {
					pkg.SourceRef = remoteRef
				}
				if remoteSHA != "" && remoteSHA != "NA" {
					pkg.SourceCommit = remoteSHA
				}
				if remoteSubdir != "" && remoteSubdir != "NA" {
					pkg.SourceSubdir = remoteSubdir
				}
			}
		}
		if spec, ok := env.SourceDeps[pkg.Name]; ok {
			switch spec.Type {
			case "github":
				pkg.Source = "github"
				if pkg.SourceHost == "" {
					pkg.SourceHost = spec.Host
				}
				if pkg.SourceLocation == "" {
					pkg.SourceLocation = spec.Repo
				}
				if pkg.SourceRef == "" {
					pkg.SourceRef = spec.Ref
				}
				if pkg.SourceSubdir == "" {
					pkg.SourceSubdir = spec.Subdir
				}
			case "local":
				pkg.Source = "local"
				pkg.SourceLocation = spec.Path
			}
		}
		if meta, ok := metaByName[pkg.Name]; ok {
			if meta.Source != "" {
				pkg.Source = meta.Source
			}
			if meta.SourceHost != "" {
				pkg.SourceHost = meta.SourceHost
			}
			if meta.SourceLocation != "" {
				pkg.SourceLocation = meta.SourceLocation
			}
			if meta.SourceRef != "" {
				pkg.SourceRef = meta.SourceRef
			}
			if meta.SourceCommit != "" {
				pkg.SourceCommit = meta.SourceCommit
			}
			if meta.SourceSubdir != "" {
				pkg.SourceSubdir = meta.SourceSubdir
			}
			if meta.SourceFingerprint != "" {
				pkg.SourceFingerprint = meta.SourceFingerprint
			}
			if meta.SourceFingerprintKind != "" {
				pkg.SourceFingerprintKind = meta.SourceFingerprintKind
			}
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}

func InspectRuntime(env ResolvedEnvironment) (RuntimeMetadata, error) {
	if env.Runtime.Interpreter != "" {
		return env.Runtime, nil
	}
	return inspectRuntimeWithInterpreter(env.Interpreter, filepath.Dir(env.ScriptPath), env.Stderr)
}

func ValidateLockfileInputs(env ResolvedEnvironment, mode ValidationMode) (validationContext, error) {
	file, err := lockfile.Read(env.LockfilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return validationContext{}, ValidationError{
				Mode:         mode,
				Kind:         ValidationKindMissing,
				ScriptPath:   env.ScriptPath,
				LockfilePath: env.LockfilePath,
				Issues:       []string{fmt.Sprintf("lockfile not found: %s", env.LockfilePath)},
			}
		}
		return validationContext{}, err
	}

	meta, err := InspectRuntime(env)
	if err != nil {
		return validationContext{}, err
	}

	issues := collectInputValidationIssues(env, file, meta)
	if len(issues) > 0 {
		return validationContext{}, ValidationError{
			Mode:         mode,
			Kind:         ValidationKindInputs,
			ScriptPath:   env.ScriptPath,
			LockfilePath: env.LockfilePath,
			Issues:       issues,
		}
	}

	return validationContext{
		Lockfile: file,
		Runtime:  meta,
	}, nil
}

func ValidateInstalledPackages(env ResolvedEnvironment, file lockfile.File, mode ValidationMode) error {
	actualPkgs, err := InstalledPackages(env)
	if err != nil {
		return err
	}

	issues := compareInstalledPackages(file.Packages, actualPkgs)
	if len(issues) > 0 {
		return ValidationError{
			Mode:         mode,
			Kind:         ValidationKindInstalled,
			ScriptPath:   env.ScriptPath,
			LockfilePath: env.LockfilePath,
			LibraryPath:  env.LibraryPath,
			Issues:       issues,
		}
	}
	return nil
}

func collectValidationIssues(env ResolvedEnvironment, file lockfile.File, runtime RuntimeMetadata, actualPkgs []lockfile.Package) []string {
	issues := collectInputValidationIssues(env, file, runtime)
	issues = append(issues, compareInstalledPackages(file.Packages, actualPkgs)...)
	return issues
}

func collectInputValidationIssues(env ResolvedEnvironment, file lockfile.File, runtime RuntimeMetadata) []string {
	var issues []string

	if file.Version != 1 {
		issues = append(issues, fmt.Sprintf("unsupported lockfile version %d", file.Version))
	}
	if file.Script != env.ScriptPath {
		issues = append(issues, fmt.Sprintf("script mismatch: lockfile has %s, current script is %s", file.Script, env.ScriptPath))
	}
	if file.Repo != env.Repo {
		issues = append(issues, fmt.Sprintf("repository mismatch: lockfile has %s, current repo is %s", file.Repo, env.Repo))
	}
	if file.Library != env.LibraryPath {
		issues = append(issues, fmt.Sprintf("library mismatch: lockfile has %s, current library is %s", file.Library, env.LibraryPath))
	}
	if file.Metadata.Interpreter != "" && file.Metadata.Interpreter != runtime.Interpreter {
		issues = append(issues, fmt.Sprintf("interpreter mismatch: lockfile has %s, current interpreter is %s", file.Metadata.Interpreter, runtime.Interpreter))
	}
	if file.Metadata.RVersion != "" && file.Metadata.RVersion != runtime.RVersion {
		issues = append(issues, fmt.Sprintf("R version mismatch: lockfile has %s, current runtime is %s", file.Metadata.RVersion, runtime.RVersion))
	}
	if file.Metadata.Platform != "" && file.Metadata.Platform != runtime.Platform {
		issues = append(issues, fmt.Sprintf("platform mismatch: lockfile has %s, current runtime is %s", file.Metadata.Platform, runtime.Platform))
	}
	if file.Metadata.Arch != "" && file.Metadata.Arch != runtime.Arch {
		issues = append(issues, fmt.Sprintf("arch mismatch: lockfile has %s, current runtime is %s", file.Metadata.Arch, runtime.Arch))
	}
	if file.Metadata.OS != "" && file.Metadata.OS != runtime.OS {
		issues = append(issues, fmt.Sprintf("os mismatch: lockfile has %s, current runtime is %s", file.Metadata.OS, runtime.OS))
	}
	if file.Metadata.PackageType != "" && file.Metadata.PackageType != runtime.PackageType {
		issues = append(issues, fmt.Sprintf("package type mismatch: lockfile has %s, current runtime is %s", file.Metadata.PackageType, runtime.PackageType))
	}

	issues = append(issues, collectStalenessIssues(env, file)...)
	issues = append(issues, compareLockedDependencies(env, file.Packages)...)
	issues = append(issues, compareLockedSources(env, file.Packages)...)
	return issues
}

func collectStalenessIssues(env ResolvedEnvironment, file lockfile.File) []string {
	var issues []string

	scriptInfo, err := os.Stat(env.ScriptPath)
	if err == nil && scriptInfo.ModTime().UTC().After(file.GeneratedAt) {
		issues = append(issues, fmt.Sprintf("script changed after lockfile was generated at %s", file.GeneratedAt.Format(time.RFC3339)))
	}

	if env.ProjectConfig.Path != "" {
		cfgInfo, err := os.Stat(env.ProjectConfig.Path)
		if err == nil && cfgInfo.ModTime().UTC().After(file.GeneratedAt) {
			issues = append(issues, fmt.Sprintf("project config changed after lockfile was generated at %s", file.GeneratedAt.Format(time.RFC3339)))
		}
	}

	return issues
}

func compareLockedDependencies(env ResolvedEnvironment, locked []lockfile.Package) []string {
	expected := allDeps(env)
	expectedSet := map[string]struct{}{}
	for _, dep := range expected {
		expectedSet[dep] = struct{}{}
	}

	lockedSet := map[string]struct{}{}
	for _, pkg := range locked {
		lockedSet[pkg.Name] = struct{}{}
	}

	var issues []string
	for _, dep := range expected {
		if _, ok := lockedSet[dep]; !ok {
			issues = append(issues, fmt.Sprintf("missing package in lockfile: %s", dep))
		}
	}

	lockedNames := make([]string, 0, len(lockedSet))
	for name := range lockedSet {
		lockedNames = append(lockedNames, name)
		if _, ok := expectedSet[name]; !ok {
			issues = append(issues, fmt.Sprintf("lockfile contains unexpected package: %s", name))
		}
	}
	slices.Sort(lockedNames)

	return issues
}

func compareInstalledPackages(locked, actual []lockfile.Package) []string {
	lockedByName := mapPackages(locked)
	actualByName := mapPackages(actual)

	names := make([]string, 0, len(lockedByName))
	for name := range lockedByName {
		names = append(names, name)
	}
	slices.Sort(names)

	var issues []string
	for _, name := range names {
		lockedPkg := lockedByName[name]
		actualPkg, ok := actualByName[name]
		if !ok {
			issues = append(issues, fmt.Sprintf("package not installed in managed library: %s", name))
			continue
		}
		if lockedPkg.Version != actualPkg.Version {
			issues = append(issues, fmt.Sprintf("version mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.Version, actualPkg.Version))
		}
		if lockedPkg.Source != "" && actualPkg.Source != "" && lockedPkg.Source != actualPkg.Source {
			issues = append(issues, fmt.Sprintf("source mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.Source, actualPkg.Source))
		}
		if lockedPkg.SourceHost != "" && actualPkg.SourceHost != "" && lockedPkg.SourceHost != actualPkg.SourceHost {
			issues = append(issues, fmt.Sprintf("source host mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.SourceHost, actualPkg.SourceHost))
		}
		if lockedPkg.SourceLocation != "" && actualPkg.SourceLocation != "" && lockedPkg.SourceLocation != actualPkg.SourceLocation {
			issues = append(issues, fmt.Sprintf("source location mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.SourceLocation, actualPkg.SourceLocation))
		}
		if lockedPkg.SourceRef != "" && actualPkg.SourceRef != "" && lockedPkg.SourceRef != actualPkg.SourceRef {
			issues = append(issues, fmt.Sprintf("source ref mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.SourceRef, actualPkg.SourceRef))
		}
		if lockedPkg.SourceCommit != "" && actualPkg.SourceCommit != "" && lockedPkg.SourceCommit != actualPkg.SourceCommit {
			issues = append(issues, fmt.Sprintf("source commit mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.SourceCommit, actualPkg.SourceCommit))
		}
		if lockedPkg.SourceSubdir != "" && actualPkg.SourceSubdir != "" && lockedPkg.SourceSubdir != actualPkg.SourceSubdir {
			issues = append(issues, fmt.Sprintf("source subdir mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.SourceSubdir, actualPkg.SourceSubdir))
		}
		if lockedPkg.SourceFingerprintKind != "" && actualPkg.SourceFingerprintKind != "" && lockedPkg.SourceFingerprintKind != actualPkg.SourceFingerprintKind {
			issues = append(issues, fmt.Sprintf("source fingerprint kind mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.SourceFingerprintKind, actualPkg.SourceFingerprintKind))
		}
		if lockedPkg.SourceFingerprint != "" && actualPkg.SourceFingerprint != "" && lockedPkg.SourceFingerprint != actualPkg.SourceFingerprint {
			issues = append(issues, fmt.Sprintf("source fingerprint mismatch for %s: lockfile has %s, installed is %s", name, lockedPkg.SourceFingerprint, actualPkg.SourceFingerprint))
		}
		if lockedPkg.Priority != actualPkg.Priority {
			issues = append(issues, fmt.Sprintf("priority mismatch for %s: lockfile has %s, installed is %s", name, displayOrNone(lockedPkg.Priority), displayOrNone(actualPkg.Priority)))
		}
	}
	return issues
}

func compareLockedSources(env ResolvedEnvironment, locked []lockfile.Package) []string {
	if len(env.SourceDeps) == 0 {
		return nil
	}

	lockedByName := mapPackages(locked)
	names := sourceDepNames(env.SourceDeps)
	var issues []string

	for _, name := range names {
		spec := env.SourceDeps[name]
		pkg, ok := lockedByName[name]
		if !ok {
			continue
		}
		if pkg.Source != spec.Type {
			issues = append(issues, fmt.Sprintf("source type mismatch for %s: lockfile has %s, config requires %s", name, pkg.Source, spec.Type))
		}
		switch spec.Type {
		case "github":
			if pkg.SourceHost != spec.Host {
				issues = append(issues, fmt.Sprintf("source host mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceHost), displayOrNone(spec.Host)))
			}
			if pkg.SourceLocation != spec.Repo {
				issues = append(issues, fmt.Sprintf("source location mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceLocation), spec.Repo))
			}
			if pkg.SourceRef != spec.Ref {
				issues = append(issues, fmt.Sprintf("source ref mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceRef), displayOrNone(spec.Ref)))
			}
			if pkg.SourceSubdir != spec.Subdir {
				issues = append(issues, fmt.Sprintf("source subdir mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceSubdir), displayOrNone(spec.Subdir)))
			}
		case "git":
			if pkg.SourceLocation != spec.URL {
				issues = append(issues, fmt.Sprintf("source location mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceLocation), spec.URL))
			}
			if pkg.SourceRef != spec.Ref {
				issues = append(issues, fmt.Sprintf("source ref mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceRef), displayOrNone(spec.Ref)))
			}
			if pkg.SourceSubdir != spec.Subdir {
				issues = append(issues, fmt.Sprintf("source subdir mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceSubdir), displayOrNone(spec.Subdir)))
			}
		case "local":
			if pkg.SourceLocation != spec.Path {
				issues = append(issues, fmt.Sprintf("source location mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceLocation), spec.Path))
			}
			currentKind, currentFingerprint := describeLocalSourceFingerprint(spec.Path)
			if pkg.SourceFingerprintKind != "" && pkg.SourceFingerprintKind != currentKind {
				issues = append(issues, fmt.Sprintf("source fingerprint kind mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceFingerprintKind), displayOrNone(currentKind)))
			}
			if pkg.SourceFingerprint != "" && pkg.SourceFingerprint != currentFingerprint {
				issues = append(issues, fmt.Sprintf("source fingerprint mismatch for %s: lockfile has %s, config requires %s", name, displayOrNone(pkg.SourceFingerprint), displayOrNone(currentFingerprint)))
			}
		}
	}
	return issues
}

func mapPackages(pkgs []lockfile.Package) map[string]lockfile.Package {
	byName := make(map[string]lockfile.Package, len(pkgs))
	for _, pkg := range pkgs {
		byName[pkg.Name] = pkg
	}
	return byName
}

func displayOrNone(value string) string {
	if value == "" {
		return "<none>"
	}
	return value
}

func runtimeEnv(env ResolvedEnvironment, installEnabled bool) []string {
	effectivePrefixes, effectivePkgConfig, _, err := effectiveToolchainConfig(env.ToolchainPrefixes, env.PkgConfigPath)
	if err != nil {
		effectivePrefixes = env.ToolchainPrefixes
		effectivePkgConfig = env.PkgConfigPath
	}
	base := toolchainenv.Apply(os.Environ(), effectivePrefixes, effectivePkgConfig)
	return append(base,
		"R_PROFILE_USER="+env.BootstrapPath,
		"R_LIBS_USER="+env.LibraryPath,
		"RS_LIB_PATH="+env.LibraryPath,
		"RS_REPOS="+env.Repo,
		"RS_CRAN_DEPS="+strings.Join(env.CRANDeps, ","),
		"RS_BIOC_DEPS="+strings.Join(env.BiocDeps, ","),
		"RS_SOURCE_DEPS="+encodeSourceSpecs(env.SourceDeps),
		"RS_BOOTSTRAP_FILE="+env.BootstrapPath,
		"RS_INSTALL_BACKEND="+installBackend(),
		fmt.Sprintf("RS_INSTALL_ENABLED=%t", installEnabled),
	)
}

func effectiveToolchainConfig(prefixes, pkgConfig []string) ([]string, []string, *toolchainenv.Candidate, error) {
	return toolchainenv.MergeWithDetected(prefixes, pkgConfig, "")
}

func maybeBootstrapResolvedEnvironment(env *ResolvedEnvironment) error {
	if env == nil {
		return nil
	}
	candidate, err := maybeBootstrapToolchain(env.ToolchainPrefixes, env.PkgConfigPath, env.Stdout, env.Stderr)
	if err != nil {
		return err
	}
	if candidate != nil && len(env.ToolchainPrefixes) == 0 && len(env.PkgConfigPath) == 0 {
		env.ToolchainPrefixes = append([]string(nil), candidate.ToolchainPrefixes...)
		env.PkgConfigPath = append([]string(nil), candidate.PkgConfigPath...)
	}
	return nil
}

func maybeBootstrapPlanToolchain(plan *dependencyPlan, stdout, stderr io.Writer) error {
	if plan == nil {
		return nil
	}
	candidate, err := maybeBootstrapToolchain(plan.ToolchainPrefixes, plan.PkgConfigPath, stdout, stderr)
	if err != nil {
		return err
	}
	if candidate != nil && len(plan.ToolchainPrefixes) == 0 && len(plan.PkgConfigPath) == 0 {
		plan.ToolchainPrefixes = append([]string(nil), candidate.ToolchainPrefixes...)
		plan.PkgConfigPath = append([]string(nil), candidate.PkgConfigPath...)
	}
	return nil
}

func maybeBootstrapToolchain(prefixes, pkgConfig []string, stdout, stderr io.Writer) (*toolchainenv.Candidate, error) {
	if len(toolchainenv.PrefixesFromEnv(os.Environ())) > 0 || len(toolchainenv.PkgConfigPathsFromEnv(os.Environ())) > 0 || len(prefixes) > 0 || len(pkgConfig) > 0 {
		return nil, nil
	}
	recommended, err := toolchainenv.RecommendedCandidate("")
	if err != nil {
		return nil, err
	}
	if recommended != nil && recommended.Complete {
		return recommended, nil
	}
	return toolchainenv.Bootstrap("auto", "", os.Environ(), stdout, stderr)
}

func installBackend() string {
	if backend := strings.TrimSpace(os.Getenv("RS_INSTALL_BACKEND")); backend != "" {
		return backend
	}
	return "auto"
}

func allDeps(env ResolvedEnvironment) []string {
	return mergeDeps(env.CRANDeps, env.BiocDeps, sourceDepNames(env.SourceDeps))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func defaultLockfilePath(projectRoot, scriptPath string) string {
	if projectRoot != "" {
		return filepath.Join(projectRoot, "rs.lock.json")
	}
	return filepath.Join(filepath.Dir(scriptPath), "rs.lock.json")
}

func printEnvironment(env ResolvedEnvironment) {
	effectivePrefixes, effectivePkgConfig, detectedToolchain, err := effectiveToolchainConfig(env.ToolchainPrefixes, env.PkgConfigPath)
	if err != nil {
		effectivePrefixes = env.ToolchainPrefixes
		effectivePkgConfig = env.PkgConfigPath
	}
	fmt.Fprintf(env.Stderr, "[rs] script: %s\n", env.ScriptPath)
	if env.ProjectConfig.Path != "" {
		fmt.Fprintf(env.Stderr, "[rs] project config: %s\n", env.ProjectConfig.Path)
	}
	if env.ScriptConfig.ScriptKey != "" {
		fmt.Fprintf(env.Stderr, "[rs] script profile: %s\n", env.ScriptConfig.ScriptKey)
	}
	fmt.Fprintf(env.Stderr, "[rs] interpreter: %s\n", env.Interpreter)
	fmt.Fprintf(env.Stderr, "[rs] library: %s\n", env.LibraryPath)
	fmt.Fprintf(env.Stderr, "[rs] lockfile: %s\n", env.LockfilePath)
	if detectedToolchain != nil {
		fmt.Fprintf(env.Stderr, "[rs] auto-detected toolchain preset: %s\n", detectedToolchain.Preset)
	}
	if len(effectivePrefixes) == 0 {
		fmt.Fprintln(env.Stderr, "[rs] toolchain prefixes: <none>")
	} else {
		fmt.Fprintf(env.Stderr, "[rs] toolchain prefixes: %s\n", strings.Join(effectivePrefixes, ", "))
	}
	if len(effectivePkgConfig) == 0 {
		fmt.Fprintln(env.Stderr, "[rs] pkg-config path: <none>")
	} else {
		fmt.Fprintf(env.Stderr, "[rs] pkg-config path: %s\n", strings.Join(effectivePkgConfig, ", "))
	}
	if len(env.DetectedDeps) == 0 {
		fmt.Fprintln(env.Stderr, "[rs] detected packages: <none>")
	} else {
		fmt.Fprintf(env.Stderr, "[rs] detected packages: %s\n", strings.Join(env.DetectedDeps, ", "))
	}
	if len(env.CRANDeps) == 0 {
		fmt.Fprintln(env.Stderr, "[rs] resolved CRAN packages: <none>")
	} else {
		fmt.Fprintf(env.Stderr, "[rs] resolved CRAN packages: %s\n", strings.Join(env.CRANDeps, ", "))
	}
	if len(env.BiocDeps) == 0 {
		fmt.Fprintln(env.Stderr, "[rs] resolved Bioconductor packages: <none>")
	} else {
		fmt.Fprintf(env.Stderr, "[rs] resolved Bioconductor packages: %s\n", strings.Join(env.BiocDeps, ", "))
	}
	if len(env.SourceDeps) == 0 {
		fmt.Fprintln(env.Stderr, "[rs] resolved custom sources: <none>")
	} else {
		fmt.Fprintf(env.Stderr, "[rs] resolved custom sources: %s\n", strings.Join(sourceSummary(env.SourceDeps), ", "))
	}
}

func selectSourceDeps(sourceMap map[string]project.SourceSpec, requested, bioc []string) map[string]project.SourceSpec {
	if len(sourceMap) == 0 {
		return nil
	}

	requestedSet := map[string]struct{}{}
	for _, dep := range mergeDeps(requested, bioc) {
		requestedSet[dep] = struct{}{}
	}

	selected := map[string]project.SourceSpec{}
	for name, spec := range sourceMap {
		if _, ok := requestedSet[name]; ok {
			selected[name] = spec
		}
	}
	if len(selected) == 0 {
		return nil
	}
	return selected
}

func validateSourceDeps(sourceDeps map[string]project.SourceSpec) error {
	for name, spec := range sourceDeps {
		switch spec.Type {
		case "github":
			if spec.Repo == "" {
				return fmt.Errorf("source %q is type github but missing repo", name)
			}
			if spec.TokenEnv != "" && os.Getenv(spec.TokenEnv) == "" {
				return fmt.Errorf("source %q requires environment variable %s, but it is not set", name, spec.TokenEnv)
			}
		case "git":
			if spec.URL == "" {
				return fmt.Errorf("source %q is type git but missing url", name)
			}
		case "local":
			if spec.Path == "" {
				return fmt.Errorf("source %q is type local but missing path", name)
			}
		case "":
			return fmt.Errorf("source %q is missing type", name)
		default:
			return fmt.Errorf("source %q has unsupported type %q", name, spec.Type)
		}
	}
	return nil
}

func hasSourceType(sourceDeps map[string]project.SourceSpec, sourceType string) bool {
	for _, spec := range sourceDeps {
		if spec.Type == sourceType {
			return true
		}
	}
	return false
}

func collectSourceDefinitionIssues(sourceDeps map[string]project.SourceSpec) []string {
	var issues []string
	for name, spec := range sourceDeps {
		switch spec.Type {
		case "github":
			if spec.Repo == "" {
				issues = append(issues, fmt.Sprintf("source %q is type github but missing repo", name))
			}
			if spec.TokenEnv != "" && os.Getenv(spec.TokenEnv) == "" {
				issues = append(issues, fmt.Sprintf("source %q requires environment variable %s, but it is not set", name, spec.TokenEnv))
			}
		case "git":
			if spec.URL == "" {
				issues = append(issues, fmt.Sprintf("source %q is type git but missing url", name))
			}
		case "local":
			if spec.Path == "" {
				issues = append(issues, fmt.Sprintf("source %q is type local but missing path", name))
			}
		case "":
			issues = append(issues, fmt.Sprintf("source %q is missing type", name))
		default:
			issues = append(issues, fmt.Sprintf("source %q has unsupported type %q", name, spec.Type))
		}
	}
	return issues
}

func collectSourceAvailabilityIssues(sourceDeps map[string]project.SourceSpec) []string {
	var issues []string
	for name, spec := range sourceDeps {
		switch spec.Type {
		case "local":
			if spec.Path != "" {
				if _, err := os.Stat(spec.Path); errors.Is(err, os.ErrNotExist) {
					issues = append(issues, fmt.Sprintf("local source %q does not exist: %s", name, spec.Path))
				}
			}
		case "git":
			if spec.URL != "" {
				if localPath, ok := localGitPath(spec.URL); ok {
					if _, err := os.Stat(localPath); errors.Is(err, os.ErrNotExist) {
						issues = append(issues, fmt.Sprintf("git source %q does not exist: %s", name, localPath))
					}
				}
			}
		}
	}
	return issues
}

func localGitPath(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "file://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		path := parsed.Path
		if runtime.GOOS == "windows" {
			switch {
			case parsed.Host != "":
				path = "//" + parsed.Host + path
			case len(path) >= 3 && path[0] == '/' && path[2] == ':':
				path = path[1:]
			}
			path = filepath.FromSlash(path)
		}
		return path, true
	}
	if strings.Contains(raw, "://") || strings.Contains(raw, "@") {
		return "", false
	}
	return raw, true
}

func filterManagedDeps(cranDeps []string, sourceDeps map[string]project.SourceSpec, biocDeps []string) []string {
	if len(sourceDeps) == 0 && len(biocDeps) == 0 {
		return cranDeps
	}

	biocSet := map[string]struct{}{}
	for _, dep := range biocDeps {
		biocSet[dep] = struct{}{}
	}

	filtered := make([]string, 0, len(cranDeps))
	for _, dep := range cranDeps {
		if _, ok := sourceDeps[dep]; ok {
			continue
		}
		if _, ok := biocSet[dep]; ok {
			continue
		}
		filtered = append(filtered, dep)
	}
	return filtered
}

func filterBiocDeps(biocDeps []string, sourceDeps map[string]project.SourceSpec) []string {
	if len(sourceDeps) == 0 {
		return biocDeps
	}

	filtered := make([]string, 0, len(biocDeps))
	for _, dep := range biocDeps {
		if _, ok := sourceDeps[dep]; ok {
			continue
		}
		filtered = append(filtered, dep)
	}
	return filtered
}

func sourceDepNames(sourceDeps map[string]project.SourceSpec) []string {
	if len(sourceDeps) == 0 {
		return nil
	}

	names := make([]string, 0, len(sourceDeps))
	for name := range sourceDeps {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func sourceSummary(sourceDeps map[string]project.SourceSpec) []string {
	names := sourceDepNames(sourceDeps)
	out := make([]string, 0, len(names))
	for _, name := range names {
		spec := sourceDeps[name]
		switch spec.Type {
		case "github":
			label := name + "=github:" + spec.Repo
			if spec.Host != "" {
				label = name + "=github[" + spec.Host + "]:" + spec.Repo
			}
			if spec.Ref != "" {
				label += "@" + spec.Ref
			}
			if spec.Subdir != "" {
				label += "#" + spec.Subdir
			}
			out = append(out, label)
		case "git":
			label := name + "=git:" + spec.URL
			if spec.Ref != "" {
				label += "@" + spec.Ref
			}
			if spec.Subdir != "" {
				label += "#" + spec.Subdir
			}
			out = append(out, label)
		case "local":
			out = append(out, name+"=local:"+spec.Path)
		default:
			out = append(out, name+"="+spec.Type)
		}
	}
	return out
}

func fingerprintSourceDeps(sourceDeps map[string]project.SourceSpec) string {
	if len(sourceDeps) == 0 {
		return ""
	}

	names := sourceDepNames(sourceDeps)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		spec := sourceDeps[name]
		fingerprintKind := ""
		fingerprint := ""
		if spec.Type == "local" {
			fingerprintKind, fingerprint = describeLocalSourceFingerprint(spec.Path)
		}
		lines = append(lines, strings.Join([]string{
			name,
			spec.Type,
			url.QueryEscape(spec.Host),
			url.QueryEscape(spec.Repo),
			url.QueryEscape(spec.URL),
			url.QueryEscape(spec.Path),
			url.QueryEscape(spec.Ref),
			url.QueryEscape(spec.Subdir),
			url.QueryEscape(fingerprintKind),
			url.QueryEscape(fingerprint),
		}, "\t"))
	}
	return strings.Join(lines, "\n")
}

func encodeSourceSpecs(sourceDeps map[string]project.SourceSpec) string {
	if len(sourceDeps) == 0 {
		return ""
	}

	names := sourceDepNames(sourceDeps)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		spec := sourceDeps[name]
		fingerprintKind := ""
		fingerprint := ""
		if spec.Type == "local" {
			fingerprintKind, fingerprint = describeLocalSourceFingerprint(spec.Path)
		}
		repo := url.QueryEscape(spec.Repo)
		ref := url.QueryEscape(spec.Ref)
		path := url.QueryEscape(spec.Path)
		subdir := url.QueryEscape(spec.Subdir)
		host := url.QueryEscape(spec.Host)
		tokenEnv := url.QueryEscape(spec.TokenEnv)
		sourceURL := url.QueryEscape(spec.URL)
		lines = append(lines, strings.Join([]string{
			name,
			spec.Type,
			repo,
			ref,
			path,
			subdir,
			host,
			tokenEnv,
			sourceURL,
			url.QueryEscape(fingerprintKind),
			url.QueryEscape(fingerprint),
		}, "\t"))
	}
	return strings.Join(lines, "\n")
}

func describeLocalSourceFingerprint(path string) (string, string) {
	kind, fingerprint, err := readLocalSourceFingerprint(path)
	if err == nil {
		return kind, fingerprint
	}
	if errors.Is(err, os.ErrNotExist) {
		return localSourceFingerprintMissing, ""
	}
	return localSourceFingerprintError, ""
}

func readLocalSourceFingerprint(path string) (string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		fingerprint, err := fingerprintDirectoryTree(path)
		if err != nil {
			return "", "", err
		}
		return localSourceFingerprintKindDir, fingerprint, nil
	}
	fingerprint, err := fingerprintFileContents(path)
	if err != nil {
		return "", "", err
	}
	return localSourceFingerprintKindFile, fingerprint, nil
}

func fingerprintFileContents(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func fingerprintDirectoryTree(root string) (string, error) {
	sum := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "" {
			rel = "."
		}

		entryType := "other"
		value := ""
		switch {
		case d.IsDir():
			entryType = "dir"
		case d.Type()&os.ModeSymlink != 0:
			entryType = "symlink"
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value = target
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				entryType = "file"
				value, err = fingerprintFileContents(path)
				if err != nil {
					return err
				}
			} else {
				entryType = info.Mode().Type().String()
			}
		}

		line := rel + "\t" + entryType + "\t" + value + "\n"
		if _, err := io.WriteString(sum, line); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func inspectRuntimeWithInterpreter(interpreter, workDir string, stderr io.Writer) (RuntimeMetadata, error) {
	script := `
cat("version\t", as.character(getRversion()), "\n", sep = "")
cat("platform\t", R.version$platform, "\n", sep = "")
cat("arch\t", R.version$arch, "\n", sep = "")
cat("os\t", R.version$os, "\n", sep = "")
cat("pkg_type\t", getOption("pkgType"), "\n", sep = "")
`
	tempDir := ""
	if info, err := os.Stat(workDir); err == nil && info.IsDir() {
		tempDir = workDir
	}
	tmpFile, err := os.CreateTemp(tempDir, "rs-inspect-*.R")
	if err != nil {
		return RuntimeMetadata{}, fmt.Errorf("prepare runtime inspection script: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.WriteString(script); err != nil {
		_ = tmpFile.Close()
		return RuntimeMetadata{}, fmt.Errorf("write runtime inspection script: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return RuntimeMetadata{}, fmt.Errorf("close runtime inspection script: %w", err)
	}

	cmd := exec.Command(interpreter, "--vanilla", tmpPath)
	if tempDir != "" {
		cmd.Dir = tempDir
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}

	output, err := cmd.Output()
	if err != nil {
		return RuntimeMetadata{}, fmt.Errorf("inspect R runtime: %w", err)
	}

	meta := RuntimeMetadata{
		Interpreter:     interpreter,
		InterpreterKind: classifyInterpreterKind(interpreter),
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch key {
		case "version":
			meta.RVersion = value
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

func classifyInterpreterKind(interpreter string) string {
	path := strings.TrimSpace(interpreter)
	if path == "" {
		return ""
	}
	if root, err := managedRRoot(); err == nil {
		managedVersions := filepath.Join(root, "versions") + string(filepath.Separator)
		cleaned := filepath.Clean(path)
		if strings.HasPrefix(cleaned, managedVersions) {
			return "managed"
		}
	}
	lower := strings.ToLower(filepath.ToSlash(path))
	condaMarkers := []string{
		"/miniconda",
		"/anaconda",
		"/mambaforge",
		"/micromamba",
		"/conda/",
		"/envs/",
	}
	for _, marker := range condaMarkers {
		if strings.Contains(lower, marker) {
			return "external-conda"
		}
	}
	return "external-standard"
}

func managedRRoot() (string, error) {
	if value := strings.TrimSpace(os.Getenv("RS_R_ROOT")); value != "" {
		return value, nil
	}
	if home := strings.TrimSpace(os.Getenv("RS_HOME")); home != "" {
		return filepath.Join(home, "r"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
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

func wrapExternalInterpreterInstallError(err error, runtime RuntimeMetadata) error {
	if err == nil || runtime.InterpreterKind != "external-conda" || !looksLikePackageInstallFailure(err) {
		return err
	}
	target := firstNonEmpty(runtime.RVersion, "4.4")
	hints := []string{
		fmt.Sprintf("hint: the selected interpreter %s looks like an external Conda-style R; source package installs can be less reliable there. Consider switching to a managed rs R with `rs r install %s && rs r use %s`", runtime.Interpreter, target, target),
	}
	if looksLikeToolchainFailure(err) {
		toolchainHint := "hint: if you must stay on the current interpreter, provide a user-local toolchain prefix and validate it with `rs toolchain detect`, `rs toolchain template auto`, and `rs doctor --toolchain-only`"
		if candidate, detectErr := toolchainenv.RecommendedCandidate(""); detectErr == nil && candidate != nil {
			toolchainHint = fmt.Sprintf("%s. Detected recommended preset on this machine: %s. Setup follow-up: `%s`. Project follow-up: `%s`", toolchainHint, candidate.Preset, candidate.SuggestedSetupCommand, candidate.SuggestedInitCommand)
		}
		hints = append(hints, toolchainHint)
	}
	return fmt.Errorf("%w\n%s", err, strings.Join(hints, "\n"))
}

func looksLikePackageInstallFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "install ") &&
		(strings.Contains(message, " from cran") ||
			strings.Contains(message, " from bioconductor") ||
			strings.Contains(message, " from local source") ||
			strings.Contains(message, " from git source") ||
			strings.Contains(message, " from github source"))
}

func looksLikeToolchainFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	markers := []string{
		"requires linux source build tools",
		"required compilers are missing",
		"compiler",
		"gfortran",
		"g++",
		"gcc",
		"make",
		"pkg-config",
		"cannot create executables",
		"configuration failed",
		"fatal error:",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func enrichLockedPackages(env ResolvedEnvironment, pkgs []lockfile.Package) {
	for i := range pkgs {
		if spec, ok := env.SourceDeps[pkgs[i].Name]; ok {
			pkgs[i].Source = spec.Type
			switch spec.Type {
			case "github":
				pkgs[i].SourceHost = spec.Host
				pkgs[i].SourceLocation = spec.Repo
				pkgs[i].SourceRef = spec.Ref
				pkgs[i].SourceSubdir = spec.Subdir
			case "git":
				pkgs[i].SourceLocation = spec.URL
				pkgs[i].SourceRef = spec.Ref
				pkgs[i].SourceSubdir = spec.Subdir
			case "local":
				pkgs[i].SourceLocation = spec.Path
				fingerprintKind, fingerprint, err := readLocalSourceFingerprint(spec.Path)
				if err == nil {
					pkgs[i].SourceFingerprint = fingerprint
					pkgs[i].SourceFingerprintKind = fingerprintKind
				}
			}
		}
	}
}

func readInstalledSourceMetadata(libraryPath string) (map[string]sourceMetadata, error) {
	metaDir := filepath.Join(libraryPath, ".rs-source-meta")
	entries, err := os.ReadDir(metaDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read source metadata dir: %w", err)
	}

	metaByName := make(map[string]sourceMetadata, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tsv") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".tsv")
		data, err := os.ReadFile(filepath.Join(metaDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read source metadata file: %w", err)
		}
		line := strings.TrimSpace(string(data))
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		for len(fields) < 6 {
			fields = append(fields, "")
		}
		for len(fields) < 8 {
			fields = append(fields, "")
		}
		decode := func(value string) string {
			decoded, err := url.QueryUnescape(value)
			if err != nil {
				return value
			}
			return decoded
		}
		metaByName[name] = sourceMetadata{
			Source:                decode(fields[0]),
			SourceHost:            decode(fields[1]),
			SourceLocation:        decode(fields[2]),
			SourceRef:             decode(fields[3]),
			SourceCommit:          decode(fields[4]),
			SourceSubdir:          decode(fields[5]),
			SourceFingerprint:     decode(fields[6]),
			SourceFingerprintKind: decode(fields[7]),
		}
	}
	return metaByName, nil
}
