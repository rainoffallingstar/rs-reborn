package rdeps

import internalrdeps "github.com/rainoffallingstar/rs-reborn/internal/rdeps"

func FromFile(path string) ([]string, error) {
	return internalrdeps.FromFile(path)
}

func FromSource(src string) []string {
	return internalrdeps.FromSource(src)
}

func IsBundledPackage(name string) bool {
	return internalrdeps.IsBundledPackage(name)
}

func FilterInstallable(deps []string) []string {
	return internalrdeps.FilterInstallable(deps)
}

func IsKnownBiocPackage(name string) bool {
	return internalrdeps.IsKnownBiocPackage(name)
}

func SplitBiocPackages(deps []string) ([]string, []string) {
	return internalrdeps.SplitBiocPackages(deps)
}
