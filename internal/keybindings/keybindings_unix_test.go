//go:build !windows

package keybindings

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/logbridge"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// collector is a tiny thread-safe []string sink for Options callbacks —
// Run's own goroutines (the output-pump reader, the wait loop) can call
// Info/Warn/Err concurrently with the test's own assertions.
type collector struct {
	mu   sync.Mutex
	logs []string
}

func (c *collector) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, s)
}

func (c *collector) contains(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.logs {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// TestRunReturnsCleanExitCode proves Run runs the wrapped command through
// to completion and returns its exit code.
func TestRunReturnsCleanExitCode(t *testing.T) {
	rc, err := Run("true", nil, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
}

// TestRunReturnsNonZeroExitCode proves a wrapped command's real exit code
// (not force-mapped to 0/1) comes back through Run.
func TestRunReturnsNonZeroExitCode(t *testing.T) {
	warn := &collector{}
	rc, err := Run("exit 5", nil, Options{Warn: warn.add})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rc != 5 {
		t.Fatalf("rc = %d, want 5", rc)
	}
}

// TestRunStreamsOutputThroughFormatterAndLogFn proves the wrapped process's
// stdout reaches both LogFn (raw log-file append) and, via the formatter,
// Stdout — the shared-pump wiring this ticket's brief requires ("Import
// internal/streams.OutputPump").
func TestRunStreamsOutputThroughFormatterAndLogFn(t *testing.T) {
	var logged []string
	var lmu sync.Mutex
	var out strings.Builder
	var omu sync.Mutex

	rc, err := Run(`echo hello-from-child`, nil, Options{
		LogFn: func(s string) {
			lmu.Lock()
			defer lmu.Unlock()
			logged = append(logged, s)
		},
		Formatter: func(line string) string { return "STAMPED: " + line },
		Stdout:    &syncWriter{w: &out, mu: &omu},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	lmu.Lock()
	joined := strings.Join(logged, "")
	lmu.Unlock()
	if !strings.Contains(joined, "hello-from-child") {
		t.Fatalf("LogFn never saw the child's output: %q", joined)
	}
	omu.Lock()
	stdoutText := out.String()
	omu.Unlock()
	if !strings.Contains(stdoutText, "STAMPED: hello-from-child") {
		t.Fatalf("Stdout = %q, want the formatter applied", stdoutText)
	}
}

type syncWriter struct {
	w  *strings.Builder
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// TestRunStartsAndStopsLogBridge proves a Run with LogBridge configured
// starts the listener (an Info line names its port) and shuts it down
// cleanly when the wrapped command finishes, with no hang.
func TestRunStartsAndStopsLogBridge(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logbridge.DiscoveryDir = filepath.Join(dir, "discovery")

	info := &collector{}
	rc, err := Run("true", nil, Options{
		LogPath:   logPath,
		LogBridge: &logbridge.Config{Hosts: []string{"example.com"}, PortRange: [2]int{19970, 19980}},
		Info:      info.add,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !info.contains("[log-bridge] listening on port") {
		t.Fatalf("Info lines = %v, want a log-bridge listening line", info.logs)
	}
}

// TestRunSkipsLogBridgeWhenDisabled proves a nil/disabled LogBridge starts
// no listener at all — no Info line naming a port, and Run still completes.
func TestRunSkipsLogBridgeWhenDisabled(t *testing.T) {
	info := &collector{}
	rc, err := Run("true", nil, Options{Info: info.add})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if info.contains("log-bridge") {
		t.Fatalf("Info lines = %v, want no log-bridge line when disabled", info.logs)
	}
}

// TestSignalByNameAcceptsBareAndPrefixed proves both "TERM" and "SIGTERM"
// (any case) resolve to the same signal, and an unknown name is rejected.
func TestSignalByNameAcceptsBareAndPrefixed(t *testing.T) {
	for _, name := range []string{"TERM", "SIGTERM", "term", "sigterm"} {
		sig, ok := signalByName(name)
		if !ok || sig != syscall.SIGTERM {
			t.Errorf("signalByName(%q) = (%v, %v), want (SIGTERM, true)", name, sig, ok)
		}
	}
	if _, ok := signalByName("NOTASIGNAL"); ok {
		t.Fatal("signalByName(NOTASIGNAL) = ok, want false")
	}
}

// TestSendSignalUnknownNameCallsErr proves an unresolvable signal name
// reports via Err, never crashes.
func TestSendSignalUnknownNameCallsErr(t *testing.T) {
	errs := &collector{}
	sendSignal("NOTASIGNAL", "/does/not/matter", Options{Err: errs.add})
	if !errs.contains("unknown signal name") {
		t.Fatalf("Err lines = %v, want one naming the bad signal", errs.logs)
	}
}

// TestSendSignalMissingPidFileWarns proves a nonexistent pid_file warns
// rather than erroring — matches admin_lib/keybindings.py::_send_signal's
// own "stale/missing PID file -> warn + no-op" contract.
func TestSendSignalMissingPidFileWarns(t *testing.T) {
	warns := &collector{}
	sendSignal("TERM", filepath.Join(t.TempDir(), "nope.pid"), Options{Warn: warns.add})
	if !warns.contains("can't read pid file") {
		t.Fatalf("Warn lines = %v, want one naming the missing file", warns.logs)
	}
}

// TestSendSignalDeadPidWarns proves a pid file naming a process that isn't
// running warns "is gone" instead of erroring.
func TestSendSignalDeadPidWarns(t *testing.T) {
	// Spawn and immediately reap a child so its PID is guaranteed free of a
	// live process (barring PID reuse in the brief window, acceptable here).
	c := exec.Command("/bin/sh", "-c", "true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "dead.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(c.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	warns := &collector{}
	sendSignal("TERM", pidFile, Options{Warn: warns.add})
	if !warns.contains("is gone") {
		t.Fatalf("Warn lines = %v, want one saying the pid is gone", warns.logs)
	}
}

// TestSendSignalDeliversToLiveProcess proves sendSignal actually reaches a
// running process: a real long-lived child is signaled TERM via its pid
// file and exits.
func TestSendSignalDeliversToLiveProcess(t *testing.T) {
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if c.Process != nil {
			_ = c.Process.Kill()
		}
	}()
	pidFile := filepath.Join(t.TempDir(), "live.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(c.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	sendSignal("TERM", pidFile, Options{})

	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
		// exited — SIGTERM delivered.
	case <-time.After(3 * time.Second):
		t.Fatal("process did not exit after sendSignal(TERM)")
	}
}

// TestDispatchBindingRunInline proves a `run` binding actually executes the
// shell snippet.
func TestDispatchBindingRunInline(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker.txt")
	dispatchBinding(validate.KeyBinding{Run: "touch " + marker}, Options{})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("marker file %s was never created by the inline run binding", marker)
}

// TestDispatchBindingAction proves an `action` binding calls opts.RunAction
// with the (already @-stripped, per validate.ParseKeys) action name.
func TestDispatchBindingAction(t *testing.T) {
	var got string
	dispatchBinding(validate.KeyBinding{Action: "rebuild"}, Options{
		RunAction: func(name string) error {
			got = name
			return nil
		},
	})
	if got != "rebuild" {
		t.Fatalf("RunAction called with %q, want rebuild", got)
	}
}

// TestDispatchBindingActionErrorReportsErr proves a RunAction failure is
// reported via Err rather than propagated/crashing the wrapped process's
// own loop.
func TestDispatchBindingActionErrorReportsErr(t *testing.T) {
	errs := &collector{}
	dispatchBinding(validate.KeyBinding{Action: "rebuild"}, Options{
		RunAction: func(name string) error { return os.ErrInvalid },
		Err:       errs.add,
	})
	if !errs.contains("rebuild") {
		t.Fatalf("Err lines = %v, want one naming the failed action", errs.logs)
	}
}
