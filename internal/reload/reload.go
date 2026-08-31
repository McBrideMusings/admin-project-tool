// Package reload runs a long-lived dev process (a backend server, a
// bundler, an unpackaged Electron shell) with SIGUSR1-triggered restart —
// the Go port of admin_lib/reload.py's run_with_reload, and per this repo's
// CLAUDE.md ("Reload state lives only in admin_lib/reload.py") the single
// place this state and this loop live in the Go runtime too.
//
// Every byte a child prints reaches the caller through
// internal/streams.OutputPump — the repo's single output-pump rule applies
// here exactly as it does to internal/luahost.Host.RunCmd; Options.Out and
// Options.LogFn are wired to frame.Write and the active log file by the
// kind factory that calls RunWithReload (internal/kinds/vitedev.go), not by
// this package, mirroring how runcmd.go itself wires frame.Write rather
// than this package importing internal/luahost (which would cycle: a future
// Lua binding of "run with reload" would need to import this package).
//
// Not ported: admin_lib/reload.py's raw-terminal 'r'-keypress reload
// (termios/tty — no stdlib equivalent, and the repo's dependency list has no
// terminal-raw-mode package pulled in for it yet). SIGUSR1 — the same signal
// `admin logs`/an external `kill -USR1` sends — is the fully-ported trigger.
// Tracked as a follow-up; not a silent gap.
package reload

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/streams"
)

// maxFastExitAttempts mirrors admin_lib/reload.py's _MAX_FAST_EXIT_ATTEMPTS.
const maxFastExitAttempts = 3

// fastExitWindow mirrors the "elapsed < 2.0" fast-exit threshold in
// run_with_reload.
const fastExitWindow = 2 * time.Second

// Options configures RunWithReload. Out/LogFn/Info/Warn are the same shape
// as internal/luahost.Host's own output surface, passed as plain functions
// rather than a *Host to keep this package import-cycle-free (see the
// package doc comment).
type Options struct {
	// Out receives each chunk of the child's combined stdout+stderr, in the
	// order it arrived — the caller wires this to frame.Write(stdout, ...).
	Out func(text string)
	// LogFn, if set, receives the same chunks for appending to the active
	// log file.
	LogFn func(text string)
	// Info/Warn print a status line the way admin_lib's info()/warn() do.
	// Both required; a nil value panics on first call rather than silently
	// dropping status lines a user relies on to know a restart happened.
	Info func(msg string)
	Warn func(msg string)
}

// RunWithReload runs cmd (via `/bin/sh -c`) as a long-lived process. On
// SIGUSR1 it tears the child down (SIGTERM, escalating to SIGKILL after 5s)
// and relaunches it; SIGINT/SIGTERM tear it down and return (130 for
// SIGINT, matching Python's "Stopped." path; the child's own exit code
// otherwise). A process that exits within fastExitWindow of launch is
// retried after a 1s pause, up to maxFastExitAttempts total launches, then
// its exit code is returned as-is.
//
// This is the Go port of admin_lib/reload.py::run_with_reload, minus PID
// files (no other admin invocation reads them in the Go runtime — `admin
// logs`/`kill` finding a running dev loop through a pidfile is
// admin-3rgp.15/.18 territory) and the keypress-reload half (see the
// package doc comment).
func RunWithReload(cmd, target string, opts Options) int {
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	fastExitCount := 0

	for {
		launch := time.Now()
		opts.Info("Starting " + target + "...")

		c := exec.Command("/bin/sh", "-c", cmd)
		c.Stdin = nil
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		pr, pw := io.Pipe()
		c.Stdout = pw
		c.Stderr = pw

		pump := &streams.OutputPump{Out: opts.Out, LogFn: opts.LogFn}
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			buf := make([]byte, 64*1024)
			for {
				n, err := pr.Read(buf)
				if n > 0 {
					pump.Write(string(buf[:n]))
				}
				if err != nil {
					return
				}
			}
		}()

		if err := c.Start(); err != nil {
			pw.Close()
			<-readDone
			opts.Warn("could not start " + target + ": " + err.Error())
			return 1
		}

		waitCh := make(chan error, 1)
		go func() { waitCh <- c.Wait() }()

		reloadRequested := false

	inner:
		for {
			select {
			case sig := <-sigCh:
				switch sig {
				case syscall.SIGUSR1:
					reloadRequested = true
					opts.Info("Reload requested — restarting...")
					killProcessGroup(c, syscall.SIGTERM)
					waitOrKill(c, waitCh, 5*time.Second)
					break inner
				case syscall.SIGINT:
					killProcessGroup(c, syscall.SIGINT)
					<-waitCh
					pw.Close()
					<-readDone
					pump.Flush()
					opts.Info("Stopped.")
					return 130
				case syscall.SIGTERM:
					killProcessGroup(c, syscall.SIGTERM)
					<-waitCh
					pw.Close()
					<-readDone
					pump.Flush()
					return 0
				}
			case <-waitCh:
				break inner
			}
		}

		pw.Close()
		<-readDone
		pump.Flush()

		if reloadRequested {
			fastExitCount = 0
			continue
		}

		elapsed := time.Since(launch)
		rc := exitCode(c)
		if elapsed < fastExitWindow {
			fastExitCount++
			exited := "Process exited in " + elapsed.Round(100*time.Millisecond).String() + " (code " + strconv.Itoa(rc) + ")"
			if fastExitCount < maxFastExitAttempts {
				opts.Warn(exited + " — retrying (" + strconv.Itoa(fastExitCount) + "/" + strconv.Itoa(maxFastExitAttempts) + ")...")
				time.Sleep(1 * time.Second)
				continue
			}
			opts.Warn(exited + " — giving up after " + strconv.Itoa(maxFastExitAttempts) + " attempts.")
		}
		return rc
	}
}

// killProcessGroup signals the child's whole process group (Setpgid above
// makes the child its own group leader), matching Python's
// os.killpg(os.getpgid(proc.pid), sig) — a supervisor (nodemon, cargo-watch)
// forks further children that a plain proc.Kill() would leave orphaned.
func killProcessGroup(c *exec.Cmd, sig syscall.Signal) {
	if c.Process == nil {
		return
	}
	if err := syscall.Kill(-c.Process.Pid, sig); err != nil {
		_ = c.Process.Signal(sig)
	}
}

// waitOrKill waits for the child to exit, escalating to SIGKILL on the
// process group if it ignores the signal already sent — the Go port of
// admin_lib/reload.py::_wait_or_kill.
func waitOrKill(c *exec.Cmd, waitCh <-chan error, timeout time.Duration) {
	select {
	case <-waitCh:
	case <-time.After(timeout):
		killProcessGroup(c, syscall.SIGKILL)
		<-waitCh
	}
}

// exitCode extracts the child's exit code the way admin_lib/reload.py reads
// proc.returncode — 0 when Wait reported no error (already consumed by the
// caller before this is called), otherwise the process's real exit status,
// falling back to 1 for a non-ExitError failure (e.g. exec itself failed
// after Start succeeded, which practically never happens for /bin/sh -c).
func exitCode(c *exec.Cmd) int {
	if c.ProcessState == nil {
		return 1
	}
	return c.ProcessState.ExitCode()
}
