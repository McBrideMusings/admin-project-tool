// workflowquery.go is admin-3rgp.26's other half of "let a query-sourced
// workflow run bare": resolving `over = { query = "..." }` into a concrete
// []Target BEFORE anything dispatches. ParseWorkflowOver (workflow.go)
// already validated that the query string is present and non-empty; this
// file is where its contents are actually read.
//
// The tracker this resolves against is internal/bd — the same local,
// version-controlled beads workspace the dashboard's own Beads tab reads
// (internal/bd/bd.go), so a query never leaves the machine and never
// mutates anything: it is a read, exactly like every other bd call this
// binary makes. A GitHub-tracked project (tracker = "github") has no query
// resolver yet — that is a real gap, not an oversight, and is worth its own
// ticket rather than folding a second, network-backed resolution path (with
// its own rate limits and its own idea of "label") into this one under time
// pressure.
package kinds

import (
	"context"
	"fmt"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// listBeads is the seam ResolveQuery reads its tracker state through,
// swapped out in tests exactly like workflowdispatch.go's startAgent/
// runAgent/agentStatus: bd.List shells out to the real `bd` binary, which a
// unit test proving the query-filtering logic has no business needing.
var listBeads = bd.List

// ResolveQuery resolves query against repoRoot's beads workspace and returns
// the []Target a confirmed dispatch would run over. It never dispatches
// anything itself — internal/tui's confirm overlay is the only caller, and
// it is responsible for showing the returned set to the user before ever
// handing it to DispatchTargets (the epic's "a query matching nine things
// must not be able to start nine agents without the set having been seen").
//
// Two forms are recognized, both plain bd filters rather than a new query
// language:
//
//   - "ready": every non-closed bead with no open blocker — bd's own
//     "available to start" definition (bd.IsBlocked against bd.OpenSet),
//     matching the PRD's "review everything waiting on me" story (#17).
//   - "label:<name>": every non-closed bead carrying that label — the exact
//     form admin-3rgp.24's own test fixture already used
//     (workflow_test.go's `over = { query = "label:review-me" }`) before
//     this ticket gave it a meaning.
//
// A query resolving to zero beads is NOT an error: it is reported back as
// an empty, non-nil slice so the caller can say "matched nothing" and
// dispatch nothing — this ticket's own acceptance criterion. Only a
// malformed query string or an unreadable tracker (no bd workspace, a
// schema bd.List can't read) come back as err.
func ResolveQuery(ctx context.Context, repoRoot, query string) ([]Target, error) {
	q := strings.TrimSpace(query)

	beads, err := listBeads(ctx, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving query %q: %w", query, err)
	}

	var matched []bd.Bead
	switch {
	case q == "ready":
		open := bd.OpenSet(beads)
		for _, b := range beads {
			if !bd.IsBlocked(b, open) {
				matched = append(matched, b)
			}
		}
	case strings.HasPrefix(q, "label:"):
		label := strings.TrimPrefix(q, "label:")
		if label == "" {
			return nil, fmt.Errorf("query %q: 'label:' needs a name", query)
		}
		for _, b := range beads {
			if hasLabel(b.Labels, label) {
				matched = append(matched, b)
			}
		}
	default:
		return nil, fmt.Errorf(
			`query %q: unrecognized — supported forms are "ready" and "label:<name>"`, query)
	}

	targets := make([]Target, len(matched))
	for i, b := range matched {
		targets[i] = Target{Name: b.ID, Prompt: ImplementPrompt(b.ID)}
	}
	return targets, nil
}

// ImplementPrompt is the one prompt shape every issue-driven Workflow target
// gets, whichever invocation path built it: the same "one worktree, one
// /implement pass" this dashboard's own dispatch machinery and the project's
// swarm tooling already use for every tracked-item agent run. ResolveQuery
// (query-sourced) and internal/tui's selection-sourced picker (admin-3rgp.27)
// both call this rather than each formatting their own — a bare id is the
// only input either has, so there is nothing invocation-path-specific left
// to vary.
func ImplementPrompt(id string) string {
	return fmt.Sprintf("/implement %s continuous", id)
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}
