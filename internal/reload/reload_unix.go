//go:build !windows

package reload

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/muesli/cancelreader"

	xterm "github.com/charmbracelet/x/term"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/logformat"
	"github.com/McBrideMusings/admin-project-tool/internal/streams"
	"github.com/McBrideMusings/admin-project-tool/internal/supervise"
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
// and writes this process's PID record under target. For the CALLER-OWNED
// loop shape (a build-install-launch cycle that re-runs itself, not a single
// child process this package restarts) — admin-3rgp.12's Apple dev loops are
// the intended adopter. dir is the loop's working directory, the record's
// Root when SetOwner never ran. The returned cleanup func stops the signal
// watch and removes the record; call it in a defer.
func SetupReload(target, dir string) (cleanup func(), err error) {
	pidFile, err := writeRecord(PIDPrefix, target, dir)
	if err != nil {
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
		removeRecord(pidFile)
	}
	return cleanup, nil
}

// keyReaderMu guards the process-wide shared stdin reader below. Every
// startKeyReader call — the exported StartKeyReader, PollForReload's and
// RunWithReload's own internal calls, and therefore every caller reached
// through any of those (internal/luahost's picker, internal/keybindings' key
// loop) — goes through this one singleton rather than each opening its own
// goroutine blocked in Read(os.Stdin). Two readers competing for the same tty
// steal each other's bytes (a stolen byte is gone, not merely delayed) and
// shred multi-byte sequences like an arrow key's ESC '[' 'B' across two
// listeners — a refcounted singleton makes that impossible by construction:
// there is only ever one goroutine actually reading the fd, however many
// subscribers are currently registered.
var (
	keyReaderMu    sync.Mutex
	keyReaderCR    cancelreader.CancelReader
	keyReaderState *termState
	keyReaderSubs  []chan byte
	keyReaderRefs  int
	// keyReaderDone is closed by pumpKeyReader right before it returns. The
	// last restore() waits on it after cancelling the reader, so a
	// subsequent startKeyReader() call — gated on the same mutex — is
	// guaranteed the previous pump goroutine has actually exited before it
	// opens a new cancelreader on the same fd. Without this join, the old
	// and new pump goroutines can both read stdin for a brief window, which
	// is the same byte-stealing race the singleton exists to eliminate.
	keyReaderDone chan struct{}
)

// startKeyReader puts fd into cbreak mode (canonical mode and echo off,
// signal generation left on) on the first subscriber, and hands every
// subscriber its own byte channel fed by one shared goroutine reading stdin.
// restore unsubscribes and is always safe to call, including when cbreak
// mode was never entered (stdin not a tty, or the ioctl failed) — restore is
// then a no-op. Only the LAST outstanding restore actually restores termios
// and stops the shared reader: it cancels the reader's blocking Read via
// github.com/muesli/cancelreader (which — on the darwin/BSD kqueue backend
// this project runs under — unblocks the pending read synchronously rather
// than leaving it parked in the kernel) and closes it, so the goroutine
// exits for real instead of leaking blocked in Read until the process exits.
// A later fresh startKeyReader call, once refs have dropped to zero, starts
// a brand-new cancelreader and re-enters cbreak cleanly.
func startKeyReader() (keys <-chan byte, restore func()) {
	fd := os.Stdin.Fd()
	if !xterm.IsTerminal(fd) {
		return nil, func() {}
	}

	keyReaderMu.Lock()
	if keyReaderRefs == 0 {
		state, err := makeCbreak(fd)
		if err != nil {
			keyReaderMu.Unlock()
			return nil, func() {}
		}
		cr, err := cancelreader.NewReader(os.Stdin)
		if err != nil {
			_ = restoreTerm(fd, state)
			keyReaderMu.Unlock()
			return nil, func() {}
		}
		keyReaderState = state
		keyReaderCR = cr
		keyReaderDone = make(chan struct{})
		go pumpKeyReader(cr, keyReaderDone)
	}
	ch := make(chan byte, 8)
	keyReaderSubs = append(keyReaderSubs, ch)
	keyReaderRefs++
	keyReaderMu.Unlock()

	var once sync.Once
	restore = func() {
		once.Do(func() {
			keyReaderMu.Lock()
			for i, sub := range keyReaderSubs {
				if sub == ch {
					keyReaderSubs = append(keyReaderSubs[:i], keyReaderSubs[i+1:]...)
					break
				}
			}
			keyReaderRefs--
			var done chan struct{}
			if keyReaderRefs <= 0 {
				keyReaderRefs = 0
				_ = restoreTerm(fd, keyReaderState)
				keyReaderState = nil
				if keyReaderCR != nil {
					keyReaderCR.Cancel()
					_ = keyReaderCR.Close()
					keyReaderCR = nil
				}
				done = keyReaderDone
				keyReaderDone = nil
			}
			keyReaderMu.Unlock()

			// Join the pump goroutine (if this was the last subscriber) with
			// the mutex released, not held: pumpKeyReader itself needs
			// keyReaderMu (to fan out a byte a concurrently-unblocking Read
			// returns) before it can loop again, see ErrCanceled, and close
			// done — holding the mutex across this wait would deadlock
			// against that. A subsequent startKeyReader() call is still
			// serialized on keyReaderMu, and keyReaderDone/keyReaderCR were
			// already cleared above, so it cannot observe a half-torn-down
			// reader even without holding the lock here.
			if done != nil {
				<-done
			}
		})
	}
	return ch, restore
}

// pumpKeyReader is the ONE goroutine reading stdin at any given time — it
// reads a byte at a time from cr and fans it out to every currently
// subscribed channel (a non-blocking send, same drop-on-full behavior each
// per-call reader used before this became shared). It returns when cr's Read
// errors, which the owning startKeyReader restore's Cancel()+Close() call
// causes to happen promptly. done is closed right before this function
// returns, so the restore that cancelled cr can join this goroutine and know
// the fd is no longer being read before a fresh startKeyReader opens a new
// cancelreader on it.
func pumpKeyReader(cr cancelreader.CancelReader, done chan struct{}) {
	defer close(done)
	buf := make([]byte, 1)
	for {
		n, err := cr.Read(buf)
		if n > 0 {
			b := buf[0]
			keyReaderMu.Lock()
			for _, sub := range keyReaderSubs {
				select {
				case sub <- b:
				default:
				}
			}
			keyReaderMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// PollForReload blocks until a reload is requested (SIGUSR1, or 'r'/'R' on a
// terminal stdin) or isRunning returns false, checking isRunning at least
// once per timeout. Returns true for "reload requested", false for "the
// watched process is gone" — for the caller-owned loop shape.
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
	// Root is the project root (the directory holding admin.toml), recorded
	// as ADMIN_CHECKOUT on each spawned child.
	Root string
	// Unsupervised is the action's `supervise = false`; see keybindings.Options.
	Unsupervised bool

	// Dir is the working directory each spawned child runs in — the calling
	// kind's project root. Left empty, the child inherits this process's own
	// cwd, which is wherever `admin` was typed rather than where the project
	// is; a reload loop restarts the child many times, so an inherited cwd is
	// wrong for every one of those restarts too.
	Dir string
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
// that exit code.
//
// Returns the child's exit code, or 130 on a user-initiated Ctrl-C/SIGINT
// (the shell convention for a SIGINT-terminated process).
func RunWithReload(cmd, target string, opts Options) int {
	pidFile, err := writeRecord(PIDPrefix, target, opts.Dir)
	if err != nil && opts.Warn != nil {
		opts.Warn("could not write pid file " + pidFile + ": " + err.Error())
	}
	defer removeRecord(pidFile)

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
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
		c.Dir = opts.Dir
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		run := supervise.Start(c, opts.Root, !opts.Unsupervised)
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
			LogFn:            opts.LogFn,
			Formatter:        opts.Formatter,
			DisplayTransform: logformat.StampLine,
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
			run.Stop()
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
		// The group kill misses descendants that called setsid; sweep the run.
		run.Stop()
		pw.Close()
		<-lineDone
		pump.Flush()

		switch outcome {
		case outcomeSigint:
			// Prints "Stopped." and returns the shell's conventional
			// 128+SIGINT exit code.
			if opts.Info != nil {
				opts.Info("Stopped.")
			}
			return 130
		case outcomeSigterm:
			// A clean shutdown with no "Stopped." banner and exit code 0,
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
	// a clean shutdown, exit code 0, no banner. Deliberately distinct from
	// outcomeSigint.
	outcomeSigterm
)

// waitForExitOrSignal blocks until the running child (c) exits on its own, a
// reload is requested (SIGUSR1 or 'r'/'R'), or the process is asked to stop
// (SIGINT/SIGTERM/Ctrl-C). On a reload or stop outcome it tears the child's
// whole process group down before returning.
func waitForExitOrSignal(c *exec.Cmd, sigCh <-chan os.Signal, keys <-chan byte, exitCh <-chan error, opts Options) waitOutcome {
	// teardown tears the child's group down and records in the action's log
	// any member that joined the group after killAndWait's first read.
	teardown := func(sig syscall.Signal) {
		late := killAndWait(c, exitCh, sig)
		if len(late) > 0 && opts.LogFn != nil {
			opts.LogFn(fmt.Sprintf("reload: %v teardown signalled pids %v, which joined the process group after it started\n", sig, late))
		}
	}
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
				teardown(syscall.SIGTERM)
				return outcomeReload
			case syscall.SIGINT:
				teardown(syscall.SIGINT)
				return outcomeSigint
			case syscall.SIGTERM, syscall.SIGHUP:
				// A closed terminal (SIGHUP) tears down exactly like SIGTERM.
				teardown(syscall.SIGTERM)
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
				teardown(syscall.SIGTERM)
				return outcomeReload
			// Cbreak mode leaves ISIG on, so a terminal Ctrl-C normally
			// arrives on sigCh as a real SIGINT and never gets here. This
			// stays for the case where 0x03 is piped/typed into a stdin
			// whose ISIG is off.
			case b == 0x03:
				teardown(syscall.SIGINT)
				return outcomeSigint
			}
		}
	}
}

// sweepInterval is how often killAndWait re-reads the process group while it
// waits, looking for members that have not had the signal yet.
const sweepInterval = 10 * time.Millisecond

// killAndWait sends sig to c's whole process group, then waits up to 5s for
// it to exit before escalating to SIGKILL. It returns the pids that joined
// the group after the first read and were signalled when a later read found
// them.
//
// On darwin a group kill is not atomic against a fork in progress: a child
// the shell forks while kill(-pgid) runs is not signalled, outlives its
// parent, and holds the output pipe open, so c.Wait — and with it a Ctrl-C,
// SIGTERM or reload — stalls until the SIGKILL fallback. Re-sending a group
// signal to whoever turns up later cannot fix that, since some of those did
// get it and some tools treat a second SIGINT as "force quit now". So where
// the group can be read, killAndWait never signals the group: it signals each
// member by pid, once, and keeps re-reading the group until the child exits,
// so a member forked at any point before then gets the signal exactly once.
// A member still being created by fork is left for the next read, because a
// signal sent to it can be lost.
func killAndWait(c *exec.Cmd, exitCh <-chan error, sig syscall.Signal) (late []int) {
	if c.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(c.Process.Pid)
	if err != nil {
		pgid = c.Process.Pid
	}

	members, perPid := groupMembers(pgid)
	if !perPid {
		_ = syscall.Kill(-pgid, sig)
	}
	signalled := map[int]bool{}
	signalNew := func(members map[int]bool, sig syscall.Signal) (fresh []int) {
		for pid, idle := range members {
			if !idle && !signalled[pid] {
				signalled[pid] = true
				_ = syscall.Kill(pid, sig)
				fresh = append(fresh, pid)
			}
		}
		return fresh
	}
	signalNew(members, sig)

	tick := time.NewTicker(sweepInterval)
	defer tick.Stop()
	escalate := time.NewTimer(5 * time.Second)
	defer escalate.Stop()
	for {
		select {
		case <-exitCh:
			return late
		case <-tick.C:
			if members, ok := groupMembers(pgid); perPid && ok {
				late = append(late, signalNew(members, sig)...)
			}
		case <-escalate.C:
			// A second SIGKILL is harmless, so the escalation signals the
			// group and every member again.
			sig = syscall.SIGKILL
			signalled = map[int]bool{}
			_ = syscall.Kill(-pgid, sig)
			if members, ok := groupMembers(pgid); perPid && ok {
				signalNew(members, sig)
			}
		}
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

// Signal sends SIGUSR1 to every admin-managed dev loop whose PID record under
// prefix satisfies match (MatchTarget, MatchRow). Stale records (parse
// failure, or a process that is no longer running) are removed whether or
// not they match. Returns the number of signals actually delivered; a zero
// count is always accompanied by a non-nil error naming why (no PID files at
// all, vs. none matched/alive) so a caller (the server-reload kind) can tell
// the two apart.
func Signal(prefix string, match func(Record) bool, r Reporter) (int, error) {
	files, _ := globPIDFiles(prefix)
	if len(files) == 0 {
		if r != nil {
			r.Err("No running targets found (no PID files)")
		}
		return 0, fmt.Errorf("no running targets found (no PID files)")
	}

	sent := 0
	for _, pidfile := range files {
		rec, err := readRecord(pidfile)
		if err != nil {
			if r != nil {
				r.Warn("Cannot read " + pidfile + " — removing stale file")
			}
			removeRecord(pidfile)
			continue
		}

		if err := syscall.Kill(rec.PID, 0); err != nil {
			if r != nil {
				r.Warn(fmt.Sprintf("PID %d (%s) is not running — removing stale file", rec.PID, rec.Target))
			}
			removeRecord(pidfile)
			continue
		}

		if !match(rec) {
			continue
		}
		if err := syscall.Kill(rec.PID, syscall.SIGUSR1); err != nil {
			continue
		}
		if r != nil {
			r.Ok(fmt.Sprintf("Reload signal sent to %s (PID %d)", rec.Target, rec.PID))
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
