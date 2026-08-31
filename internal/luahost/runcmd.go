package luahost

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// RunCmdOptions mirrors the handful of admin_lib/core.py run_cmd knobs a Lua
// body actually uses (measured in ADR-0010: 4 call sites, none passing a pty
// or collect callback). Left extensible rather than exhaustive — the
// process-supervision half of run_cmd (threading.Lock + Popen + poll/wait,
// used by exactly one body) is explicitly out of this ticket's scope; it
// belongs in admin_lib/reload.go's Go port (per the repo's OutputPump rule),
// not reimplemented here.
type RunCmdOptions struct {
	// Dir overrides the command's working directory. Empty means
	// Host.ProjectRoot.
	Dir string
	// CaptureLog mirrors Python's capture_log=True default: when true (the
	// default), each line the child prints is also appended to the active
	// log file, same as info/ok/warn/err already do.
	CaptureLog bool
}

// RunCmdResult is what a Lua run_cmd(...) call returns: whether the command
// exited zero, and its combined stdout+stderr.
type RunCmdResult struct {
	Ok     bool
	Output string
}

// RunCmd runs cmd through /bin/sh -c, streaming each line to Host.Stdout (and,
// unless opts.CaptureLog is false, to the active log file) as it arrives —
// not buffered until exit — while also collecting the combined output to
// return. This is the in-process replacement ADR-0010 calls for: no
// subprocess-of-a-subprocess wire protocol, just Go's os/exec directly from
// the host function a Lua body calls.
// Host function: run_cmd(cmd[, {dir=path, capture_log=bool}]).
func (h *Host) RunCmd(cmd string, opts RunCmdOptions) (RunCmdResult, error) {
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Dir = opts.Dir
	if c.Dir == "" {
		c.Dir = h.ProjectRoot
	}
	c.Env = os.Environ()

	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	var collected []byte
	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			collected = append(collected, line...)
			collected = append(collected, '\n')
			fmt.Fprintln(h.Stdout, line)
			if opts.CaptureLog {
				h.logWrite(line)
			}
		}
		done <- scanner.Err()
	}()

	startErr := c.Start()
	if startErr != nil {
		pw.Close()
		<-done
		return RunCmdResult{Ok: false, Output: ""}, startErr
	}
	waitErr := c.Wait()
	pw.Close()
	<-done

	return RunCmdResult{Ok: waitErr == nil, Output: string(collected)}, nil
}
