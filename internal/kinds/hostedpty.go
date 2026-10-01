// hostedpty.go is admin-3rgp.61's dispatch mechanism: how the dashboard runs
// a Command onto a pseudo-terminal it owns, without ever releasing its own
// alt-screen. It mirrors pane.go's launcher-dispatch pattern (ADR-0015) —
// re-exec this same binary against a hidden re-entry verb — but with
// pty.Start in place of the project's own [terminal] spawn command
// (ADR-0020), and __run-command in place of __run-action. Both re-entry verbs call kinds.RunCommand/RunAction with
// kinds.MarkReentry() set first (cmd/root.go), so __run-command's whole step
// loop runs inline (RunAction, via runSteps) in the pty child regardless of
// what any step's own `locale` declares — StartHostedPTY IS the built-in
// launcher a
// dashboard dispatch reaches when the manifest names none, so nothing it
// starts can itself reach a second one. Running the whole step loop in a
// child process is what keeps Host.Stdout, the frame footer, and every one
// of the ~30 newHost() call sites in this package off the dashboard's own
// terminal: from the child's point of view the pty IS its terminal, so
// nothing here has to know that.
package kinds

import (
	"bufio"
	"errors"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// readLogAnnounce records the first log path a hosted child reports over its
// luahost.LogAnnounceFDEnv pipe on entry id, then drains the rest — a later
// step, or the end-of-run announce, names the same file — until the child
// and everything it spawned have closed the write end. The child is a
// separate OS process (StartHostedPTY re-execs this binary), so its
// materialized log path only exists on its side; this pipe is how the path
// the dashboard shows and the file the child writes are the same.
func readLogAnnounce(id string, r *os.File) {
	defer r.Close()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if path := strings.TrimSpace(line); path != "" {
			livework.Default.SetLogPath(id, path)
			break
		}
		if err != nil {
			return
		}
	}
	_, _ = io.Copy(io.Discard, br)
}

// HostedNoFrameEnv marks the hosted pty child's environment so
// internal/frame's Activate declines outright, whatever the dispatched
// Command itself declares — an explicit `footer = true` included.
// internal/frame exists to pin rows to the bottom of a REAL terminal the
// user is looking at; a hosted child's pty is read only by this process's
// own io.Copy (recordHostedPTYWork) and never shown to anyone as a
// terminal, so raising a footer there just repaints the same three facts
// (label, elapsed, log path) into the ring the dashboard already shows as
// the child's own output — the detail pane prints exactly those already.
// ADMIN_NO_FRAME is the same suppression a user sets by hand
// (internal/frame/frame.go's Activate checks it unconditionally, even when
// enterFrame's explicitlyAsked bypasses the CanActivate pre-check for an
// explicit ask), so this is the dashboard asserting that same fact about
// its own hosted child rather than adding a second, parallel flag.
const HostedNoFrameEnv = "ADMIN_NO_FRAME"

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

// ErrHostedKilled is what a hosted child's exit channel yields after its
// entry's Ops.Kill, so a workflow dispatch does not count a killed worker as
// a finished one.
var ErrHostedKilled = errors.New("killed")

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
// The registered live-work entry is keyed on the command name — the thing
// the user ran — so only that command's row shows it, never another command
// that happens to share one of its steps. When the command's first step is
// a genuine multi-target menu (more than one declared target) and args
// names a resolvable target, the key is multiTargetLiveID's
// "<command>:<target>" instead, so the target row resolves its own entry.
func StartHostedPTY(m *manifest.Manifest, name string, args []string, rows, cols int, env []string) error {
	cmd, ok := resolveCommand(m, name)
	if !ok {
		return fmt.Errorf("command %q: not declared", name)
	}
	if len(cmd.Steps) == 0 {
		return fmt.Errorf("command %q has no steps", name)
	}
	liveID := name
	if action, ok := m.Actions[cmd.Steps[0]]; ok {
		liveID = multiTargetLiveID(name, action, args)
	}

	// The child reports its log path on this pipe (fd 3 on its side, named
	// by luahost.LogAnnounceFDEnv) rather than in its output.
	announceR, announceW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("command %q: log announce pipe: %w", name, err)
	}
	c := exec.Command(selfExecutable(), hostedReentryArgs(name, args)...)
	c.ExtraFiles = []*os.File{announceW}
	c.Env = append(hostedChildEnv(), luahost.LogAnnounceFDEnv+"=3")
	c.Env = append(c.Env, env...)
	_, err = startHostedChild(liveID, name, c, rows, cols, announceR, nil)
	// The child holds its own copy of the write end; closing ours is what
	// lets readLogAnnounce see EOF once the child's copies are gone.
	_ = announceW.Close()
	if err != nil {
		_ = announceR.Close()
	}
	return err
}

// StartHostedWorker runs argv, a workflow worker's command line, on a pty the
// dashboard owns, in dir (the target's worktree), and returns as soon as the
// child is spawned. The returned channel receives the child's exit error (nil for a clean exit, ErrHostedKilled
// after a Kill) and is then closed, which is how a workflow
// dispatch learns its concurrency slot is free. The live-work entry is the
// same hosted-pty shape StartHostedPTY records — Via and Worker empty, so the
// dashboard treats it as dying with itself — keyed on id.
func StartHostedWorker(id string, argv []string, dir string, env []string, rows, cols int, worker *livework.WorkerInfo) (<-chan error, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("worker %q: empty command line", id)
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = dir
	c.Env = append(hostedChildEnv(), env...)
	return startHostedChild(id, id, c, rows, cols, nil, worker)
}

// hostedChildEnv is the dashboard's own environment plus the two resolved
// color facts and the no-frame marker every hosted child gets.
func hostedChildEnv() []string {
	return append(os.Environ(),
		HostedColorProfileEnv+"="+strconv.Itoa(int(lipgloss.ColorProfile())),
		HostedDarkBackgroundEnv+"="+strconv.FormatBool(lipgloss.HasDarkBackground()),
		HostedNoFrameEnv+"=1",
	)
}

// startHostedChild starts c on a fresh pty sized rows x cols (80x24 when
// either is <= 0) and hands it to recordHostedPTYWork under liveID. announce
// is the read end of the child's log-path pipe, nil when the child has none.
func startHostedChild(liveID, name string, c *exec.Cmd, rows, cols int, announce *os.File, worker *livework.WorkerInfo) (<-chan error, error) {
	// Setsid (pty.StartWithSize sets this unconditionally on the SysProcAttr
	// it allocates when nil, so pinning it here is behaviour-neutral) makes
	// the child both a session leader and its own process-group leader
	// (pgid == pid) — which is what lets Ops.Kill signal the whole group a
	// dispatched Command spawns (a bundler, a container, a simulator) rather
	// than only this direct child. Do NOT also set Setpgid: true here — Go's
	// forkAndExecInChild runs setsid() before setpgid(0,0), and setpgid on a
	// session leader returns EPERM, which would fail every hosted dispatch.
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if rows <= 0 || cols <= 0 {
		rows, cols = 24, 80
	}
	ptmx, err := pty.StartWithSize(c, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("command %q: starting hosted pty: %w", name, err)
	}
	return recordHostedPTYWork(liveID, name, rows, cols, c, ptmx, announce, worker), nil
}

// recordHostedPTYWork adds a running hosted-pty entry (Via left empty) for
// a successfully-spawned hosted dispatch, and waits for the child to exit
// in a goroutine so the entry can flip to exited/failed the same way
// recordLiveWork's launcher-backed entry does — the only visible
// difference to a caller is whether Via is empty. announce is the read end of
// the child's log-path pipe, nil for a child with none. The returned channel
// receives the child's exit error (ErrHostedKilled after a Kill) once its final state
// is recorded, then closes; when the entry cannot be registered the child is killed and it closes
// after reaping the child.
func recordHostedPTYWork(entryID, name string, rows, cols int, c *exec.Cmd, ptmx, announce *os.File, worker *livework.WorkerInfo) <-chan error {
	exit := make(chan error, 1)
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

	// screen is the child's terminal: it answers the child's terminal
	// queries — bubbletea's package init asks for the background color
	// before main, so no environment variable can pre-empt it, and an
	// unanswered query costs termenv's 5s OSCTimeout on every run. Its
	// background reports the dashboard's own terminal's, light or dark.
	screen := vt.NewSafeEmulator(cols, rows)
	if !lipgloss.HasDarkBackground() {
		screen.SetDefaultBackgroundColor(color.White)
	}
	// replied closes when the reply copy returns. Emulator.Read checks an
	// unlocked closed flag that Close writes, so screen.Close must wait on it.
	replied := make(chan struct{})
	go func() {
		defer close(replied)
		_, _ = io.Copy(ptmx, screen)
	}()

	e := &livework.Entry{
		ID:     entryID,
		Name:   entryID,
		Kind:   "hosted-pty",
		Handle: ptmx.Name(),
		Output: livework.NewRing(0),
		Screen: screen,
		Worker: worker,
	}
	if worker != nil {
		// A worker's own terminal title (claude sets one) is its row's
		// activity line, the same field a window worker's herdr title fills.
		screen.SetCallbacks(vt.Callbacks{Title: func(t string) {
			livework.Default.SetActivity(entryID, t)
		}})
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
			screen.Resize(cols, rows)
			return nil
		},
	}
	if err := livework.Default.Add(e); err != nil {
		log.Printf("recordHostedPTYWork: %v", err)
		if announce != nil {
			_ = announce.Close()
		}
		// Nothing would read the pty or reap the child, so stop it here.
		if err := killProcessGroup(c); err != nil {
			_ = c.Process.Kill()
		}
		_ = ptmx.Close()
		go func() {
			_ = c.Wait()
			exit <- fmt.Errorf("command %q: %w", name, err)
			close(exit)
		}()
		return exit
	}
	if announce != nil {
		go readLogAnnounce(e.ID, announce)
	}

	// Ordering is load-bearing (admin-3rgp.61's Cause A): the pty's output
	// queue only drains when something reads the master, and on macOS the
	// child's tty-close at exit blocks in the kernel until that drain
	// happens — so the copy must run, and reach EOF/EIO, BEFORE c.Wait() is
	// called, and ptmx must not be closed until after that. Closing ptmx
	// early truncates the child's tail output; never draining at all is the
	// exact hang that failed Verify.
	go func() {
		defer close(exit)
		_, copyErr := io.Copy(io.MultiWriter(e.Output, screen), ptmx)
		// Closing the reply pipe's write end ends the reply copy's read with
		// EOF without touching the emulator's closed flag; only once that copy
		// has returned is Close safe. The grid stays readable after Close.
		_ = screen.InputPipe().(io.Closer).Close()
		<-replied
		_ = screen.Close()
		// On Linux the master read returns EIO (not io.EOF) once the last
		// slave closes — that is normal end-of-output, not a failure, so it
		// must not be surfaced as a wait error.
		_ = copyErr

		waitErr := c.Wait()
		closed.Store(true)
		_ = ptmx.Close()
		exited.Store(true)
		if killed.Load() {
			exit <- ErrHostedKilled
			return
		}
		if waitErr != nil {
			livework.Default.SetState(e.ID, livework.StateFailed, waitErr)
		} else {
			livework.Default.SetState(e.ID, livework.StateExited, nil)
		}
		exit <- waitErr
	}()
	return exit
}
