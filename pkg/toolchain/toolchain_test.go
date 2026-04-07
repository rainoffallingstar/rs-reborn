package toolchain_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	publictoolchain "github.com/rainoffallingstar/rs-reborn/pkg/toolchain"
)

func writeToolchainCommand(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	content := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		path += ".cmd"
		content = "@echo off\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func TestPublicToolchainDetectAndRecommend(t *testing.T) {
	home := t.TempDir()
	prefix := filepath.Join(home, "homebrew")
	for _, path := range []string{
		prefix,
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	if !slices.Contains(publictoolchain.SupportedPresets(), "homebrew") {
		t.Fatalf("SupportedPresets() missing homebrew")
	}

	candidates, err := publictoolchain.DetectCandidates(home)
	if err != nil {
		t.Fatalf("DetectCandidates() error = %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("DetectCandidates() returned no candidates")
	}
	if candidates[0].Preset != "homebrew" {
		t.Fatalf("DetectCandidates()[0].Preset = %q, want homebrew", candidates[0].Preset)
	}

	recommended, err := publictoolchain.RecommendedCandidate(home)
	if err != nil {
		t.Fatalf("RecommendedCandidate() error = %v", err)
	}
	if recommended == nil || recommended.Preset != "homebrew" {
		t.Fatalf("RecommendedCandidate() = %#v, want homebrew", recommended)
	}

	described, err := publictoolchain.DescribePreset("homebrew", home)
	if err != nil {
		t.Fatalf("DescribePreset() error = %v", err)
	}
	if described == nil || !described.Complete {
		t.Fatalf("DescribePreset() = %#v, want complete candidate", described)
	}
}

func TestPublicToolchainPlanPreviewAndValidate(t *testing.T) {
	prefix := t.TempDir()
	for _, path := range []string{
		filepath.Join(prefix, "bin"),
		filepath.Join(prefix, "include"),
		filepath.Join(prefix, "lib"),
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	preview := publictoolchain.BuildPreview([]string{prefix}, []string{filepath.Join(prefix, "lib", "pkgconfig")})
	if !slices.Contains(preview.CPPFLAGS, "-I"+filepath.Join(prefix, "include")) {
		t.Fatalf("Preview.CPPFLAGS = %v", preview.CPPFLAGS)
	}

	validation := publictoolchain.Validate([]string{prefix}, []string{filepath.Join(prefix, "lib", "pkgconfig")}, []string{"PATH=" + os.Getenv("PATH")})
	if len(validation.Errors) != 0 {
		t.Fatalf("Validate().Errors = %v, want none", validation.Errors)
	}

	plan, err := publictoolchain.BuildPackagePlan("enva", []string{"encoding", "xml"})
	if err != nil {
		t.Fatalf("BuildPackagePlan() error = %v", err)
	}
	if !slices.Contains(plan.Packages, "cmake") || !slices.Contains(plan.Packages, "libxml2") {
		t.Fatalf("BuildPackagePlan().Packages = %v", plan.Packages)
	}

	categories := publictoolchain.NativeCategoriesForPackages([]string{"haven", "xml2"})
	if !slices.Contains(categories, "encoding") || !slices.Contains(categories, "xml") {
		t.Fatalf("NativeCategoriesForPackages() = %v", categories)
	}

	env := publictoolchain.ApplyWithPlan(
		[]string{"PATH=/usr/bin"},
		[]string{prefix},
		[]string{filepath.Join(prefix, "lib", "pkgconfig")},
		publictoolchain.NativeFixupPlan{
			LDFLAGS: []string{"-L" + filepath.Join(prefix, "lib")},
			LIBS:    []string{"-liconv"},
		},
	)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "RS_TOOLCHAIN_PREFIXES="+prefix) {
		t.Fatalf("ApplyWithPlan() env missing toolchain prefix: %v", env)
	}
	if !strings.Contains(joined, "LIBS=-liconv") {
		t.Fatalf("ApplyWithPlan() env missing LIBS: %v", env)
	}
}

func TestPublicToolchainWrapCommand(t *testing.T) {
	dir := t.TempDir()
	runner := writeToolchainCommand(t, dir, "enva")
	prefix := filepath.Join(dir, "rattler", "envs", "rs-sysdeps")
	for _, path := range []string{
		filepath.Join(prefix, "bin"),
		filepath.Join(prefix, "lib", "pkgconfig"),
		filepath.Join(prefix, "share", "pkgconfig"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	env := []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"RS_TOOLCHAIN_PREFIXES=" + prefix,
		"RS_PKG_CONFIG_PATH=" + filepath.Join(prefix, "lib", "pkgconfig"),
	}
	name, args, wrappedEnv, wrapped, err := publictoolchain.WrapCommand("g++", []string{"smoke.cpp", "-o", "smoke"}, env)
	if err != nil {
		t.Fatalf("WrapCommand() error = %v", err)
	}
	if !wrapped {
		t.Fatal("WrapCommand() wrapped = false, want true")
	}
	if name != runner {
		t.Fatalf("WrapCommand() name = %q, want %q", name, runner)
	}
	if len(args) < 4 || args[0] != "run" || args[1] != "rs-sysdeps" || args[2] != "--" || args[3] != "g++" {
		t.Fatalf("WrapCommand() args = %v", args)
	}
	if got := strings.Join(wrappedEnv, "\n"); !strings.Contains(got, "RS_TOOLCHAIN_PREFIXES="+prefix) {
		t.Fatalf("WrapCommand() env = %v", wrappedEnv)
	}
}
