package luahost

import (
	"io"
	"os"
	"syscall"

	xterm "github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/streams"
)

// RunCmdPTY is RunCmd's pty=true sibling: cmd runs on a real pseudo-terminal
// (github.com/creack/pty, already a dependency — internal/kinds/hostedpty.go
// is the pattern this borrows) instead of a pipe, so a child that prompts on
// stdin (grafana.ts's process.stdin.once('data')) sees a real TTY and gets a
// whole typed line in one read, exactly as it would running directly in a
// terminal. RunCmd's pipe instead delivers whatever chunk the writer happened
// to flush, which for an interactive shell is one keypress at a time.
//
// The pty's slave side is left exactly as a freshly allocated pty starts:
// canonical input, echo on. Line assembly and local echo are the CHILD's tty
// driver's job, never admin's — the only reason admin's own terminal (when
// h.Stdin is one) goes into raw mode here is to stop admin's tty driver from
// running a second, competing round of line editing on top of the child's;
// two canonical readers stacked on the same keystrokes is what echoes text
// twice and eats the first character of the next line.
//
// Before that handover, drainPendingInput discards whatever the kernel has
// already queued on h.Stdin — the same defensive drain internal/prompt.Confirm
// does around its own single-keypress read, and for the same reason: a byte
// typed ahead of the prompt (impatiently, or left behind by an earlier
// raw-mode reader) would otherwise reach the child before the user's real
// answer does, and a once('data') handler fires on that first byte alone.
func (h *Host) RunCmdPTY(cmd string, opts RunCmdOptions) (RunCmdResult, error) {
	c, err := h.shellCommand(cmd, opts)
	if err != nil {
		return RunCmdResult{}, err
	}

	ptmx, err := pty.Start(c)
	if err != nil {
		return RunCmdResult{}, err
	}
	defer func() { _ = ptmx.Close() }()

	if stdinFile, ok := h.Stdin.(*os.File); ok {
		if xterm.IsTerminal(stdinFile.Fd()) {
			if width, height, sizeErr := xterm.GetSize(stdinFile.Fd()); sizeErr == nil {
				frame.SetWinsize(ptmx.Fd(), height, width)
			}
			drainPendingInput(stdinFile)
			if state, rawErr := xterm.MakeRaw(stdinFile.Fd()); rawErr == nil {
				defer func() { _ = xterm.Restore(stdinFile.Fd(), state) }()
			}
		}
		// forwardStdin is cancelable — stop closes and the returned wait
		// blocks until the forwarding goroutine has actually stopped
		// reading h.Stdin, not merely "will stop on its next Write to the
		// now-closed ptmx". A plain `go io.Copy(ptmx, h.Stdin)` stays
		// blocked in Read until the next byte arrives, and h.Stdin is the
		// process's real os.Stdin (luahost.New's default) shared by every
		// action — a second pty=true step later in the same command's step
		// sequence would then race a leaked reader from THIS step for the
		// first bytes of its own prompt's answer, the exact class of bug
		// drainPendingInput exists to prevent, just moved one step later.
		stop, wait := forwardStdin(ptmx, stdinFile)
		defer func() { stop(); wait() }()
	} else if h.Stdin != nil {
		// Not a real fd (a test double, a Lua-supplied io.Reader) — no
		// select(2) target to cancel, but such a reader reaches EOF on its
		// own rather than blocking on a live terminal forever, so the
		// leaked-goroutine risk above does not apply here.
		go func() { _, _ = io.Copy(ptmx, h.Stdin) }()
	}

	pump := h.newOutputPump(opts)
	var collected []byte
	buf := make([]byte, 32*1024)
	for {
		n, readErr := ptmx.Read(buf)
		if n > 0 {
			collected = append(collected, buf[:n]...)
			pump.Write(string(buf[:n]))
		}
		// EOF once the child's slave closes on Linux; macOS instead returns
		// EIO for the same condition (hostedpty.go's Cause A) — either way the
		// child is simply done writing, not failed.
		if readErr != nil {
			break
		}
	}

	waitErr := c.Wait()
	return RunCmdResult{Ok: waitErr == nil, Output: string(collected)}, nil
}

// forwardStdin copies src into dst until the returned stop func is called,
// then wait blocks until the copying goroutine has actually exited — not
// merely until its next write fails. It polls src with select(2) on a short
// timeout rather than blocking in Read, which is what makes the stop
// cancelable at all: a blocking Read on a live terminal fd only returns on
// the next keystroke, by which point the caller has moved on. select(2) is
// used instead of File.SetReadDeadline because a controlling terminal's fd
// does not reliably support read deadlines on every platform this tool
// ships for, while select does.
func forwardStdin(dst io.Writer, src *os.File) (stop func(), wait func()) {
	fd := int(src.Fd())
	done := make(chan struct{})
	stopCh := make(chan struct{})

	if err := syscall.SetNonblock(fd, true); err != nil {
		// Nonblocking reads aren't available on this fd — fall back to the
		// same uncancelable copy this function replaces rather than not
		// forwarding stdin at all. wait is a no-op here, not <-done: done
		// only closes once Read returns, which on a live terminal fd is
		// "on the next keystroke", so blocking wait() on it would hang
		// RunCmdPTY's own defer indefinitely instead of merely leaking a
		// goroutine the way the pre-cancelable version already did.
		go func() { _, _ = io.Copy(dst, src) }()
		return func() {}, func() {}
	}

	go func() {
		defer close(done)
		defer syscall.SetNonblock(fd, false)
		buf := make([]byte, 32*1024)
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			var rfds unix.FdSet
			rfds.Zero()
			rfds.Set(fd)
			tv := unix.Timeval{Sec: 0, Usec: 100_000}
			n, err := unix.Select(fd+1, &rfds, nil, nil, &tv)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				return
			}
			if n == 0 || !rfds.IsSet(fd) {
				continue
			}
			// A raw syscall.Read, not src.Read: os.File.Read layers its own
			// pollability tracking on top of the fd, decided when the File
			// was created — reading through the raw fd instead keeps this
			// loop's idea of "nonblocking" (set by hand, above) the one
			// thing that is actually true of the fd.
			nr, readErr := syscall.Read(fd, buf)
			if nr > 0 {
				if _, writeErr := dst.Write(buf[:nr]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				if readErr == syscall.EAGAIN {
					continue
				}
				return
			}
		}
	}()

	return func() { close(stopCh) }, func() { <-done }
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
		if n, err := f.Read(buf); n <= 0 || err != nil {
			return
		}
	}
}

// newOutputPump builds the pump both RunCmd and RunCmdPTY tee a child's
// output through: to Host.Stdout via frame.Write, and to the active log file
// when opts.CaptureLog is set. It is the only place either path constructs
// one, which is what keeps internal/streams.OutputPump the single owner of
// the read-a-chunk/format/write loop (the repo's OutputPump rule).
func (h *Host) newOutputPump(opts RunCmdOptions) *streams.OutputPump {
	pump := &streams.OutputPump{
		Out: func(s string) { frame.Write(h.Stdout, s) },
	}
	if opts.CaptureLog {
		pump.LogFn = h.logWrite
	}
	return pump
}
