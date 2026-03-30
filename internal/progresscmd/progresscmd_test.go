package progresscmd

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestRunSuppressesSuccessfulCommandOutput(t *testing.T) {
	var progress bytes.Buffer
	var errors bytes.Buffer
	cmd := exec.Command("/bin/sh", "-c", "echo hidden stdout; echo hidden stderr >&2")

	if err := Run(cmd, "testing success", &progress, &errors); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := errors.String(); got != "" {
		t.Fatalf("errors = %q, want empty", got)
	}
	if !strings.Contains(progress.String(), "[rs] testing success...") {
		t.Fatalf("progress = %q, want start message", progress.String())
	}
}

func TestRunPrintsTailOnFailure(t *testing.T) {
	var progress bytes.Buffer
	var errors bytes.Buffer
	cmd := exec.Command("/bin/sh", "-c", "echo first; echo second >&2; exit 1")

	err := Run(cmd, "testing failure", &progress, &errors)
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	out := errors.String()
	if !strings.Contains(out, "[rs] testing failure failed") {
		t.Fatalf("errors = %q, want failure header", out)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("errors = %q, want command tail", out)
	}
}

func TestStageWritesMessage(t *testing.T) {
	var buf bytes.Buffer
	Stage(&buf, "resolving dependencies")
	if !strings.Contains(buf.String(), "[rs] resolving dependencies") {
		t.Fatalf("Stage() output = %q", buf.String())
	}
}

func TestCopyReportsNonTTYStage(t *testing.T) {
	var progress bytes.Buffer
	var dst bytes.Buffer
	src := strings.NewReader("abcdef")
	if err := Copy(&dst, src, int64(src.Len()), "downloading file", &progress); err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if got := dst.String(); got != "abcdef" {
		t.Fatalf("Copy() wrote %q, want abcdef", got)
	}
	if !strings.Contains(progress.String(), "[rs] downloading file...") {
		t.Fatalf("progress = %q, want stage line", progress.String())
	}
}
