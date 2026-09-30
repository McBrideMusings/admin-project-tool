// workflowhosted.go is admin-spjp.5: the per-target step DispatchTargets takes
// when a workflow's workers are declared `locale = "hosted"` instead of run
// in a launcher window. It is dispatchOne's sibling, not a second dispatch
// path — worktree creation, the concurrency ceiling and result collection all
// stay in DispatchTargets; only "how one target's agent runs" differs.
//
// The agent is the real command line, not a herdr agent session: interactive
// `claude [prompt]` by default, `claude -p <prompt>` when the action says
// `mode = "print"`. There is no pane and no Driver, so a hosted worker needs
// neither a [terminal] launcher nor herdr.
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
	"strings"

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

// Workflow agent modes: how a hosted claude worker runs.
const (
	ModeInteractive = "interactive"
	ModePrint       = "print"
)

// workflowMode reads the action's `mode` key: "" when absent.
func workflowMode(action manifest.Action) string {
	m, _ := actionString(action, "mode")
	return m
}

// workerLocale is where a workflow's workers run: the action's own `locale`,
// else [terminal].locale, else "window" — the launcher, which is what a
// workflow did before `hosted` was a choice.
func workerLocale(action manifest.Action, term string) string {
	if l := firstLocale(actionLocale(action), term); l != "" {
		return l
	}
	return validate.LocaleWindow
}

// agentArgv builds the command line of one hosted worker. Only claude has a
// known command line; any other agent kind is an error rather than a guess.
func agentArgv(kind, mode, prompt string) ([]string, error) {
	if kind != "claude" {
		return nil, fmt.Errorf(
			"agent %q cannot run hosted — locale = %q runs the claude command line only", kind, validate.LocaleHosted)
	}
	prompt = strings.TrimSpace(prompt)
	switch mode {
	case "", ModeInteractive:
		if prompt == "" {
			return []string{"claude"}, nil
		}
		return []string{"claude", prompt}, nil
	case ModePrint:
		if prompt == "" {
			return nil, fmt.Errorf("mode = %q runs claude -p, which needs a prompt and this target has none", ModePrint)
		}
		return []string{"claude", "-p", prompt}, nil
	default:
		return nil, fmt.Errorf("mode %q is not %q or %q", mode, ModeInteractive, ModePrint)
	}
}

// hostedAgentArgv, startHostedAgent and runInlineAgent are the seams tests
// swap so a hosted dispatch runs a stand-in command instead of claude.
var (
	hostedAgentArgv  = agentArgv
	startHostedAgent = StartHostedAgent
	runInlineAgent   = func(argv []string, dir string) error {
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = dir
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
func hostedOne(repoRoot, actionName, agentKind, mode string, surface DispatchSurface, tgt Target, wt create.Result) TargetResult {
	res := TargetResult{Target: tgt.Name, WorktreePath: wt.Path}
	if err := worktreeGone(repoRoot, tgt, wt); err != nil {
		res.Err = err
		return res
	}
	argv, err := hostedAgentArgv(agentKind, mode, tgt.Prompt)
	if err != nil {
		res.Err = err
		return res
	}
	if surface == SurfaceShell {
		if err := runInlineAgent(argv, wt.Path); err != nil {
			res.Err = fmt.Errorf("agent %q: %w", agentKind, err)
		}
		return res
	}
	exit, err := startHostedAgent(TargetLiveKey(actionName, tgt.Name), argv, wt.Path, 0, 0)
	if err != nil {
		res.Err = fmt.Errorf("starting agent %q: %w", agentKind, err)
		return res
	}
	if err := <-exit; err != nil {
		res.Err = fmt.Errorf("agent %q: %w", agentKind, err)
	}
	return res
}
