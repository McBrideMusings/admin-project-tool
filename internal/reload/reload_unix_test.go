//go:build !windows

package reload

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeReporter records every Warn/Ok/Err call it receives, for assertions
// without needing a real *luahost.Host.
type fakeReporter struct {
	mu               sync.Mutex
	warns, oks, errs []string
}

func (f *fakeReporter) Warn(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warns = append(f.warns, msg)
}
func (f *fakeReporter) Ok(msg string) { f.mu.Lock(); defer f.mu.Unlock(); f.oks = append(f.oks, msg) }
func (f *fakeReporter) Err(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs = append(f.errs, msg)
}

// TestSignalNoPIDFiles proves Signal reports "no running targets" (not "no
// live targets") when the prefix matches nothing at all — the two distinct
// error messages Signal produces.
func TestSignalNoPIDFiles(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "admin-run")
	r := &fakeReporter{}
	sent, err := Signal(prefix, MatchTarget("", ""), r)
	if sent != 0 || err == nil {
		t.Fatalf("Signal() = (%d, %v), want (0, non-nil)", sent, err)
	}
	if len(r.errs) != 1 || r.errs[0] != "No running targets found (no PID files)" {
		t.Errorf("Err() calls = %v, want exactly the no-PID-files message", r.errs)
	}
}

// TestSignalRemovesStalePIDFile proves a record naming a dead process is
// removed and reported as "no live targets found", distinct from the
// no-PID-files case.
func TestSignalRemovesStalePIDFile(t *testing.T) {
	deadPID := spawnAndReap(t)

	prefix := filepath.Join(t.TempDir(), "admin-run")
	pidFile := prefix + "-web-1.pid"
	if err := writeRecordFile(pidFile, Record{PID: deadPID, Target: "web"}); err != nil {
		t.Fatalf("writeRecordFile: %v", err)
	}

	r := &fakeReporter{}
	sent, err := Signal(prefix, MatchTarget("", ""), r)
	if sent != 0 || err == nil {
		t.Fatalf("Signal() = (%d, %v), want (0, non-nil)", sent, err)
	}
	if _, statErr := os.Stat(pidFile); statErr == nil {
		t.Error("stale pid file should have been removed")
	}
}

// catchSIGUSR1 keeps a SIGUSR1 sent to the test process from terminating it.
func catchSIGUSR1(t *testing.T) {
	t.Helper()
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1)
	t.Cleanup(func() { signal.Stop(sigCh) })
	go func() {
		for range sigCh {
		}
	}()
}

// TestSignalFiltersBySubstring proves the target filter matches by
// substring, not exact equality — filter "eb" must still match a target
// named "web".
//
// MUTATION EVIDENCE: change MatchTarget's strings.Contains to equality and
// "eb" no longer matches "web", so sent stays 0 instead of 1.
func TestSignalFiltersBySubstring(t *testing.T) {
	catchSIGUSR1(t)
	prefix := filepath.Join(t.TempDir(), "admin-run")
	for _, target := range []string{"web", "worker"} {
		if err := writeRecordFile(prefix+"-"+target+"-1.pid", Record{PID: os.Getpid(), Target: target, Root: "/proj"}); err != nil {
			t.Fatalf("write %s record: %v", target, err)
		}
	}

	r := &fakeReporter{}
	sent, err := Signal(prefix, MatchTarget("/proj", "eb"), r)
	if sent != 1 || err != nil {
		t.Fatalf("Signal() = (%d, %v), want sent=1, err=nil", sent, err)
	}
	if _, statErr := os.Stat(prefix + "-worker-1.pid"); statErr != nil {
		t.Error("the non-matching worker pid file should have been left alone")
	}
}

// TestSignalMatchRowSkipsOtherProjects proves a row-targeted reload signals
// only the loop its own project and command started, when another project
// runs a loop under the same target and command name.
func TestSignalMatchRowSkipsOtherProjects(t *testing.T) {
	catchSIGUSR1(t)
	prefix := filepath.Join(t.TempDir(), "admin-run")
	recs := map[string]Record{
		"-dev-1.pid": {PID: os.Getpid(), Target: "dev", Root: "/a", Live: "dev"},
		"-dev-2.pid": {PID: os.Getpid(), Target: "dev", Root: "/b", Live: "dev"},
	}
	for suffix, rec := range recs {
		if err := writeRecordFile(prefix+suffix, rec); err != nil {
			t.Fatalf("writeRecordFile: %v", err)
		}
	}

	r := &fakeReporter{}
	sent, err := Signal(prefix, MatchRow("/a", "dev"), r)
	if sent != 1 || err != nil {
		t.Fatalf("Signal() = (%d, %v), want sent=1, err=nil", sent, err)
	}
}

// spawnAndReap starts and waits for a trivial child process, returning its
// PID — a PID guaranteed to be dead by the time the caller uses it, without
// relying on any specific unused-PID convention.
func spawnAndReap(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawnAndReap start: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("spawnAndReap wait: %v", err)
	}
	return pid
}

// TestRunWithReloadFastExitGivesUp proves a command that exits immediately,
// every time, is retried up to maxFastExitAttempts and then gives up
// returning that exit code.
func TestRunWithReloadFastExitGivesUp(t *testing.T) {
	r := &fakeReporter{}
	rc := RunWithReload("exit 7", "test-fastexit", Options{
		Warn: r.Warn,
	})
	if rc != 7 {
		t.Errorf("RunWithReload() = %d, want 7", rc)
	}
	found := false
	for _, w := range r.warns {
		if strings.Contains(w, "giving up after") && strings.Contains(w, "3 attempts") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warn() calls = %v, want one announcing giving up after 3 attempts", r.warns)
	}
}

// TestRunWithReloadSigusr1Reloads proves a SIGUSR1 delivered to this process
// while RunWithReload is running a long-lived child restarts it — the whole
// point of this package. Ends the loop with SIGTERM (expecting exit code 0,
// not 130 — SIGTERM is a clean shutdown, distinct from SIGINT's 130).
func TestRunWithReloadSigusr1Reloads(t *testing.T) {
	var mu sync.Mutex
	var infos []string
	info := func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		infos = append(infos, msg)
	}

	done := make(chan int, 1)
	go func() {
		done <- RunWithReload("sleep 30", "test-sigusr1", Options{Info: info})
	}()

	// Give the child a moment to actually start before signalling reload.
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("kill SIGUSR1: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill SIGTERM: %v", err)
	}

	select {
	case rc := <-done:
		if rc != 0 {
			t.Errorf("RunWithReload() = %d, want 0 (clean SIGTERM shutdown)", rc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunWithReload did not return after SIGTERM")
	}

	mu.Lock()
	defer mu.Unlock()
	startCount := 0
	sawReload := false
	for _, m := range infos {
		if m == "Starting test-sigusr1..." {
			startCount++
		}
		if m == "Reload requested — restarting..." {
			sawReload = true
		}
	}
	if !sawReload {
		t.Errorf("Info() calls = %v, want a reload-requested banner", infos)
	}
	if startCount < 2 {
		t.Errorf("saw %d 'Starting' banners, want at least 2 (initial + post-reload)", startCount)
	}
}

// TestRunWithReloadReturnsChildExitCodeAfterFastExitWindow proves a child
// that runs past the fast-exit window still surfaces its real exit code —
// this isn't just exercising the retry path.
func TestRunWithReloadReturnsChildExitCodeAfterFastExitWindow(t *testing.T) {
	rc := RunWithReload("sleep 2.1 && exit 7", "t", Options{})
	if rc != 7 {
		t.Fatalf("RunWithReload rc = %d, want 7", rc)
	}
}

// TestRunWithReloadStreamsChildOutput proves the child's stdout reaches
// Options.Stdout (via frame.Write) — the whole point of routing it through
// internal/streams.OutputPump rather than discarding it.
//
// MUTATION EVIDENCE: change RunWithReload's pump wiring to never call
// pump.Write and this test fails — "hello-reload" never appears in Stdout.
func TestRunWithReloadStreamsChildOutput(t *testing.T) {
	var buf bytes.Buffer
	rc := RunWithReload("sleep 2.1 && echo hello-reload", "t", Options{Stdout: &buf})
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	if !strings.Contains(buf.String(), "hello-reload") {
		t.Fatalf("output = %q, want it to contain the child's stdout", buf.String())
	}
}

// TestRunWithReloadRetriesAFastExitExactCount complements
// TestRunWithReloadFastExitGivesUp by counting real launches directly (via
// a marker file appended once per launch) rather than only checking the
// final Warn banner — proving maxFastExitAttempts bounds the retry loop,
// not just that it eventually gives up.
//
// MUTATION EVIDENCE: change maxFastExitAttempts from 3 to 1 and this test
// fails — the marker file shows 1 launch instead of the 3 asserted here.
func TestRunWithReloadRetriesAFastExitExactCount(t *testing.T) {
	marker := t.TempDir() + "/launches"
	rc := RunWithReload("echo x >> "+marker+" && exit 5", "t", Options{})
	if rc != 5 {
		t.Fatalf("rc = %d, want 5 (final attempt's exit code)", rc)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := strings.Count(string(data), "x")
	const wantLaunches = 3 // matches maxFastExitAttempts
	if got != wantLaunches {
		t.Fatalf("launches = %d, want %d", got, wantLaunches)
	}
}

// TestRunWithReloadSIGUSR1RestartsTheChildProcess complements
// TestRunWithReloadSigusr1Reloads by proving a real second process actually
// launched (via a growing marker file), not just that the Info banner fired.
func TestRunWithReloadSIGUSR1RestartsTheChildProcess(t *testing.T) {
	marker := t.TempDir() + "/starts"

	done := make(chan int, 1)
	go func() {
		done <- RunWithReload("echo start >> "+marker+" && sleep 5", "t", Options{})
	}()

	// Give the child time to actually launch and write its marker before
	// signalling — a signal sent before Start() would have nothing to
	// interrupt.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && strings.Count(string(data), "start") >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("Kill(SIGUSR1): %v", err)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && strings.Count(string(data), "start") >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := strings.Count(string(data), "start"); got < 2 {
		t.Fatalf("launches after SIGUSR1 = %d, want at least 2 (original + restart)", got)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("Kill(SIGTERM): %v", err)
	}
	select {
	case rc := <-done:
		if rc != 0 {
			t.Errorf("rc after SIGTERM = %d, want 0", rc)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunWithReload did not return after SIGTERM")
	}
}
