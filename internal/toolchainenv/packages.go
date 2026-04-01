package toolchainenv

import (
	"fmt"
	"slices"
	"strings"
)

type PackageGroup struct {
	Category string   `json:"category"`
	Packages []string `json:"packages"`
}

type PackagePlan struct {
	Preset         string         `json:"preset"`
	BasePackages   []string       `json:"base_packages"`
	SystemPackages []string       `json:"system_packages"`
	Packages       []string       `json:"packages"`
	Groups         []PackageGroup `json:"groups"`
}

func NativeCategoriesForPackages(packages []string) []string {
	if len(packages) == 0 {
		return nil
	}

	packageSet := map[string]struct{}{}
	for _, pkg := range packages {
		normalized := strings.ToLower(strings.TrimSpace(pkg))
		if normalized == "" {
			continue
		}
		packageSet[normalized] = struct{}{}
	}
	if len(packageSet) == 0 {
		return nil
	}

	categories := make([]string, 0, len(nativeCategoryPackageGroups()))
	for _, group := range nativeCategoryPackageGroups() {
		for _, pkg := range group.Packages {
			if _, ok := packageSet[strings.ToLower(pkg)]; !ok {
				continue
			}
			categories = appendUniqueStrings(categories, group.Category)
			break
		}
	}
	return categories
}

func BuildPackagePlan(preset string, categories []string) (PackagePlan, error) {
	normalized := strings.TrimSpace(strings.ToLower(preset))
	if normalized == "" {
		return PackagePlan{}, fmt.Errorf("toolchain package plan requires a preset")
	}

	base, ok := basePackagesForPreset(normalized)
	if !ok {
		return PackagePlan{}, fmt.Errorf("unsupported toolchain preset %q for package planning", preset)
	}

	seenCategories := map[string]struct{}{}
	groups := make([]PackageGroup, 0, len(categories))
	system := make([]string, 0, 16)
	for _, category := range categories {
		category = strings.TrimSpace(strings.ToLower(category))
		if category == "" {
			continue
		}
		if _, ok := seenCategories[category]; ok {
			continue
		}
		seenCategories[category] = struct{}{}
		packages := systemPackagesForPreset(normalized, category)
		if len(packages) == 0 {
			continue
		}
		groups = append(groups, PackageGroup{
			Category: category,
			Packages: append([]string(nil), packages...),
		})
		system = appendUniqueStrings(system, packages...)
	}

	all := append([]string(nil), base...)
	all = appendUniqueStrings(all, system...)
	return PackagePlan{
		Preset:         normalized,
		BasePackages:   append([]string(nil), base...),
		SystemPackages: append([]string(nil), system...),
		Packages:       all,
		Groups:         groups,
	}, nil
}

func (p PackagePlan) PackagesForPhase(phase string) ([]string, error) {
	switch normalized := strings.TrimSpace(strings.ToLower(phase)); normalized {
	case "", "full":
		return append([]string(nil), p.Packages...), nil
	case "base":
		return append([]string(nil), p.BasePackages...), nil
	default:
		return nil, fmt.Errorf("unsupported toolchain phase %q; supported phases: base, full", phase)
	}
}

func basePackagesForPreset(preset string) ([]string, bool) {
	switch preset {
	case "enva", "micromamba", "mamba", "conda":
		return []string{
			"compilers",
			"binutils",
			"sysroot_linux-64=2.17",
			"pkg-config",
			"make",
			"cmake",
			"libiconv",
		}, true
	case "homebrew":
		return []string{"pkg-config", "gcc", "cmake", "libiconv"}, true
	case "spack":
		return []string{"pkgconf", "gcc", "cmake", "libiconv"}, true
	default:
		return nil, false
	}
}

func nativeCategoryPackageGroups() []PackageGroup {
	return []PackageGroup{
		{
			Category: "network",
			Packages: []string{"curl", "openssl", "gert", "git2r", "httr", "httr2", "gitcreds", "gh", "crul"},
		},
		{
			Category: "icu",
			Packages: []string{"stringi", "stringr"},
		},
		{
			Category: "xml",
			Packages: []string{"xml2", "XML", "xslt", "rvest"},
		},
		{
			Category: "fonts",
			Packages: []string{"textshaping", "ragg", "systemfonts", "gdtools", "svglite", "showtext"},
		},
		{
			Category: "encoding",
			Packages: []string{"haven", "readr", "vroom"},
		},
	}
}

func systemPackagesForPreset(preset, category string) []string {
	switch preset {
	case "enva", "micromamba", "mamba", "conda":
		switch category {
		case "network":
			return []string{"libcurl", "openssl"}
		case "icu":
			return []string{"icu"}
		case "xml":
			return []string{"libxml2"}
		case "geospatial":
			return []string{"gdal", "geos", "proj", "udunits"}
		case "java":
			return []string{"openjdk"}
		case "database":
			return []string{"unixodbc", "libpq", "mariadb-connector-c"}
		case "javascript":
			return []string{"v8"}
		case "imaging":
			return []string{"imagemagick"}
		case "fonts":
			return []string{"freetype", "harfbuzz", "fribidi", "cairo"}
		case "encoding":
			return []string{"libiconv"}
		case "pdf":
			return []string{"poppler", "qpdf"}
		case "cpp":
			return []string{"arrow-cpp"}
		}
	case "homebrew":
		switch category {
		case "network":
			return []string{"curl", "openssl@3"}
		case "icu":
			return []string{"icu4c"}
		case "xml":
			return []string{"libxml2"}
		case "geospatial":
			return []string{"gdal", "geos", "proj", "udunits"}
		case "java":
			return []string{"openjdk"}
		case "database":
			return []string{"unixodbc", "libpq", "mariadb-connector-c"}
		case "javascript":
			return []string{"v8"}
		case "imaging":
			return []string{"imagemagick"}
		case "fonts":
			return []string{"freetype", "harfbuzz", "fribidi", "cairo"}
		case "encoding":
			return []string{"libiconv"}
		case "pdf":
			return []string{"poppler", "qpdf"}
		case "cpp":
			return []string{"apache-arrow"}
		}
	case "spack":
		switch category {
		case "network":
			return []string{"curl", "openssl"}
		case "icu":
			return []string{"icu4c"}
		case "xml":
			return []string{"libxml2"}
		case "geospatial":
			return []string{"gdal", "geos", "proj", "udunits"}
		case "java":
			return []string{"openjdk"}
		case "database":
			return []string{"unixodbc", "postgresql", "mariadb-c-client"}
		case "javascript":
			return []string{"v8"}
		case "imaging":
			return []string{"imagemagick"}
		case "fonts":
			return []string{"freetype", "harfbuzz", "fribidi", "cairo"}
		case "encoding":
			return []string{"libiconv"}
		case "pdf":
			return []string{"poppler", "qpdf"}
		case "cpp":
			return []string{"arrow"}
		}
	}
	return nil
}

func appendUniqueStrings(dst []string, values ...string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || slices.Contains(dst, value) {
			continue
		}
		dst = append(dst, value)
	}
	return dst
}
