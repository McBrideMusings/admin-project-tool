package ports

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Environment variables admin stamps on every action it starts. They are
// inherited through fork/exec, survive Node's `detached: true` (setsid) and
// reparenting to PID 1, and are readable from outside the process, so they
// identify a run's whole tree even after it has left its process group.
const (
	EnvRunID    = "ADMIN_RUN_ID"
	EnvCheckout = "ADMIN_CHECKOUT"
)

// ProcInfo is one visible process with what a sweep needs to judge it.
type ProcInfo struct {
	PID  int
	PGID int
	Cmd  string
	// EnvReadable is false when the environment came back empty: another
	// user's process, or a macOS system binary, whose environment the kernel
	// hides. Such a process can neither be matched nor ruled out by its tag.
	EnvReadable bool
	RunID       string // "" when untagged
	Checkout    string
}

// Tagged is one process carrying an ADMIN_RUN_ID.
type Tagged struct {
	PID      int
	PGID     int
	Cmd      string
	RunID    string
	Checkout string
}

// EnvValue returns the value of key in a KEY=VALUE list; the last entry
// wins, as it does for exec.
func EnvValue(env []string, key string) string {
	val := ""
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			val = e[len(prefix):]
		}
	}
	return val
}

// StripTag returns env without ADMIN_RUN_ID and ADMIN_CHECKOUT — the one
// filter every caller that replaces or removes the tag uses.
func StripTag(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, EnvRunID+"=") || strings.HasPrefix(e, EnvCheckout+"=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// snapshot lists every visible process: one `ps` call, then one environment
// read per pid. unparsable lists pids whose environment the kernel returned
// but this package could not decode.
func snapshot() (procs []ProcInfo, unparsable []int) {
	for _, p := range ListProcesses() {
		env, err := readProcEnv(p.PID)
		if err != nil {
			unparsable = append(unparsable, p.PID)
		}
		pgid, gerr := syscall.Getpgid(p.PID)
		if gerr != nil {
			pgid = 0
		}
		procs = append(procs, ProcInfo{
			PID: p.PID, PGID: pgid, Cmd: p.Cmdline,
			EnvReadable: len(env) > 0,
			RunID:       EnvValue(env, EnvRunID),
			Checkout:    EnvValue(env, EnvCheckout),
		})
	}
	return procs, unparsable
}

// TaggedProcesses lists every visible process whose environment carries an
// ADMIN_RUN_ID.
func TaggedProcesses() []Tagged {
	procs, _ := snapshot()
	var out []Tagged
	for _, p := range procs {
		if p.RunID != "" {
			out = append(out, Tagged{PID: p.PID, PGID: p.PGID, Cmd: p.Cmd, RunID: p.RunID, Checkout: p.Checkout})
		}
	}
	return out
}

// SweepOptions bounds what SweepTagged may signal.
type SweepOptions struct {
	// ProtectPIDs and ProtectPGIDs are never signalled, on top of the
	// built-ins: pids 0 and 1, this process, its ancestors, and its own
	// process group.
	ProtectPIDs  []int
	ProtectPGIDs []int
	// Scope, when set, further restricts the processes a sweep may touch: a
	// process is signalled only when both Scope and the sweep's own match
	// accept it. Tests use it to confine a sweep to the processes they
	// planted.
	Scope func(Tagged) bool
	// Logf receives one line per decision; nil discards them.
	Logf func(format string, args ...any)
	// Timeout is the SIGTERM grace before SIGKILL (default 3s).
	Timeout time.Duration
}

// Plan is what a sweep will signal for the tagged processes it matched.
type Plan struct {
	Groups  []int // process groups to signal (-pgid)
	PIDs    []int // pids signalled alone because their group is not wholly the run's
	Covered []int // every matched pid the plan signals, directly or via its group
}

// SafeGroup reports whether pgid may be signalled as -pgid at all. 0, 1 and
// negatives are never safe, nor is any group in protected.
func SafeGroup(pgid int, protected map[int]bool) bool {
	return pgid > 1 && !protected[pgid]
}

// groupIsWhollyTagged reports whether every live member of pgid is one of the
// matched processes. A member whose environment is hidden (a system binary
// such as /bin/sh) cannot be judged and does not disqualify the group; a
// member with a readable environment that is not matched, or a protected
// member, does.
func groupIsWhollyTagged(pgid int, all []ProcInfo, matchedPIDs, protectPIDs map[int]bool) bool {
	for _, p := range all {
		if p.PGID != pgid {
			continue
		}
		if protectPIDs[p.PID] {
			return false
		}
		if !matchedPIDs[p.PID] && p.EnvReadable {
			return false
		}
	}
	return true
}

// PlanSweep decides, for the matched processes, which whole groups to kill
// and which lone pids to kill instead. A group is signalled only when its
// pgid is safe and every live member of it is part of the matched run;
// otherwise each matched member is signalled on its own, so an untagged
// process that shares the group survives. A matched process whose pid is
// protected is dropped. Pure, so the safety rules are testable.
func PlanSweep(matched []Tagged, all []ProcInfo, protectPIDs, protectPGIDs map[int]bool) Plan {
	matchedPIDs := map[int]bool{}
	for _, t := range matched {
		matchedPIDs[t.PID] = true
	}
	var plan Plan
	seenG := map[int]bool{}
	seenP := map[int]bool{}
	groupOK := map[int]bool{}
	for _, t := range matched {
		if t.PID <= 1 || protectPIDs[t.PID] {
			continue
		}
		if SafeGroup(t.PGID, protectPGIDs) {
			ok, known := groupOK[t.PGID]
			if !known {
				ok = groupIsWhollyTagged(t.PGID, all, matchedPIDs, protectPIDs)
				groupOK[t.PGID] = ok
			}
			if ok {
				if !seenG[t.PGID] {
					seenG[t.PGID] = true
					plan.Groups = append(plan.Groups, t.PGID)
				}
				plan.Covered = append(plan.Covered, t.PID)
				continue
			}
		}
		if !seenP[t.PID] {
			seenP[t.PID] = true
			plan.PIDs = append(plan.PIDs, t.PID)
			plan.Covered = append(plan.Covered, t.PID)
		}
	}
	return plan
}

// ancestors returns pid and every ancestor up to (not including) 1.
func ancestors(pid int) []int {
	ppids := pidPpidMap()
	var out []int
	for i := 0; i < 64 && pid > 1; i++ {
		out = append(out, pid)
		pid = ppids[pid]
	}
	return out
}

// SweepTagged kills every tagged process for which match (and opts.Scope)
// returns true: SIGTERM to each one's process group when the whole group
// belongs to the run, otherwise to the pid alone; a grace period; then
// SIGKILL to survivors. It rescans up to three times so a supervisor that
// respawns a child (wrangler restarting workerd) is caught. label names the
// sweep in the log lines. It returns the pids it signalled and every kill
// result.
func SweepTagged(label string, match func(Tagged) bool, opts SweepOptions) (signalled []int, results []KillResult) {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	protectPIDs := map[int]bool{0: true, 1: true, os.Getpid(): true}
	for _, p := range ancestors(os.Getpid()) {
		protectPIDs[p] = true
	}
	for _, p := range opts.ProtectPIDs {
		protectPIDs[p] = true
	}
	protectPGIDs := map[int]bool{0: true, 1: true, syscall.Getpgrp(): true}
	for _, g := range opts.ProtectPGIDs {
		protectPGIDs[g] = true
	}

	for round := 1; round <= 3; round++ {
		procs, unparsable := snapshot()
		if round == 1 {
			for _, pid := range unparsable {
				logf("sweep %s: pid %d: environment unreadable (could not parse KERN_PROCARGS2/environ)", label, pid)
			}
		}
		var matched []Tagged
		for _, p := range procs {
			if p.RunID == "" {
				continue
			}
			t := Tagged{PID: p.PID, PGID: p.PGID, Cmd: p.Cmd, RunID: p.RunID, Checkout: p.Checkout}
			if match(t) && (opts.Scope == nil || opts.Scope(t)) {
				matched = append(matched, t)
			}
		}
		plan := PlanSweep(matched, procs, protectPIDs, protectPGIDs)
		if len(plan.Groups) == 0 && len(plan.PIDs) == 0 {
			if round == 1 {
				logf("sweep %s: no tagged processes", label)
			}
			break
		}
		cmds := map[int]string{}
		covered := map[int]bool{}
		for _, pid := range plan.Covered {
			covered[pid] = true
		}
		for _, t := range matched {
			if !covered[t.PID] {
				continue
			}
			logf("sweep %s: round %d run=%s checkout=%s pid=%d pgid=%d cmd=%q",
				label, round, t.RunID, t.Checkout, t.PID, t.PGID, clip(t.Cmd, 200))
			cmds[t.PID] = t.Cmd
			if _, ok := cmds[-t.PGID]; !ok {
				cmds[-t.PGID] = t.Cmd
			}
		}
		signalled = append(signalled, plan.Covered...)
		targets := make([]int, 0, len(plan.Groups)+len(plan.PIDs))
		for _, g := range plan.Groups {
			targets = append(targets, -g)
		}
		targets = append(targets, plan.PIDs...)
		res := killTargets(targets, timeout)
		for _, r := range res {
			outcome := "terminated (TERM)"
			if r.Escalated {
				outcome = "killed (TERM then KILL)"
			}
			if r.Alive {
				outcome = "STILL ALIVE"
			}
			kind := "pid"
			id := r.PID
			if id < 0 {
				kind, id = "pgid", -id
			}
			logf("sweep %s: %s %d %s cmd=%q", label, kind, id, outcome, clip(cmds[r.PID], 120))
		}
		results = append(results, res...)
	}
	return signalled, results
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(+%d)", len(s)-n)
}

// realPath resolves symlinks so /tmp and /private/tmp compare equal; the
// input comes back unchanged when it cannot be resolved.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// NukeTagged is nuke-dev's tag pass: it kills every process stamped by an
// admin run of the checkout at root — or, with anyCheckout, by any admin run
// — except this process's own run. It scans once for the sweep (rescanning
// only to catch respawns) and returns the pids it signalled plus the kill
// results. Each signalled process is logged with its run id, checkout and
// pid. opts.Scope can confine the sweep further.
func NukeTagged(root string, anyCheckout bool, opts SweepOptions) (signalled []int, results []KillResult) {
	ownRun := os.Getenv(EnvRunID)
	rootReal := realPath(root)
	match := func(t Tagged) bool {
		if t.RunID == ownRun {
			return false
		}
		return anyCheckout || (t.Checkout != "" && realPath(t.Checkout) == rootReal)
	}
	label := "nuke-dev checkout=" + root
	if anyCheckout {
		label = "nuke-dev --any-checkout"
	}
	return SweepTagged(label, match, opts)
}
