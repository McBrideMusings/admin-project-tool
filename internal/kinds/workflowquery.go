// workflowquery.go is admin-3rgp.26's other half of "let a query-sourced
// workflow run bare": resolving `over = { query = "..." }` into a concrete
// []Target BEFORE anything dispatches. ParseWorkflowOver (workflow.go)
// already validated that the query string is present and non-empty; this
// file is where its contents are actually read.
//
// ResolveQuery dispatches on the project's configured tracker, resolving the
// same query string against two entirely different backends:
//
//   - "" or "beads" (undeclared defaults to beads, matching cmd/check.go's
//     !m.HasTracker precedent) resolves against internal/bd — the same
//     local, version-controlled beads workspace the dashboard's own Beads
//     tab reads (internal/bd/bd.go), so a query never leaves the machine
//     and never mutates anything.
//   - "github" resolves against the GitHub API via internal/gh, but only
//     for the "label:<name>" form. "ready" means "no open blocker", which
//     is a beads concept — bd models dependency edges as first-class data.
//     GitHub has none: deriving "ready" from prose "blocked by #12"
//     references would build a dependency graph out of free text, and a
//     wrong answer here dispatches real agents into real worktrees. So a
//     github-tracked "ready" query is REJECTED with an error naming the
//     tracker, never synthesized from guesswork.
//
// Any other declared tracker has no resolver at all and errors by name.
package kinds

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

// listBeads is the seam resolveBeadsQuery reads its tracker state through,
// swapped out in tests exactly like workflowdispatch.go's startAgent/
// runAgent/agentStatus: bd.List shells out to the real `bd` binary, which a
// unit test proving the query-filtering logic has no business needing.
var listBeads = bd.List

// listGitHubIssues is the seam resolveGitHubQuery reads its tracker state
// through, mirroring listBeads: gh.OpenIssuesByLabel shells out to the real
// `gh` binary, which a unit test proving the label-filtering logic has no
// business needing.
var listGitHubIssues = gh.OpenIssuesByLabel

// ResolveQuery resolves query against repoRoot's configured tracker and
// returns the []Target a confirmed dispatch would run over. It never
// dispatches anything itself — internal/tui's confirm overlay is the only
// caller, and it is responsible for showing the returned set to the user
// before ever handing it to DispatchTargets (the epic's "a query matching
// nine things must not be able to start nine agents without the set having
// been seen").
//
// tracker and nwo come from the caller, which already has both (internal/tui
// reads m.trackerKind() and m.nwo) — ResolveQuery has no manifest of its own
// to read them from.
func ResolveQuery(ctx context.Context, repoRoot, tracker, nwo, query string) ([]Target, error) {
	switch tracker {
	case "", "beads":
		return resolveBeadsQuery(ctx, repoRoot, query)
	case "github":
		return resolveGitHubQuery(ctx, nwo, tracker, query)
	default:
		return nil, fmt.Errorf("query %q: tracker %q has no query resolver", query, tracker)
	}
}

// resolveBeadsQuery is ResolveQuery's original, and still only, beads-backed
// body — unchanged behaviour from before this file gained a second tracker.
//
// Two forms are recognized, both plain bd filters rather than a new query
// language:
//
//   - "ready": every non-closed bead with no open blocker — bd's own
//     "available to start" definition (bd.IsBlocked against bd.OpenSet),
//     which is what "review everything waiting on me" resolves to.
//   - "label:<name>": every non-closed bead carrying that label — the exact
//     form admin-3rgp.24's own test fixture already used
//     (workflow_test.go's `over = { query = "label:review-me" }`) before
//     admin-3rgp.26 gave it a meaning.
//
// A query resolving to zero beads is NOT an error: it is reported back as
// an empty, non-nil slice so the caller can say "matched nothing" and
// dispatch nothing — this ticket's own acceptance criterion. Only a
// malformed query string or an unreadable tracker (no bd workspace, a
// schema bd.List can't read) come back as err.
func resolveBeadsQuery(ctx context.Context, repoRoot, query string) ([]Target, error) {
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

// resolveGitHubQuery is ResolveQuery's github-backed resolver: only
// "label:<name>" is supported. "ready" is refused rather than synthesized —
// see this file's header comment for why — and any other form is the same
// "unrecognized" error resolveBeadsQuery raises, so a typo reads the same
// regardless of which tracker caught it.
//
// A query resolving to zero issues is NOT an error, matching
// resolveBeadsQuery: it comes back as an empty, non-nil slice.
func resolveGitHubQuery(ctx context.Context, nwo, tracker, query string) ([]Target, error) {
	q := strings.TrimSpace(query)

	switch {
	case q == "ready":
		return nil, fmt.Errorf(
			`query %q: "ready" is beads-only — it means "no open blocker", and tracker = %q has no dependency edges to derive that from; use "label:<name>"`,
			query, tracker)
	case strings.HasPrefix(q, "label:"):
		label := strings.TrimPrefix(q, "label:")
		if label == "" {
			return nil, fmt.Errorf("query %q: 'label:' needs a name", query)
		}
		issues, err := listGitHubIssues(ctx, nwo, label)
		if err != nil {
			return nil, fmt.Errorf("resolving query %q: %w", query, err)
		}
		targets := make([]Target, len(issues))
		for i, it := range issues {
			id := strconv.Itoa(it.Number)
			targets[i] = Target{Name: id, Prompt: ImplementPrompt(id)}
		}
		return targets, nil
	default:
		return nil, fmt.Errorf(
			`query %q: unrecognized — supported forms are "ready" and "label:<name>"`, query)
	}
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
