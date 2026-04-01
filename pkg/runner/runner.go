package runner

import (
	internalrunner "github.com/rainoffallingstar/rs-reborn/internal/runner"
	publiclockfile "github.com/rainoffallingstar/rs-reborn/pkg/lockfile"
)

type RunOptions = internalrunner.RunOptions
type ShellOptions = internalrunner.ShellOptions
type ExecOptions = internalrunner.ExecOptions
type SyncOptions = internalrunner.SyncOptions
type LockOptions = internalrunner.LockOptions
type CheckOptions = internalrunner.CheckOptions
type DoctorOptions = internalrunner.DoctorOptions
type ListOptions = internalrunner.ListOptions
type PruneOptions = internalrunner.PruneOptions
type CacheDirOptions = internalrunner.CacheDirOptions
type CacheListOptions = internalrunner.CacheListOptions
type CacheRemoveOptions = internalrunner.CacheRemoveOptions
type ExitError = internalrunner.ExitError
type ReportedError = internalrunner.ReportedError
type ResolvedEnvironment = internalrunner.ResolvedEnvironment
type RuntimeMetadata = internalrunner.RuntimeMetadata
type ValidationError = internalrunner.ValidationError
type ValidationMode = internalrunner.ValidationMode
type ValidationKind = internalrunner.ValidationKind
type DoctorError = internalrunner.DoctorError
type ListReport = internalrunner.ListReport
type ListSource = internalrunner.ListSource
type CacheListReport = internalrunner.CacheListReport
type CacheLibrary = internalrunner.CacheLibrary
type CheckReport = internalrunner.CheckReport
type InstalledIssueDetail = internalrunner.InstalledIssueDetail
type DoctorReport = internalrunner.DoctorReport
type DoctorSummary = internalrunner.DoctorSummary
type DoctorIssueDetail = internalrunner.DoctorIssueDetail
type SystemHintDetail = internalrunner.SystemHintDetail
type NextStepDetail = internalrunner.NextStepDetail

const (
	ValidationModeGeneric = internalrunner.ValidationModeGeneric
	ValidationModeLocked  = internalrunner.ValidationModeLocked
	ValidationModeFrozen  = internalrunner.ValidationModeFrozen
	ValidationModeCheck   = internalrunner.ValidationModeCheck
)

const (
	ValidationKindGeneric   = internalrunner.ValidationKindGeneric
	ValidationKindMissing   = internalrunner.ValidationKindMissing
	ValidationKindInputs    = internalrunner.ValidationKindInputs
	ValidationKindInstalled = internalrunner.ValidationKindInstalled
)

func Run(opts RunOptions) error {
	return internalrunner.Run(opts)
}

func Shell(opts ShellOptions) error {
	return internalrunner.Shell(opts)
}

func Exec(opts ExecOptions) error {
	return internalrunner.Exec(opts)
}

func Sync(opts SyncOptions) error {
	return internalrunner.Sync(opts)
}

func Lock(opts LockOptions) error {
	return internalrunner.Lock(opts)
}

func List(opts ListOptions) error {
	return internalrunner.List(opts)
}

func Prune(opts PruneOptions) error {
	return internalrunner.Prune(opts)
}

func CacheDir(opts CacheDirOptions) error {
	return internalrunner.CacheDir(opts)
}

func CacheList(opts CacheListOptions) error {
	return internalrunner.CacheList(opts)
}

func CacheRemove(opts CacheRemoveOptions) error {
	return internalrunner.CacheRemove(opts)
}

func Check(opts CheckOptions) error {
	return internalrunner.Check(opts)
}

func Doctor(opts DoctorOptions) error {
	return internalrunner.Doctor(opts)
}

func ResolveRscriptPath(override, configValue string) (string, error) {
	return internalrunner.ResolveRscriptPath(override, configValue)
}

func ScanScript(path string) ([]string, error) {
	return internalrunner.ScanScript(path)
}

func EnsureInstalled(env ResolvedEnvironment) error {
	return internalrunner.EnsureInstalled(env)
}

func WriteLockfile(env ResolvedEnvironment) error {
	return internalrunner.WriteLockfile(env)
}

func ValidateLockfile(env ResolvedEnvironment, mode ValidationMode) error {
	return internalrunner.ValidateLockfile(env, mode)
}

func InstalledPackages(env ResolvedEnvironment) ([]publiclockfile.Package, error) {
	return internalrunner.InstalledPackages(env)
}

func InspectRuntime(env ResolvedEnvironment) (RuntimeMetadata, error) {
	return internalrunner.InspectRuntime(env)
}
