// Package bd reads a beads workspace — the local, version-controlled issue
// database the `bd` CLI keeps under .beads — for the dashboard's Beads tab.
//
// Everything here is a local read: no network, no rate limit, no remote. The
// dashboard never writes a bead. Moving one to in_progress belongs to whoever is
// doing the work, not to the thing watching it.
package bd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the `bd --json` contract this package is written against.
// Every bd JSON response carries a schema_version; a workspace reporting a
// different one is refused outright rather than parsed on a guess, because the
// fields that would move are the ones the whole pane is built out of.
const SchemaVersion = 1

// ErrNoWorkspace means this repo has no beads workspace — either `bd` isn't
// installed or `bd where` found nothing. It is the ordinary answer for a
// GitHub-backed repo, not a failure: the Beads tab simply doesn't exist there.
var ErrNoWorkspace = errors.New("no beads workspace")

// Workspace is what `bd where` reports: where the database lives and what
// prefix its ids carry.
type Workspace struct {
	Path         string `json:"path"`
	DatabasePath string `json:"database_path"`
	Prefix       string `json:"prefix"`
	Schema       int    `json:"schema_version"`
}

// Bead is one open work item. Blockers holds the ids this bead waits on; a bead
// is "blocked" when any of them is still around (see IsBlocked).
type Bead struct {
	ID       string
	Title    string
	Status   string // open, in_progress, blocked, deferred (closed ones aren't listed)
	Type     string // bug, feature, task, epic, chore
	Priority int    // 0 (critical) … 4 (backlog)
	Assignee string
	Labels   []string
	Blockers []string
	// Parent is the id of the bead this one nests under, from a parent-child
	// dependency edge naming this bead as the child. Empty when this bead has
	// none. A bead has at most one parent, so this holds a single id rather
	// than a slice the way Blockers does.
	Parent  string
	Updated time.Time

	// Description and AcceptanceCriteria are the bead's prose, carried for the
	// overlay. No column renders either one.
	Description        string
	AcceptanceCriteria string
}

// Detect reports the beads workspace covering dir. A worktree has no .beads of
// its own — bd's own discovery walks to the main repository — so this asks bd
// rather than testing for the directory, which would hide the tab in every
// worktree of a beads repo.
//
// A repo with no workspace comes back as ErrNoWorkspace. Any other error means a
// workspace is there but this build can't read it, which the tab reports instead
// of pretending beads isn't set up.
func Detect(ctx context.Context, dir string) (Workspace, error) {
	out, err := runner(ctx, dir, "where", "--json")
	if err != nil {
		return Workspace{}, ErrNoWorkspace
	}
	var ws Workspace
	if err := json.Unmarshal(out, &ws); err != nil {
		return Workspace{}, fmt.Errorf("bd where: %w", err)
	}
	if ws.Path == "" {
		return Workspace{}, ErrNoWorkspace
	}
	if ws.Schema != SchemaVersion {
		return ws, fmt.Errorf("bd speaks schema %d, this build reads %d — upgrade one of them", ws.Schema, SchemaVersion)
	}
	return ws, nil
}

// List returns every bead that isn't closed, newest-priority first. Closed ones
// are excluded by bd's own default filter; deferred ones come back and are the
// caller's to drop (they still block whatever depends on them).
//
// The default limit is 50 rows, so -n 0 is not optional — without it a repo with
// more beads than that silently shows a slice of itself.
func List(ctx context.Context, dir string) ([]Bead, error) {
	out, err := runner(ctx, dir, "list", "--json", "-n", "0", "--no-pager")
	if err != nil {
		return nil, err
	}
	var raw []rawBead
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}
	beads := make([]Bead, 0, len(raw))
	for _, r := range raw {
		beads = append(beads, r.bead())
	}
	sortBeads(beads)
	return beads, nil
}

// IsBlocked reports whether b is waiting on work that still exists. A blocker
// that has been closed is gone from the list entirely, so "still open" is
// exactly "still in the set" — matching what `bd blocked` reports without a
// second subprocess.
func IsBlocked(b Bead, open map[string]bool) bool {
	for _, id := range b.Blockers {
		if open[id] {
			return true
		}
	}
	return false
}

// Unblocks counts, for every bead, how much open work finishing it would
// eventually free: the transitive closure of "things waiting on this", not just
// the immediate dependents. A bead gating a chain of five and one gating five
// leaves are different propositions, and only the transitive number tells them
// apart.
//
// Closed beads aren't in the list, so the count is over open work by
// construction. Only "blocks" edges are in Blockers, so parent-child structure
// contributes nothing — an epic scores whatever its children actually declare.
//
// The walk is memoised per bead and guards against a cycle by marking a bead in
// progress before descending: bd does not promise an acyclic graph, and a cycle
// here would otherwise be an unbounded recursion in the render path.
func Unblocks(beads []Bead) map[string]int {
	open := OpenSet(beads)
	// waiters[x] is every open bead that waits on x.
	waiters := make(map[string][]string, len(beads))
	for _, b := range beads {
		for _, id := range b.Blockers {
			if open[id] {
				waiters[id] = append(waiters[id], b.ID)
			}
		}
	}

	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	state := make(map[string]int, len(beads))
	reach := make(map[string]map[string]bool, len(beads))

	var walk func(id string) map[string]bool
	walk = func(id string) map[string]bool {
		if state[id] == done {
			return reach[id]
		}
		if state[id] == active {
			// A cycle: whatever this bead reaches is already being accumulated by
			// the frame that entered it.
			return nil
		}
		state[id] = active
		set := make(map[string]bool)
		for _, w := range waiters[id] {
			set[w] = true
			for d := range walk(w) {
				set[d] = true
			}
		}
		state[id], reach[id] = done, set
		return set
	}

	out := make(map[string]int, len(beads))
	for _, b := range beads {
		if n := len(walk(b.ID)); n > 0 {
			out[b.ID] = n
		}
	}
	return out
}

// OpenSet indexes beads by id for IsBlocked.
func OpenSet(beads []Bead) map[string]bool {
	open := make(map[string]bool, len(beads))
	for _, b := range beads {
		open[b.ID] = true
	}
	return open
}

// rawBead is bd's JSON row. Only the fields the pane renders are named; bd sends
// a good deal more (comments, metadata, molecule wiring) that no column shows.
type rawBead struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Priority  int       `json:"priority"`
	IssueType string    `json:"issue_type"`
	Assignee  string    `json:"owner"`
	Labels    []string  `json:"labels"`
	UpdatedAt time.Time `json:"updated_at"`

	// Description and AcceptanceCriteria are the bead's prose. The columns show
	// neither; the overlay shows both, and it reads them from the list response
	// rather than shelling out to `bd show` for a bead already in memory.
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptance_criteria"`

	Dependencies []struct {
		DependsOnID string `json:"depends_on_id"`
		Type        string `json:"type"`
	} `json:"dependencies"`
}

func (r rawBead) bead() Bead {
	b := Bead{
		ID:                 r.ID,
		Title:              r.Title,
		Status:             r.Status,
		Type:               r.IssueType,
		Priority:           r.Priority,
		Assignee:           r.Assignee,
		Labels:             r.Labels,
		Updated:            r.UpdatedAt,
		Description:        r.Description,
		AcceptanceCriteria: r.AcceptanceCriteria,
	}
	// bd stores an owner as the address the work was assigned under. The column has
	// seven cells and the domain is the same for every bead in a workspace, so the
	// part that identifies a person is the only part worth the width.
	if i := strings.IndexByte(b.Assignee, '@'); i > 0 {
		b.Assignee = b.Assignee[:i]
	}
	// Only "blocks" holds work up. discovered-from and related are structure,
	// not order — a bead whose only link is one of those is ready to start.
	// parent-child is structure too, but unlike those it names a specific
	// relationship (this bead's parent) worth keeping as Parent for the
	// family-tree view rather than discarding.
	for _, d := range r.Dependencies {
		switch d.Type {
		case "blocks":
			b.Blockers = append(b.Blockers, d.DependsOnID)
		case "parent-child":
			if b.Parent == "" {
				b.Parent = d.DependsOnID
			}
		}
	}
	return b
}

// sortBeads orders beads the way they get picked up: highest priority first,
// then most recently touched, then by id so the order never wobbles between two
// reads of the same data.
func sortBeads(beads []Bead) {
	sort.SliceStable(beads, func(i, j int) bool {
		a, b := beads[i], beads[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		return a.ID < b.ID
	})
}

// runner is the seam every command in this package goes through. It exists so a
// test can record the argv a call builds: a dropped `--json`, `-n 0` or filter
// is otherwise invisible, since the arguments never leave the process in a form
// anything can read. See argv_test.go.
var runner = run

// run executes bd in dir and returns its stdout. bd finds its own database from
// there (BEADS_DIR, else the main repository's .beads), so no --db is passed.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("bd %s: %s", args[0], firstLine(msg))
		}
		return nil, fmt.Errorf("bd %s: %w", args[0], err)
	}
	return out, nil
}

func firstLine(s string) string {
	head, _, _ := strings.Cut(s, "\n")
	return head
}
