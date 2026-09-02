// workflowdispatch.go is admin-3rgp.25: the worktree-per-target,
// concurrency-ceiling machinery a kind="workflow" action's targets share.
// Resolving a query into a target list (admin-3rgp.26) and reading a row
// selection (admin-3rgp.27) are explicitly out of this ticket's scope —
// both are the "two invocation paths" workflow.go's own doc comment
// names — so this file exports DispatchTargets for those to call once
// they already have a confirmed []Target, and owns everything after that
// point:
//
//   - one git worktree per target, seeded with the project's configured
//     populate files (internal/create.Worktree already does both — see
//     admin-3rgp.16);
//   - re-checking, immediately before a target's agent starts, that its
//     worktree is still the real thing internal/create made — a worktree
//     created well before its ceiling slot frees can be removed out from
//     under this dispatch by something outside it (a cleanup sweeper, a
//     person), and the fix is to fail that target loudly, never to
//     silently recreate one at a base nobody chose;
//   - running the configured herdr agent in each, no more than the
//     ceiling at once, the rest queued and started as slots free;
//   - recording every running target in internal/livework with its pane
//     handle before DispatchTargets returns for it.
//
// It never deletes a worktree, before, during, or after dispatch. Per this
// ticket's scope (and the project CLAUDE.md's "a worker cannot remove its
// own worktree"), retirement is entirely someone else's job.
package kinds

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/create"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
)

// DefaultCeiling applies when neither the workflow action nor the project
// configuration names one.
const DefaultCeiling = 3

// Target is one workflow target to dispatch. Name identifies it for the
// worktree/branch (an issue number, a PR branch, a resolved query row —
// whatever the invocation path resolved it to) and is sanitized into a
// branch name by internal/create exactly as `repo branch` sanitizes one
// today. Prompt is the instruction handed to the agent once it is running
// in that worktree; the two are kept separate because a target's identity
// and the task description an agent reads are different things, and a
// prompt often contains characters (spaces, punctuation) a branch name
// cannot.
type Target struct {
	Name   string
	Prompt string
}

// TargetResult is one Target's outcome from DispatchTargets.
type TargetResult struct {
	Target       string
	WorktreePath string
	// PaneID is the herdr pane the target's agent ran in — the acceptance
	// criterion's "appears in the live registry with its pane handle" —
	// set whenever a pane was actually opened, even if the agent itself
	// then failed to start or the prompt stalled.
	PaneID string
	Err    error
}

// startAgent is DispatchTargets's launch step: open a pane in dir and
// start kind running in it as name, returning the pane's ID as soon as the
// agent is up. Swapped out in tests so DispatchTargets is exercised
// without a real herdr binary.
var startAgent = func(dir, name, kind string) (paneID string, err error) {
	if !shim.Available() {
		return "", shim.ErrHerdrUnavailable
	}
	paneID, err = shim.SplitPane(dir)
	if err != nil {
		return "", fmt.Errorf("opening pane: %w", err)
	}
	if err := shim.StartAgent(name, kind, paneID); err != nil {
		return paneID, fmt.Errorf("starting agent %q: %w", kind, err)
	}
	return paneID, nil
}

// runAgent is DispatchTargets's wait step: hand prompt to the already-
// running agent name and block until herdr reports it has settled
// (idle/done/blocked). Returning is what frees the target's concurrency-
// ceiling slot, so this blocking on the real `agent prompt --wait`
// lifecycle transition is not incidental — it is the entire mechanism the
// ceiling relies on to know a slot is free. Swapped out in tests, like
// startAgent, so the ceiling's queueing is verified deterministically
// instead of against a real long-running agent.
var runAgent = func(name, prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	return shim.PromptAndWait(name, prompt)
}

// agentStatus is dispatchOne's post-wait status check, swapped out in tests
// like startAgent/runAgent. See shim.AgentStatus's doc comment for why this
// is needed at all: PromptAndWait's return alone can't tell "the agent hit
// an approval/question prompt" apart from "the agent actually finished".
var agentStatus = shim.AgentStatus

// ResolveAgent returns the herdr agent kind a workflow target runs: the
// action's own `agent` key when it is a non-empty string, else the
// project's configured [workflow].agent default. ok is false when neither
// names one, which DispatchTargets treats as every target failing rather
// than guessing an agent kind nobody configured.
func ResolveAgent(action manifest.Action, cfg config.Config) (agent string, ok bool) {
	if a, present := actionString(action, "agent"); present && strings.TrimSpace(a) != "" {
		return a, true
	}
	if a := strings.TrimSpace(cfg.Workflow.Agent); a != "" {
		return a, true
	}
	return "", false
}

// Ceiling returns the workflow's concurrency ceiling: the project
// configuration's [workflow].ceiling when positive, else DefaultCeiling.
// Per-workflow overrides are not read here — the acceptance criteria name
// only an agent override, and a project-wide ceiling is the simpler design
// that satisfies them.
func Ceiling(cfg config.Config) int {
	if cfg.Workflow.Ceiling > 0 {
		return cfg.Workflow.Ceiling
	}
	return DefaultCeiling
}

// DispatchTargets creates (or reuses) a worktree per target under repoRoot,
// seeded with the configured populate files, and runs the resolved agent
// in each — no more than Ceiling(cfg) running at once, the remainder
// queued and started as slots free. actionName and action identify the
// workflow action that named these targets, used for the agent override
// (ResolveAgent) and the live-registry entry's Name/Kind.
//
// Every worktree is created up front, before any agent starts. That, not a
// lazily-created-on-its-turn worktree, is what actually needs a
// concurrency ceiling to survive: a low ceiling can leave a just-created
// worktree sitting untouched for a long time before its turn to dispatch
// arrives, and dispatchOne's create.IsRegisteredWorktree check is what
// catches it having been removed out from under this dispatch in that
// window.
func DispatchTargets(repoRoot, actionName string, action manifest.Action, targets []Target) []TargetResult {
	results := make([]TargetResult, len(targets))

	cfg, err := config.Load(repoRoot)
	if err != nil {
		cfg = config.Config{}
	}
	agentKind, ok := ResolveAgent(action, cfg)
	if !ok {
		missing := fmt.Errorf(
			"action %q: kind=\"workflow\" has no agent configured — set 'agent' on the action "+
				"or [workflow].agent in admin.toml", actionName)
		for i, t := range targets {
			results[i] = TargetResult{Target: t.Name, Err: missing}
		}
		return results
	}
	ceiling := Ceiling(cfg)

	// Phase 1: create every target's worktree up front, seeded with the
	// configured populate files (create.Worktree does both — the
	// acceptance criterion "dispatching over N targets creates N
	// worktrees, each seeded with the populate files").
	ctx := context.Background()
	worktrees := make([]create.Result, len(targets))
	for i, t := range targets {
		res, err := create.Worktree(ctx, repoRoot, t.Name)
		if err != nil {
			results[i] = TargetResult{Target: t.Name, Err: fmt.Errorf("creating worktree: %w", err)}
			continue
		}
		worktrees[i] = res
	}

	// Phase 2: run the agent per target under the ceiling. sem's buffer
	// size IS the ceiling: sending to it blocks once `ceiling` goroutines
	// already hold a slot, which is exactly "targets beyond it queue and
	// start as slots free" — a slot frees when dispatchOne's blocking
	// runAgent call returns, not when the pane merely opens.
	sem := make(chan struct{}, ceiling)
	var wg sync.WaitGroup
	for i, t := range targets {
		if results[i].Err != nil {
			continue // phase-1 failure already recorded for this target
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t Target, wt create.Result) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = dispatchOne(actionName, agentKind, t, wt)
		}(i, t, worktrees[i])
	}
	wg.Wait()
	return results
}

// dispatchOne runs one target's agent once its ceiling slot is held: it
// re-verifies wt is still a real worktree (never recreating one that
// isn't), starts the agent session, and records the running target in the
// live-work registry with its pane handle before returning — mirroring
// internal/kinds/pane.go's recordLiveWork for the single-pane LongRunning
// case, but keyed on the pane this dispatch itself opened rather than one
// RunActionRouted opened for it.
func dispatchOne(actionName, agentKind string, t Target, wt create.Result) TargetResult {
	if !create.IsRegisteredWorktree(context.Background(), wt.Path) {
		return TargetResult{
			Target:       t.Name,
			WorktreePath: wt.Path,
			Err: fmt.Errorf(
				"worktree %s for target %q no longer exists — not recreating it at a base "+
					"nobody chose; something removed it after it was created and before its "+
					"turn to dispatch", wt.Path, t.Name),
		}
	}

	agentName := agentSessionName(actionName, t.Name)
	paneID, err := startAgent(wt.Path, agentName, agentKind)
	if err != nil {
		return TargetResult{Target: t.Name, WorktreePath: wt.Path, PaneID: paneID, Err: err}
	}

	// Registered as soon as a real pane exists to key it on and BEFORE the
	// blocking wait below — the acceptance criterion is "each RUNNING
	// target appears in the live registry with its pane handle", which
	// means a reader of the registry mid-dispatch, not only after every
	// target has finished.
	//
	// Keyed on agentName, not paneID: agentName is stable across a Retry
	// (which starts a new agent under the same name in a new pane), so the
	// entry Retry re-adds under this same ID replaces this one in place
	// rather than appearing as a second row beside a stale failed entry.
	e := &livework.Entry{
		ID:     agentName,
		Name:   actionName,
		Kind:   "workflow",
		Handle: paneID,
		Via:    "herdr",
		Worker: &livework.WorkerInfo{Branch: wt.Branch, Worktree: wt.Path},
	}
	e.Ops = livework.Ops{
		Attach: func() error { return shim.FocusPane(e.Handle) },
		Kill: func() error {
			if err := shim.ClosePane(e.Handle); err != nil {
				return err
			}
			livework.Default.SetState(e.ID, livework.StateExited, nil)
			return nil
		},
		// Retry re-runs this exact target against its existing worktree
		// (never a fresh one — a worker never removes its own worktree, so
		// there's nothing to recreate) in the background and returns
		// immediately, matching how DispatchTargets originally launched it.
		Retry: func() error {
			go dispatchOne(actionName, agentKind, t, wt)
			return nil
		},
	}
	if err := livework.Default.Add(e); err != nil {
		return TargetResult{Target: t.Name, WorktreePath: wt.Path, PaneID: paneID, Err: err}
	}

	runErr := runAgent(agentName, t.Prompt)
	if runErr != nil {
		livework.Default.SetState(agentName, livework.StateFailed, runErr)
		return TargetResult{Target: t.Name, WorktreePath: wt.Path, PaneID: paneID, Err: runErr}
	}

	// PromptAndWait settles on idle, done, OR blocked — an agent waiting on
	// an approval or question prompt returns exactly like one that actually
	// finished, so the settled status has to be re-checked to tell "waiting
	// on input" apart from "still working"/"done" without attaching.
	if status, statusErr := agentStatus(agentName); statusErr == nil && status == "blocked" {
		livework.Default.SetState(agentName, livework.StateWaiting, nil)
	} else {
		livework.Default.SetState(agentName, livework.StateExited, nil)
	}
	return TargetResult{Target: t.Name, WorktreePath: wt.Path, PaneID: paneID}
}

// agentSessionName derives a herdr agent name from the workflow action and
// target: `herdr agent start` requires a name matching
// [a-z][a-z0-9_-]{0,31}, unique among live agents, so this lowercases and
// strips everything else out rather than passing a target name (an issue
// title, a branch with slashes) straight through.
func agentSessionName(actionName, target string) string {
	raw := actionName + "-" + target
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "workflow"
	}
	if len(name) > 32 {
		name = name[:32]
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = "w-" + name
		if len(name) > 32 {
			name = name[:32]
		}
	}
	return name
}
