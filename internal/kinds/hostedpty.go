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
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"

	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// HostedColorProfileEnv and HostedDarkBackgroundEnv carry the PARENT's
// already-resolved color profile into the pty child's environment. Without
// this, the child's stdout is a real TTY for the first time (it usually
// runs on a herdr pane or a plain pipe) and lipgloss/termenv's default
// output issues an OSC 11 "what's your background color?" query plus a
// CSI 6n cursor-position query, then blocks for termenv.OSCTimeout (5s)
// waiting for a reply the dashboard never sends — a 5s stall on every
// dispatch (Cause B, diagnosed 2026-08-31). cmd/root.go's runCommandCmd
// reads these two vars and calls termenv.SetDefaultOutput +
// lipgloss.SetColorProfile/SetHasDarkBackground before anything can render,
// so the probe is never issued at all.
const (
	HostedColorProfileEnv   = "ADMIN_HOSTED_COLOR_PROFILE"
	HostedDarkBackgroundEnv = "ADMIN_HOSTED_DARK_BG"
)

// killProcessGroup signals SIGKILL to the whole process group led by c's
// child, not just the direct child — a dispatched Command routinely spawns
// children of its own (a bundler, a container, a simulator), and signalling
// only the direct child leaves those orphaned holding ports. StartHostedPTY
// starts the child with Setsid, which makes it a process-group leader
// (pgid == pid), so -pgid reaches the whole tree.
//
// It refuses to signal unless the resolved pgid is a real positive pid and
// is not this orchestrator's own process group — getting either wrong here
// would kill the dashboard itself instead of the hosted child.
func killProcessGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return fmt.Errorf("killProcessGroup: no process")
	}
	pgid, err := syscall.Getpgid(c.Process.Pid)
	if err != nil {
		return fmt.Errorf("killProcessGroup: resolving pgid: %w", err)
	}
	if pgid <= 0 {
		return fmt.Errorf("killProcessGroup: refusing to signal non-positive pgid %d", pgid)
	}
	if pgid == syscall.Getpgrp() {
		return fmt.Errorf("killProcessGroup: refusing to signal the orchestrator's own process group %d", pgid)
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

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
// 80x24, matching frame.newFrame's own terminal-size fallback. env is extra
// KEY=VALUE pairs appended on top of the dashboard's own environment — a
// row-scoped key binding's ADMIN_ROW_KIND/ADMIN_ROW_ID/ADMIN_ROW_URL
// (admin-l6kb.2); nil for every ordinary Commands-tab dispatch.
//
// The registered live-work entry is keyed on the command's first Step's
// action name, not the command name — internal/tui/work.go's liveEntryFor
// looks entries up by step name, so keying on the command name would leave
// the row not showing live and let a second run start on top of the one
// that already owns the log file.
func StartHostedPTY(m *manifest.Manifest, name string, args []string, rows, cols int, env []string) error {
	cmd, ok := resolveCommand(m, name)
	if !ok {
		return fmt.Errorf("command %q: not declared", name)
	}
	if len(cmd.Steps) == 0 {
		return fmt.Errorf("command %q has no steps", name)
	}
	entryID := cmd.Steps[0]

	c := exec.Command(selfExecutable(), hostedReentryArgs(name, args)...)
	c.Env = append(os.Environ(),
		HostedColorProfileEnv+"="+strconv.Itoa(int(lipgloss.ColorProfile())),
		HostedDarkBackgroundEnv+"="+strconv.FormatBool(lipgloss.HasDarkBackground()),
	)
	c.Env = append(c.Env, env...)
	// Setsid (pty.StartWithSize sets this unconditionally on the SysProcAttr
	// it allocates when nil, so pinning it here is behaviour-neutral) makes
	// the child both a session leader and its own process-group leader
	// (pgid == pid) — which is what lets Ops.Kill signal the whole group a
	// dispatched Command spawns (a bundler, a container, a simulator) rather
	// than only this direct child. Do NOT also set Setpgid: true here — Go's
	// forkAndExecInChild runs setsid() before setpgid(0,0), and setpgid on a
	// session leader returns EPERM, which would fail every hosted dispatch.
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	ptmx, err := pty.Start(c)
	if err != nil {
		return fmt.Errorf("command %q: starting hosted pty: %w", name, err)
	}
	if rows <= 0 || cols <= 0 {
		rows, cols = 24, 80
	}
	frame.SetWinsize(ptmx.Fd(), rows, cols)

	recordHostedPTYWork(entryID, m, name, args, rows, cols, env, c, ptmx)
	return nil
}

// recordHostedPTYWork adds a running PTYID entry for a successfully-spawned
// hosted dispatch, and waits for the child to exit in a goroutine so the
// entry can flip to exited/failed the same way recordLiveWork's pane-backed
// entry does — the only visible difference to a caller is which of
// PaneID/PTYID is set. args, rows/cols, and env are kept so Retry can re-run
// StartHostedPTY exactly as it ran the first time.
func recordHostedPTYWork(entryID string, m *manifest.Manifest, name string, args []string, rows, cols int, env []string, c *exec.Cmd, ptmx *os.File) {
	// killed is set by Ops.Kill before it closes ptmx, so the wait goroutine
	// below knows the resulting "signal: killed" c.Wait() error is expected
	// and must not overwrite the StateExited Kill already set. exited is set
	// by the wait goroutine the instant c.Wait() returns, so Kill can tell
	// "this already finished on its own" without reading e.State — that field
	// is unsynchronised (the pre-existing race admin-ddc8, not fixed here).
	var killed atomic.Bool
	var exited atomic.Bool
	// closed mirrors whether ptmx itself has been closed — by Kill, or by
	// the wait goroutine once the child has exited on its own — so Write
	// and Resize (admin-3rgp.65's terminal view) can refuse a call that
	// would otherwise hit a closed *os.File, rather than surfacing
	// whatever the OS happens to return for that.
	var closed atomic.Bool

	e := &livework.Entry{
		ID:     entryID,
		Name:   entryID,
		Kind:   "hosted-pty",
		PTYID:  ptmx.Name(),
		Output: livework.NewRing(0),
	}
	e.Ops = livework.Ops{
		Kill: func() error {
			// Idempotent: an entry that already exited (naturally or via a
			// prior Kill) is a no-op returning nil, never an error, and must
			// not re-run SetState over whatever the wait goroutine already
			// recorded.
			if exited.Load() || killed.Load() {
				return nil
			}
			killed.Store(true)
			if c.Process != nil {
				if err := killProcessGroup(c); err != nil {
					// Fall back to signalling only the direct child rather
					// than guessing at a group id we couldn't resolve or
					// verify safe.
					_ = c.Process.Kill()
				}
			}
			// Close the master after signalling, not before, so the child
			// gets SIGKILL rather than racing a SIGHUP from the pty close.
			// closed is set with the close so the terminal view's Write and
			// Resize refuse rather than hitting a closed *os.File.
			closed.Store(true)
			_ = ptmx.Close()
			livework.Default.SetState(e.ID, livework.StateExited, nil)
			return nil
		},
		// Retry re-dispatches to a fresh pty and re-adds the entry under the
		// same ID (entryID) — Add's documented overwrite-in-place behavior is
		// what keeps this the same row rather than a second one appearing
		// beside the stale failed entry.
		Retry: func() error {
			return StartHostedPTY(m, name, args, rows, cols, env)
		},
		// Write and Resize are the terminal view's (admin-3rgp.65) only way
		// to reach this child: a keystroke typed while attached, and a pty
		// resize following the dashboard's own window resize.
		Write: func(p []byte) (int, error) {
			if closed.Load() {
				return 0, fmt.Errorf("command %q: pty closed", name)
			}
			return ptmx.Write(p)
		},
		Resize: func(rows, cols int) error {
			if closed.Load() {
				return fmt.Errorf("command %q: pty closed", name)
			}
			frame.SetWinsize(ptmx.Fd(), rows, cols)
			return nil
		},
	}
	if err := livework.Default.Add(e); err != nil {
		log.Printf("recordHostedPTYWork: %v", err)
		return
	}

	// Ordering is load-bearing (admin-3rgp.61's Cause A): the pty's output
	// queue only drains when something reads the master, and on macOS the
	// child's tty-close at exit blocks in the kernel until that drain
	// happens — so the copy must run, and reach EOF/EIO, BEFORE c.Wait() is
	// called, and ptmx must not be closed until after that. Closing ptmx
	// early truncates the child's tail output; never draining at all is the
	// exact hang that failed Verify.
	go func() {
		_, copyErr := io.Copy(e.Output, ptmx)
		// On Linux the master read returns EIO (not io.EOF) once the last
		// slave closes — that is normal end-of-output, not a failure, so it
		// must not be surfaced as a wait error.
		_ = copyErr

		waitErr := c.Wait()
		closed.Store(true)
		_ = ptmx.Close()
		exited.Store(true)
		if killed.Load() {
			return
		}
		if waitErr != nil {
			livework.Default.SetState(e.ID, livework.StateFailed, waitErr)
		} else {
			livework.Default.SetState(e.ID, livework.StateExited, nil)
		}
	}()
}
