package luahost

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/supervise"
	"github.com/McBrideMusings/admin-project-tool/internal/timing"
)

// RunCmdOptions covers the handful of run_cmd knobs a Lua body actually
// uses (measured in ADR-0010: 4 call sites, none passing a pty or collect
// callback). Left extensible rather than exhaustive — process supervision
// (a locked, long-running child with poll/wait) is explicitly out of this
// ticket's scope; it belongs in internal/reload (per the repo's OutputPump
// rule), not reimplemented here.
type RunCmdOptions struct {
	// Dir overrides the command's working directory. Empty means
	// Host.ProjectRoot.
	Dir string
	// CaptureLog defaults to true: when true (the default), each line the
	// child prints is also appended to the active log file, same as
	// info/ok/warn/err already do.
	CaptureLog bool
	// Unsupervised is the action's `supervise = false`: the child is not
	// tagged and no watcher sweeps it.
	Unsupervised bool
}

// RunCmdResult is what a Lua run_cmd(...) call returns: whether the command
// exited zero, and its combined stdout+stderr.
type RunCmdResult struct {
	Ok     bool
	Output string
	// ExitCode is 0 when Ok is true, the child's exit status when Ok is
	// false and the process ran to completion, and -1 when it was killed by
	// a signal or never started. Not part of the Lua run_cmd(...) return
	// shape — a Go caller reads it directly off the struct.
	ExitCode int
}

// shellCommand builds the /bin/sh -c child both RunCmd and RunCmdPTY run, so
// the working directory and environment a command sees never depend on which
// of the two it went through. Stdin, stdout and stderr are left unset — how
// the child is wired to the terminal is exactly what differs between them.
//
// Every string reaching a shell is expanded here and nowhere else — strings
// are passed through ResolveEnv first. A caller that resolves first is
// unaffected — a resolved string has no "${" left to expand — but a caller
// that forgets no longer ships a
// literal ${VAR} to /bin/sh, which expands an unset name to the empty
// string and runs anyway: that is how `ssh ${UNRAID_USER:-root}@${UNRAID_HOST}`
// became `ssh root@` and reported "Could not resolve hostname" instead of a
// missing-variable error.
func (h *Host) shellCommand(cmd string, opts RunCmdOptions) (*exec.Cmd, error) {
	expanded, err := manifest.ResolveEnv(cmd)
	if err != nil {
		return nil, err
	}
	c := exec.Command("/bin/sh", "-c", expanded)
	c.Dir = opts.Dir
	if c.Dir == "" {
		c.Dir = h.ProjectRoot
	}
	c.Env = os.Environ()
	supervise.Start(c, h.ProjectRoot, !opts.Unsupervised)
	return c, nil
}

// RunCmd runs cmd through /bin/sh -c, streaming each line to Host.Stdout (and,
// unless opts.CaptureLog is false, to the active log file) as it arrives —
// not buffered until exit — while also collecting the combined output to
// return. This is the in-process replacement ADR-0010 calls for: no
// subprocess-of-a-subprocess wire protocol, just Go's os/exec directly from
// the host function a Lua body calls.
//
// Every line reaches the terminal through internal/streams.OutputPump,
// which is in turn the only thing that calls internal/frame.Write — the
// repo's "the output pump exists in exactly one place" rule (this ticket,
// admin-3rgp.11). h.Stdout stays the actual destination (frame.Write writes
// to whatever writer it's given and only repaints a footer if one is
// active on that same writer), so a test that redirects h.Stdout still sees
// exactly the bytes this used to write directly.
// Host function: run_cmd(cmd[, {dir=path, capture_log=bool}]).
func (h *Host) RunCmd(cmd string, opts RunCmdOptions) (RunCmdResult, error) {
	c, err := h.shellCommand(cmd, opts)
	if err != nil {
		return RunCmdResult{}, err
	}

	// os/exec gives a nil Stdin the NULL DEVICE, not the parent's stdin, so
	// leaving it unset made every action's child read instant EOF. Anything that
	// asks a question got an empty answer and failed in milliseconds — `admin
	// grafana deploy` read "" where it wanted the word "deploy", so it aborted
	// and looked like a hang. Assigning the *os.File directly hands the child
	// the real fd, which is what keeps a terminal a terminal; a non-file reader
	// (tests) falls back to exec's copying pipe.
	if f, ok := h.Stdin.(*os.File); ok {
		c.Stdin = f
	} else if h.Stdin != nil {
		c.Stdin = h.Stdin
	}

	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	var collected []byte
	pump := h.newOutputPump(opts)

	// Read raw chunks rather than scanning whole lines. A prompt has no trailing
	// newline, so a line scanner holds it until the child writes one — which it
	// never does, because it is blocked waiting for the answer to the prompt it
	// just wrote. The result is a blank terminal that looks like a hang. Chunks
	// go to a channel so the consumer can notice a gap in output and show
	// whatever partial line is pending.
	chunks := make(chan []byte, 16)
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 32*1024)
		firstByte := true
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				if firstByte {
					timing.MarkEach("first byte from child")
					firstByte = false
				}
				b := make([]byte, n)
				copy(b, buf[:n])
				chunks <- b
			}
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				readErr <- err
				close(chunks)
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() {
		var pending []byte
		// Long enough that a child mid-write is not chopped, short enough that a
		// prompt appears as fast as a person can react to it.
		const idle = 120 * time.Millisecond
		timer := time.NewTimer(idle)
		defer timer.Stop()
		flushPending := func() {
			if len(pending) > 0 {
				pump.Write(string(pending))
				pending = nil
			}
		}
		for {
			select {
			case b, ok := <-chunks:
				if !ok {
					flushPending()
					done <- <-readErr
					return
				}
				collected = append(collected, b...)
				pending = append(pending, b...)
				// Emit every complete line immediately; hold only the tail.
				if i := bytes.LastIndexByte(pending, '\n'); i >= 0 {
					pump.Write(string(pending[:i+1]))
					pending = pending[i+1:]
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			case <-timer.C:
				flushPending()
				timer.Reset(idle)
			}
		}
	}()

	startErr := c.Start()
	if startErr != nil {
		pw.Close()
		<-done
		return RunCmdResult{Ok: false, Output: "", ExitCode: -1}, startErr
	}
	waitErr := c.Wait()
	pw.Close()
	<-done

	exitCode := 0
	if waitErr != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}

	return RunCmdResult{Ok: waitErr == nil, Output: string(collected), ExitCode: exitCode}, nil
}
