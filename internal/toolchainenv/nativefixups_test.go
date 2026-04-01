package toolchainenv

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestBuildNativeFixupPlanAddsLibiconvForEncodingCategory(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "prefix")
	libDir := filepath.Join(prefix, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", libDir, err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libiconv.so.2"), []byte("binary"), 0o644); err != nil {
		t.Fatalf("WriteFile(libiconv) error = %v", err)
	}

	plan := BuildNativeFixupPlanWithEnv([]string{"PATH=" + t.TempDir()}, []string{prefix}, nil, []string{"encoding", "encoding"})
	if !reflect.DeepEqual(plan.LIBS, []string{"-liconv"}) {
		t.Fatalf("plan.LIBS = %v", plan.LIBS)
	}
	if len(plan.Reasons) != 1 {
		t.Fatalf("plan.Reasons = %v", plan.Reasons)
	}
}

func TestBuildNativeFixupPlanUsesPkgConfigFlagsWhenAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pkg-config fixture uses a POSIX shell script")
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", binDir, err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' '-I/tmp/freetype2 -L/tmp/lib -lxml2'\n"
	if err := os.WriteFile(filepath.Join(binDir, "pkg-config"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(pkg-config) error = %v", err)
	}

	plan := BuildNativeFixupPlanWithEnv([]string{"PATH=" + binDir}, nil, nil, []string{"xml"})
	if !reflect.DeepEqual(plan.CPPFLAGS, []string{"-I/tmp/freetype2"}) {
		t.Fatalf("plan.CPPFLAGS = %v", plan.CPPFLAGS)
	}
	if !reflect.DeepEqual(plan.LDFLAGS, []string{"-L/tmp/lib"}) {
		t.Fatalf("plan.LDFLAGS = %v", plan.LDFLAGS)
	}
	if !reflect.DeepEqual(plan.LIBS, []string{"-lxml2"}) {
		t.Fatalf("plan.LIBS = %v", plan.LIBS)
	}
}

func TestBuildNativeFixupPlanSkipsMissingLibraries(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "prefix")
	if err := os.MkdirAll(filepath.Join(prefix, "lib"), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Join(prefix, "lib"), err)
	}

	plan := BuildNativeFixupPlanWithEnv([]string{"PATH=" + t.TempDir()}, []string{prefix}, nil, []string{"encoding"})
	if len(plan.LIBS) != 0 || len(plan.Reasons) != 0 {
		t.Fatalf("plan = %+v, want no fixups", plan)
	}
}
