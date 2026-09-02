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
//
// ClassifyQuery is the single place that decides WHICH of the four syntactic
// forms a query string is — cmd/check.go's check-time queryTrackerProblem
// and this file's dispatch-time resolveGitHubQuery/resolveBeadsQuery both
// call it rather than each re-deriving the same four-way switch (they
// diverged once already: dispatch-time's "unrecognized" message used to
// advertise "ready" as supported on github, which the case two above it
// rejects). Only WHETHER a form is supported, and the wording of the
// resulting message, still varies per tracker and per call site.
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

// QueryForm is the syntactic case a workflow `over = { query }` string falls
// into. It says WHICH case a query is, never whether that case is supported
// on a given tracker — that decision stays with each resolver/checker.
type QueryForm int

const (
	QueryFormUnrecognized QueryForm = iota
	QueryFormReady
	QueryFormEmptyLabel // "label:" with nothing after the colon
	QueryFormLabel      // "label:<name>"
)

// GitHubQueryForms names, in ONE place, the query forms a github tracker
// actually resolves — so an "unrecognized" message can never advertise a
// form the resolver rejects (the bug ROUND 2 of this ticket fixes: dispatch
// time used to say "ready" was supported when the case above it refused it).
const GitHubQueryForms = `"label:<name>"`

// ClassifyQuery trims query and reports which of the four syntactic forms it
// is, returning the label name for QueryFormLabel and "" for every other
// form. It is tracker-neutral — beads supports QueryFormReady and github
// does not, but that is the caller's decision, not this function's.
func ClassifyQuery(query string) (QueryForm, string) {
	q := strings.TrimSpace(query)
	switch {
	case q == "ready":
		return QueryFormReady, ""
	case strings.HasPrefix(q, "label:") && strings.TrimPrefix(q, "label:") == "":
		return QueryFormEmptyLabel, ""
	case strings.HasPrefix(q, "label:"):
		return QueryFormLabel, strings.TrimPrefix(q, "label:")
	default:
		return QueryFormUnrecognized, ""
	}
}

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
	beads, err := listBeads(ctx, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving query %q: %w", query, err)
	}

	form, label := ClassifyQuery(query)

	var matched []bd.Bead
	switch form {
	case QueryFormReady:
		open := bd.OpenSet(beads)
		for _, b := range beads {
			if !bd.IsBlocked(b, open) {
				matched = append(matched, b)
			}
		}
	case QueryFormEmptyLabel:
		return nil, fmt.Errorf("query %q: 'label:' needs a name", query)
	case QueryFormLabel:
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
// see this file's header comment for why — and any other form gets its own
// "unrecognized" error naming only the forms github actually resolves
// (GitHubQueryForms), which is never the same message resolveBeadsQuery
// raises since beads genuinely supports "ready" too.
//
// A query resolving to zero issues is NOT an error, matching
// resolveBeadsQuery: it comes back as an empty, non-nil slice.
func resolveGitHubQuery(ctx context.Context, nwo, tracker, query string) ([]Target, error) {
	form, label := ClassifyQuery(query)

	switch form {
	case QueryFormReady:
		return nil, fmt.Errorf(
			`query %q: "ready" is beads-only — it means "no open blocker", and tracker = %q has no dependency edges to derive that from; use "label:<name>"`,
			query, tracker)
	case QueryFormEmptyLabel:
		return nil, fmt.Errorf("query %q: 'label:' needs a name", query)
	case QueryFormLabel:
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
			`query %q: unrecognized — tracker %q supports only %s`, query, tracker, GitHubQueryForms)
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
