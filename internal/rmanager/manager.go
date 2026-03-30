package rmanager

import (
	"io"
	"os"
	"strings"
)

const defaultAutoInstallVersionEnv = "RS_R_VERSION"
const autoInstallREnv = "RS_AUTO_INSTALL_R"
const managerRootEnv = "RS_R_ROOT"

type InstallMethod string

const (
	InstallMethodAuto   InstallMethod = "auto"
	InstallMethodBinary InstallMethod = "binary"
	InstallMethodSource InstallMethod = "source"
)

type InstallOptions struct {
	Version string
	Method  InstallMethod
	Stdout  io.Writer
	Stderr  io.Writer
}

type Installation struct {
	Name        string
	Version     string
	RscriptPath string
	RPath       string
	Managed     bool
	External    bool
	Current     bool
	Default     bool
	Source      string
}

func List(stdout, stderr io.Writer) error {
	return nativeList(stdout, stderr)
}

func Install(version string, stdout, stderr io.Writer) error {
	return InstallWithOptions(InstallOptions{
		Version: version,
		Method:  InstallMethodAuto,
		Stdout:  stdout,
		Stderr:  stderr,
	})
}

func InstallWithOptions(opts InstallOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Method == "" {
		opts.Method = InstallMethodAuto
	}
	return nativeInstallWithOptions(opts)
}

func ResolveVersionOrPath(spec string) (string, error) {
	return nativeResolveVersionOrPath(spec)
}

func EnsureInstalledRscript(spec string, stdout, stderr io.Writer) (string, error) {
	return nativeEnsureInstalledRscript(spec, stdout, stderr)
}

func BootstrapAdvice() RBootstrapAdvice {
	return nativeBootstrapAdvice()
}

func AutoInstallREnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(autoInstallREnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func ValidateVersionSelector(spec string) error {
	return validateVersionSelector(spec)
}
