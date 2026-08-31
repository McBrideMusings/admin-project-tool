// hostedpty.go is admin-3rgp.61's dispatch mechanism: how the dashboard runs
// a Command onto a pseudo-terminal it owns, without ever releasing its own
// alt-screen. It mirrors pane.go's herdr-dispatch pattern (ADR-0015) —
// re-exec this same binary against a hidden re-entry verb — but with
// pty.Start in place of shim.SplitPane, and __run-command (which calls
// RunCommandHosted) in place of __run-action (which calls the pane-capable
// RunAction). Running the whole step loop in a child process is what keeps
// Host.Stdout, the frame footer, and every one of the ~30 newHost() call
// sites in this package off the dashboard's own terminal: from the child's
// point of view the pty IS its terminal, so nothing here has to know that.
package kinds

import (
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/creack/pty"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// hostedReentryArgs builds the argv for the pty child: the hidden
// "__run-command" re-entry verb, the command name, and its args. Unlike
// pane.go's reentryCommandLine, this is plain argv rather than a
// shell-quoted string — pty.Start runs the child directly via exec.Command,
// with no shell in between to need quoting.
func hostedReentryArgs(name string, args []string) []string {
	out := make([]string, 0, len(args)+2)
	out = append(out, "__run-command", name)
	out = append(out, args...)
	return out
}

// StartHostedPTY dispatches name (a declared command) onto a freshly
// allocated pty and returns as soon as the child is spawned — the run itself
// continues in a background goroutine. rows/cols size the pty to the region
// the dashboard's terminal view will render it in (internal/frame's own
// winsize helper, not a second ioctl); either being <= 0 falls back to
// 80x24, matching frame.newFrame's own terminal-size fallback.
//
// The registered live-work entry is keyed on the command's first Step's
// action name, not the command name — internal/tui/work.go's liveEntryFor
// looks entries up by step name, so keying on the command name would leave
// the row not showing live and let a second run start on top of the one
// that already owns the log file.
func StartHostedPTY(m *manifest.Manifest, name string, args []string, rows, cols int) error {
	cmd, ok := resolveCommand(m, name)
	if !ok {
		return fmt.Errorf("command %q: not declared", name)
	}
	if len(cmd.Steps) == 0 {
		return fmt.Errorf("command %q has no steps", name)
	}
	entryID := cmd.Steps[0]

	c := exec.Command(selfExecutable(), hostedReentryArgs(name, args)...)
	ptmx, err := pty.Start(c)
	if err != nil {
		return fmt.Errorf("command %q: starting hosted pty: %w", name, err)
	}
	if rows <= 0 || cols <= 0 {
		rows, cols = 24, 80
	}
	frame.SetWinsize(ptmx.Fd(), rows, cols)

	recordHostedPTYWork(entryID, m, name, args, rows, cols, c, ptmx)
	return nil
}

// recordHostedPTYWork adds a running PTYID entry for a successfully-spawned
// hosted dispatch, and waits for the child to exit in a goroutine so the
// entry can flip to exited/failed the same way recordLiveWork's pane-backed
// entry does — the only visible difference to a caller is which of
// PaneID/PTYID is set. args and rows/cols are kept so Retry can re-run
// StartHostedPTY exactly as it ran the first time.
func recordHostedPTYWork(entryID string, m *manifest.Manifest, name string, args []string, rows, cols int, c *exec.Cmd, ptmx *os.File) {
	e := &livework.Entry{
		ID:    entryID,
		Name:  entryID,
		Kind:  "hosted-pty",
		PTYID: ptmx.Name(),
	}
	e.Ops = livework.Ops{
		Kill: func() error {
			_ = ptmx.Close()
			if c.Process != nil {
				_ = c.Process.Kill()
			}
			livework.Default.SetState(e.ID, livework.StateExited, nil)
			return nil
		},
		// Retry re-dispatches to a fresh pty and re-adds the entry under the
		// same ID (entryID) — Add's documented overwrite-in-place behavior is
		// what keeps this the same row rather than a second one appearing
		// beside the stale failed entry.
		Retry: func() error {
			return StartHostedPTY(m, name, args, rows, cols)
		},
	}
	if err := livework.Default.Add(e); err != nil {
		log.Printf("recordHostedPTYWork: %v", err)
		return
	}
	go func() {
		waitErr := c.Wait()
		_ = ptmx.Close()
		if waitErr != nil {
			livework.Default.SetState(e.ID, livework.StateFailed, waitErr)
		} else {
			livework.Default.SetState(e.ID, livework.StateExited, nil)
		}
	}()
}
