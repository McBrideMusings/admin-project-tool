package luahost

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/streams"
)

// GroupCmd is one member of a run_group call — a tagged shell command run
// concurrently with its siblings.
type GroupCmd struct {
	Tag string
	Cmd string
}

// RunGroup runs every entry in cmds concurrently through /bin/sh -c,
// prefixing each line of output with "[tag] " (matching
// admin_lib/forge_engine's workspace-mode dev loop's own "[" + tag + "] "
// convention) as it streams to Host.Stdout and the active log. This is the
// Go port of the ONE archetype body ADR-0010 names as not becoming Lua: the
// forge-engine `dev` workspace path's threading.Lock + subprocess.Popen +
// poll/wait supervision. Per this repo's CLAUDE.md ("reload state lives only
// in admin_lib/reload.py; the output pump only in streams.OutputPump"), the
// composition here is exactly those two pieces — internal/streams.OutputPump
// per child (this file's only reason to exist: giving N children their own
// tagged pump feeding one shared, mutex-serialized writer) plus this
// package's own os/exec — not a third hand-rolled pump.
//
// On SIGINT: cmds already share this process's terminal foreground process
// group (no Setpgid), so the terminal delivers Ctrl+C's SIGINT to every
// child directly, same as the Python original's Popen children did. This
// function additionally intercepts SIGINT itself (so the Go runtime's
// default "terminate immediately" doesn't race the children's own signal
// handling) and, mirroring the original's belt-and-suspenders cleanup,
// explicitly SIGTERMs any still-running child, gives them 5s, then SIGKILLs
// stragglers — for a child that caught SIGINT but hung, or ignored it.
//
// Returns the highest exit code across all children (0 if every child
// exited 0), matching the original's sys.exit(max(rcs)).
func (h *Host) RunGroup(cmds []GroupCmd, dir string) (int, error) {
	if dir == "" {
		dir = h.ProjectRoot
	}
	if len(cmds) == 0 {
		return 0, nil
	}

	var writeMu sync.Mutex
	write := func(s string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		frame.Write(h.Stdout, s)
	}

	type running struct {
		cmd  *exec.Cmd
		done chan struct{}
		code int
	}
	procs := make([]*running, len(cmds))

	for i, c := range cmds {
		cmd := exec.Command("/bin/sh", "-c", c.Cmd)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw

		tag := c.Tag
		pump := &streams.OutputPump{
			Out:       write,
			Formatter: func(line string) string { return "[" + tag + "] " + line },
			LogFn:     h.logWrite,
		}

		r := &running{cmd: cmd, done: make(chan struct{})}
		procs[i] = r

		if err := cmd.Start(); err != nil {
			h.Err("could not start " + tag + ": " + err.Error())
			pw.Close()
			close(r.done)
			r.code = 1
			continue
		}

		go func(pr *io.PipeReader, pump *streams.OutputPump, r *running) {
			scanner := bufio.NewScanner(pr)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				pump.Write(scanner.Text() + "\n")
			}
			pump.Flush()
		}(pr, pump, r)

		go func(cmd *exec.Cmd, pw *io.PipeWriter, r *running) {
			waitErr := cmd.Wait()
			pw.Close()
			if waitErr != nil {
				if exitErr, ok := waitErr.(*exec.ExitError); ok {
					r.code = exitErr.ExitCode()
				} else {
					r.code = 1
				}
			}
			close(r.done)
		}(cmd, pw, r)
	}

	allDone := make(chan struct{})
	go func() {
		for _, r := range procs {
			<-r.done
		}
		close(allDone)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	select {
	case <-allDone:
	case <-sigCh:
		h.Warn("interrupt — terminating workspace projects")
		for _, r := range procs {
			select {
			case <-r.done:
			default:
				if r.cmd.Process != nil {
					_ = r.cmd.Process.Signal(os.Interrupt)
				}
			}
		}
		select {
		case <-allDone:
		case <-time.After(5 * time.Second):
			for _, r := range procs {
				select {
				case <-r.done:
				default:
					h.Warn("child ignored termination — killing")
					if r.cmd.Process != nil {
						_ = r.cmd.Process.Kill()
					}
				}
			}
			<-allDone
		}
	}

	maxCode := 0
	for _, r := range procs {
		if r.code > maxCode {
			maxCode = r.code
		}
	}
	return maxCode, nil
}
