package toolchain

import (
	"io"

	internaltoolchain "github.com/rainoffallingstar/rs-reborn/internal/toolchainenv"
)

type Candidate = internaltoolchain.Candidate
type ValidationResult = internaltoolchain.ValidationResult
type Preview = internaltoolchain.Preview
type PackageGroup = internaltoolchain.PackageGroup
type PackagePlan = internaltoolchain.PackagePlan
type NativeFixupPlan = internaltoolchain.NativeFixupPlan

func SupportedPresets() []string {
	return internaltoolchain.SupportedPresets()
}

func DetectCandidates(home string) ([]Candidate, error) {
	return internaltoolchain.DetectCandidates(home)
}

func RecommendedCandidate(home string) (*Candidate, error) {
	return internaltoolchain.RecommendedCandidate(home)
}

func DescribePreset(name, home string) (*Candidate, error) {
	return internaltoolchain.DescribePreset(name, home)
}

func ResolvePreset(name, home string) ([]string, []string, error) {
	return internaltoolchain.ResolvePreset(name, home)
}

func Apply(base, prefixes, pkgConfigPaths []string) []string {
	return internaltoolchain.Apply(base, prefixes, pkgConfigPaths)
}

func ApplyWithPlan(base, prefixes, pkgConfigPaths []string, plan NativeFixupPlan) []string {
	return internaltoolchain.ApplyWithPlan(base, prefixes, pkgConfigPaths, plan)
}

func Validate(prefixes, pkgConfigPaths, env []string) ValidationResult {
	return internaltoolchain.Validate(prefixes, pkgConfigPaths, env)
}

func BuildPreview(prefixes, pkgConfigPaths []string) Preview {
	return internaltoolchain.BuildPreview(prefixes, pkgConfigPaths)
}

func NativeCategoriesForPackages(packages []string) []string {
	return internaltoolchain.NativeCategoriesForPackages(packages)
}

func BuildPackagePlan(preset string, categories []string) (PackagePlan, error) {
	return internaltoolchain.BuildPackagePlan(preset, categories)
}

func BuildNativeFixupPlan(prefixes, categories []string) NativeFixupPlan {
	return internaltoolchain.BuildNativeFixupPlan(prefixes, categories)
}

func BuildNativeFixupPlanWithEnv(baseEnv, prefixes, pkgConfigPaths, categories []string) NativeFixupPlan {
	return internaltoolchain.BuildNativeFixupPlanWithEnv(baseEnv, prefixes, pkgConfigPaths, categories)
}

func BootstrapCandidate(name, home string, env []string) (*Candidate, error) {
	return internaltoolchain.BootstrapCandidate(name, home, env)
}

func BootstrapWithPackages(name, home string, env, packages []string, stdout, stderr io.Writer) (*Candidate, error) {
	return internaltoolchain.BootstrapWithPackages(name, home, env, packages, stdout, stderr)
}

func WrapCommand(name string, args []string, env []string) (string, []string, []string, bool, error) {
	return internaltoolchain.WrapCommand(name, args, env)
}
