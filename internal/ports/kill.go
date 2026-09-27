package ports

import (
	"syscall"
	"time"
)

// KillResult reports what happened to one pid during an escalating kill,
// needed here because the report is part of the acceptance criteria:
// "killing a held port reports which processes were killed."
type KillResult struct {
	PID       int
	Signal    string // "TERM" or "KILL" — the strongest signal that was sent
	Escalated bool   // true if TERM didn't work and KILL was needed
	Alive     bool   // true if the pid was still alive after the whole sweep
}

// KillPIDs sends SIGTERM to every pid, polls for up to timeout for them to
// exit, then SIGKILLs any survivor, reporting a per-pid outcome. A pid that
// is already gone, or one this process has no permission to signal, is
// treated as a best-effort no-op, never a hard failure.
func KillPIDs(pids []int, timeout time.Duration) []KillResult {
	results := make(map[int]*KillResult, len(pids))
	order := make([]int, 0, len(pids))
	for _, pid := range pids {
		if _, ok := results[pid]; ok {
			continue
		}
		order = append(order, pid)
		results[pid] = &KillResult{PID: pid, Signal: "TERM"}
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}

	deadline := time.Now().Add(timeout)
	remaining := append([]int(nil), order...)
	for len(remaining) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		var still []int
		for _, pid := range remaining {
			if pidAlive(pid) {
				still = append(still, pid)
			}
		}
		remaining = still
	}

	for _, pid := range remaining {
		results[pid].Signal = "KILL"
		results[pid].Escalated = true
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	time.Sleep(100 * time.Millisecond)

	out := make([]KillResult, 0, len(order))
	for _, pid := range order {
		r := results[pid]
		r.Alive = pidAlive(pid)
		out = append(out, *r)
	}
	return out
}

// pidAlive reports whether pid is still running, via signal 0. A permission
// error (the pid exists but belongs to another user) is reported as alive:
// killing it may still fail, but it hasn't exited.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if err == syscall.ESRCH {
		return false
	}
	return true
}

// AnyAlive reports whether any result in results is still alive after the
// sweep.
func AnyAlive(results []KillResult) bool {
	for _, r := range results {
		if r.Alive {
			return true
		}
	}
	return false
}
