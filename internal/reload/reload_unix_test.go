//go:build !windows

package reload

import (
	"fmt"
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

// TestSignalAllNoPIDFiles proves SignalAll reports "no running targets"
// (not "no live targets") when the prefix matches nothing at all — the two
// distinct Python error messages this ports.
func TestSignalAllNoPIDFiles(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "admin-run")
	r := &fakeReporter{}
	sent, err := SignalAll(prefix, "", r)
	if sent != 0 || err == nil {
		t.Fatalf("SignalAll() = (%d, %v), want (0, non-nil)", sent, err)
	}
	if len(r.errs) != 1 || r.errs[0] != "No running targets found (no PID files)" {
		t.Errorf("Err() calls = %v, want exactly the no-PID-files message", r.errs)
	}
}

// TestSignalAllRemovesStalePIDFile proves a PID file naming a dead process
// is removed and reported as "no live targets found", distinct from the
// no-PID-files case.
func TestSignalAllRemovesStalePIDFile(t *testing.T) {
	deadPID := spawnAndReap(t)

	dir := t.TempDir()
	prefix := filepath.Join(dir, "admin-run")
	pidFile := prefix + "-web.pid"
	if err := WritePIDFileWithPID(pidFile, deadPID); err != nil {
		t.Fatalf("WritePIDFileWithPID: %v", err)
	}

	r := &fakeReporter{}
	sent, err := SignalAll(prefix, "", r)
	if sent != 0 || err == nil {
		t.Fatalf("SignalAll() = (%d, %v), want (0, non-nil)", sent, err)
	}
	if _, statErr := os.Stat(pidFile); statErr == nil {
		t.Error("stale pid file should have been removed")
	}
}

// TestSignalAllFiltersBySubstring proves the target filter matches by
// substring, not exact equality — filter "eb" must still match a target
// named "web".
//
// MUTATION EVIDENCE: change SignalAll's `strings.Contains(targetName,
// filter)` to `targetName == filter` and this test fails: "eb" != "web", so
// the web target is skipped and sent stays 0 instead of 1 — verified by
// hand-editing reload_unix.go to that shape, re-running (got
// "SignalAll() = (0, no live targets found), want sent=1, err=nil"), then
// reverting.
func TestSignalAllFiltersBySubstring(t *testing.T) {
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
		}
	}()

	dir := t.TempDir()
	prefix := filepath.Join(dir, "admin-run")
	if err := WritePIDFileWithPID(prefix+"-web.pid", os.Getpid()); err != nil {
		t.Fatalf("write web pid file: %v", err)
	}
	if err := WritePIDFileWithPID(prefix+"-worker.pid", os.Getpid()); err != nil {
		t.Fatalf("write worker pid file: %v", err)
	}

	r := &fakeReporter{}
	sent, err := SignalAll(prefix, "eb", r)
	if sent != 1 || err != nil {
		t.Fatalf("SignalAll() = (%d, %v), want sent=1, err=nil", sent, err)
	}
	if _, statErr := os.Stat(prefix + "-worker.pid"); statErr != nil {
		t.Error("the non-matching worker pid file should have been left alone")
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

// WritePIDFileWithPID is WritePIDFile for an arbitrary PID rather than the
// current process's own — test-only, since SignalAll needs to observe both
// a live PID (the test process itself) and a definitely-dead one.
func WritePIDFileWithPID(path string, pid int) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d", pid)), 0o644)
}

// TestRunWithReloadFastExitGivesUp proves a command that exits immediately,
// every time, is retried up to maxFastExitAttempts and then gives up
// returning that exit code — admin_lib/reload.py's fast-exit-retry loop.
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
// not 130 — Python's SIGTERM handler is a clean SystemExit(0), distinct from
// SIGINT's 130).
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
