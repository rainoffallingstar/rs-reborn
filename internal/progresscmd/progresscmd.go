package progresscmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const defaultTailLines = 120

var progressIsTTY = isTTY
var progressHeartbeatInterval = 15 * time.Second
var ttyProgressSessionMu sync.Mutex

type RunOptions struct {
	SuppressTTYSuccess bool
	NonTTYStartDelay   time.Duration
	NonTTYHeartbeat    time.Duration
}

type CopyOptions struct {
	NonTTYStartDelay time.Duration
	NonTTYHeartbeat  time.Duration
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func Stage(w io.Writer, label string) {
	if w == nil || w == io.Discard || label == "" {
		return
	}
	if progressIsTTY(w) {
		release, claimed := tryAcquireTTYProgressSession(w)
		if !claimed {
			return
		}
		defer release()
		fmt.Fprintf(w, "[rs] %s\n", label)
		return
	}
	fmt.Fprintf(w, "[rs] %s...\n", label)
}

func Run(cmd *exec.Cmd, label string, progress io.Writer, errors io.Writer) error {
	return RunWithOptions(cmd, label, progress, errors, RunOptions{})
}

func RunWithOptions(cmd *exec.Cmd, label string, progress io.Writer, errors io.Writer, opts RunOptions) error {
	if progress == nil {
		progress = io.Discard
	}
	if errors == nil {
		errors = progress
	}
	animatedProgress := progress
	releaseTTY, claimedTTY := tryAcquireTTYProgressSession(progress)
	if claimedTTY {
		defer releaseTTY()
	} else if progressIsTTY(progress) {
		animatedProgress = io.Discard
	}

	buffer := &lockedBuffer{}
	cmd.Stdout = buffer
	cmd.Stderr = buffer

	stop := make(chan struct{})
	done := make(chan struct{})
	go animate(animatedProgress, label, stop, done, opts)

	err := cmd.Run()
	close(stop)
	<-done

	if err != nil {
		writeFailure(errors, label, buffer.String())
		return err
	}
	if opts.SuppressTTYSuccess && progressIsTTY(animatedProgress) {
		clearTTYLine(animatedProgress)
		return nil
	}
	writeSuccess(animatedProgress, label)
	return nil
}

func Copy(dst io.Writer, src io.Reader, size int64, label string, progress io.Writer) error {
	return CopyWithOptions(dst, src, size, label, progress, CopyOptions{})
}

func CopyWithOptions(dst io.Writer, src io.Reader, size int64, label string, progress io.Writer, opts CopyOptions) error {
	if progress == nil {
		progress = io.Discard
	}
	animatedProgress := progress
	releaseTTY, claimedTTY := tryAcquireTTYProgressSession(progress)
	if claimedTTY {
		defer releaseTTY()
	} else if progressIsTTY(progress) {
		animatedProgress = io.Discard
	}
	pw := &progressWriter{
		label:    label,
		progress: animatedProgress,
		total:    size,
		opts:     opts,
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go pw.animate(stop, done)
	_, err := io.Copy(io.MultiWriter(dst, pw), src)
	close(stop)
	<-done
	if err != nil {
		return err
	}
	pw.finish()
	return nil
}

func animate(w io.Writer, label string, stop <-chan struct{}, done chan<- struct{}, opts RunOptions) {
	defer close(done)
	if w == nil || w == io.Discard || label == "" {
		<-stop
		return
	}
	start := time.Now()
	if !progressIsTTY(w) {
		delay := opts.NonTTYStartDelay
		heartbeat := nonTTYHeartbeatInterval(opts)
		var delayTimer *time.Timer
		var ticker *time.Ticker
		defer func() {
			if delayTimer != nil {
				delayTimer.Stop()
			}
			if ticker != nil {
				ticker.Stop()
			}
		}()
		if delay > 0 {
			delayTimer = time.NewTimer(delay)
		} else {
			fmt.Fprintf(w, "[rs] %s...\n", label)
			ticker = time.NewTicker(heartbeat)
		}
		for {
			var delayC <-chan time.Time
			var tickerC <-chan time.Time
			if delayTimer != nil {
				delayC = delayTimer.C
			}
			if ticker != nil {
				tickerC = ticker.C
			}
			select {
			case <-stop:
				return
			case <-delayC:
				fmt.Fprintf(w, "[rs] %s...\n", label)
				ticker = time.NewTicker(heartbeat)
				delayTimer = nil
			case <-tickerC:
				fmt.Fprintf(w, "[rs] %s (%s elapsed)\n", label, formatElapsed(time.Since(start)))
			}
		}
	}
	frames := []string{"|", "/", "-", "\\"}
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	idx := 0
	for {
		writeTTYLine(w, fmt.Sprintf("[rs] %s %s %s", label, frames[idx%len(frames)], formatElapsed(time.Since(start))))
		idx++
		select {
		case <-stop:
			clearTTYLine(w)
			return
		case <-ticker.C:
		}
	}
}

func writeSuccess(w io.Writer, label string) {
	if w == nil || w == io.Discard || label == "" {
		return
	}
	if progressIsTTY(w) {
		writeTTYLine(w, fmt.Sprintf("[rs] %s done", label))
		fmt.Fprintln(w)
	}
}

func writeFailure(w io.Writer, label, output string) {
	if w == nil || w == io.Discard {
		return
	}
	if label != "" {
		fmt.Fprintf(w, "[rs] %s failed\n", label)
	}
	tail := tailLines(output, defaultTailLines)
	if strings.TrimSpace(tail) == "" {
		return
	}
	fmt.Fprintln(w, tail)
}

func tailLines(output string, n int) string {
	if n <= 0 {
		return ""
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func isTTY(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

type progressWriter struct {
	mu       sync.Mutex
	written  int64
	total    int64
	label    string
	progress io.Writer
	opts     CopyOptions
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.written += int64(len(p))
	return len(p), nil
}

func (w *progressWriter) animate(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	if w.progress == nil || w.progress == io.Discard || w.label == "" {
		<-stop
		return
	}
	start := time.Now()
	if !progressIsTTY(w.progress) {
		delay := w.opts.NonTTYStartDelay
		heartbeat := copyNonTTYHeartbeatInterval(w.opts)
		var delayTimer *time.Timer
		var ticker *time.Ticker
		defer func() {
			if delayTimer != nil {
				delayTimer.Stop()
			}
			if ticker != nil {
				ticker.Stop()
			}
		}()
		if delay > 0 {
			delayTimer = time.NewTimer(delay)
		} else {
			fmt.Fprintf(w.progress, "[rs] %s...\n", w.label)
			ticker = time.NewTicker(heartbeat)
		}
		for {
			var delayC <-chan time.Time
			var tickerC <-chan time.Time
			if delayTimer != nil {
				delayC = delayTimer.C
			}
			if ticker != nil {
				tickerC = ticker.C
			}
			select {
			case <-stop:
				return
			case <-delayC:
				fmt.Fprintf(w.progress, "[rs] %s...\n", w.label)
				ticker = time.NewTicker(heartbeat)
				delayTimer = nil
			case <-tickerC:
				fmt.Fprintf(w.progress, "[rs] %s (%s elapsed)\n", w.label, formatElapsed(time.Since(start)))
			}
		}
	}
	frames := []string{"|", "/", "-", "\\"}
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	idx := 0
	for {
		w.mu.Lock()
		written := w.written
		total := w.total
		w.mu.Unlock()
		status := humanBytes(written)
		if total > 0 {
			status = fmt.Sprintf("%s/%s", humanBytes(written), humanBytes(total))
		}
		writeTTYLine(w.progress, fmt.Sprintf("[rs] %s %s %s %s", w.label, frames[idx%len(frames)], status, formatElapsed(time.Since(start))))
		idx++
		select {
		case <-stop:
			clearTTYLine(w.progress)
			return
		case <-ticker.C:
		}
	}
}

func (w *progressWriter) finish() {
	if w.progress == nil || w.progress == io.Discard || w.label == "" {
		return
	}
	if progressIsTTY(w.progress) {
		writeTTYLine(w.progress, fmt.Sprintf("[rs] %s done", w.label))
		fmt.Fprintln(w.progress)
	}
}

func writeTTYLine(w io.Writer, line string) {
	clearTTYLine(w)
	fmt.Fprint(w, line)
}

func clearTTYLine(w io.Writer) {
	fmt.Fprint(w, "\r\033[2K")
}

func acquireTTYProgressSession(w io.Writer) func() {
	if w == nil || w == io.Discard || !progressIsTTY(w) {
		return func() {}
	}
	ttyProgressSessionMu.Lock()
	return func() {
		ttyProgressSessionMu.Unlock()
	}
}

func tryAcquireTTYProgressSession(w io.Writer) (func(), bool) {
	if w == nil || w == io.Discard || !progressIsTTY(w) {
		return func() {}, false
	}
	if !ttyProgressSessionMu.TryLock() {
		return func() {}, false
	}
	return func() {
		ttyProgressSessionMu.Unlock()
	}, true
}

func nonTTYHeartbeatInterval(opts RunOptions) time.Duration {
	if opts.NonTTYHeartbeat > 0 {
		return opts.NonTTYHeartbeat
	}
	return progressHeartbeatInterval
}

func copyNonTTYHeartbeatInterval(opts CopyOptions) time.Duration {
	if opts.NonTTYHeartbeat > 0 {
		return opts.NonTTYHeartbeat
	}
	return progressHeartbeatInterval
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		minutes := int(d / time.Minute)
		seconds := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}
