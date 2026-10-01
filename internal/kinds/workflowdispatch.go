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
//   - re-checking, immediately before a target's pane opens, that its
//     worktree is still the real thing internal/create made — a worktree
//     created well before its ceiling slot frees can be removed out from
//     under this dispatch by something outside it (a cleanup sweeper, a
//     person), and the fix is to fail that target loudly, never to
//     silently recreate one at a base nobody chose;
//   - opening each target's pane through the project's declared [terminal]
//     launcher (ADR-0020) with the action's run string as its command
//     (ADR-0021), no more than the ceiling at once, the rest queued and
//     started as slots free;
//   - recording every running target in internal/livework with its
//     launcher handle before DispatchTargets returns for it;
//   - holding a window worker's ceiling slot from its spawn until
//     ReleaseWorkerSlot frees it, so a finished-but-unlanded worker still
//     counts against the ceiling.
//
// It never deletes a worktree, before, during, or after dispatch; retiring a
// finished worker is reap.go's job, run from the dashboard tick.
package kinds

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
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
// today. Run is the shell command the target's pane executes, read from the
// workflow action's `run` key, and Done is the optional predicate the
// dashboard polls to learn the worker finished (the action's `done` key).
type Target struct {
	Name string
	Run  string
	Done string
}

// ActionRun is the workflow action's `run` string, "" when it declares none.
func ActionRun(action manifest.Action) string {
	run, _ := actionString(action, "run")
	return run
}

// NewTarget builds the Target for id from the workflow action's own `run` and
// `done` keys. Every path that builds targets — a resolved query, a row
// selection, a row key — goes through it, so none formats its own command.
func NewTarget(action manifest.Action, id string) Target {
	done, _ := actionString(action, "done")
	return Target{Name: id, Run: ActionRun(action), Done: done}
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
// declared [terminal] launcher with ADMIN_CMD set to cmdLine (the target's
// run string), ADMIN_CWD to the target's own worktree and ADMIN_TARGET to its
// id, returning the opaque launcher handle. Swapped out in tests so
// DispatchTargets is exercised without a real launcher.
var spawnPane = func(cmdLine, cwd string, ctx launchCtx, t config.Terminal) (string, error) {
	return spawnLauncher(cmdLine, cwd, ctx, t)
}

// Ceiling returns the workflow's concurrency ceiling: the project
// configuration's [workflow].ceiling when positive, else DefaultCeiling.
// Per-workflow overrides are not read here; the ceiling is project-wide.
func Ceiling(cfg config.Config) int {
	if cfg.Workflow.Ceiling > 0 {
		return cfg.Workflow.Ceiling
	}
	return DefaultCeiling
}

// DispatchTargets creates (or reuses) a worktree per target under repoRoot,
// seeded with the configured populate files, and runs each target's run string
// in it — no more than Ceiling(cfg) running at once, the remainder
// queued and started as slots free. Workers run in launcher windows unless
// the action (or [terminal]) says locale = "hosted" — see workflowhosted.go,
// where surface decides whether a hosted worker gets a dashboard pty or the
// caller's own terminal. actionName and action identify the
// workflow action that named these targets, used for the live-registry
// entry's Name/Kind and the locale.
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
	for _, t := range targets {
		if err := RequireRun(actionName, t.Run); err != nil {
			return failAll(err)
		}
	}
	hosted := workerLocale(action, cfg.Terminal.Locale) == validate.LocaleHosted
	ceiling := Ceiling(cfg)
	if hosted {
		if surface == SurfaceShell {
			// Inline workers share one terminal, so they run one at a time.
			ceiling = 1
		}
	} else if cfg.Terminal.Spawn == "" {
		return failAll(fmt.Errorf(
			"action %q: kind=\"workflow\" dispatch requires a [terminal] launcher — "+
				"declare spawn/attach/kill in admin.toml (ADR-0020), or set locale = %q to run the workers hosted",
			actionName, validate.LocaleHosted))
	}

	// Phase 1: create every target's worktree up front, seeded with the
	// configured populate files (create.Worktree does both — the
	// acceptance criterion "dispatching over N targets creates N
	// worktrees, each seeded with the populate files").
	ctx := context.Background()
	group := actionName + "-" + time.Now().Format("20060102T150405.000000000")
	worktrees := make([]create.Result, len(targets))
	for i, t := range targets {
		res, err := create.Worktree(ctx, repoRoot, t.Name)
		if err != nil {
			results[i] = TargetResult{Target: t.Name, Err: fmt.Errorf("creating worktree: %w", err)}
			continue
		}
		worktrees[i] = res
	}

	// Phase 2: run each target under the ceiling. sem's buffer
	// size IS the ceiling: sending to it blocks once `ceiling` goroutines
	// already hold a slot, which is exactly "targets beyond it queue and
	// start as slots free".
	sem := make(chan struct{}, ceiling)
	var wg sync.WaitGroup
	for i, t := range targets {
		if results[i].Err != nil {
			continue // phase-1 failure already recorded for this target
		}
		wg.Add(1)
		sem <- struct{}{}
		release := sync.OnceFunc(func() { <-sem })
		go func(i int, t Target, wt create.Result) {
			defer wg.Done()
			if hosted {
				defer release()
				results[i] = hostedOne(repoRoot, actionName, surface, t, wt)
				return
			}
			// A window worker keeps its slot after the spawn returns:
			// dispatchOne hands release to ReleaseWorkerSlot once the worker
			// is registered, and this frees it on any failure.
			results[i] = dispatchOne(repoRoot, actionName, group, i, cfg.Terminal, t, wt, release)
			if results[i].Err != nil {
				release()
			}
		}(i, t, worktrees[i])
	}
	wg.Wait()
	return results
}

// dispatchOne runs one target's run string once its ceiling slot is held: it
// re-verifies wt is still a real worktree (never recreating one that
// isn't), opens a pane through the project's [terminal] launcher whose
// command is the run string, and records the running target in the
// live-work registry with its launcher handle before returning. group and
// index are the dispatch's shared id and the target's position in it.
// release frees the target's ceiling slot: on success dispatchOne parks it under the
// worker's live id for ReleaseWorkerSlot, so the slot lasts as long as the
// worker; on error the caller calls it. The registry entry mirrors
// internal/kinds/pane.go's recordLiveWork for the single-pane LongRunning
// case, but is keyed on the pane this dispatch itself opened rather than one
// launchCommand opened for it.
func dispatchOne(repoRoot, actionName, group string, index int, t config.Terminal, tgt Target, wt create.Result, release func()) TargetResult {
	if err := worktreeGone(repoRoot, tgt, wt); err != nil {
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Err: err}
	}

	id := workerLiveID(actionName, tgt.Name)

	lc := launchCtxFromEnv(actionName)
	lc.Target = tgt.Name
	lc.Group = group
	lc.Index = index
	handle, err := spawnPane(tgt.Run, wt.Path, lc, t)
	if err != nil {
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Err: fmt.Errorf("opening pane: %w", err)}
	}
	// Once the pane exists, a registry failure must close it: nothing has
	// registered it yet, so nothing else would.
	closePane := func(cause error) error {
		if killErr := runLauncherShell(t.Kill, handle); killErr != nil {
			return errors.Join(cause, fmt.Errorf("closing pane %s: %w", handle, killErr))
		}
		return cause
	}

	// Keyed on the full sanitized action-target name, not the handle: two
	// targets that differ only late in the name must stay two entries.
	e := &livework.Entry{
		ID:     id,
		Name:   actionName,
		Kind:   "workflow",
		Handle: handle,
		Via:    "terminal",
		Worker: &livework.WorkerInfo{Branch: wt.Branch, Worktree: wt.Path, Target: tgt.Name, Done: tgt.Done},
	}
	e.Ops = livework.Ops{
		Attach: func() error { return runLauncherShell(t.Attach, e.Handle) },
		Kill: func() error {
			// A pane that already closed makes kill fail; closing it was the
			// goal, so the row still exits and frees its slot.
			ctx, cancel := context.WithTimeout(context.Background(), doneTimeout)
			defer cancel()
			if err := closeFinishedPane(ctx, e.Handle, t); err != nil {
				return err
			}
			livework.Default.SetState(e.ID, livework.StateExited, nil)
			ReleaseWorkerSlot(e.ID)
			return nil
		},
	}
	if err := livework.Default.Add(e); err != nil {
		return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Handle: handle, Err: closePane(err)}
	}
	holdWorkerSlot(id, release)
	return TargetResult{Target: tgt.Name, WorktreePath: wt.Path, Handle: handle}
}

// workerSlots maps a window worker's live id to the function that frees its
// ceiling slot. An entry exists from the worker's spawn until
// ReleaseWorkerSlot runs.
var (
	workerSlotsMu sync.Mutex
	workerSlots   = map[string]func(){}
)

func holdWorkerSlot(id string, release func()) {
	workerSlotsMu.Lock()
	old := workerSlots[id]
	workerSlots[id] = release
	workerSlotsMu.Unlock()
	// A re-dispatch of an id whose earlier worker exited unreleased: that
	// worker's slot is free now, so return it rather than lose it.
	if old != nil {
		old()
	}
}

// ReleaseWorkerSlot frees the ceiling slot the window worker with live id
// holds, letting a queued target start. It is a no-op for an id that holds
// none or whose slot is already free.
func ReleaseWorkerSlot(id string) {
	workerSlotsMu.Lock()
	release := workerSlots[id]
	delete(workerSlots, id)
	workerSlotsMu.Unlock()
	if release != nil {
		release()
	}
}

// workerLiveID derives a window-locale worker's live-work ID from the
// workflow action and target: the sanitized "<action>-<target>", uncapped.
// herdr's own 32-character agent name is derived separately from the pane
// handle (internal/agent/herdr), so nothing here needs to fit it.
func workerLiveID(actionName, target string) string {
	return agent.SanitizeName(actionName+"-"+target, "workflow", "w-", 0)
}
