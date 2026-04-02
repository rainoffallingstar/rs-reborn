package progresscmd

import (
	"bytes"
	"fmt"
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

func TestRunWithOptionsSuppressesTTYSuccessLine(t *testing.T) {
	oldTTY := progressIsTTY
	t.Cleanup(func() {
		progressIsTTY = oldTTY
	})
	progressIsTTY = func(io.Writer) bool { return true }

	var progress bytes.Buffer
	var errors bytes.Buffer
	cmd := shellCommand("echo hidden stdout; echo hidden stderr >&2", "echo hidden stdout & echo hidden stderr 1>&2")

	if err := RunWithOptions(cmd, "testing success", &progress, &errors, RunOptions{
		SuppressTTYSuccess: true,
	}); err != nil {
		t.Fatalf("RunWithOptions() error = %v", err)
	}
	if got := errors.String(); got != "" {
		t.Fatalf("errors = %q, want empty", got)
	}
	if strings.Contains(progress.String(), "testing success done") {
		t.Fatalf("progress = %q, want TTY success line suppressed", progress.String())
	}
}

func TestRunWithOptionsSuppressesFastNonTTYStartWhenDelayed(t *testing.T) {
	var progress bytes.Buffer
	var errors bytes.Buffer
	cmd := timedShellCommand(20)

	if err := RunWithOptions(cmd, "testing success", &progress, &errors, RunOptions{
		NonTTYStartDelay: 200 * time.Millisecond,
	}); err != nil {
		t.Fatalf("RunWithOptions() error = %v", err)
	}
	if got := progress.String(); got != "" {
		t.Fatalf("progress = %q, want delayed non-TTY start to stay silent for fast command", got)
	}
}

func TestRunWithOptionsEmitsDelayedNonTTYStartForSlowCommand(t *testing.T) {
	var progress bytes.Buffer
	var errors bytes.Buffer
	cmd := timedShellCommand(80)

	if err := RunWithOptions(cmd, "testing success", &progress, &errors, RunOptions{
		NonTTYStartDelay: 20 * time.Millisecond,
	}); err != nil {
		t.Fatalf("RunWithOptions() error = %v", err)
	}
	if !strings.Contains(progress.String(), "[rs] testing success...") {
		t.Fatalf("progress = %q, want delayed non-TTY start line", progress.String())
	}
}

func TestAnimateUsesCustomNonTTYHeartbeat(t *testing.T) {
	oldInterval := progressHeartbeatInterval
	t.Cleanup(func() {
		progressHeartbeatInterval = oldInterval
	})
	progressHeartbeatInterval = time.Hour

	var buf bytes.Buffer
	stop := make(chan struct{})
	done := make(chan struct{})
	go animate(&buf, "compiling package", stop, done, RunOptions{
		NonTTYHeartbeat: 10 * time.Millisecond,
	})
	time.Sleep(25 * time.Millisecond)
	close(stop)
	<-done

	out := buf.String()
	if !strings.Contains(out, "[rs] compiling package...") {
		t.Fatalf("animate() output = %q, want start line", out)
	}
	if !strings.Contains(out, "elapsed") {
		t.Fatalf("animate() output = %q, want custom heartbeat line", out)
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

func TestCopyWithOptionsSuppressesFastNonTTYStartWhenDelayed(t *testing.T) {
	var progress bytes.Buffer
	var dst bytes.Buffer
	src := strings.NewReader("abcdef")
	if err := CopyWithOptions(&dst, src, int64(src.Len()), "downloading file", &progress, CopyOptions{
		NonTTYStartDelay: 200 * time.Millisecond,
	}); err != nil {
		t.Fatalf("CopyWithOptions() error = %v", err)
	}
	if got := progress.String(); got != "" {
		t.Fatalf("progress = %q, want delayed non-TTY copy to stay silent for fast transfer", got)
	}
}

func TestCopyWithOptionsEmitsDelayedNonTTYStartForSlowCopy(t *testing.T) {
	var progress bytes.Buffer
	var dst bytes.Buffer
	src := &slowReader{data: []byte("abcdef"), delay: 60 * time.Millisecond}
	if err := CopyWithOptions(&dst, src, 6, "downloading file", &progress, CopyOptions{
		NonTTYStartDelay: 20 * time.Millisecond,
	}); err != nil {
		t.Fatalf("CopyWithOptions() error = %v", err)
	}
	if !strings.Contains(progress.String(), "[rs] downloading file...") {
		t.Fatalf("progress = %q, want delayed non-TTY copy start line", progress.String())
	}
}

type slowReader struct {
	data  []byte
	delay time.Duration
	read  bool
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.read = true
	n := copy(p, r.data)
	return n, io.EOF
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
	go animate(&buf, "compiling package", stop, done, RunOptions{})
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

func TestTryAcquireTTYProgressSessionFallsBackInsteadOfBlocking(t *testing.T) {
	oldTTY := progressIsTTY
	t.Cleanup(func() {
		progressIsTTY = oldTTY
	})
	progressIsTTY = func(io.Writer) bool { return true }

	var first bytes.Buffer
	release := acquireTTYProgressSession(&first)
	defer release()

	started := time.Now()
	var second bytes.Buffer
	releaseSecond, ok := tryAcquireTTYProgressSession(&second)
	if ok {
		releaseSecond()
		t.Fatal("tryAcquireTTYProgressSession() = acquired, want fallback while another session is active")
	}
	if time.Since(started) > 20*time.Millisecond {
		t.Fatalf("tryAcquireTTYProgressSession() blocked for %s", time.Since(started))
	}
}

func TestStageSkipsTTYWriteWhenProgressSessionIsBusy(t *testing.T) {
	oldTTY := progressIsTTY
	t.Cleanup(func() {
		progressIsTTY = oldTTY
	})
	progressIsTTY = func(io.Writer) bool { return true }

	var first bytes.Buffer
	release := acquireTTYProgressSession(&first)
	defer release()

	var second bytes.Buffer
	started := time.Now()
	Stage(&second, "resolving dependencies")
	if time.Since(started) > 20*time.Millisecond {
		t.Fatalf("Stage() blocked for %s", time.Since(started))
	}
	if got := second.String(); got != "" {
		t.Fatalf("Stage() output = %q, want empty fallback while session busy", got)
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

func timedShellCommand(ms int) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("powershell", "-NoProfile", "-Command", fmt.Sprintf("Start-Sleep -Milliseconds %d", ms))
	}
	return exec.Command("/bin/sh", "-c", fmt.Sprintf("sleep %.3f", float64(ms)/1000.0))
}
