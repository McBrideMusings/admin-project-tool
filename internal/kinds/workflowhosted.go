// workflowhosted.go is admin-spjp.5: the per-target step DispatchTargets takes
// when a workflow's workers are declared `locale = "hosted"` instead of run
// in a launcher window. It is dispatchOne's sibling, not a second dispatch
// path — worktree creation, the concurrency ceiling and result collection all
// stay in DispatchTargets; only "how one target's agent runs" differs.
//
// The worker is the action's own `run` string under /bin/sh with ADMIN_TARGET
// set. There is no pane, so a hosted worker needs neither a [terminal]
// launcher nor herdr.
//
// Where it runs depends on who called DispatchTargets:
//
//   - SurfaceDashboard: on a pty the dashboard owns (StartHostedAgent), 24x80
//     until the dashboard attaches and resizes it. The worker's ceiling slot
//     frees when its process exits.
//   - SurfaceShell: inline, one target at a time (the ceiling is 1), each
//     agent inheriting the terminal until it exits.
package kinds

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/McBrideMusings/admin-project-tool/internal/create"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// DispatchSurface says who called DispatchTargets, and so where a hosted
// worker can run.
type DispatchSurface int

const (
	// SurfaceShell is a plain command line (`admin <workflow>`): no dashboard
	// owns a pty, so a hosted worker runs inline in the caller's terminal.
	SurfaceShell DispatchSurface = iota
	// SurfaceDashboard is the dashboard process, which owns hosted ptys.
	SurfaceDashboard
)

// workerLocale is where a workflow's workers run: the action's own `locale`,
// else [terminal].locale, else "window" — the launcher, which is what a
// workflow did before `hosted` was a choice.
func workerLocale(action manifest.Action, term string) string {
	if l := firstLocale(actionLocale(action), term); l != "" {
		return l
	}
	return validate.LocaleWindow
}

// hostedRunArgv is the command line of one hosted worker: the action's run
// string under /bin/sh, the same interpreter the launcher's pane gives it.
func hostedRunArgv(run string) []string {
	return []string{"/bin/sh", "-c", run}
}

// targetEnv is the environment a worker's run string sees beyond the parent's
// own: the target id, as ADMIN_TARGET.
func targetEnv(tgt Target) []string {
	return []string{"ADMIN_TARGET=" + tgt.Name}
}

// startHostedAgent and runInlineAgent are the seams tests swap so a hosted
// dispatch runs a stand-in command instead of the manifest's run string.
var (
	startHostedAgent = StartHostedAgent
	runInlineAgent   = func(argv []string, dir string, env []string) error {
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), env...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	}
)

// worktreeGone reports why wt no longer counts as the worktree
// internal/create made for tgt, or nil when it still does. It never
// recreates one at a base nobody chose.
func worktreeGone(repoRoot string, tgt Target, wt create.Result) error {
	if create.IsRegisteredWorktree(context.Background(), repoRoot, wt.Path) {
		return nil
	}
	return fmt.Errorf(
		"worktree %s for target %q no longer exists — not recreating it at a base "+
			"nobody chose; something removed it after it was created and before its "+
			"turn to dispatch", wt.Path, tgt.Name)
}

// hostedOne runs one target's agent as a hosted worker and returns once the
// agent's process has exited, which is when DispatchTargets frees its slot.
// The live-work entry (dashboard surface) is keyed "<action>:<target>" on the
// full target name — the hosted-command entry shape, with no view of its own.
func hostedOne(repoRoot, actionName string, surface DispatchSurface, tgt Target, wt create.Result) TargetResult {
	res := TargetResult{Target: tgt.Name, WorktreePath: wt.Path}
	if err := worktreeGone(repoRoot, tgt, wt); err != nil {
		res.Err = err
		return res
	}
	argv := hostedRunArgv(tgt.Run)
	if surface == SurfaceShell {
		if err := runInlineAgent(argv, wt.Path, targetEnv(tgt)); err != nil {
			res.Err = fmt.Errorf("run %q: %w", tgt.Run, err)
		}
		return res
	}
	exit, err := startHostedAgent(TargetLiveKey(actionName, tgt.Name), argv, wt.Path, targetEnv(tgt), 0, 0)
	if err != nil {
		res.Err = fmt.Errorf("starting run %q: %w", tgt.Run, err)
		return res
	}
	if err := <-exit; err != nil {
		res.Err = fmt.Errorf("run %q: %w", tgt.Run, err)
	}
	return res
}
