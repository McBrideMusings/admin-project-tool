// pane.go is admin-ysxr.2's launcher-dispatch mechanism: how a `detach =
// true` action's dispatch reaches the project's own [terminal] launcher
// (ADR-0020), and admin-ysxr.4's re-entry signal that replaces the old
// argv-sentinel (reentryDetachedMarker) guard. The launcher's spawn command
// runs the SAME binary re-invoked against a hidden re-entry verb —
// "__run-action" (cmd/root.go), which calls RunAction for exactly one named
// action, or "__run-command" (dispatchToLauncher's other caller,
// dispatch.go's launchCommand), which calls RunCommand for a whole
// multi-step command. Both hidden verbs call MarkReentry before dispatching,
// so ANY dispatch reached from inside an already-launched run — a step
// delegating to another action, a multi-target target reached one level
// down, a nested command hop — runs inline instead of trying to reach a
// second launcher from inside the first. That is a process-wide fact
// (inReentry, below), not a per-target one: it is deliberately stronger than
// the old marker it replaces, which only guarded the one detached target
// that dispatched it.
package kinds

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/timing"
)

// selfExecutable resolves the path to the currently running admin binary,
// falling back to the bare "admin" name (resolved via PATH by the
// launcher's own shell) when os.Executable can't answer — e.g. under an
// unusual exec environment in a test. Var, not a plain call, so tests can
// stub it without touching the real binary on disk.
var selfExecutable = func() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "admin"
}

// errNoLauncher reports that the project declared no [terminal] table —
// launchCommand's "fall back to the producer's default surface, silently"
// path. Never wrapped, so a
// caller can distinguish "no launcher declared, the expected default path"
// from "a launcher IS declared but spawning through it failed", which is a
// real error worth surfacing rather than silently swallowing.
var errNoLauncher = fmt.Errorf("no [terminal] launcher declared")

// ReentryEnv is the environment variable that marks a process as already
// running inside a launched dispatch — the out-of-band replacement for the
// old reentryDetachedMarker argv sentinel. An env var, not a package
// global, so the fact survives into any child process this run spawns (a
// step that shells out to `admin <cmd>` inside an already-launched window
// must not launch again either).
const ReentryEnv = "ADMIN_REENTRY"

// MarkReentry records that this process is itself a re-entry dispatch.
// cmd/root.go's two hidden re-entry verbs — __run-action and __run-command —
// call it before dispatching, since each of them IS by definition a
// re-entry: the fact is asserted at the verb rather than depending on the
// launched process having inherited anything from its parent.
func MarkReentry() {
	os.Setenv(ReentryEnv, "1")
}

// inReentry reports whether this process is already running inside a
// launched dispatch. RunCommand consults it before deciding whether p (a
// non-nil producer) gets to reach launchCommand at all: nothing inside an
// already-launched run reaches a second launcher, however many levels of
// delegation separate it from the dispatch that opened that run.
func inReentry() bool {
	return os.Getenv(ReentryEnv) == "1"
}

// launchCtx is the row identity a launcher's spawn command receives via
// ADMIN_ROW_KIND/ADMIN_ROW_ID/ADMIN_ROW_URL (ADR-0020) — empty for a
// dispatch that did not come from a bound row. It is read off the current
// process's own environment: StartHostedPTY already exports these three
// vars into a row-dispatched child, so a nested `admin <cmd>` invocation
// (the common case for a detached action reached through a row binding)
// inherits them for free.
type launchCtx struct {
	Label   string
	RowKind string
	RowID   string
	RowURL  string
}

// launchCtxFromEnv builds a launchCtx for label from the process's own
// ADMIN_ROW_* environment — the row triple a row-bound dispatch already
// carries, or all-empty for a dispatch that did not come from a row. It is
// launchCtxFrom with no extra pairs, which is exactly "fall back to the
// process environment for everything".
func launchCtxFromEnv(label string) launchCtx {
	return launchCtxFrom(label, nil)
}

// launchCtxFrom builds a launchCtx for label, reading each ADMIN_ROW_* value
// out of extraEnv (KEY=VALUE pairs) first and falling back to the process's
// own environment when a key isn't present there. This is what a dashboard
// dispatch needs: internal/tui/rowkeys.go hands a row-bound command's row
// triple to the dashboard as an explicit env slice (kinds.CommandProducer's
// own Env field), not through the dashboard process's own os.Environ() — so
// reading only os.Getenv here would silently drop the row identity for any
// row-bound command that ends up reaching the launcher. A plain-CLI dispatch
// passes nil, which behaves exactly like launchCtxFromEnv.
func launchCtxFrom(label string, extraEnv []string) launchCtx {
	lookup := func(key string) string {
		for _, kv := range extraEnv {
			if v, ok := strings.CutPrefix(kv, key+"="); ok {
				return v
			}
		}
		return os.Getenv(key)
	}
	return launchCtx{
		Label:   label,
		RowKind: lookup("ADMIN_ROW_KIND"),
		RowID:   lookup("ADMIN_ROW_ID"),
		RowURL:  lookup("ADMIN_ROW_URL"),
	}
}

// env renders ctx plus cmdLine/wd into the ADMIN_* environment variables
// ADR-0020's spawn/attach/kill contract passes to the shell — RowEnv
// supplies the row triple so this construction and internal/tui/rowkeys.go's
// key-binding dispatch can never drift apart.
func (c launchCtx) env(cmdLine, wd string) []string {
	out := []string{
		"ADMIN_CMD=" + cmdLine,
		"ADMIN_LABEL=" + c.Label,
		"ADMIN_CWD=" + wd,
	}
	out = append(out, RowEnv(c.RowKind, c.RowID, c.RowURL)...)
	return out
}

// RowEnv builds the ADMIN_ROW_KIND/ADMIN_ROW_ID/ADMIN_ROW_URL variables a
// row-bound dispatch's environment carries — the single construction this
// package's launcher dispatch and internal/tui/rowkeys.go's key-binding
// dispatch both use, so they cannot drift apart (ADR-0018's row env and
// ADR-0020's spawn context are the same triple). ADMIN_ROW_URL is appended
// only when url is genuinely non-empty — never as an empty value, since the
// dispatched process inherits the caller's own os.Environ() as its base and
// an empty ADMIN_ROW_URL="" would still read as "set" to anything that only
// checks presence. kind == "" (no row bound at all) still emits
// ADMIN_ROW_KIND="" and ADMIN_ROW_ID="" — a launcher's spawn command can
// rely on both vars always being present, only URL is conditional.
func RowEnv(kind, id, url string) []string {
	env := []string{
		"ADMIN_ROW_KIND=" + kind,
		"ADMIN_ROW_ID=" + id,
	}
	if url != "" {
		env = append(env, "ADMIN_ROW_URL="+url)
	}
	return env
}

// dispatchToLauncher is the detach half launchCommand — its only caller now
// that `detach` is honoured on the producer path alone (admin-ysxr.9) —
// reaches: it shells out to the project's declared t.Spawn command with the
// ADR-0020 ADMIN_* context in its environment, and returns the trimmed
// stdout as the opaque handle. verb selects which hidden re-entry command
// the launcher's spawned process runs — "__run-action" for one named
// action, "__run-command" for a whole multi-step command (launchCommand's
// own case): using the action verb for a whole command would silently drop
// steps 2..n, since __run-action only ever runs the one action it names.
//
// root is the project root ADMIN_CWD carries — always projectRoot(m), never
// os.Getwd(): a project found by walking up from a subdirectory (FindRoot)
// leaves the process cwd below the root, and the ADR-0020/CLAUDE.md contract
// documents ADMIN_CWD as "this project's root", not wherever `admin` was
// typed.
//
// It returns errNoLauncher — never wrapped — whenever t declares no spawn
// command at all, so a caller can tell "fall back to inline, this is the
// expected default path" apart from "a launcher IS configured but something
// about this specific call failed", which is a real error worth surfacing
// rather than silently swallowing.
func dispatchToLauncher(verb, name string, args []string, ctx launchCtx, t config.Terminal, root string) (string, error) {
	cmdLine := reentryCommandLine(verb, name, args)
	return spawnLauncher(cmdLine, root, ctx, t)
}

// spawnLauncher is the exec half of dispatchToLauncher, factored out so a
// caller that isn't re-entering this same binary — a workflow target
// (internal/kinds/workflowdispatch.go), which spawns a pane with an EMPTY
// ADMIN_CMD so it sits at an interactive shell prompt (exactly what `herdr
// agent start` requires) and ADMIN_CWD set to the worktree, not the
// process's own os.Getwd() — can reach the project's declared [terminal]
// launcher without going through the re-entry command-line construction
// dispatchToLauncher's callers need.
//
// It returns errNoLauncher — never wrapped — whenever t declares no spawn
// command at all, so a caller can tell "fall back to inline, this is the
// expected default path" apart from "a launcher IS configured but something
// about this specific call failed", which is a real error worth surfacing
// rather than silently swallowing.
func spawnLauncher(cmdLine, wd string, ctx launchCtx, t config.Terminal) (string, error) {
	if t.Spawn == "" {
		return "", errNoLauncher
	}
	start := timing.Start()
	cmd := exec.Command("/bin/sh", "-c", t.Spawn)
	cmd.Env = append(os.Environ(), ctx.env(cmdLine, wd)...)
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		timing.Phase("launcher spawn", start)
		return "", fmt.Errorf("terminal.spawn: %w", err)
	}
	timing.Phase("launcher spawn", start)
	handle := strings.TrimSpace(out.String())
	if handle == "" {
		return "", fmt.Errorf("terminal.spawn printed no handle")
	}
	return handle, nil
}

// reentryCommandLine builds the shell command line the launcher runs: the
// current binary, the hidden re-entry verb, the name (an action's for
// "__run-action", a command's for "__run-command"), and its arguments, each
// shell-quoted for safe inclusion in the launcher's shell.
func reentryCommandLine(verb, name string, args []string) string {
	parts := make([]string, 0, len(args)+3)
	parts = append(parts, shellQuote(selfExecutable()), verb, shellQuote(name))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// runLauncherShell runs one of t's attach/kill commands with ADMIN_HANDLE
// set to handle — the shape ADR-0020 gives both.
func runLauncherShell(run, handle string) error {
	cmd := exec.Command("/bin/sh", "-c", run)
	cmd.Env = append(os.Environ(), "ADMIN_HANDLE="+handle)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// recordLiveWork adds a running entry for a successfully-dispatched
// detached action or command to the process-wide live-work registry, keyed
// on id — stable across a Retry, which opens a brand new launcher handle
// under the same id. verb and reentryName are what Retry re-dispatches:
// reentryName is the action name for an "__run-action" entry, or the whole
// command name for an "__run-command" one, which is not always the same
// string as id (launchCommand keys id on the command's first step, per
// StartHostedPTY's own convention, while reentryName is the command itself).
// args, ctx and root are kept only so Retry can re-run dispatchToLauncher
// exactly as it ran the first time.
func recordLiveWork(id, kind, handle, verb, reentryName string, args []string, ctx launchCtx, t config.Terminal, root string) {
	e := &livework.Entry{
		ID:     id,
		Name:   id,
		Kind:   kind,
		Handle: handle,
		Via:    "terminal",
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
		// Retry re-dispatches through the launcher and re-adds the entry
		// under the same ID (id) — Add's documented overwrite-in-place
		// behavior is what keeps this the same row rather than a second
		// one appearing beside the stale failed entry.
		Retry: func() error {
			newHandle, err := dispatchToLauncher(verb, reentryName, args, ctx, t, root)
			if err != nil {
				return err
			}
			recordLiveWork(id, kind, newHandle, verb, reentryName, args, ctx, t, root)
			return nil
		},
	}
	if err := livework.Default.Add(e); err != nil {
		log.Printf("recordLiveWork: %v", err)
	}
}
