package tui

import (
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

// trackSort names one of the five orders the tracker list — the Beads tab and
// the Issues tab — cycles through on `s`. The zero value, sortLastModified, is
// what both tabs already rendered before this existed (bd's own priority/
// recency sort for beads, GitHub's default for issues), so a fresh launch
// looks exactly as it always has.
type trackSort int

const (
	sortLastModified trackSort = iota
	sortOldestFirst
	sortNewestFirst
	sortMostDeps
	sortByType
	numTrackSorts
)

// label names the active order for the header, so it's readable without
// opening anything.
func (s trackSort) label() string {
	switch s {
	case sortLastModified:
		return "last modified"
	case sortOldestFirst:
		return "oldest first"
	case sortNewestFirst:
		return "newest first"
	case sortMostDeps:
		return "most dependencies"
	case sortByType:
		return "by type"
	default:
		return ""
	}
}

// next steps to the following order, wrapping past the last back to the first.
func (s trackSort) next() trackSort {
	return (s + 1) % numTrackSorts
}

// sortBeadsBy stably reorders beads in place under one of the five orders.
// unblocks is the whole-graph "how much open work would finishing this free"
// map (bd.Unblocks) — "most dependencies" is the closest thing the graph has
// to "what unblocks the most", not a count of this bead's own blockers, which
// is why it reads unblocks rather than len(b.Blockers).
//
// sort.SliceStable is what makes this stable: two beads equal under the
// active order (same timestamp, same unblocks count, same type) keep
// whatever relative order they arrived in.
func sortBeadsBy(beads []bd.Bead, order trackSort, unblocks map[string]int) {
	sort.SliceStable(beads, func(i, j int) bool {
		a, b := beads[i], beads[j]
		switch order {
		case sortOldestFirst:
			return a.Updated.Before(b.Updated)
		case sortNewestFirst, sortLastModified:
			return a.Updated.After(b.Updated)
		case sortMostDeps:
			return unblocks[a.ID] > unblocks[b.ID]
		case sortByType:
			return a.Type < b.Type
		}
		return false
	})
}

// sortIssuesBy stably reorders issues in place. Issues carry no dependency
// graph and no type field (only free-text labels), so sortMostDeps and
// sortByType are no-ops here — they cycle and the header names them, but
// leave the incoming order untouched on this tab. oldest/newest use the issue
// number rather than a created timestamp: GitHub assigns numbers in creation
// order, and gh.IssueItem carries only Updated, not CreatedAt.
func sortIssuesBy(items []gh.IssueItem, order trackSort) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch order {
		case sortOldestFirst:
			return a.Number < b.Number
		case sortNewestFirst:
			return a.Number > b.Number
		case sortLastModified:
			return a.Updated.After(b.Updated)
		}
		return false
	})
}
