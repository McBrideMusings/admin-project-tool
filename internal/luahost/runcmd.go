package luahost

import (
	"bufio"
	"io"
	"os"
	"os/exec"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/streams"
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
	// Every string reaching a shell is expanded here and nowhere else, matching
	// admin_lib/core.py::run_cmd ("Strings are passed through resolve_env()
	// first"). A caller that resolves first is unaffected — a resolved string
	// has no "${" left to expand — but a caller that forgets no longer ships a
	// literal ${VAR} to /bin/sh, which expands an unset name to the empty
	// string and runs anyway: that is how `ssh ${UNRAID_USER:-root}@${UNRAID_HOST}`
	// became `ssh root@` and reported "Could not resolve hostname" instead of a
	// missing-variable error.
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

	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	var collected []byte
	pump := &streams.OutputPump{
		Out: func(s string) { frame.Write(h.Stdout, s) },
	}
	if opts.CaptureLog {
		pump.LogFn = h.logWrite
	}

	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text() + "\n"
			collected = append(collected, line...)
			pump.Write(line)
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
