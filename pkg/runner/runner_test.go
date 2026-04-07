package runner_test

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	publicrunner "github.com/rainoffallingstar/rs-reborn/pkg/runner"
)

func writeFakePublicRscript(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "Rscript")
	content := `#!/bin/sh
if [ "$1" = "--vanilla" ]; then
	shift
fi
if [ "$1" = "-e" ]; then
	cat <<'EOF'
version	4.4.1
platform	x86_64-pc-linux-gnu
arch	x86_64
os	linux-gnu
pkg_type	source
EOF
	exit 0
fi
exit 0
`
	if runtime.GOOS == "windows" {
		path += ".cmd"
		content = `@echo off
setlocal
if /I "%~1"=="--vanilla" shift
if /I "%~1"=="-e" goto inspect
exit /b 0
:inspect
echo version	4.4.1
echo platform	x86_64-pc-windows-gnu
echo arch	x86_64
echo os	mingw32
echo pkg_type	binary
exit /b 0
`
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func TestPublicScanScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "analysis.R")
	src := "library(jsonlite)\nlibrary(Biostrings)\n"
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	deps, err := publicrunner.ScanScript(script)
	if err != nil {
		t.Fatalf("ScanScript() error = %v", err)
	}
	slices.Sort(deps)
	want := []string{"Biostrings", "jsonlite"}
	if !slices.Equal(deps, want) {
		t.Fatalf("deps = %v, want %v", deps, want)
	}
}

func TestPublicResolveEnvironmentPreservesScriptArgs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RS_HOME", filepath.Join(dir, "rs-home"))

	script := filepath.Join(dir, "analysis.R")
	if err := os.WriteFile(script, []byte("library(jsonlite)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	rscript := writeFakePublicRscript(t, dir)

	env, err := publicrunner.ResolveEnvironment(publicrunner.RunOptions{
		ScriptPath:  script,
		ScriptArgs:  []string{"--flag", "value"},
		RscriptPath: rscript,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	})
	if err != nil {
		t.Fatalf("ResolveEnvironment() error = %v", err)
	}
	if !slices.Equal(env.ScriptArgs, []string{"--flag", "value"}) {
		t.Fatalf("ResolvedEnvironment.ScriptArgs = %v", env.ScriptArgs)
	}
	if _, err := os.Stat(env.LockfilePath); !os.IsNotExist(err) {
		t.Fatalf("ResolveEnvironment() should not write lockfile, stat err = %v", err)
	}
}

func TestPublicPlanToolchainEmitsEvents(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "analysis.R")
	if err := os.WriteFile(script, []byte("library(jsonlite)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	rscript := writeFakePublicRscript(t, dir)

	var events []publicrunner.Event
	report, err := publicrunner.PlanToolchain(publicrunner.ToolchainPlanOptions{
		ScriptPath:  script,
		RscriptPath: rscript,
		Progress:    io.Discard,
		Events: func(event publicrunner.Event) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("PlanToolchain() error = %v", err)
	}
	if report.Script != script {
		t.Fatalf("PlanToolchain().Script = %q, want %q", report.Script, script)
	}
	if !slices.Contains(report.DetectedDeps, "jsonlite") {
		t.Fatalf("PlanToolchain().DetectedDeps = %v", report.DetectedDeps)
	}
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	if !slices.Contains(kinds, "preparing_project") || !slices.Contains(kinds, "resolving_runtime") {
		t.Fatalf("PlanToolchain() events = %v", kinds)
	}
}
