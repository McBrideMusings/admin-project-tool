// Package livework is the process-lifetime registry of everything this repo
// started that is still alive: a long-running kind dispatched into its own
// Herdr pane (admin-3rgp.17, e.g. a dev server) and an agent worker a
// workflow dispatched into a git worktree (admin-3rgp.25). Both are things
// "this repo started that are still alive" — the epic's own words for why
// they share one registry rather than two.
//
// admin-3rgp.20's Work tab is this registry's one real reader: it renders
// commands and live work as one list under one cursor, with a detail pane
// whose shape follows the row's kind. This package is deliberately the
// "half that knows what is alive" and nothing about rendering — the AC that
// matters here is "the registry is queryable programmatically, so the Work
// tab renders it rather than owning it."
//
// There is exactly one registry per process (Default), matching the "one
// process runs one dev loop" assumption internal/frame and internal/reload
// already make. An entry living in a launcher pane outlives the process that
// opened it, so Default also shares such entries with every other admin
// process through files under DefaultStateDir — see persist.go.
package livework

import (
	"fmt"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
)

// State is an Entry's coarse lifecycle. Nothing in this package transitions
// an entry between states on its own — a herdr pane or agent worker runs as
// its own OS process, independent of the admin invocation that started it,
// so every transition is reported by the producer that dispatched the entry
// (having observed the real thing settle: an agent's blocking wait
// returning, a pane closing) via SetState.
type State string

const (
	StateRunning State = "running"
	// StateWaiting marks an entry that has settled on a question or approval
	// prompt rather than finished — herdr's own "blocked" agent status. This
	// is the state that makes "waiting on input" distinguishable from "still
	// working" without attaching to anything: a reader of List needs only
	// compare State, never open the pane to find out.
	StateWaiting State = "waiting"
	StateExited  State = "exited"
	StateFailed  State = "failed"
	// StateFinished marks a worker whose work is over but whose reap was
	// refused (a worktree holding files only the worker has, a branch ahead
	// of its base). The pane and worktree are still there, so the entry is
	// live: it can be attached, killed and reaped again. Only the reaper sets
	// it, and sets it back to StateRunning if the work stops being finished.
	StateFinished State = "finished"
	// StateStopped marks a worker whose launcher pane is gone before its done
	// predicate passed: nothing runs any more, but its worktree and branch may
	// still hold the work. The entry stays live so a person can inspect and
	// kill it, and reapable so it is still removed once done passes. Only the
	// reaper sets it, and nothing sets it back: a pane that closed does not
	// reopen.
	StateStopped State = "stopped"
	// StateStarting is the pre-registration, pre-handle phase between a
	// dispatch keypress and the entry that eventually records it — Add
	// rejects an entry with an empty Handle, and a pending dispatch has no
	// handle yet (the pty device name or launcher pane id only exists once
	// the spawn returns). No Entry is ever stored in this state today; the
	// constant exists purely so a consumer (internal/tui) can render one
	// uniform vocabulary of states for a row it tracks by other means during
	// that window, without inventing a second, TUI-local enum.
	StateStarting State = "starting"
)

// Reapable reports whether a worker in state s is still the reaper's to
// judge: running, waiting, finished and awaiting a safe removal, or stopped
// and awaiting its done predicate.
func (s State) Reapable() bool {
	return s == StateRunning || s == StateWaiting || s == StateFinished || s == StateStopped
}

// WorkerInfo is present only on an entry for an agent worker — a workflow
// target running in its own git worktree — and nil for a long-running
// process, which has neither. The epic's amendment is explicit that the two
// "share almost no fields, which is why one field set could not hold both";
// a pointer that is simply absent for a process entry is how Entry honors
// that without inventing two separate registries or two Add signatures.
type WorkerInfo struct {
	// Branch is the git branch the worker's worktree was created on.
	Branch string
	// Worktree is the worktree's checkout path.
	Worktree string
	// Target is the workflow target id the worker was dispatched for — the
	// value its done predicate sees as ADMIN_TARGET.
	Target string
	// Done is the workflow action's done predicate, "" when it declares none.
	Done string
}

// Ops holds the real, producer-supplied implementations of an entry's
// lifecycle actions — attach (focus the pane) and kill (stop it). A nil
// field means that action genuinely isn't available for this entry, and the Registry methods below report that as an error rather than a
// panic, so a caller (the Work tab) never has to know which entry shape it
// is holding before invoking one.
//
// Ops lives on the entry, not the Registry, because only the producer that
// actually opened the pane or launcher handle (internal/kinds/pane.go,
// internal/kinds/workflowdispatch.go) knows how to reach it again — the
// registry itself has no dependency on Herdr or any launcher and stays a
// pure, mechanism-agnostic store. Attach is documented as the launcher-only
// op (Entry.Via non-empty): a hosted-pty entry's (Via empty) attach target
// is the dashboard's terminal view instead, resolved by Registry.Attach via
// the registry's terminalOpener seam rather than a per-entry field — see
// Registry.SetTerminalOpener.
type Ops struct {
	Attach func() error
	Kill   func() error

	// Write and Resize are the pty-entry counterpart of Attach's pane-only
	// role: a pane-addressed entry's terminal IS a real terminal (attach
	// focuses it and the OS handles keystrokes and resize directly), but a
	// pty-addressed entry's terminal is this process's own master fd, which
	// only the producer that opened it (internal/kinds/hostedpty.go) can
	// reach. Write sends bytes to the child (a keystroke, from the terminal
	// view admin-3rgp.65 renders); Resize sets the child's pty window size.
	// Both are nil for a pane-addressed entry, which needs neither.
	Write  func(p []byte) (int, error)
	Resize func(rows, cols int) error
}

// Entry is one live-work record: a long-running kind's single-pane
// dispatch, or one agent worker a workflow started.
type Entry struct {
	// ID uniquely identifies this live-work slot and is chosen by the
	// producer that adds the entry — the declared action name for a
	// single-pane long-running kind (one dispatch, one slot), or the
	// derived agent session name for a workflow target. ID is not the
	// pane ID: the pane address lives in Handle.
	ID string

	// Name is the declared action name that was dispatched (e.g. the
	// [actions.dev-server] this entry came from, or the [actions.review]
	// a workflow target belongs to).
	Name string

	// Kind is the action's declared kind (e.g. "vite-dev", "workflow").
	Kind string

	// Handle is the opaque address this entry is reachable at — a hosted
	// pty's device name, or whatever a project's [terminal] launcher minted
	// for it (ADR-0020).
	//
	// Handle must be non-empty — Add rejects an entry that leaves it
	// blank.
	Handle string

	// Via says what kind of address Handle is: empty means the built-in
	// hosted pty (internal/kinds/hostedpty.go), non-empty names the
	// launcher that minted the handle — "terminal" for every [terminal]
	// launcher dispatch, including a workflow target's pane (ADR-0020);
	// what runs inside that pane is a separate concern, resolved through a
	// compiled-in internal/agent.Driver rather than named here (ADR-0021).
	// Like Handle, Via is set by the producer.
	Via string

	// Output is the drained bytes read off Handle's master fd — nil
	// unless Via is empty (a hosted-pty entry), since only that case has
	// a master this process reads. StartHostedPTY is the sole producer:
	// it creates the Ring, copies the pty into it for as long as the
	// child runs, and leaves it here for the Work band's text tail rather
	// than opening a second reader on the same master.
	Output *Ring

	// Screen is the terminal the hosted child writes to: a vt emulator fed
	// the same bytes as Output, for the child's whole run, whose replies to
	// terminal queries (OSC 11, ESC[6n) go back to the child as a real
	// terminal's would. nil exactly when Output is. The attached terminal
	// view renders this grid directly.
	Screen *vt.SafeEmulator

	StartedAt time.Time
	State     State

	// Activity is a one-line human-readable description of what the entry
	// is doing right now (e.g. "writing internal/lua/host.go") — the
	// acceptance criterion's "current-activity line". Empty means the
	// producer hasn't reported one.
	Activity string

	// Note is why the entry is not progressing the way its owner expects —
	// a finished worker the reaper declined to delete. Empty when there is
	// nothing to say. Read it through Registry.Line; SetNote writes it under
	// r.mu.
	Note string

	// Snapshot is the last text a launcher's [terminal].read printed for
	// Handle: the pane's current last lines, replaced whole on every refresh
	// (never appended to). Empty until the first read succeeds, and always
	// empty for a hosted-pty entry, whose bytes are in Output. Read it
	// through Registry.Snapshot — SetSnapshot writes it under r.mu.
	Snapshot string

	// Worker is non-nil for an agent worker and nil for a long-running
	// process — see WorkerInfo's doc comment.
	Worker *WorkerInfo

	// Ops are this entry's real attach/kill implementations. See
	// Ops's doc comment.
	Ops Ops

	// Err is set when State is StateFailed; nil otherwise.
	Err error

	// Root is the primary checkout of the repo this entry was dispatched
	// from. A launcher-dispatched entry carrying one is shared with every
	// other admin process working in that repo (persist.go); "" keeps the
	// entry in this process only.
	Root string

	// onDisk is true once this entry's shared file has been written or the
	// entry was loaded from one, so Sync can read a missing file as "ended
	// elsewhere" without misreading a write that failed. Guarded by r.mu.
	onDisk bool

	// LogPath is the real log file this entry's process is writing to, once
	// known. It is empty until the producer learns it — for a hosted-pty
	// entry (internal/kinds/hostedpty.go's recordHostedPTYWork), that means
	// sniffing the child's own announce line off its pty output, since the
	// child is a separate OS process and the parent has no other way to
	// learn the path luahost's deferred naming settled on. The Work tab
	// (internal/tui/work.go) prefers this over its own best-effort glob
	// whenever it's set.
	LogPath string
}

// Registry is a concurrency-safe, append-mostly set of Entry records, keyed
// by ID. A registry with a state dir also shares its launcher-dispatched
// entries through that dir (persist.go).
type Registry struct {
	mu             sync.Mutex
	dir            string          // state dir; "" keeps every entry in memory only
	writeErrs      []error         // failed updates, reported by the next Sync
	reported       map[string]bool // malformed files Sync has already named
	entries        map[string]*Entry
	order          []string // insertion order, for a deterministic List
	terminalOpener func(handle string) error
}

// NewRegistry returns an empty, in-memory Registry.
func NewRegistry() *Registry {
	return &Registry{entries: map[string]*Entry{}}
}

// NewSharedRegistry returns an empty Registry that shares its
// launcher-dispatched entries through files under dir.
func NewSharedRegistry(dir string) *Registry {
	r := NewRegistry()
	r.dir = dir
	return r
}

// SetTerminalOpener installs the dashboard's "open the terminal view over
// this pty" callback, invoked by Attach for a hosted-pty entry (Via empty).
// There is no per-entry equivalent of Ops.Attach for this because the
// terminal view is one piece of dashboard UI shared by every hosted-pty
// entry, not something each entry's producer wires individually — see
// admin-3rgp.61.
func (r *Registry) SetTerminalOpener(open func(handle string) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.terminalOpener = open
}

// Add records e, defaulting StartedAt to now and State to StateRunning when
// the caller left them zero. Re-adding the ID of an entry that has finished
// (exited or failed) overwrites that entry's fields but keeps its original
// position in List's order, so a re-dispatch under the same ID stays on the
// same row.
//
// Add rejects e and stores nothing when e carries an empty Handle — an
// entry must address something — or when an entry with the same ID is still
// running or waiting: overwriting it would orphan that entry's pane. A
// shared entry is also rejected while another admin process holds a live
// file for its ID.
func (r *Registry) Add(e *Entry) error {
	if e.Handle == "" {
		return fmt.Errorf("live-work: entry %q must carry a Handle", e.ID)
	}
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}
	if e.State == "" {
		e.State = StateRunning
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, exists := r.entries[e.ID]
	if exists && old.State.live() {
		return fmt.Errorf("live-work: entry %q is already %s (handle %s)", e.ID, old.State, old.Handle)
	}
	// Claiming the file refuses an id another process holds live. A shared
	// entry no other process can see would be lost the moment this process
	// exits, so a failed write rejects it like a duplicate does.
	if err := r.claimLocked(e); err != nil {
		return err
	}
	if !exists {
		r.order = append(r.order, e.ID)
	}
	r.entries[e.ID] = e
	return nil
}

// Get returns the entry for id, if any.
func (r *Registry) Get(id string) (*Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	return e, ok
}

// State returns the entry for id's current State, safe for concurrent use
// with SetState — a caller polling for a transition (tests included) must
// go through this rather than reading a *Entry's State field directly,
// since that field is mutated under r.mu by SetState's own goroutine.
func (r *Registry) State(id string) (State, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return "", false
	}
	return e.State, true
}

// Activity returns the entry for id's current Activity line, safe for
// concurrent use with SetActivity — a caller reading the current-activity
// line (a render, a header) must go through this rather than reading a
// *Entry's Activity field directly, since that field is mutated under r.mu
// by SetActivity's own goroutine.
func (r *Registry) Activity(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return "", false
	}
	return e.Activity, true
}

// LogPath returns the entry for id's current LogPath, safe for concurrent
// use with SetLogPath — a render must go through this rather than reading a
// *Entry's LogPath field directly, since that field is mutated under r.mu by
// the producer's announce-line sniffer on its own goroutine.
func (r *Registry) LogPath(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return "", false
	}
	return e.LogPath, true
}

// SetState transitions the entry for id, if present. err is recorded only
// when state is StateFailed.
func (r *Registry) SetState(id string, state State, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return
	}
	changed := e.State != state
	e.State = state
	if state == StateFailed {
		e.Err = err
	}
	if changed {
		r.updateLocked(e)
	}
}

// SetLogPath records the real log file id's process is writing to, if the
// entry is present. A no-op for an unknown id — the same "if present"
// shape as SetState/SetActivity, since the producer learning the path
// (hostedpty.go's announce-line sniffer) races the entry's own removal in
// no case that matters here, but must never panic if it did.
func (r *Registry) SetLogPath(id, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return
	}
	if e.LogPath != path {
		e.LogPath = path
		r.updateLocked(e)
	}
}

// SetActivity updates the entry for id's current-activity line, if present.
func (r *Registry) SetActivity(id, activity string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return
	}
	e.Activity = activity
}

// SetNote replaces the entry for id's note, if present. "" clears it.
func (r *Registry) SetNote(id, note string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[id]; ok && e.Note != note {
		e.Note = note
		r.updateLocked(e)
	}
}

// Line returns the one line a row shows beside the entry: its Note and its
// Activity, joined when both are set so a note never hides what the entry is
// doing.
func (r *Registry) Line(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return "", false
	}
	switch {
	case e.Note == "":
		return e.Activity, true
	case e.Activity == "":
		return e.Note, true
	}
	return e.Note + " · " + e.Activity, true
}

// SetSnapshot replaces the entry for id's pane snapshot, if present.
func (r *Registry) SetSnapshot(id, snapshot string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[id]; ok {
		e.Snapshot = snapshot
	}
}

// Snapshot returns the entry for id's pane snapshot, safe for concurrent use
// with SetSnapshot.
func (r *Registry) Snapshot(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return "", false
	}
	return e.Snapshot, true
}

// List returns every entry, oldest-added first. The returned slice is a
// snapshot — safe to range over without holding any lock, and mutating it
// does not affect the registry.
func (r *Registry) List() []*Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Entry, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.entries[id])
	}
	return out
}

// WorkerStates maps each target that has a live worker to that worker's state:
// running, waiting or finished — a worker whose pane is still up and whose
// worktree is still there. An exited or failed worker is not live, so its
// target is absent and may be dispatched again. When one target somehow holds
// two live workers (two actions over the same id), the one added first wins.
func (r *Registry) WorkerStates() map[string]State {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]State{}
	for _, id := range r.order {
		e := r.entries[id]
		if e.Worker == nil || !e.State.Reapable() {
			continue
		}
		if _, seen := out[e.Worker.Target]; !seen {
			out[e.Worker.Target] = e.State
		}
	}
	return out
}

// lookup is Attach/Kill's shared "find the entry or say why not"
// step.
func (r *Registry) lookup(id string) (*Entry, error) {
	r.mu.Lock()
	e, ok := r.entries[id]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("live-work: no entry %q", id)
	}
	return e, nil
}

// Attach resolves by whichever address id's entry carries: a hosted-pty
// entry (Via empty) opens the dashboard's terminal view over its pty via
// the installed terminal opener (SetTerminalOpener); a launcher-dispatched
// entry (Via non-empty) attaches via Ops.Attach — the producer's own
// implementation of that launcher's attach command. It errors when id
// doesn't exist, or when the resolved path has nothing wired for it.
func (r *Registry) Attach(id string) error {
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	if e.Via == "" {
		// A hosted pty's screen outlives its child: attaching an exited one
		// opens its transcript.
		r.mu.Lock()
		open := r.terminalOpener
		r.mu.Unlock()
		if open == nil {
			return fmt.Errorf("live-work: entry %q does not support attach", id)
		}
		return open(e.Handle)
	}
	// A launcher pane that is gone cannot be focused, and its address may now
	// name another pane.
	if st, _ := r.State(id); !st.live() || st == StateStopped {
		return fmt.Errorf("live-work: entry %q has %s: its pane is gone", id, st)
	}
	if e.Ops.Attach == nil {
		return fmt.Errorf("live-work: entry %q does not support attach", id)
	}
	return e.Ops.Attach()
}

// Kill stops id — closes the pane it is running in. It errors when id
// doesn't exist, is already over, or its producer never wired a Kill
// operation. An entry that is over runs nothing: its handle names a pane that
// is gone, and a launcher may have reused that address for another one.
func (r *Registry) Kill(id string) error {
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	if st, _ := r.State(id); !st.live() {
		return fmt.Errorf("live-work: entry %q has already %s", id, st)
	}
	if e.Ops.Kill == nil {
		return fmt.Errorf("live-work: entry %q does not support kill", id)
	}
	return e.Ops.Kill()
}

// Default is the process-wide registry every long-running dispatch and
// every workflow target records itself into, and what the Work tab
// (admin-3rgp.20) reads.
var Default = NewSharedRegistry(DefaultStateDir())

// UseRegistry points Default at r for the duration of a test and returns a
// restore func that puts the previous registry back — the same seam pattern
// as internal/archetype/search.go's UseInstalledDir. A test that calls
// Default.Add (or anything that reads Default.List through the package under
// test) should call this with a fresh NewRegistry() rather than resetting or
// clearing the shared instance: Default is a process-wide singleton, so an
// entry one test adds and never removes is still there for the next
// -count>1 iteration of the same test, or for an unrelated test in the same
// package that also reads Default (admin-hw2i). Swapping the pointer to a
// throwaway registry, instead of mutating the shared one, removes that class
// of leak structurally rather than depending on every test author to
// remember cleanup.
func UseRegistry(r *Registry) (restore func()) {
	prev := Default
	Default = r
	return func() { Default = prev }
}
