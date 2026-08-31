// pane.go is admin-3rgp.17's herdr-dispatch mechanism: how a LongRunning
// kind's dispatch actually reaches a herdr pane. The pane runs the SAME
// binary re-invoked against the hidden "__run-action" cobra command
// (cmd/root.go), which calls RunAction directly for exactly that one
// action — never the whole multi-step command it may have been a step of,
// and never RunActionRouted again (so a long-running action never tries to
// open a second pane for itself; only a *different*, nested action reached
// through multi-target delegation would route again, which is correct: a
// nested long-running target deserves its own pane too).
package kinds

import (
	"os"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
)

// selfExecutable resolves the path to the currently running admin binary,
// falling back to the bare "admin" name (resolved via PATH by the pane's
// own shell) when os.Executable can't answer — e.g. under an unusual
// exec environment in a test. Var, not a plain call, so tests can stub it
// without touching the real binary on disk.
var selfExecutable = func() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "admin"
}

// dispatchToPane is RunActionRouted's herdr half: it opens a new pane
// rooted at the current directory (RunAction/RunCommand already run from
// the project root — internal/kinds never changes directory itself) and
// types `<admin> __run-action <name> [args...]` into it. It returns the new
// pane's ID on success.
//
// It returns shim.ErrHerdrUnavailable — never wrapped — whenever herdr
// simply isn't usable here, so RunActionRouted can tell "fall back to
// inline, this is the expected default path" apart from "herdr is
// available but something about this specific call failed", which is a
// real error worth surfacing rather than silently swallowing.
func dispatchToPane(name string, args []string) (string, error) {
	if !shim.Available() {
		return "", shim.ErrHerdrUnavailable
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	paneID, err := shim.SplitPane(wd)
	if err != nil {
		return "", err
	}
	if err := shim.RunInPane(paneID, reentryCommandLine(name, args)); err != nil {
		return "", err
	}
	return paneID, nil
}

// reentryCommandLine builds the shell command line the pane runs: the
// current binary, the hidden re-entry verb, the action name, and its
// arguments, each shell-quoted for safe inclusion in the pane's shell.
func reentryCommandLine(name string, args []string) string {
	parts := make([]string, 0, len(args)+3)
	parts = append(parts, shellQuote(selfExecutable()), "__run-action", shellQuote(name))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// recordLiveWork adds a running entry for a successfully-dispatched
// long-running action to the process-wide live-work registry.
func recordLiveWork(name, kind, paneID string) {
	livework.Default.Add(&livework.Entry{
		ID:     paneID,
		Name:   name,
		Kind:   kind,
		PaneID: paneID,
	})
}
