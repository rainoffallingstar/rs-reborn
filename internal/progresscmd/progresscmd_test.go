package progresscmd

import (
	"bytes"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunSuppressesSuccessfulCommandOutput(t *testing.T) {
	var progress bytes.Buffer
	var errors bytes.Buffer
	cmd := shellCommand("echo hidden stdout; echo hidden stderr >&2", "echo hidden stdout & echo hidden stderr 1>&2")

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
	cmd := shellCommand("echo first; echo second >&2; exit 1", "echo first & echo second 1>&2 & exit /b 1")

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

func TestWriteTTYLineClearsPreviousOutput(t *testing.T) {
	var buf bytes.Buffer
	writeTTYLine(&buf, "[rs] downloading file | 1.0 MiB/5.0 MiB")
	if !strings.Contains(buf.String(), "\r\033[2K[rs] downloading file | 1.0 MiB/5.0 MiB") {
		t.Fatalf("writeTTYLine() output = %q", buf.String())
	}
}

func TestWriteSuccessClearsTTYLineBeforeDone(t *testing.T) {
	oldTTY := progressIsTTY
	t.Cleanup(func() {
		progressIsTTY = oldTTY
	})
	progressIsTTY = func(io.Writer) bool { return true }

	var buf bytes.Buffer
	writeSuccess(&buf, "downloading file")
	if !strings.Contains(buf.String(), "\033[2K[rs] downloading file done\n") {
		t.Fatalf("writeSuccess() output = %q", buf.String())
	}
}

func TestAnimateEmitsHeartbeatForNonTTY(t *testing.T) {
	oldInterval := progressHeartbeatInterval
	t.Cleanup(func() {
		progressHeartbeatInterval = oldInterval
	})
	progressHeartbeatInterval = 10 * time.Millisecond

	var buf bytes.Buffer
	stop := make(chan struct{})
	done := make(chan struct{})
	go animate(&buf, "compiling package", stop, done)
	time.Sleep(25 * time.Millisecond)
	close(stop)
	<-done

	out := buf.String()
	if !strings.Contains(out, "[rs] compiling package...") {
		t.Fatalf("animate() output = %q, want start line", out)
	}
	if !strings.Contains(out, "elapsed") {
		t.Fatalf("animate() output = %q, want heartbeat line", out)
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{d: 8 * time.Second, want: "8s"},
		{d: 72 * time.Second, want: "1m12s"},
		{d: 2*time.Hour + 5*time.Minute, want: "2h05m"},
	}
	for _, tc := range cases {
		if got := formatElapsed(tc.d); got != tc.want {
			t.Fatalf("formatElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func shellCommand(unixScript, windowsScript string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", windowsScript)
	}
	return exec.Command("/bin/sh", "-c", unixScript)
}
