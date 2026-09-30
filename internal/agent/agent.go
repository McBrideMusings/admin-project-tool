// Package agent is the ADR-0021 half of the launcher split: WHERE an agent
// lives (a pane, a window) is [terminal] configuration, resolved through
// internal/kinds/pane.go's launcher dispatch (ADR-0020); WHAT IS INSIDE that
// pane and how to talk to it — starting an agent, prompting it, reading its
// output, asking its status — is this package, compiled-in Go rather than a
// project's shell strings.
//
// A Driver is keyed on the opaque launcher handle [terminal].spawn returns,
// with no new admin.toml markup: the same handle a project's own spawn line
// already prints is what a Driver asks the underlying tool about.
//
// Driver selection is by detection, no config (ADR-0021 decision 2A): a
// driver package registers itself from its own init() via Register, and
// Select returns the first one whose availability check passes. When a
// second Driver exists, selection gets designed then — today there is
// exactly one (internal/agent/herdr).
package agent

import "context"

// Status is one of a small closed set of words a Driver reports for the
// agent living at a handle — herdr's own status vocabulary (agent_status),
// which every Driver implementation is expected to normalize onto.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusWorking Status = "working"
	StatusBlocked Status = "blocked"
	StatusDone    Status = "done"
	StatusUnknown Status = "unknown"
)

// Info is one Driver.Info reply: what is running at a handle right now.
type Info struct {
	// Status is the agent's current lifecycle state.
	Status Status
	// Activity is a one-line human-readable description of what the agent
	// is doing right now (e.g. a stripped terminal title) — empty when the
	// underlying tool has nothing to report.
	Activity string
}

// Driver is WHAT IS INSIDE a handle and how to talk to it: starting an
// agent of a given kind, prompting it and waiting for it to settle, and
// asking its current status. handle is always the
// opaque address a [terminal].spawn line printed — a Driver never opens,
// closes or focuses one itself; that is [terminal]'s attach/kill, which
// stays in config (ADR-0021's "no overlap" rule).
type Driver interface {
	// Start begins running an agent of kind inside the pane/window at
	// handle, with any extra args passed through to the underlying tool.
	// The pane must already be sitting at an interactive shell prompt —
	// [terminal].spawn with an empty ADMIN_CMD is exactly that.
	Start(handle, kind string, args []string) error
	// Prompt sends text to the agent running at handle and blocks until it
	// settles into one of the given states (or the underlying tool's own
	// default settle set, when until is empty).
	Prompt(handle, text string, until []Status) error
	// Info reports what is currently running at handle, bounded by ctx so a
	// wedged underlying tool cannot hang the caller.
	Info(ctx context.Context, handle string) (Info, error)
}

// registration is one registered Driver plus its availability check.
type registration struct {
	name      string
	available func() bool
	driver    Driver
}

// registry holds every Driver a linked driver package has registered via
// its own init(). Package-level and unsynchronized: registration happens
// only from init() functions, which run single-threaded before main starts.
var registry []registration

// Register adds a named Driver to the registry, gated by available — called
// from a driver package's own init() (see internal/agent/herdr). Selection
// order is registration order, so the first package imported (today, the
// only one) that reports itself available wins.
func Register(name string, available func() bool, d Driver) {
	registry = append(registry, registration{name: name, available: available, driver: d})
}

// Select returns the first registered Driver whose availability check
// passes, or ok=false when none is. A caller that needs a Driver and gets
// ok=false must report "no agent driver available" rather than guessing —
// most likely the driver package that would answer this was never linked
// in (a blank import missing somewhere), or its own tool isn't reachable.
func Select() (Driver, bool) {
	for _, r := range registry {
		if r.available() {
			return r.driver, true
		}
	}
	return nil, false
}
