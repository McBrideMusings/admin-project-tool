// Package livework is the process-lifetime registry admin-3rgp.17 creates:
// one entry per long-running kind dispatched into its own herdr pane. It is
// deliberately small — admin-3rgp.28 ("The live-work registry: agent
// workers and long-running processes") is the ticket that grows this into
// what the Swarm/Work tab actually reads (agent workers, richer state
// transitions, exit detection); this package only has to satisfy .17's own
// acceptance criteria: a long-running dispatch appears here with its pane
// handle, a transient one never does, and an entry survives — and reports
// its state — for the life of the process.
//
// There is exactly one registry per process (Default), matching the "one
// process runs one dev loop" assumption internal/frame and internal/reload
// already make: the registry is in-memory only and is not persisted or
// shared across admin invocations.
package livework

import (
	"sync"
	"time"
)

// State is an Entry's coarse lifecycle. Nothing in this package transitions
// an entry out of StateRunning on its own — the herdr pane a long-running
// kind dispatches into runs as its own OS process, independent of the
// admin invocation that started it, and this ticket has no polling loop to
// watch it (that is part of admin-3rgp.28's scope, not this one's). SetState
// exists so a future caller can record a transition it observed by some
// other means (e.g. reading the pane back).
type State string

const (
	StateRunning State = "running"
	StateExited  State = "exited"
	StateFailed  State = "failed"
)

// Entry is one long-running kind's dispatch record.
type Entry struct {
	// ID uniquely identifies this entry. It is the herdr pane ID (PaneID) —
	// one dispatch opens exactly one pane, so the pane ID is already a
	// unique, meaningful handle; minting a second identifier alongside it
	// would just be a synonym.
	ID string

	// Name is the declared action name that was dispatched (e.g. the
	// [actions.dev-server] this entry came from).
	Name string

	// Kind is the action's declared kind (e.g. "vite-dev").
	Kind string

	// PaneID is the herdr pane handle the action is running in — the
	// acceptance criterion's "appears in the live registry with its pane
	// handle".
	PaneID string

	StartedAt time.Time
	State     State

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
// entry's fields but keeps its original position in List's order.
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

// Default is the process-wide registry every long-running dispatch records
// itself into, and what a future Swarm/Work tab (admin-3rgp.20/.28) reads.
var Default = NewRegistry()
