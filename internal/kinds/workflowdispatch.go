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
//   - opening each target's pane through the project's declared [terminal]
//     launcher (ADR-0020) and running the configured agent in it through a
//     compiled-in internal/agent.Driver (ADR-0021), no more than the
//     ceiling at once, the rest queued and started as slots free;
//   - recording every running target in internal/livework with its
//     launcher handle before DispatchTargets returns for it.
//
// It never deletes a worktree, before, during, or after dispatch. Per this
// ticket's scope (and the project CLAUDE.md's "a worker cannot remove its
// own worktree"), retirement is entirely someone else's job.
package kinds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
	_ "github.com/McBrideMusings/admin-project-tool/internal/agent/herdr" // registers the herdr Driver
	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/create"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
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
	// Handle is the launcher handle the target's agent ran in — the
	// acceptance criterion's "appears in the live registry with its pane
	// handle" — set whenever a pane was actually opened, even if the agent
	// itself then failed to start or the prompt stalled.
	Handle string
	Err    error
}

// spawnPane is DispatchTargets's pane-opening step: reach the project's
// declared [terminal] launcher with an EMPTY ADMIN_CMD (the pane lands at
// an interactive shell prompt, exactly what `herdr agent start` requires)
// and ADMIN_CWD set to the target's own worktree, returning the opaque
// launcher handle. Swapped out in tests so DispatchTargets is exercised
// without a real launcher.
var spawnPane = func(cwd string, ctx launchCtx, t config.Terminal) (string, error) {
	return spawnLauncher("", cwd, ctx, t)
}

// selectDriver resolves the compiled-in internal/agent.Driver that talks to
// whatever a target's pane holds — ADR-0021's "protocol is code" half.
// Swapped out in tests like spawnPane.
var selectDriver = agent.Select

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
// queued and started as slots free. Workers run in launcher windows unless
// the action (or [terminal]) says locale = "hosted" — see workflowhosted.go,
// where surface decides whether a hosted worker gets a dashboard pty or the
// caller's own terminal. actionName and action identify the
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
func DispatchTargets(repoRoot, actionName string, action manifest.Action, targets []Target, surface DispatchSurface) []TargetResult {
	results := make([]TargetResult, len(targets))
	failAll := func(err error) []TargetResult {
		for i, t := range targets {
			results[i] = TargetResult{Target: t.Name, Err: err}
		}
		return results
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return failAll(fmt.Errorf("loading config: %w", err))
	}
	agentKind, ok := ResolveAgent(action, cfg)
	if !ok {
		return failAll(fmt.Errorf(
			"action %q: kind=\"workflow\" has no agent configured — set 'agent' on the action "+
				"or [workflow].agent in admin.toml", actionName))
	}
	hosted := workerLocale(action, cfg.Terminal.Locale) == validate.LocaleHosted
	mode := workflowMode(action)
	var drv agent.Driver
	ceiling := Ceiling(cfg)
	if hosted {
		if surface == SurfaceShell {
			// Inline agents share one terminal, so they run one at a time.
			ceiling = 1
		}
	} else {
		if mode != "" {
			return failAll(fmt.Errorf(
				"action %q: mode applies to locale = %q workers only — set locale = %q or drop mode",
				actionName, validate.LocaleHosted, validate.LocaleHosted))
		}
		if cfg.Terminal.Spawn == "" {
			return failAll(fmt.Errorf(
				"action %q: kind=\"workflow\" dispatch requires a [terminal] launcher — "+
					"declare spawn/attach/kill in admin.toml (ADR-0020), or set locale = %q to run the agents hosted",
				actionName, validate.LocaleHosted))
		}
		drv, ok = selectDriver()
		if !ok {
			return failAll(fmt.Errorf(
				"action %q: no agent driver available — is the configured launcher's underlying "+
					"tool (e.g. herdr) reachable?", actionName))
		}
	}

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
	// drv.Prompt call returns, not when the pane merely opens.
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
			if hosted {
				results[i] = hostedOne(repoRoot, actionName, agentKind, mode, surface, t, wt)
			} else {
				results[i] = dispatchOne(repoRoot, actionName, agentKind, cfg.Terminal, drv, t, wt)
			}
		}(i, t, worktrees[i])
	}
	wg.Wait()
	return results
}

// dispatchOne runs one target's agent once its ceiling slot is held: it
// re-verifies wt is still a real worktree (never recreating one that
// isn't), opens a pane through the project's [terminal] launcher, starts
// the agent session in it through drv, and records the running target in
// the live-work registry with its launcher handle before returning —
// mirroring internal/kinds/pane.go's recordLiveWork for the single-pane
// LongRunning case, but keyed on the pane this dispatch itself opened
// rather than one launchCommand opened for it.
func dispatchOne(repoRoot, actionName, agentKind string, t config.Terminal, drv agent.Driver, tgt Target, wt create.Result) TargetResult {
	if err := worktreeGone(repoRoot, tgt, wt); err != nil {
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Err: err}
	}

	id := workerLiveID(actionName, tgt.Name)

	// spawnPane opens the pane with an EMPTY ADMIN_CMD, ADMIN_CWD set to
	// this target's own worktree — the pane lands at an interactive shell
	// prompt, exactly what drv.Start requires.
	handle, err := spawnPane(wt.Path, launchCtxFromEnv(actionName), t)
	if err != nil {
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Err: fmt.Errorf("opening pane: %w", err)}
	}
	// Once the pane exists, every failure below must close it: nothing has
	// registered it yet, so nothing else would.
	closePane := func(cause error) error {
		if killErr := runLauncherShell(t.Kill, handle); killErr != nil {
			return errors.Join(cause, fmt.Errorf("closing pane %s: %w", handle, killErr))
		}
		return cause
	}
	if err := drv.Start(handle, agentKind, nil); err != nil {
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Handle: handle,
			Err: closePane(fmt.Errorf("starting agent %q: %w", agentKind, err))}
	}

	// Registered as soon as a real pane exists to key it on and BEFORE the
	// blocking wait below — the acceptance criterion is "each RUNNING
	// target appears in the live registry with its pane handle", which
	// means a reader of the registry mid-dispatch, not only after every
	// target has finished.
	//
	// Keyed on the full sanitized action-target name, not the handle and not
	// the 32-character herdr agent name: two targets that differ only after
	// character 32 must stay two entries.
	e := &livework.Entry{
		ID:     id,
		Name:   actionName,
		Kind:   "workflow",
		Handle: handle,
		Via:    "terminal",
		Worker: &livework.WorkerInfo{Branch: wt.Branch, Worktree: wt.Path},
	}
	e.Ops = livework.Ops{
		Attach: func() error { return runLauncherShell(t.Attach, e.Handle) },
		Kill: func() error {
			if err := runLauncherShell(t.Kill, e.Handle); err != nil {
				return err
			}
			livework.Default.SetState(e.ID, livework.StateExited, nil)
			return nil
		},
	}
	if err := livework.Default.Add(e); err != nil {
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Handle: handle, Err: closePane(err)}
	}

	// A blank prompt means the agent was started with nothing to do: it stays
	// running for a person to drive, and there is no settle to wait for.
	if strings.TrimSpace(tgt.Prompt) == "" {
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Handle: handle}
	}
	if err := drv.Prompt(handle, tgt.Prompt, []agent.Status{agent.StatusIdle, agent.StatusDone, agent.StatusBlocked}); err != nil {
		livework.Default.SetState(id, livework.StateFailed, err)
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Handle: handle, Err: err}
	}

	// Prompt settles on idle, done, OR blocked — an agent waiting on an
	// approval or question prompt returns exactly like one that actually
	// finished, so the settled status has to be re-checked to tell
	// "waiting on input" apart from "still working"/"done" without
	// attaching.
	if info, infoErr := drv.Info(context.Background(), handle); infoErr == nil && info.Status == agent.StatusBlocked {
		livework.Default.SetState(id, livework.StateWaiting, nil)
	} else {
		livework.Default.SetState(id, livework.StateExited, nil)
	}
	return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Handle: handle}
}

// workerLiveID derives a window-locale worker's live-work ID from the
// workflow action and target: the sanitized "<action>-<target>", uncapped.
// herdr's own 32-character agent name is derived separately from the pane
// handle (internal/agent/herdr), so nothing here needs to fit it.
func workerLiveID(actionName, target string) string {
	return agent.SanitizeName(actionName+"-"+target, "workflow", "w-", 0)
}
