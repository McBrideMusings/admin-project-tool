package luahost

import (
	"io"
	"os"
	"os/exec"
	"syscall"

	xterm "github.com/charmbracelet/x/term"
	"github.com/creack/pty"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/streams"
)

// RunCmdPTY is RunCmd's pty=true sibling: cmd runs on a real pseudo-terminal
// (github.com/creack/pty, already a dependency — see
// internal/kinds/hostedpty.go for the pattern this borrows) instead of a
// pipe, so a child that prompts on stdin (grafana.ts's
// process.stdin.once('data')) sees a real TTY and gets a whole typed line in
// one read, exactly as it would running directly in a terminal — RunCmd's
// own pipe delivers only whatever chunk the writer happened to flush, which
// for an interactive shell is one keypress at a time.
//
// The pty's slave side is left exactly as a freshly allocated pty starts:
// canonical input, echo on. Line assembly and local echo are the CHILD's
// tty driver's job, never admin's — the only reason admin's own terminal
// (when h.Stdin is one) goes into raw mode here is to stop admin's tty
// driver from doing a second, competing round of line editing on top of
// the child's; two canonical readers stacked on the same keystrokes is what
// echoes text twice and eats the first character of the next line.
//
// Before that handover, drainPendingInput discards whatever the kernel has
// already queued on h.Stdin — the same defensive drain
// internal/prompt.Confirm does around its own single-keypress read, and for
// the same reason: a byte typed ahead of the prompt (impatiently, or left
// behind by an earlier raw-mode reader) would otherwise reach the child
// before the user's real answer does, and a `once('data')` handler fires on
// that first byte alone.
func (h *Host) RunCmdPTY(cmd string, opts RunCmdOptions) (RunCmdResult, error) {
	cmd, err := manifest.ResolveEnv(cmd)
	if err != nil {
		return RunCmdResult{}, err
	}
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Dir = opts.Dir
	if c.Dir == "" {
		c.Dir = h.ProjectRoot
	}
	c.Env = os.Environ()

	ptmx, err := pty.Start(c)
	if err != nil {
		return RunCmdResult{}, err
	}
	defer func() { _ = ptmx.Close() }()

	if stdinFile, ok := h.Stdin.(*os.File); ok && xterm.IsTerminal(stdinFile.Fd()) {
		if width, height, sizeErr := xterm.GetSize(stdinFile.Fd()); sizeErr == nil {
			frame.SetWinsize(ptmx.Fd(), height, width)
		}
		drainPendingInput(stdinFile)
		if state, rawErr := xterm.MakeRaw(stdinFile.Fd()); rawErr == nil {
			defer func() { _ = xterm.Restore(stdinFile.Fd(), state) }()
		}
	}

	// Forwarding host input into the child's pty master is a Go-level copy
	// with no natural end while the child is alive, and nothing here waits
	// on it once the child exits either — same as internal/reload's
	// startKeyReader, this goroutine simply leaks blocked in Read until the
	// process exits, which is fine because the process exits right after.
	if h.Stdin != nil {
		go func() { _, _ = io.Copy(ptmx, h.Stdin) }()
	}

	var collected []byte
	pump := &streams.OutputPump{
		Out: func(s string) { frame.Write(h.Stdout, s) },
	}
	if opts.CaptureLog {
		pump.LogFn = h.logWrite
	}

	buf := make([]byte, 32*1024)
	for {
		n, readErr := ptmx.Read(buf)
		if n > 0 {
			collected = append(collected, buf[:n]...)
			pump.Write(string(buf[:n]))
		}
		if readErr != nil {
			// EOF once the child's slave closes on Linux; macOS instead
			// returns EIO for the same condition (hostedpty.go's Cause A)
			// — either way the child is simply done writing, not failed.
			break
		}
	}

	waitErr := c.Wait()
	return RunCmdResult{Ok: waitErr == nil, Output: string(collected)}, nil
}

// drainPendingInput discards whatever the kernel has already queued on f
// without blocking — the same SetNonblock-then-read technique
// internal/prompt.Confirm uses around its own raw-mode read, generalised to
// drain until the queue is actually empty (Confirm only needs to eat one
// stray byte after a single keypress; a full pty handover needs the queue
// clean before it starts forwarding a real answer).
func drainPendingInput(f *os.File) {
	fd := int(f.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return
	}
	defer syscall.SetNonblock(fd, false)
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n <= 0 || err != nil {
			return
		}
	}
}
