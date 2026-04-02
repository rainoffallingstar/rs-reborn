package rmanager

import (
	"io"

	internalrmanager "github.com/rainoffallingstar/rs-reborn/internal/rmanager"
)

type RBootstrapAdvice = internalrmanager.RBootstrapAdvice
type InstallMethod = internalrmanager.InstallMethod

const (
	InstallMethodAuto   = internalrmanager.InstallMethodAuto
	InstallMethodBinary = internalrmanager.InstallMethodBinary
	InstallMethodSource = internalrmanager.InstallMethodSource
)

type InstallOptions = internalrmanager.InstallOptions
type Installation = internalrmanager.Installation

func DiscoverInstallations() ([]Installation, error) {
	return internalrmanager.DiscoverInstallations()
}

func List(stdout, stderr io.Writer) error {
	return internalrmanager.List(stdout, stderr)
}

func Install(version string, stdout, stderr io.Writer) error {
	return internalrmanager.Install(version, stdout, stderr)
}

func InstallWithOptions(opts InstallOptions) error {
	return internalrmanager.InstallWithOptions(opts)
}

func ResolveVersionOrPath(spec string) (string, error) {
	return internalrmanager.ResolveVersionOrPath(spec)
}

func ResolveVersionSelector(spec string) (string, error) {
	return internalrmanager.ResolveVersionSelector(spec)
}

func CurrentManagedRscript() (string, error) {
	return internalrmanager.CurrentManagedRscript()
}

func LookupManagedInstallation(rscriptPath string) (Installation, bool, error) {
	return internalrmanager.LookupManagedInstallation(rscriptPath)
}

func EnsureInstalledRscript(spec string, stdout, stderr io.Writer) (string, error) {
	return internalrmanager.EnsureInstalledRscript(spec, stdout, stderr)
}

func BootstrapAdvice() RBootstrapAdvice {
	return internalrmanager.BootstrapAdvice()
}

func BootstrapAdviceFor(spec string) RBootstrapAdvice {
	return internalrmanager.BootstrapAdviceFor(spec)
}

func AutoInstallREnabled() bool {
	return internalrmanager.AutoInstallREnabled()
}

func ValidateVersionSelector(spec string) error {
	return internalrmanager.ValidateVersionSelector(spec)
}

func LooksLikeVersionSpec(spec string) bool {
	return internalrmanager.LooksLikeVersionSpec(spec)
}

func VersionMatchesSpec(spec, actual string) bool {
	return internalrmanager.VersionMatchesSpec(spec, actual)
}
