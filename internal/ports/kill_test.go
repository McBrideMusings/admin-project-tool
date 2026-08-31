package ports

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// shellQuoteForTest wraps p in single quotes for the /bin/sh -c command
// lines this file builds — a local helper rather than exporting the
// package's own shellQuote (which lives in internal/kinds, not here).
func shellQuoteForTest(p string) string {
	return "'" + p + "'"
}

// TestKillPIDsTerminatesACooperativeProcess proves the common case: a
// process that dies to SIGTERM is reported as killed via TERM, not escalated
// to KILL.
func TestKillPIDsTerminatesACooperativeProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	// Reap the child the instant it exits — otherwise it sits as a zombie
	// until this test process calls Wait(), and signal 0 to a zombie still
	// succeeds (it's still in the process table), which would make Alive
	// wrongly read as true regardless of what KillPIDs actually did.
	go cmd.Wait()

	results := KillPIDs([]int{pid}, 2*time.Second)
	if len(results) != 1 {
		t.Fatalf("KillPIDs returned %d results, want 1", len(results))
	}
	r := results[0]
	if r.Escalated {
		t.Errorf("KillResult.Escalated = true, want false for a plain `sleep` (dies to SIGTERM)")
	}
	if r.Signal != "TERM" {
		t.Errorf("KillResult.Signal = %q, want %q", r.Signal, "TERM")
	}
	if r.Alive {
		t.Errorf("KillResult.Alive = true, want false — sleep should have exited")
	}
}

// TestKillPIDsEscalatesToSIGKILL proves the actual acceptance-critical
// behaviour: a process that ignores SIGTERM is escalated to SIGKILL, and the
// report says so.
//
// MUTATION EVIDENCE: comment out the `for len(remaining) > 0 &&
// time.Now().Before(deadline)` poll loop's SIGKILL escalation (replace the
// `for _, pid := range remaining { ... syscall.Kill(pid, syscall.SIGKILL) }`
// block with a no-op) and re-run this test — real output pasted below.
func TestKillPIDsEscalatesToSIGKILL(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	// `trap '' TERM` ignores SIGTERM entirely, forcing KillPIDs down its
	// SIGKILL path — sh itself, not a child, ignores the signal, so there's
	// no supervisor process to reparent orphans. Writes `ready` only once
	// the trap is actually installed, so the test never races SIGTERM
	// against sh still starting up.
	cmd := exec.Command("sh", "-c", "trap '' TERM; touch "+shellQuoteForTest(ready)+"; sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process never signalled it installed the TERM trap")
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	results := KillPIDs([]int{pid}, 500*time.Millisecond)
	elapsed := time.Since(start)

	if len(results) != 1 {
		t.Fatalf("KillPIDs returned %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Escalated {
		t.Errorf("KillResult.Escalated = false, want true — process ignores SIGTERM")
	}
	if r.Signal != "KILL" {
		t.Errorf("KillResult.Signal = %q, want %q", r.Signal, "KILL")
	}
	if r.Alive {
		t.Errorf("KillResult.Alive = true, want false — SIGKILL cannot be caught")
	}
	// Sanity: the escalation must actually wait out (roughly) the timeout
	// before giving up on TERM, not fire KILL immediately.
	if elapsed < 400*time.Millisecond {
		t.Errorf("KillPIDs took %v, want it to respect its ~500ms TERM grace period before escalating", elapsed)
	}
}

// TestKillPIDsOnAnAlreadyDeadPIDIsANoOp proves a pid that's already gone
// doesn't crash or hang the sweep.
func TestKillPIDsOnAnAlreadyDeadPIDIsANoOp(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	pid := cmd.Process.Pid // now exited

	results := KillPIDs([]int{pid}, 200*time.Millisecond)
	if len(results) != 1 {
		t.Fatalf("KillPIDs returned %d results, want 1", len(results))
	}
	if results[0].Alive {
		t.Errorf("KillResult.Alive = true, want false for an already-exited pid")
	}
}

// TestAnyAliveReflectsResults is a small direct-mutation check for the
// AnyAlive helper's own logic.
func TestAnyAliveReflectsResults(t *testing.T) {
	if AnyAlive([]KillResult{{Alive: false}, {Alive: false}}) {
		t.Error("AnyAlive = true, want false when nothing is alive")
	}
	if !AnyAlive([]KillResult{{Alive: false}, {Alive: true}}) {
		t.Error("AnyAlive = false, want true when one result is alive")
	}
}
