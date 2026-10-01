// workflowcli.go is admin-spjp.2: the bare-CLI invocation path for
// kind="workflow" — `admin <action>` typed straight into a terminal, as
// opposed to internal/tui/workflowconfirm.go's dashboard row. It adds no
// dispatch machinery of its own: ResolveQuery (workflowquery.go) resolves a
// query into targets and DispatchTargets (workflowdispatch.go) runs them —
// the same two calls the TUI confirm overlay makes. This file only decides
// what a bare CLI run does before and around those two calls: refuse a
// row-sourced workflow outright (there is no row here to run over), and for a query-sourced one, print what the overlay would have
// shown — the resolved set, the agent, the ceiling — and read a
// confirmation from the terminal's own stdin before dispatching anything.
//
// Every message goes to stderr, matching cmd/prompt.go's own convention:
// stdout stays clean for the `repo()` shell function's cd sentinel.
package kinds

import (
	"context"
	"fmt"
	"os"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/prompt"
)

// confirmDispatch is the seam runWorkflowQuery reads the user's yes/no
// through — prompt.Confirm by default, swapped out in tests so the query
// path is provable without a real terminal (prompt.Confirm's own non-TTY
// fallback is exercised separately, in internal/prompt/confirm_test.go).
var confirmDispatch = prompt.Confirm

// resolveWorkflowQuery is the seam runWorkflowQuery resolves a query
// through — kinds.ResolveQuery by default, swapped out in tests exactly as
// internal/tui/workflowconfirm.go's own runResolveQuery seam is, so a test
// proving this file's printing/confirm/dispatch sequencing never shells out
// to a real `bd` or `gh` binary.
var resolveWorkflowQuery = ResolveQuery

// dispatchWorkflowTargets is the seam runWorkflowQuery dispatches a
// confirmed set through — kinds.DispatchTargets, the one and only dispatch
// implementation; internal/tui's confirm overlay calls the same function.
// Swapped out in tests so an accept test proves exactly which targets were
// handed to it without creating a single real git worktree.
var dispatchWorkflowTargets = DispatchTargets

// runWorkflowCLI is makeWorkflow's Runner body: dispatch on target.Source,
// since a row-sourced and a query-sourced workflow need entirely
// different handling from the bare CLI. args is accepted only to match the
// Runner signature — a workflow reads no arguments of its own.
func runWorkflowCLI(name string, action manifest.Action, m *manifest.Manifest, target WorkflowTarget, _ []string) error {
	switch target.Source {
	case WorkflowSourceRow:
		return runWorkflowRow(name)
	case WorkflowSourceQuery:
		return runWorkflowQuery(name, action, m, target)
	default:
		// Unreachable: ParseWorkflowOver never returns WorkflowSourceNone
		// alongside a nil error, and makeWorkflow already propagated any
		// parse error at build time before this Runner could exist.
		return fmt.Errorf("action %q: kind=\"workflow\" has no resolved target source", name)
	}
}

// runWorkflowRow is the row-sourced case: there is no row outside the
// dashboard, so this fails loudly and names the gesture that does have one,
// rather than running over nothing or guessing.
func runWorkflowRow(name string) error {
	return fmt.Errorf(
		"action %q: kind=\"workflow\" with over = \"row\" is row-only — it has no targets to "+
			"run over from the bare command line. Open the dashboard (run `admin` with no arguments), "+
			"put the cursor on a row and press the key bound to this workflow under [keys.<row-kind>]", name)
}

// runWorkflowQuery is the query-sourced case: resolve, print what would be
// dispatched (the same four facts internal/tui/workflowconfirm.go's overlay
// shows — query, agent, ceiling, target list), confirm, then dispatch.
func runWorkflowQuery(name string, action manifest.Action, m *manifest.Manifest, target WorkflowTarget) error {
	repoRoot, err := os.Getwd()
	if err != nil {
		repoRoot = "."
	}

	var declared []string
	if m != nil {
		declared = m.Trackers
	}
	tracker, err := target.ResolveTracker(name, declared)
	if err != nil {
		return err
	}

	// nwo is only needed by the github resolver; tolerated here exactly as
	// cmd/prune.go does — a repo with no configured origin (or a beads-only
	// query) must not fail before ResolveQuery even gets to decide whether
	// it needs nwo at all.
	nwo, _ := git.OriginNWO(context.Background(), repoRoot)

	targets, err := resolveWorkflowQuery(context.Background(), action, repoRoot, tracker, nwo, target.Query)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "query %q matched nothing — dispatching nothing\n", target.Query)
		return nil
	}

	cfg, cfgErr := config.Load(repoRoot)
	if cfgErr != nil {
		return fmt.Errorf("loading config: %w", cfgErr)
	}
	run := ActionRun(action)
	if err := RequireRun(name, run); err != nil {
		return err
	}
	ceiling := Ceiling(cfg)

	fmt.Fprintf(os.Stderr, "query   %s\n", target.Query)
	fmt.Fprintf(os.Stderr, "run     %s\n", run)
	fmt.Fprintf(os.Stderr, "ceiling %d at once\n", ceiling)
	fmt.Fprintf(os.Stderr, "%d target(s):\n", len(targets))
	for _, t := range targets {
		fmt.Fprintf(os.Stderr, "  - %s\n", t.Name)
	}

	if !confirmDispatch(fmt.Sprintf("dispatch %s over %d target(s)? (enter/y: yes · esc/n: cancel) ", name, len(targets))) {
		fmt.Fprintln(os.Stderr, "cancelled — dispatching nothing")
		return nil
	}

	results := dispatchWorkflowTargets(repoRoot, name, action, targets, SurfaceShell)
	ok, failed := 0, 0
	var firstErr error
	for _, r := range results {
		if r.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("target %q: %w", r.Target, r.Err)
			}
		} else {
			ok++
		}
	}
	if failed == 0 {
		fmt.Fprintf(os.Stderr, "%s: %d target(s) finished\n", name, ok)
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s: %d finished, %d failed\n", name, ok, failed)
	return fmt.Errorf("%s: %d of %d target(s) failed, first: %w", name, failed, len(results), firstErr)
}
