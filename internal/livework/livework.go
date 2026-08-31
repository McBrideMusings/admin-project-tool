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
// already make: the registry is in-memory only and is not persisted or
// shared across admin invocations.
package livework

import (
	"fmt"
	"sync"
	"time"
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
)

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
}

// Ops holds the real, producer-supplied implementations of an entry's
// lifecycle actions — attach (focus the pane), kill (stop it), retry
// (restart a failed entry). A nil field means that action genuinely isn't
// available for this entry (e.g. a kind that has no notion of retrying),
// and the Registry methods below report that as an error rather than a
// panic, so a caller (the Work tab) never has to know which entry shape it
// is holding before invoking one.
//
// Ops lives on the entry, not the Registry, because only the producer that
// actually opened the pane (internal/kinds/pane.go, internal/kinds/
// workflowdispatch.go) knows how to reach it again — the registry itself
// has no dependency on Herdr and stays a pure, mechanism-agnostic store.
type Ops struct {
	Attach func() error
	Kill   func() error
	Retry  func() error
}

// Entry is one live-work record: a long-running kind's single-pane
// dispatch, or one agent worker a workflow started.
type Entry struct {
	// ID uniquely identifies this live-work slot and is chosen by the
	// producer that adds the entry — the declared action name for a
	// single-pane long-running kind (one dispatch, one slot), or the
	// derived agent session name for a workflow target (stable across a
	// Retry, which starts a brand new agent under the same name in a brand
	// new pane). ID is deliberately NOT the pane ID: a Retry that succeeds
	// opens a different pane, and the entry must keep the same identity
	// across that so the Work tab's cursor stays on the same row.
	ID string

	// Name is the declared action name that was dispatched (e.g. the
	// [actions.dev-server] this entry came from, or the [actions.review]
	// a workflow target belongs to).
	Name string

	// Kind is the action's declared kind (e.g. "vite-dev", "workflow").
	Kind string

	// PaneID is the herdr pane the entry is CURRENTLY running in. Unlike
	// ID, this can change over the entry's life — a successful Retry
	// records the new pane it opened here while ID stays put.
	PaneID string

	StartedAt time.Time
	State     State

	// Activity is a one-line human-readable description of what the entry
	// is doing right now (e.g. "writing internal/lua/host.go") — the
	// acceptance criterion's "current-activity line". Empty means the
	// producer hasn't reported one.
	Activity string

	// Worker is non-nil for an agent worker and nil for a long-running
	// process — see WorkerInfo's doc comment.
	Worker *WorkerInfo

	// Ops are this entry's real attach/kill/retry implementations. See
	// Ops's doc comment.
	Ops Ops

	// Err is set when State is StateFailed; nil otherwise.
	Err error
}

// Registry is a concurrency-safe, in-memory, append-mostly set of Entry
// records, keyed by ID.
type Registry struct {
	mu      sync.Mutex
	entries map[string]*Entry
	order   []string // insertion order, for a deterministic List
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{entries: map[string]*Entry{}}
}

// Add records e, defaulting StartedAt to now and State to StateRunning when
// the caller left them zero. Re-adding an existing ID overwrites that
// entry's fields but keeps its original position in List's order — this is
// exactly what a successful Retry relies on: it builds a brand new Entry
// (new PaneID, new Ops bound to that new pane, StartedAt reset) and Adds it
// back under the same ID, so the Work tab's cursor stays on the same row
// instead of a second one appearing beside a stale failed one.
func (r *Registry) Add(e *Entry) {
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}
	if e.State == "" {
		e.State = StateRunning
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[e.ID]; !exists {
		r.order = append(r.order, e.ID)
	}
	r.entries[e.ID] = e
}

// Get returns the entry for id, if any.
func (r *Registry) Get(id string) (*Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	return e, ok
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
	e.State = state
	if state == StateFailed {
		e.Err = err
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

// lookup is Attach/Kill/Retry's shared "find the entry or say why not"
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

// Attach focuses id's pane — herdr brings that pane's workspace and tab to
// the front. It errors when id doesn't exist or its producer never wired an
// Attach operation.
func (r *Registry) Attach(id string) error {
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	if e.Ops.Attach == nil {
		return fmt.Errorf("live-work: entry %q does not support attach", id)
	}
	return e.Ops.Attach()
}

// Kill stops id — closes the pane it is running in. It errors when id
// doesn't exist or its producer never wired a Kill operation.
func (r *Registry) Kill(id string) error {
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	if e.Ops.Kill == nil {
		return fmt.Errorf("live-work: entry %q does not support kill", id)
	}
	return e.Ops.Kill()
}

// Retry restarts id. It errors when id doesn't exist, isn't StateFailed
// (retry restarts a FAILED entry — one still running or already exited
// cleanly has nothing to restart), or its producer never wired a Retry
// operation.
func (r *Registry) Retry(id string) error {
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	if e.State != StateFailed {
		return fmt.Errorf("live-work: entry %q is %s, not failed — nothing to retry", id, e.State)
	}
	if e.Ops.Retry == nil {
		return fmt.Errorf("live-work: entry %q does not support retry", id)
	}
	return e.Ops.Retry()
}

// Default is the process-wide registry every long-running dispatch and
// every workflow target records itself into, and what the Work tab
// (admin-3rgp.20) reads.
var Default = NewRegistry()
