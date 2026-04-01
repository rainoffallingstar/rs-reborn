package toolchainenv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	PrefixesEnv  = "RS_TOOLCHAIN_PREFIXES"
	PkgConfigEnv = "RS_PKG_CONFIG_PATH"
)

type ValidationResult struct {
	Errors   []string
	Warnings []string
}

type Preview struct {
	Path          []string
	CPPFLAGS      []string
	LDFLAGS       []string
	PkgConfigPath []string
}

func Apply(base []string, prefixes, pkgConfigPaths []string) []string {
	return ApplyWithPlan(base, prefixes, pkgConfigPaths, NativeFixupPlan{})
}

func ApplyWithPlan(base []string, prefixes, pkgConfigPaths []string, plan NativeFixupPlan) []string {
	prefixes = cleanList(prefixes)
	pkgConfigPaths = cleanList(pkgConfigPaths)

	currentPath := envValue(base, "PATH")
	currentCPP := envValue(base, "CPPFLAGS")
	currentLD := envValue(base, "LDFLAGS")
	currentLibs := envValue(base, "LIBS")
	currentLibrary := envValue(base, "LIBRARY_PATH")
	currentRuntimeLibrary := runtimeLibraryEnvValue(base)
	currentPkg := envValue(base, "PKG_CONFIG_PATH")

	pathEntries := splitPathList(currentPath)
	cppFlags := splitShellWords(currentCPP)
	ldFlags := splitShellWords(currentLD)
	libsFlags := splitShellWords(currentLibs)
	libraryEntries := splitPathList(currentLibrary)
	runtimeLibraryEntries := splitPathList(currentRuntimeLibrary)
	pkgEntries := splitPathList(currentPkg)

	for i := len(prefixes) - 1; i >= 0; i-- {
		prefix := prefixes[i]
		libDir := filepath.Join(prefix, "lib")
		pathEntries = prependUnique(pathEntries, filepath.Join(prefix, "bin"))
		cppFlags = prependUniqueValue(cppFlags, "-I"+filepath.Join(prefix, "include"))
		ldFlags = prependUniqueValue(ldFlags, "-L"+libDir)
		libraryEntries = prependUnique(libraryEntries, libDir)
		runtimeLibraryEntries = prependUnique(runtimeLibraryEntries, libDir)
		pkgEntries = prependUnique(pkgEntries, filepath.Join(libDir, "pkgconfig"))
		pkgEntries = prependUnique(pkgEntries, filepath.Join(prefix, "share", "pkgconfig"))
	}
	for i := len(plan.CPPFLAGS) - 1; i >= 0; i-- {
		cppFlags = prependUniqueValue(cppFlags, plan.CPPFLAGS[i])
	}
	for i := len(plan.LDFLAGS) - 1; i >= 0; i-- {
		ldFlags = prependUniqueValue(ldFlags, plan.LDFLAGS[i])
	}
	for i := len(plan.LIBS) - 1; i >= 0; i-- {
		libsFlags = prependUniqueValue(libsFlags, plan.LIBS[i])
	}
	for i := len(pkgConfigPaths) - 1; i >= 0; i-- {
		pkgEntries = prependUnique(pkgEntries, pkgConfigPaths[i])
	}
	ldLibraryDirs := libraryDirsFromLDFLAGS(ldFlags)
	for i := len(ldLibraryDirs) - 1; i >= 0; i-- {
		libDir := ldLibraryDirs[i]
		libraryEntries = prependUnique(libraryEntries, libDir)
		runtimeLibraryEntries = prependUnique(runtimeLibraryEntries, libDir)
	}

	filtered := make([]string, 0, len(base)+6)
	for _, entry := range base {
		switch {
		case strings.HasPrefix(entry, "PATH="),
			strings.HasPrefix(entry, "CPPFLAGS="),
			strings.HasPrefix(entry, "LDFLAGS="),
			strings.HasPrefix(entry, "LIBS="),
			strings.HasPrefix(entry, "LIBRARY_PATH="),
			isRuntimeLibraryEnv(entry),
			strings.HasPrefix(entry, "PKG_CONFIG_PATH="),
			strings.HasPrefix(entry, PrefixesEnv+"="),
			strings.HasPrefix(entry, PkgConfigEnv+"="):
			continue
		default:
			filtered = append(filtered, entry)
		}
	}

	if len(pathEntries) > 0 {
		filtered = append(filtered, "PATH="+strings.Join(pathEntries, string(os.PathListSeparator)))
	}
	if len(cppFlags) > 0 {
		filtered = append(filtered, "CPPFLAGS="+strings.Join(cppFlags, " "))
	}
	if len(ldFlags) > 0 {
		filtered = append(filtered, "LDFLAGS="+strings.Join(ldFlags, " "))
	}
	if len(libsFlags) > 0 {
		filtered = append(filtered, "LIBS="+strings.Join(libsFlags, " "))
	}
	if len(libraryEntries) > 0 {
		filtered = append(filtered, "LIBRARY_PATH="+strings.Join(libraryEntries, string(os.PathListSeparator)))
	}
	if runtimeLibraryEnv := runtimeLibraryEnvName(); runtimeLibraryEnv != "" && len(runtimeLibraryEntries) > 0 {
		filtered = append(filtered, runtimeLibraryEnv+"="+strings.Join(runtimeLibraryEntries, string(os.PathListSeparator)))
	}
	if len(pkgEntries) > 0 {
		filtered = append(filtered, "PKG_CONFIG_PATH="+strings.Join(pkgEntries, string(os.PathListSeparator)))
	}
	if len(prefixes) > 0 {
		filtered = append(filtered, PrefixesEnv+"="+strings.Join(prefixes, string(os.PathListSeparator)))
	}
	if len(pkgConfigPaths) > 0 {
		filtered = append(filtered, PkgConfigEnv+"="+strings.Join(pkgConfigPaths, string(os.PathListSeparator)))
	}
	return filtered
}

func libraryDirsFromLDFLAGS(flags []string) []string {
	if len(flags) == 0 {
		return nil
	}
	dirs := []string{}
	seen := map[string]struct{}{}
	for _, flag := range flags {
		if !strings.HasPrefix(flag, "-L") {
			continue
		}
		dir := strings.TrimSpace(strings.TrimPrefix(flag, "-L"))
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return dirs
}

func PrefixesFromEnv(env []string) []string {
	return splitPathList(envValue(env, PrefixesEnv))
}

func PkgConfigPathsFromEnv(env []string) []string {
	return splitPathList(envValue(env, PkgConfigEnv))
}

func FindInPath(name string, env []string) (string, error) {
	pathValue := envValue(env, "PATH")
	if strings.TrimSpace(pathValue) == "" {
		return exec.LookPath(name)
	}
	candidates := candidateNames(name)
	for _, dir := range splitPathList(pathValue) {
		for _, candidate := range candidates {
			full := filepath.Join(dir, candidate)
			info, err := os.Stat(full)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS == "windows" || info.Mode()&0o111 != 0 {
				return full, nil
			}
		}
	}
	return "", fmt.Errorf("%q not found in configured PATH", name)
}

func Validate(prefixes, pkgConfigPaths, env []string) ValidationResult {
	result := ValidationResult{
		Errors:   []string{},
		Warnings: []string{},
	}

	prefixes = cleanList(prefixes)
	pkgConfigPaths = cleanList(pkgConfigPaths)
	for _, prefix := range prefixes {
		if issue, ok := validateDirectory("toolchain prefix", prefix); ok {
			result.Errors = append(result.Errors, issue)
		}
	}
	for _, path := range pkgConfigPaths {
		if issue, ok := validateDirectory("pkg-config path", path); ok {
			result.Errors = append(result.Errors, issue)
		}
	}
	if len(prefixes) > 0 || len(pkgConfigPaths) > 0 {
		if _, err := FindInPath("pkg-config", env); err != nil {
			result.Warnings = append(result.Warnings, "pkg-config is not available on PATH; configured pkg_config_path entries may be ignored until pkg-config is installed or exposed via toolchain_prefixes")
		}
	}

	return result
}

func BuildPreview(prefixes, pkgConfigPaths []string) Preview {
	prefixes = cleanList(prefixes)
	pkgConfigPaths = cleanList(pkgConfigPaths)

	preview := Preview{
		Path:          []string{},
		CPPFLAGS:      []string{},
		LDFLAGS:       []string{},
		PkgConfigPath: []string{},
	}
	seenPath := map[string]struct{}{}
	seenCPP := map[string]struct{}{}
	seenLD := map[string]struct{}{}
	seenPkg := map[string]struct{}{}

	for _, prefix := range prefixes {
		appendUnique(&preview.Path, filepath.Join(prefix, "bin"), seenPath)
		appendUnique(&preview.CPPFLAGS, "-I"+filepath.Join(prefix, "include"), seenCPP)
		appendUnique(&preview.LDFLAGS, "-L"+filepath.Join(prefix, "lib"), seenLD)
		appendUnique(&preview.PkgConfigPath, filepath.Join(prefix, "lib", "pkgconfig"), seenPkg)
		appendUnique(&preview.PkgConfigPath, filepath.Join(prefix, "share", "pkgconfig"), seenPkg)
	}
	for _, path := range pkgConfigPaths {
		appendUnique(&preview.PkgConfigPath, path, seenPkg)
	}

	return preview
}

func validateDirectory(label, path string) (string, bool) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("%s does not exist: %s", label, path), true
	}
	if err != nil {
		return fmt.Sprintf("could not inspect %s %s: %v", label, path, err), true
	}
	if !info.IsDir() {
		return fmt.Sprintf("%s is not a directory: %s", label, path), true
	}
	return "", false
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func runtimeLibraryEnvName() string {
	switch runtime.GOOS {
	case "linux":
		return "LD_LIBRARY_PATH"
	case "darwin":
		return "DYLD_FALLBACK_LIBRARY_PATH"
	default:
		return ""
	}
}

func runtimeLibraryEnvValue(env []string) string {
	name := runtimeLibraryEnvName()
	if name == "" {
		return ""
	}
	return envValue(env, name)
}

func isRuntimeLibraryEnv(entry string) bool {
	name := runtimeLibraryEnvName()
	if name == "" {
		return false
	}
	return strings.HasPrefix(entry, name+"=")
}

func splitPathList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return cleanList(filepath.SplitList(value))
}

func splitShellWords(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return uniqueTrimmed(strings.Fields(value))
}

func cleanList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func prependUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	cleaned := filepath.Clean(value)
	out := []string{cleaned}
	for _, existing := range values {
		if filepath.Clean(existing) == cleaned {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func prependUniqueValue(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	out := []string{value}
	for _, existing := range values {
		if strings.TrimSpace(existing) == value {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func appendUnique(dst *[]string, value string, seen map[string]struct{}) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	cleaned := filepath.Clean(value)
	if _, ok := seen[cleaned]; ok {
		return
	}
	seen[cleaned] = struct{}{}
	*dst = append(*dst, cleaned)
}

func uniqueTrimmed(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func candidateNames(name string) []string {
	if runtime.GOOS != "windows" || filepath.Ext(name) != "" {
		return []string{name}
	}
	pathext := os.Getenv("PATHEXT")
	if pathext == "" {
		pathext = ".COM;.EXE;.BAT;.CMD"
	}
	candidates := []string{name}
	for _, ext := range strings.Split(pathext, ";") {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		candidates = append(candidates, name+strings.ToLower(ext))
		candidates = append(candidates, name+strings.ToUpper(ext))
	}
	return candidates
}
