// pane.go is admin-ysxr.2's launcher-dispatch mechanism: how a `detach =
// true` action's dispatch reaches the project's own [terminal] launcher
// (ADR-0020). The launcher's spawn command runs the SAME binary re-invoked
// against the hidden "__run-action" cobra command (cmd/root.go), which
// calls RunAction directly for exactly that one action — never the whole
// multi-step command it may have been a step of, and never RunActionRouted
// again (so a detached action never tries to dispatch itself a second
// time; only a *different*, nested action reached through multi-target
// delegation would route again, which is correct: a nested detached
// target deserves its own dispatch too).
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
// RunActionRouted's "run inline, silently" default. Never wrapped, so a
// caller can distinguish "no launcher declared, the expected default path"
// from "a launcher IS declared but spawning through it failed", which is a
// real error worth surfacing rather than silently swallowing.
var errNoLauncher = fmt.Errorf("no [terminal] launcher declared")

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
// carries, or all-empty for a dispatch that did not come from a row.
func launchCtxFromEnv(label string) launchCtx {
	return launchCtx{
		Label:   label,
		RowKind: os.Getenv("ADMIN_ROW_KIND"),
		RowID:   os.Getenv("ADMIN_ROW_ID"),
		RowURL:  os.Getenv("ADMIN_ROW_URL"),
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

// dispatchToLauncher is RunActionRouted's detach half: it shells out to the
// project's declared t.Spawn command with the ADR-0020 ADMIN_* context in
// its environment, and returns the trimmed stdout as the opaque handle.
//
// It returns errNoLauncher — never wrapped — whenever t declares no spawn
// command at all, so RunActionRouted can tell "fall back to inline, this is
// the expected default path" apart from "a launcher IS configured but
// something about this specific call failed", which is a real error worth
// surfacing rather than silently swallowing.
func dispatchToLauncher(name string, args []string, ctx launchCtx, t config.Terminal) (string, error) {
	if t.Spawn == "" {
		return "", errNoLauncher
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	cmdLine := reentryCommandLine(name, args)
	cmd := exec.Command("/bin/sh", "-c", t.Spawn)
	cmd.Env = append(os.Environ(), ctx.env(cmdLine, wd)...)
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("terminal.spawn: %w", err)
	}
	handle := strings.TrimSpace(out.String())
	if handle == "" {
		return "", fmt.Errorf("terminal.spawn printed no handle")
	}
	return handle, nil
}

// reentryCommandLine builds the shell command line the launcher runs: the
// current binary, the hidden re-entry verb, the action name, and its
// arguments, each shell-quoted for safe inclusion in the launcher's shell.
func reentryCommandLine(name string, args []string) string {
	parts := make([]string, 0, len(args)+3)
	parts = append(parts, shellQuote(selfExecutable()), "__run-action", shellQuote(name))
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
// detached action to the process-wide live-work registry, keyed on the
// action's own name (stable across a Retry, which opens a brand new
// launcher handle under the same name) rather than the handle itself. args
// and ctx are kept only so Retry can re-run dispatchToLauncher exactly as
// it ran the first time.
func recordLiveWork(name, kind, handle string, args []string, ctx launchCtx, t config.Terminal) {
	e := &livework.Entry{
		ID:     name,
		Name:   name,
		Kind:   kind,
		PaneID: handle,
	}
	e.Ops = livework.Ops{
		Attach: func() error { return runLauncherShell(t.Attach, e.PaneID) },
		Kill: func() error {
			if err := runLauncherShell(t.Kill, e.PaneID); err != nil {
				return err
			}
			livework.Default.SetState(e.ID, livework.StateExited, nil)
			return nil
		},
		// Retry re-dispatches through the launcher and re-adds the entry
		// under the same ID (name) — Add's documented overwrite-in-place
		// behavior is what keeps this the same row rather than a second
		// one appearing beside the stale failed entry.
		Retry: func() error {
			newHandle, err := dispatchToLauncher(name, args, ctx, t)
			if err != nil {
				return err
			}
			recordLiveWork(name, kind, newHandle, args, ctx, t)
			return nil
		},
	}
	if err := livework.Default.Add(e); err != nil {
		log.Printf("recordLiveWork: %v", err)
	}
}
