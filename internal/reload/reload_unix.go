//go:build !windows

package reload

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	xterm "github.com/charmbracelet/x/term"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/streams"
)

// rebuildRequested is the ONE rebuild flag SIGUSR1 sets — shared by both
// shapes this package exposes (the caller-owned loop via SetupReload, and
// RunWithReload's own loop, which reads/clears it directly rather than
// keeping a second copy of the same bool).
var rebuildRequested atomic.Bool

// IsRebuildRequested reports whether a rebuild has been requested via
// SIGUSR1 or (when a caller is polling stdin itself) an 'r' keypress.
func IsRebuildRequested() bool { return rebuildRequested.Load() }

// ClearRebuildRequest resets the rebuild flag.
func ClearRebuildRequest() { rebuildRequested.Store(false) }

// SetupReload registers a SIGUSR1 handler that sets the shared rebuild flag,
// and writes pidFile. For the CALLER-OWNED loop shape (a build-install-launch
// cycle that re-runs itself, not a single child process this package
// restarts) — admin-3rgp.12's Apple dev loops are the intended adopter. The
// returned cleanup func stops the signal watch and removes pidFile; call it
// in a defer.
func SetupReload(pidFile string) (cleanup func(), err error) {
	if err := WritePIDFile(pidFile); err != nil {
		return func() {}, err
	}
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1)
	stopped := make(chan struct{})
	go func() {
		for range ch {
			rebuildRequested.Store(true)
		}
		close(stopped)
	}()
	cleanup = func() {
		signal.Stop(ch)
		close(ch)
		<-stopped
		CleanupPIDFile(pidFile)
	}
	return cleanup, nil
}

// startKeyReader puts fd into cbreak mode (canonical mode and echo off,
// signal generation left on — the same tty.setcbreak Python's poll_for_reload
// and run_with_reload both use) and streams single bytes read from it on the
// returned channel. restore undoes the terminal mode change; it is always
// safe to call, including when cbreak mode was never entered (stdin not a
// tty, or the ioctl failed) — restore is then a no-op. The reader goroutine exits
// (closing the channel) once the fd hits EOF or an error, which happens on
// Restore since that unblocks the pending Read with the terminal back in
// canonical mode... in practice the goroutine simply leaks blocked in Read
// until the process exits, exactly as Python's raw termios thread does
// nothing to unblock its own blocking read either; both are fine because the
// process is exiting right after restore in every caller of this function.
func startKeyReader() (keys <-chan byte, restore func()) {
	fd := os.Stdin.Fd()
	if !xterm.IsTerminal(fd) {
		return nil, func() {}
	}
	state, err := makeCbreak(fd)
	if err != nil {
		return nil, func() {}
	}
	ch := make(chan byte, 8)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				select {
				case ch <- buf[0]:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ch, func() { _ = restoreTerm(fd, state) }
}

// PollForReload blocks until a reload is requested (SIGUSR1, or 'r'/'R' on a
// terminal stdin) or isRunning returns false, checking isRunning at least
// once per timeout. Returns true for "reload requested", false for "the
// watched process is gone". The Go port of admin_lib/reload.py's
// poll_for_reload, for the caller-owned loop shape.
func PollForReload(isRunning func() bool, timeout time.Duration) bool {
	keys, restore := startKeyReader()
	defer restore()

	ticker := time.NewTicker(timeout)
	defer ticker.Stop()
	for {
		if rebuildRequested.Load() {
			return true
		}
		if !isRunning() {
			return false
		}
		select {
		case b, ok := <-keys:
			if ok && (b == 'r' || b == 'R') {
				return true
			}
		case <-ticker.C:
		}
	}
}

// Options configures RunWithReload's output wiring. Every field is optional;
// a nil Info/Warn is simply not called.
type Options struct {
	// Stdout is where the child's output (and Info/Warn banners, via the
	// caller) is written. Left nil, RunWithReload writes nowhere.
	Stdout io.Writer
	// Formatter, when set, is applied to each complete line before it
	// reaches Stdout — internal/streams.OutputPump's own contract.
	Formatter func(lineWithNewline string) string
	// LogFn, when set, receives the child's raw output for appending to the
	// active log file (typically (*luahost.Host).AppendLog).
	LogFn func(text string)
	Info  func(msg string)
	Warn  func(msg string)
}

const maxFastExitAttempts = 3

// RunWithReload runs cmd through /bin/sh -c in its own process group,
// streaming its output through internal/streams.OutputPump, until it exits
// on its own, is torn down by SIGINT/SIGTERM, or is restarted by a reload
// request (SIGUSR1, or an 'r'/'R' keypress when stdin is a terminal). A
// child that exits within 2s of launch is relaunched after a 1s pause, up to
// maxFastExitAttempts total launches, then the loop gives up and returns
// that exit code — the Go port of admin_lib/reload.py's run_with_reload.
//
// Returns the child's exit code, or 130 on a user-initiated Ctrl-C/SIGINT
// (matching the shell convention Python's own KeyboardInterrupt branch
// returns).
func RunWithReload(cmd, target string, opts Options) int {
	pidFile := fmt.Sprintf("%s-%s.pid", PIDPrefix, target)
	if err := WritePIDFile(pidFile); err != nil && opts.Warn != nil {
		opts.Warn("could not write pid file " + pidFile + ": " + err.Error())
	}
	defer CleanupPIDFile(pidFile)

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	keys, restoreTerm := startKeyReader()
	defer restoreTerm()

	fastExitCount := 0

	for {
		launch := time.Now()
		if opts.Info != nil {
			opts.Info("Starting " + target + "...")
		}

		c := exec.Command("/bin/sh", "-c", cmd)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		pr, pw := io.Pipe()
		c.Stdout = pw
		c.Stderr = pw

		pump := &streams.OutputPump{
			// Options documents Stdout as optional ("Left nil, RunWithReload
			// writes nowhere"), but frame.Write hands a nil io.Writer to
			// io.WriteString, which panics on the child's first line of
			// output — a crash in the caller's process, not a quiet no-op.
			Out: func(s string) {
				if opts.Stdout == nil {
					return
				}
				frame.Write(opts.Stdout, s)
			},
			LogFn:     opts.LogFn,
			Formatter: opts.Formatter,
		}

		lineDone := make(chan struct{})
		go func() {
			defer close(lineDone)
			scanner := bufio.NewScanner(pr)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				pump.Write(scanner.Text() + "\n")
			}
		}()

		if err := c.Start(); err != nil {
			pw.Close()
			<-lineDone
			if opts.Warn != nil {
				opts.Warn("failed to start " + target + ": " + err.Error())
			}
			return 1
		}

		exitCh := make(chan error, 1)
		go func() { exitCh <- c.Wait() }()

		outcome := waitForExitOrSignal(c, sigCh, keys, exitCh, opts)
		pw.Close()
		<-lineDone
		pump.Flush()

		switch outcome {
		case outcomeSigint:
			// Matches Python's KeyboardInterrupt branch: prints "Stopped."
			// and returns the shell's conventional 128+SIGINT exit code.
			if opts.Info != nil {
				opts.Info("Stopped.")
			}
			return 130
		case outcomeSigterm:
			// Matches Python's _on_sigterm, which raises SystemExit(0) — a
			// clean shutdown with no "Stopped." banner and exit code 0,
			// deliberately distinct from a user-initiated Ctrl-C.
			return 0
		case outcomeReload:
			fastExitCount = 0
			continue
		}

		rc := 0
		if c.ProcessState != nil {
			rc = c.ProcessState.ExitCode()
		}
		elapsed := time.Since(launch)
		if elapsed < 2*time.Second {
			fastExitCount++
			exited := fmt.Sprintf("Process exited in %.1fs (code %d)", elapsed.Seconds(), rc)
			if fastExitCount < maxFastExitAttempts {
				if opts.Warn != nil {
					opts.Warn(fmt.Sprintf("%s — retrying (%d/%d)...", exited, fastExitCount, maxFastExitAttempts))
				}
				time.Sleep(time.Second)
				continue
			}
			if opts.Warn != nil {
				opts.Warn(fmt.Sprintf("%s — giving up after %d attempts.", exited, maxFastExitAttempts))
			}
		}
		return rc
	}
}

// waitOutcome is what ended one launch's waitForExitOrSignal call.
type waitOutcome int

const (
	// outcomeExited means the child ended on its own — proceed to the
	// fast-exit-retry accounting.
	outcomeExited waitOutcome = iota
	// outcomeReload means the child was killed for a restart (SIGUSR1 or an
	// 'r'/'R' keypress) — loop again immediately, fast-exit streak reset.
	outcomeReload
	// outcomeSigint means a person asked to stop (Ctrl-C at the terminal, or
	// an actual SIGINT) — maps to exit code 130, with a "Stopped." banner.
	outcomeSigint
	// outcomeSigterm means the process was asked to terminate via SIGTERM —
	// mirrors Python's _on_sigterm (raises SystemExit(0)): a clean shutdown,
	// exit code 0, no banner. Deliberately distinct from outcomeSigint.
	outcomeSigterm
)

// waitForExitOrSignal blocks until the running child (c) exits on its own, a
// reload is requested (SIGUSR1 or 'r'/'R'), or the process is asked to stop
// (SIGINT/SIGTERM/Ctrl-C). On a reload or stop outcome it tears the child's
// whole process group down before returning.
func waitForExitOrSignal(c *exec.Cmd, sigCh <-chan os.Signal, keys <-chan byte, exitCh <-chan error, opts Options) waitOutcome {
	for {
		select {
		case err := <-exitCh:
			_ = err
			return outcomeExited
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGUSR1:
				if opts.Info != nil {
					opts.Info("Reload requested — restarting...")
				}
				killAndWait(c, exitCh, syscall.SIGTERM)
				return outcomeReload
			case syscall.SIGINT:
				killAndWait(c, exitCh, syscall.SIGINT)
				return outcomeSigint
			case syscall.SIGTERM:
				killAndWait(c, exitCh, syscall.SIGTERM)
				return outcomeSigterm
			}
		case b, ok := <-keys:
			if !ok {
				continue
			}
			switch {
			case b == 'r' || b == 'R':
				if opts.Info != nil {
					opts.Info("Reload requested — restarting...")
				}
				killAndWait(c, exitCh, syscall.SIGTERM)
				return outcomeReload
			// Cbreak mode leaves ISIG on, so a terminal Ctrl-C normally
			// arrives on sigCh as a real SIGINT and never gets here. This
			// stays for the case where 0x03 is piped/typed into a stdin
			// whose ISIG is off.
			case b == 0x03:
				killAndWait(c, exitCh, syscall.SIGINT)
				return outcomeSigint
			}
		}
	}
}

// killAndWait sends sig to c's whole process group, then waits up to 5s for
// it to exit before escalating to SIGKILL — the Go port of
// admin_lib/reload.py's _wait_or_kill, generalized to send the initial
// signal to the group (os.killpg) the way run_with_reload's own callers do.
func killAndWait(c *exec.Cmd, exitCh <-chan error, sig syscall.Signal) {
	if c.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(c.Process.Pid)
	if err != nil {
		pgid = c.Process.Pid
	}
	_ = syscall.Kill(-pgid, sig)
	select {
	case <-exitCh:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-exitCh
	}
}

// KillProcessGroup sends sig to c's whole process group and waits up to 5s
// for it to exit before escalating to SIGKILL — exported so a caller outside
// this package needing the same generic "tear down a child I launched with
// Setpgid" teardown (internal/keybindings's own subprocess loop) uses this
// one implementation rather than writing a second copy of it.
func KillProcessGroup(c *exec.Cmd, exitCh <-chan error, sig syscall.Signal) {
	killAndWait(c, exitCh, sig)
}

// StartKeyReader puts stdin into cbreak mode and streams single bytes read
// from it on the returned channel, exactly like the reader RunWithReload
// uses for its own 'r'/Ctrl-C keys — exported so internal/keybindings's
// arbitrary-keymap loop reads stdin the same way rather than a second raw-
// mode reader. restore is always safe to call, including when raw mode was
// never entered.
func StartKeyReader() (keys <-chan byte, restore func()) {
	return startKeyReader()
}

// SignalAll sends SIGUSR1 to every admin-managed dev target whose PID file
// matches prefix (or the subset whose target name contains filter, when
// filter is non-empty). Stale PID files (parse failure, or a process that is
// no longer running) are removed. Returns the number of signals actually
// delivered; a zero count is always accompanied by a non-nil error naming
// why (no PID files at all, vs. none matched/alive) so a caller (the
// server-reload kind) can tell the two apart the way
// admin_lib/docker.py::reload_running_targets's distinct error messages do.
func SignalAll(prefix, filter string, r Reporter) (int, error) {
	files, _ := globPIDFiles(prefix)
	if len(files) == 0 {
		if r != nil {
			r.Err("No running targets found (no PID files)")
		}
		return 0, fmt.Errorf("no running targets found (no PID files)")
	}

	base := baseName(prefix)
	sent := 0
	for _, pidfile := range files {
		targetName := strings.TrimSuffix(strings.TrimPrefix(baseName(pidfile), base+"-"), ".pid")
		if filter != "" && !strings.Contains(targetName, filter) {
			continue
		}

		pid, err := readPIDFile(pidfile)
		if err != nil {
			if r != nil {
				r.Warn("Cannot read " + pidfile + " — removing stale file")
			}
			CleanupPIDFile(pidfile)
			continue
		}

		if err := syscall.Kill(pid, 0); err != nil {
			if r != nil {
				r.Warn(fmt.Sprintf("PID %d (%s) is not running — removing stale file", pid, targetName))
			}
			CleanupPIDFile(pidfile)
			continue
		}

		if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
			continue
		}
		if r != nil {
			r.Ok(fmt.Sprintf("Reload signal sent to %s (PID %d)", targetName, pid))
		}
		sent++
	}

	if sent == 0 {
		if r != nil {
			r.Err("No live targets found")
		}
		return 0, fmt.Errorf("no live targets found")
	}
	return sent, nil
}
