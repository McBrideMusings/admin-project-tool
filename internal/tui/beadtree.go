package tui

import (
	"slices"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// beadTreeMaxDepth caps the indent a tree row can carry. Chains measured across
// four real workspaces top out at 4 levels; clamping there means a pathological
// chain eats no more of the title column than the deepest real one does.
const beadTreeMaxDepth = 4

// beadNode is one bead in the dependency forest: depth is how far down a chain
// of blockers it sits, extra counts the open blockers beyond the one that placed
// it here (an edge position cannot carry, shown as "+N" after the title).
type beadNode struct {
	b     bd.Bead
	depth int
	extra int
}

// beadForest orders beads so a blocker always sits directly above the work it
// holds up. Roots are beads nothing open blocks — that is exactly the "Ready"
// set the flat view groups, so the sections stop earning their space here.
//
// The walk is a spanning forest, not the full graph: a bead with two open
// blockers can only sit under one of them, so it lands under whichever is
// reached first and carries the rest as a count. Beads left over once every root
// has been walked are in a blocks-cycle (`bd dep cycles` finds these) — they
// append at the top level rather than dropping out of the pane.
func beadForest(beads []bd.Bead) []beadNode {
	kept := make([]bd.Bead, 0, len(beads))
	for _, b := range beads {
		if b.Status == "deferred" {
			continue
		}
		kept = append(kept, b)
	}
	open := bd.OpenSet(kept)

	// Blockers that have been closed are gone from the list; only the ones still
	// open place a bead under something.
	blockers := make(map[string][]string, len(kept))
	for _, b := range kept {
		var ob []string
		for _, id := range b.Blockers {
			if open[id] {
				ob = append(ob, id)
			}
		}
		blockers[b.ID] = ob
	}

	emitted := make(map[string]bool, len(kept))
	out := make([]beadNode, 0, len(kept))

	var walk func(b bd.Bead, depth int)
	walk = func(b bd.Bead, depth int) {
		emitted[b.ID] = true
		out = append(out, beadNode{b: b, depth: depth, extra: max(len(blockers[b.ID])-1, 0)})
		for _, c := range kept {
			if emitted[c.ID] || !slices.Contains(blockers[c.ID], b.ID) {
				continue
			}
			walk(c, depth+1)
		}
	}

	// Incoming order is bd.List's order — P0 first, then recency, then id. Roots
	// are taken in that order and never re-sorted.
	for _, b := range kept {
		if len(blockers[b.ID]) == 0 && !emitted[b.ID] {
			walk(b, 0)
		}
	}
	for _, b := range kept {
		if !emitted[b.ID] {
			walk(b, 0)
		}
	}
	return out
}

// beadFamilyForest orders beads so a parent always sits directly above its
// children — structure, not order, the same parent-child edges beadForest
// discards when it builds Blockers. Roots are beads with no Parent, or whose
// Parent isn't among the fetched beads (an epic closed or filtered out is "no
// known parent" here, exactly as an absent id is "no known blocker" for
// beadForest).
//
// A bead has at most one Parent, so unlike beadForest's blockers map this walk
// can't split across multiple parents and extra is always 0 — beadNode keeps
// the field only for row-layout compatibility with the blocks-tree view.
func beadFamilyForest(beads []bd.Bead) []beadNode {
	kept := make([]bd.Bead, 0, len(beads))
	for _, b := range beads {
		if b.Status == "deferred" {
			continue
		}
		kept = append(kept, b)
	}
	byID := make(map[string]bool, len(kept))
	for _, b := range kept {
		byID[b.ID] = true
	}

	children := make(map[string][]bd.Bead, len(kept))
	for _, b := range kept {
		if b.Parent != "" && byID[b.Parent] {
			children[b.Parent] = append(children[b.Parent], b)
		}
	}

	emitted := make(map[string]bool, len(kept))
	out := make([]beadNode, 0, len(kept))

	var walk func(b bd.Bead, depth int)
	walk = func(b bd.Bead, depth int) {
		if emitted[b.ID] {
			// A self-referential or cyclical Parent chain — guarded the same way
			// beadForest guards its emitted map, even though a single-parent walk
			// can't otherwise revisit a node.
			return
		}
		emitted[b.ID] = true
		out = append(out, beadNode{b: b, depth: depth})
		for _, c := range children[b.ID] {
			walk(c, depth+1)
		}
	}

	// Incoming order is bd.List's order — P0 first, then recency, then id. Roots
	// are taken in that order and never re-sorted.
	for _, b := range kept {
		if (b.Parent == "" || !byID[b.Parent]) && !emitted[b.ID] {
			walk(b, 0)
		}
	}
	// Anything left is part of a parent-child cycle bd itself should never
	// produce; append at the top level rather than dropping it from the pane.
	for _, b := range kept {
		if !emitted[b.ID] {
			walk(b, 0)
		}
	}
	return out
}
