package reload

import (
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// collectingOptions returns Options that append every Out chunk to a
// synchronized buffer, plus a func to read the accumulated text.
func collectingOptions(t *testing.T) (Options, func() string) {
	t.Helper()
	var mu sync.Mutex
	var sb strings.Builder
	opts := Options{
		Out:   func(s string) { mu.Lock(); sb.WriteString(s); mu.Unlock() },
		LogFn: func(s string) {},
		Info:  func(s string) { mu.Lock(); sb.WriteString("[info] " + s + "\n"); mu.Unlock() },
		Warn:  func(s string) { mu.Lock(); sb.WriteString("[warn] " + s + "\n"); mu.Unlock() },
	}
	return opts, func() string { mu.Lock(); defer mu.Unlock(); return sb.String() }
}

// TestRunWithReloadReturnsChildExitCode proves a plainly-exiting child's
// real exit code surfaces once the fast-exit retry window has genuinely
// elapsed — sleeping past fastExitWindow first so this isn't just exercising
// the retry path.
func TestRunWithReloadReturnsChildExitCode(t *testing.T) {
	opts, _ := collectingOptions(t)
	rc := RunWithReload("sleep 2.1 && exit 7", "t", opts)
	if rc != 7 {
		t.Fatalf("RunWithReload rc = %d, want 7", rc)
	}
}

// TestRunWithReloadStreamsChildOutput proves the child's stdout reaches
// Options.Out — the whole point of routing it through
// internal/streams.OutputPump rather than discarding it.
//
// MUTATION EVIDENCE: change `pump.Write(string(buf[:n]))` to only write
// when n == 0 (never, effectively) and this test fails — "hello-reload"
// never appears in the collected output.
func TestRunWithReloadStreamsChildOutput(t *testing.T) {
	opts, out := collectingOptions(t)
	rc := RunWithReload("sleep 2.1 && echo hello-reload", "t", opts)
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	if !strings.Contains(out(), "hello-reload") {
		t.Fatalf("output = %q, want it to contain the child's stdout", out())
	}
}

// TestRunWithReloadRetriesAFastExit proves a child that exits within the
// fast-exit window is relaunched, up to maxFastExitAttempts, rather than
// giving up on the first quick failure — the Go port of
// admin_lib/reload.py::run_with_reload's fast-exit retry loop. A file's
// growing byte count (one append per launch) proves multiple real launches
// happened, not just one.
//
// MUTATION EVIDENCE: change `maxFastExitAttempts` from 3 to 1 and this test
// fails — the marker file would show exactly 1 launch instead of the 3 this
// test asserts, because the loop gives up after the very first fast exit.
func TestRunWithReloadRetriesAFastExit(t *testing.T) {
	marker := t.TempDir() + "/launches"
	opts, _ := collectingOptions(t)
	rc := RunWithReload("echo x >> "+marker+" && exit 5", "t", opts)
	if rc != 5 {
		t.Fatalf("rc = %d, want 5 (final attempt's exit code)", rc)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := strings.Count(string(data), "x")
	const wantLaunches = 3 // matches admin_lib/reload.py's _MAX_FAST_EXIT_ATTEMPTS
	if got != wantLaunches {
		t.Fatalf("launches = %d, want %d", got, wantLaunches)
	}
}

// TestRunWithReloadSIGUSR1RestartsTheChild proves sending this process
// SIGUSR1 while a long-lived child is running kills that child and
// relaunches it — the Go port of run_with_reload's reload trigger. A marker
// file with two lines proves two real launches happened.
func TestRunWithReloadSIGUSR1RestartsTheChild(t *testing.T) {
	marker := t.TempDir() + "/starts"
	opts, _ := collectingOptions(t)

	done := make(chan int, 1)
	go func() {
		done <- RunWithReload("echo start >> "+marker+" && sleep 5", "t", opts)
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
