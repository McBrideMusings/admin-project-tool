// Package timing is admin-j5ep's --timing/ADMIN_TIMING mechanism: the one
// place a clock is read to answer "where did admin's own dispatch time go".
// It exists because diagnosing "admin feels slow" otherwise means
// reconstructing the run afterward from guesses — this package instead lets
// a slow run report itself, at the moment it happens, one line per phase.
//
// Every line goes to stderr, never stdout: stdout is the command's own
// output (a framed action's pump reads it), so timing output must never mix
// into it or change what a piped `admin <cmd>` produces. Nothing here
// returns an error and nothing here can change a process's exit status —
// this is diagnostic-only, by the ticket's own constraint.
//
// Zero cost when off is the other constraint: every exported call is a
// single bool load (the package-level enabled flag) followed by an
// immediate return when timing is not active — no clock read, no string
// build, no allocation. Start() is the pattern every call site uses:
//
//	start := timing.Start()          // returns a zero time.Time when off
//	... do the phase's work ...
//	timing.Phase("parse manifest", start)   // no-op when off
//
// Enable must run before ANY phase the run cares about — cmd/root.go calls
// it at the very top of Execute, before dispatchArgs, because manifest
// find/parse/resolve happen inside dispatchArgs itself.
package timing

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	mu      sync.Mutex
	enabled bool
	t0      time.Time

	buffering bool
	buf       []string

	marked = map[string]bool{}
)

// Enable turns timing on and starts the run clock that Total and Mark
// measure elapsed time against. Idempotent — a second call just re-stamps
// t0, which no caller does today (cmd/root.go calls it exactly once, before
// dispatchArgs), but re-stamping rather than ignoring keeps this safe if
// that ever changes.
func Enable() {
	mu.Lock()
	enabled = true
	t0 = time.Now()
	mu.Unlock()
}

// On reports whether timing is active. Callers that need to skip real work
// when timing is off (there is none of that in this ticket — every phase
// runs regardless, timing.Start just declines to read a clock) can guard on
// this; it is also what every other function in this package checks first.
func On() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

// Start returns the current time when timing is enabled, or a zero
// time.Time (no clock read) when it is not. Pair every Start with a Phase
// call using the same start value; Phase is a no-op against a zero Time.
func Start() time.Time {
	mu.Lock()
	on := enabled
	mu.Unlock()
	if !on {
		return time.Time{}
	}
	return time.Now()
}

// Phase emits "<name>  <N>ms" to stderr, measuring elapsed time since
// start. A no-op — no clock read, no write — when timing is off or start is
// zero (meaning Start() was called while off).
func Phase(name string, start time.Time) {
	if start.IsZero() {
		return
	}
	mu.Lock()
	on := enabled
	mu.Unlock()
	if !on {
		return
	}
	write(fmt.Sprintf("%s  %dms", name, time.Since(start).Milliseconds()))
}

// Mark emits a one-shot line naming the elapsed time since Enable() was
// called — for an event with no matching Start rather than a bounded phase.
// Firing name a second time is silently ignored, which is what the TUI's
// own "first frame" mark wants: View is invoked on every repaint, not just
// the first, and the dedup is what turns that into a single line. An event
// that legitimately recurs per logical unit of work (a child process's
// first byte, once per child, across a long-lived TUI session) wants
// MarkEach instead — Mark would swallow every occurrence after the first.
func Mark(name string) {
	mu.Lock()
	on := enabled
	if !on {
		mu.Unlock()
		return
	}
	if marked[name] {
		mu.Unlock()
		return
	}
	marked[name] = true
	elapsed := time.Since(t0).Milliseconds()
	mu.Unlock()
	write(fmt.Sprintf("%s  %dms", name, elapsed))
}

// MarkEach emits a line naming the elapsed time since Enable() was called,
// like Mark, but with no dedup — every call writes a line. Use it for an
// event that is inherently per-instance rather than once-per-process, such
// as "first byte from child": a long-lived TUI dispatches many children
// over one session, and each one's first byte is its own event. The
// one-shot-per-child guarantee is the caller's job (both call sites already
// hold a local firstByte bool scoped to one child's reader), not this
// package's — Mark's process-wide dedup would otherwise swallow every
// child after the first.
func MarkEach(name string) {
	mu.Lock()
	on := enabled
	if !on {
		mu.Unlock()
		return
	}
	elapsed := time.Since(t0).Milliseconds()
	mu.Unlock()
	write(fmt.Sprintf("%s  %dms", name, elapsed))
}

// Total emits the run's total elapsed time since Enable(). cmd/root.go
// calls this via `defer timing.Total()` so it fires however Execute
// returns, including on an error path.
func Total() {
	mu.Lock()
	on := enabled
	elapsed := time.Since(t0).Milliseconds()
	mu.Unlock()
	if !on {
		return
	}
	write(fmt.Sprintf("total  %dms", elapsed))
}

// BufferUntilFlush switches timing output into a held buffer instead of
// writing straight to stderr. The TUI draws its alt screen on stderr too, so
// a phase line written while the alt screen is up gets overwritten the
// instant Bubble Tea repaints; holding the lines and Flush()ing them after
// the program tears down is the only way both keep working. A no-op when
// timing is off.
func BufferUntilFlush() {
	mu.Lock()
	defer mu.Unlock()
	if !enabled {
		return
	}
	buffering = true
}

// Flush writes every buffered line to stderr, in order, and turns buffering
// back off. Safe to call when timing is off or nothing was buffered.
func Flush() {
	mu.Lock()
	buffering = false
	lines := buf
	buf = nil
	mu.Unlock()
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
}

// write sends line to stderr, or appends it to buf when BufferUntilFlush is
// active.
func write(line string) {
	mu.Lock()
	if buffering {
		buf = append(buf, line)
		mu.Unlock()
		return
	}
	mu.Unlock()
	fmt.Fprintln(os.Stderr, line)
}

// EnvRequests reports whether ADMIN_TIMING=1 is set — cmd/root.go checks
// this alongside a literal --timing argv flag so timing can be turned on
// for a run nobody anticipated would be slow, without editing the command
// (and it is the only way to time a manifest command, since those use
// DisableFlagParsing and would otherwise pass --timing through to the step
// instead of to admin).
func EnvRequests() bool {
	return strings.TrimSpace(os.Getenv("ADMIN_TIMING")) == "1"
}

// reset clears all package state. Test-only.
func reset() {
	mu.Lock()
	defer mu.Unlock()
	enabled = false
	t0 = time.Time{}
	buffering = false
	buf = nil
	marked = map[string]bool{}
}
