package guards

import (
	"errors"
	"sync"
)

// preflightCheck is a global preflight's implementation — same shape as
// Func, kept distinct because a preflight check is not itself addressable
// from a `requires` list (though deps-fresh happens to be both: a
// registered guard AND a preflight check, per ADR-0005's "besides being
// referenceable in requires, it also runs as a global preflight").
type preflightCheck func() (bool, string)

// preflightChecks is the always-on checks, run once before every command —
// the Go port of admin_lib/guards.py::_PREFLIGHT_CHECKS. This is the single
// source of truth (the preflight analogue of Builtin); add a check by
// appending its func() (bool, string) here. Per ADR-0005 the bar for adding
// one is narrow: it must guard the interpreter's own ability to run project
// code at all, not one action.
var preflightChecks = []preflightCheck{
	func() (bool, string) { return depsFreshCheck("") },
}

// preflightExempt names the control/diagnostic verbs that skip the whole
// preflight pass — the ones you need in order to recover from (or look
// into) the very state a check might block on. Blocking `logs`/`kill` on a
// stale install would lock a user out of fixing it.
var preflightExempt = map[string]bool{"logs": true, "kill": true}

var (
	preflightMu     sync.Mutex
	preflightResult error
	preflightRan    bool
)

// ResetPreflight clears the memoized preflight verdict. For tests, and for
// any long-lived host that dispatches more than once per process (the Go
// port of admin_lib/guards.py::reset_preflight).
func ResetPreflight() {
	preflightMu.Lock()
	defer preflightMu.Unlock()
	preflightResult = nil
	preflightRan = false
}

// RunGlobalPreflight runs the always-on preflight checks once per
// invocation, before a command dispatches (CLI or TUI) — the Go port of
// admin_lib/guards.py::run_global_preflight. Returns nil on pass, or the
// first failing check's error.
//
// command is the name of the command about to run; a name in
// preflightExempt skips the pass entirely, every other call — including a
// nested one during the same process, matching the ADR's "once per
// invocation regardless of nested dispatch" — returns the memoized verdict
// (not merely the fact that it ran) rather than silently re-passing.
func RunGlobalPreflight(command string) error {
	if preflightExempt[command] {
		return nil
	}
	preflightMu.Lock()
	defer preflightMu.Unlock()
	if preflightRan {
		return preflightResult
	}
	// preflightResult is reset here (not just by ResetPreflight) so the
	// "compute once" branch below is correct standing alone: a stale error
	// from whatever preflightResult held before this call must not survive
	// into a fresh computation that actually passes.
	preflightResult = nil
	for _, check := range preflightChecks {
		if passed, message := check(); !passed {
			preflightResult = errors.New(message)
			break
		}
	}
	preflightRan = true
	return preflightResult
}
