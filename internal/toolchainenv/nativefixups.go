package toolchainenv

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

var nativeFixupsFindInPath = FindInPath

type NativeFixupPlan struct {
	CPPFLAGS []string
	LDFLAGS  []string
	LIBS     []string
	Reasons  []string
}

type nativeFixupRule struct {
	Category       string
	PkgConfigNames []string
	LibraryNames   []string
	RequireAllLibs bool
	CPPFLAGS       []string
	LDFLAGS        []string
	LIBS           []string
	Reason         string
}

func BuildNativeFixupPlan(prefixes, categories []string) NativeFixupPlan {
	return BuildNativeFixupPlanWithEnv(os.Environ(), prefixes, nil, categories)
}

func BuildNativeFixupPlanWithEnv(baseEnv, prefixes, pkgConfigPaths, categories []string) NativeFixupPlan {
	plan := NativeFixupPlan{
		CPPFLAGS: []string{},
		LDFLAGS:  []string{},
		LIBS:     []string{},
		Reasons:  []string{},
	}
	env := Apply(baseEnv, prefixes, pkgConfigPaths)
	prefixes = cleanList(prefixes)
	seenCategories := map[string]struct{}{}
	for _, category := range categories {
		category = strings.TrimSpace(strings.ToLower(category))
		if category == "" {
			continue
		}
		if _, ok := seenCategories[category]; ok {
			continue
		}
		seenCategories[category] = struct{}{}
		for _, rule := range nativeFixupRules() {
			if rule.Category != category {
				continue
			}
			if pkgPlan, ok := pkgConfigFixupPlan(env, rule); ok {
				plan = mergeNativeFixupPlans(plan, pkgPlan)
				continue
			}
			if !prefixesProvideLibraries(prefixes, rule.LibraryNames, rule.RequireAllLibs) {
				continue
			}
			plan.CPPFLAGS = appendUniqueStrings(plan.CPPFLAGS, rule.CPPFLAGS...)
			plan.LDFLAGS = appendUniqueStrings(plan.LDFLAGS, rule.LDFLAGS...)
			plan.LIBS = appendUniqueStrings(plan.LIBS, rule.LIBS...)
			if strings.TrimSpace(rule.Reason) != "" && !slices.Contains(plan.Reasons, rule.Reason) {
				plan.Reasons = append(plan.Reasons, rule.Reason)
			}
		}
	}
	return plan
}

func nativeFixupRules() []nativeFixupRule {
	return []nativeFixupRule{
		{
			Category:       "encoding",
			PkgConfigNames: []string{"libiconv"},
			LibraryNames:   []string{"iconv"},
			LIBS:           []string{"-liconv"},
			Reason:         "encoding packages may compile against GNU libiconv headers and still need an explicit -liconv during shared-library linking",
		},
		{
			Category:       "xml",
			PkgConfigNames: []string{"libxml-2.0"},
			LibraryNames:   []string{"xml2"},
			LIBS:           []string{"-lxml2"},
			Reason:         "XML packages commonly need libxml2 compile and link flags when source builds run against a user-local prefix",
		},
		{
			Category:       "network",
			PkgConfigNames: []string{"libcurl"},
			LibraryNames:   []string{"curl"},
			LIBS:           []string{"-lcurl"},
			Reason:         "network packages commonly need libcurl compile and link flags when source builds run against a user-local prefix",
		},
		{
			Category:       "network",
			PkgConfigNames: []string{"openssl"},
			LibraryNames:   []string{"ssl", "crypto"},
			RequireAllLibs: true,
			LIBS:           []string{"-lssl", "-lcrypto"},
			Reason:         "OpenSSL-backed packages commonly need explicit SSL and crypto link flags in rootless builds",
		},
		{
			Category:       "icu",
			PkgConfigNames: []string{"icu-i18n", "icu-uc"},
			LibraryNames:   []string{"icui18n", "icuuc", "icudata"},
			RequireAllLibs: true,
			LIBS:           []string{"-licui18n", "-licuuc", "-licudata"},
			Reason:         "ICU-backed packages commonly need multi-library ICU link flags and sometimes extra include directories from pkg-config",
		},
		{
			Category:       "fonts",
			PkgConfigNames: []string{"freetype2", "harfbuzz", "fribidi", "cairo"},
			Reason:         "font and text-rendering packages commonly need pkg-config supplied freetype/harfbuzz/fribidi/cairo flags in rootless builds",
		},
	}
}

func prefixesProvideLibraries(prefixes, libraryNames []string, requireAll bool) bool {
	if len(libraryNames) == 0 {
		return false
	}
	if requireAll {
		for _, libraryName := range libraryNames {
			if !prefixesProvideAnyLibrary(prefixes, libraryName) {
				return false
			}
		}
		return true
	}
	return prefixesProvideAnyLibrary(prefixes, libraryNames...)
}

func prefixesProvideAnyLibrary(prefixes []string, libraryNames ...string) bool {
	for _, prefix := range prefixes {
		if prefixProvidesAnyLibrary(prefix, libraryNames...) {
			return true
		}
	}
	return false
}

func pkgConfigFixupPlan(env []string, rule nativeFixupRule) (NativeFixupPlan, bool) {
	if len(rule.PkgConfigNames) == 0 {
		return NativeFixupPlan{}, false
	}
	path, err := nativeFixupsFindInPath("pkg-config", env)
	if err != nil {
		return NativeFixupPlan{}, false
	}
	args := append([]string{"--cflags", "--libs"}, rule.PkgConfigNames...)
	cmd := exec.Command(path, args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return NativeFixupPlan{}, false
	}
	flags := splitShellWords(string(bytes.TrimSpace(output)))
	if len(flags) == 0 {
		return NativeFixupPlan{}, false
	}
	plan := NativeFixupPlan{
		CPPFLAGS: []string{},
		LDFLAGS:  []string{},
		LIBS:     []string{},
		Reasons:  []string{},
	}
	for _, flag := range flags {
		switch {
		case strings.HasPrefix(flag, "-I"):
			plan.CPPFLAGS = appendUniqueStrings(plan.CPPFLAGS, flag)
		case strings.HasPrefix(flag, "-L"), strings.HasPrefix(flag, "-Wl,"):
			plan.LDFLAGS = appendUniqueStrings(plan.LDFLAGS, flag)
		default:
			plan.LIBS = appendUniqueStrings(plan.LIBS, flag)
		}
	}
	if strings.TrimSpace(rule.Reason) != "" {
		plan.Reasons = append(plan.Reasons, rule.Reason)
	}
	return plan, true
}

func mergeNativeFixupPlans(left, right NativeFixupPlan) NativeFixupPlan {
	left.CPPFLAGS = appendUniqueStrings(left.CPPFLAGS, right.CPPFLAGS...)
	left.LDFLAGS = appendUniqueStrings(left.LDFLAGS, right.LDFLAGS...)
	left.LIBS = appendUniqueStrings(left.LIBS, right.LIBS...)
	left.Reasons = appendUniqueStrings(left.Reasons, right.Reasons...)
	return left
}

func prefixProvidesAnyLibrary(prefix string, libraryNames ...string) bool {
	entries, err := os.ReadDir(filepath.Join(prefix, "lib"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		for _, libraryName := range libraryNames {
			if matchesLibraryName(name, libraryName) {
				return true
			}
		}
	}
	return false
}

func matchesLibraryName(filename, libraryName string) bool {
	libraryName = strings.TrimSpace(strings.ToLower(libraryName))
	if libraryName == "" {
		return false
	}
	filename = strings.ToLower(strings.TrimSpace(filename))
	if filename == "" {
		return false
	}
	prefix := "lib" + libraryName
	switch {
	case filename == prefix+".so",
		strings.HasPrefix(filename, prefix+".so."),
		filename == prefix+".dylib",
		strings.HasPrefix(filename, prefix+".dylib."),
		filename == prefix+".a",
		filename == prefix+".dll",
		filename == prefix+".dll.a":
		return true
	default:
		return false
	}
}
