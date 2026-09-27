//go:build !windows

package keybindings

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/logbridge"
	"github.com/McBrideMusings/admin-project-tool/internal/logformat"
	"github.com/McBrideMusings/admin-project-tool/internal/reload"
	"github.com/McBrideMusings/admin-project-tool/internal/streams"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// installBridgeSink routes log-bridge lines through opts' formatter and
// frame.Write. Without a sink the bridge writes to os.Stdout directly:
// unstamped, uncolored, and — under a pinned footer — painting over the
// footer rows.
//
// Bridge lines get their OWN LogFormatter, built fresh with ReuseTimestamp
// true, rather than the caller's own Formatter (hasFormatter only gates
// whether to format at all) — only bridge lines carry a timestamp worth
// keeping (the browser's own clock, up to a second stale from the
// userscript's batching); a subprocess's own stamp is normally now() and
// not worth preserving.
func installBridgeSink(b *logbridge.Bridge, w io.Writer, hasFormatter bool) {
	if !hasFormatter {
		b.SetSink(func(line string) { frame.Write(w, line) })
		return
	}
	opts := logformat.DefaultOptions()
	opts.ReuseTimestamp = true
	fmtFn := logformat.MakeFormatter(opts)
	b.SetSink(func(line string) { frame.Write(w, fmtFn(line)) })
}

// signalByName resolves a signal name (with or without the "SIG" prefix,
// case-insensitive — "TERM" and "SIGTERM" both work) to a syscall.Signal.
// The Go standard library has no name->signal registry, so this is a small
// hand-built table covering the signals a pid_file-targeted keybinding
// would plausibly send.
var namedSignals = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT,
	"KILL": syscall.SIGKILL, "TERM": syscall.SIGTERM,
	"USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
	"ABRT": syscall.SIGABRT, "ALRM": syscall.SIGALRM, "CONT": syscall.SIGCONT,
	"STOP": syscall.SIGSTOP, "TSTP": syscall.SIGTSTP, "WINCH": syscall.SIGWINCH,
}

func signalByName(name string) (syscall.Signal, bool) {
	raw := strings.ToUpper(strings.TrimSpace(name))
	raw = strings.TrimPrefix(raw, "SIG")
	sig, ok := namedSignals[raw]
	return sig, ok
}

// sendSignal reads a PID from pidFile and signals it. A stale/missing PID
// file, an unknown signal name, or a process that's already gone is warned
// about, never fatal to the wrapped process's own run.
func sendSignal(sigName, pidFile string, opts Options) {
	sig, ok := signalByName(sigName)
	if !ok {
		if opts.Err != nil {
			opts.Err(fmt.Sprintf("keybinding signal: unknown signal name %q", sigName))
		}
		return
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		if opts.Warn != nil {
			opts.Warn(fmt.Sprintf("keybinding signal: can't read pid file %q: %v", pidFile, err))
		}
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		if opts.Warn != nil {
			opts.Warn(fmt.Sprintf("keybinding signal: can't read pid file %q: %v", pidFile, err))
		}
		return
	}
	if err := syscall.Kill(pid, sig); err != nil {
		if err == syscall.ESRCH {
			if opts.Warn != nil {
				opts.Warn(fmt.Sprintf("keybinding signal: pid %d (from %s) is gone", pid, pidFile))
			}
			return
		}
		if opts.Err != nil {
			opts.Err(fmt.Sprintf("keybinding signal: kill(%d, %s) failed: %v", pid, sigName, err))
		}
	}
}

// runInline fires a shell snippet without blocking the wrapped process,
// inheriting the real process stdout/stderr, so its output goes straight to
// admin's own stdout rather than through the pump/footer (the binding is
// meant for a fire-and-forget side effect, not something whose output needs
// to interleave cleanly with the wrapped process's own).
func runInline(snippet string, opts Options) {
	c := exec.Command("/bin/sh", "-c", snippet)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		if opts.Err != nil {
			opts.Err("keybinding run: shell launch failed: " + err.Error())
		}
	}
}

// dispatchBinding fires exactly one of a KeyBinding's three target shapes.
// validate.ParseKeys already guarantees exactly one of Run/Action/Signal is
// set, so the order checked here doesn't matter.
func dispatchBinding(b validate.KeyBinding, opts Options) {
	switch {
	case b.Run != "":
		runInline(b.Run, opts)
	case b.Action != "":
		if opts.RunAction == nil {
			return
		}
		if err := opts.RunAction(b.Action); err != nil {
			if opts.Err != nil {
				opts.Err(fmt.Sprintf("keybinding action %q raised: %v", b.Action, err))
			}
		}
	case b.Signal != "":
		sendSignal(b.Signal, b.PidFile, opts)
	}
}

// waitOutcome is what ended one Run's wait loop.
type waitOutcome int

const (
	outcomeExited waitOutcome = iota
	outcomeSigint
	outcomeSigterm
)

// waitLoop blocks dispatching keystrokes to bindings until the wrapped
// process exits on its own, a stop is requested (SIGINT/SIGTERM/Ctrl-C/
// Ctrl-D), tearing the process's whole group down on the way out for a stop
// outcome.
func waitLoop(c *exec.Cmd, sigCh <-chan os.Signal, keysCh <-chan byte, exitCh <-chan error, keys map[string]validate.KeyBinding, opts Options) waitOutcome {
	for {
		select {
		case <-exitCh:
			return outcomeExited
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGINT:
				reload.KillProcessGroup(c, exitCh, syscall.SIGINT)
				return outcomeSigint
			case syscall.SIGTERM:
				reload.KillProcessGroup(c, exitCh, syscall.SIGTERM)
				return outcomeSigterm
			}
		case b, ok := <-keysCh:
			if !ok {
				continue
			}
			if b == 0x03 || b == 0x04 { // Ctrl-C / Ctrl-D
				reload.KillProcessGroup(c, exitCh, syscall.SIGINT)
				return outcomeSigint
			}
			decoded := string(rune(b))
			if binding, ok := keys[decoded]; ok {
				dispatchBinding(binding, opts)
			}
		}
	}
}

// Run runs cmdline as a long-lived subprocess in its own process group,
// streaming its output through internal/streams.OutputPump, until it exits
// on its own or is torn down by Ctrl-C/Ctrl-D/SIGINT/SIGTERM — dispatching
// any recognized keystroke to its binding along the way instead of
// restarting the wrapped process (that restart-on-key shape is
// internal/reload.RunWithReload's job, a different kind entirely).
//
// Returns the wrapped process's exit code, or 130 on Ctrl-C/SIGINT
// (the shell convention for a keyboard interrupt) or 0 on SIGTERM (a clean
// shutdown, no "Stopped." banner).
func Run(cmdline string, keys map[string]validate.KeyBinding, opts Options) (int, error) {
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	var bridge *logbridge.Bridge
	if opts.LogBridge != nil && opts.LogBridge.Enabled() {
		b, err := logbridge.Start(opts.LogPath, *opts.LogBridge)
		if err != nil {
			if opts.Warn != nil {
				opts.Warn("log bridge: " + err.Error())
			}
		} else if b != nil {
			bridge = b
			installBridgeSink(bridge, stdout, opts.Formatter != nil)
			if opts.Info != nil {
				opts.Info(fmt.Sprintf("[log-bridge] listening on port %d  hosts=%v", bridge.Port(), opts.LogBridge.Hosts))
			}
			defer func() {
				bridge.ClearSink()
				bridge.Stop()
			}()
		}
	}

	if len(keys) > 0 && opts.Info != nil {
		opts.Info(formatBanner(keys, opts.Label))
	}

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	c := exec.Command("/bin/sh", "-c", cmdline)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	pump := &streams.OutputPump{
		Out:       func(s string) { frame.Write(stdout, s) },
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
		return 1, fmt.Errorf("failed to start: %w", err)
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- c.Wait() }()

	keysCh, restoreTerm := reload.StartKeyReader()
	defer restoreTerm()

	launch := time.Now()
	outcome := waitLoop(c, sigCh, keysCh, exitCh, keys, opts)

	pw.Close()
	<-lineDone
	pump.Flush()

	switch outcome {
	case outcomeSigint:
		if opts.Info != nil {
			opts.Info("Stopped.")
		}
		return 130, nil
	case outcomeSigterm:
		return 0, nil
	}

	rc := 0
	if c.ProcessState != nil {
		rc = c.ProcessState.ExitCode()
	}
	elapsed := time.Since(launch)
	if elapsed < 2*time.Second && rc != 0 && opts.Warn != nil {
		opts.Warn(fmt.Sprintf("Process exited in %.1fs (code %d)", elapsed.Seconds(), rc))
	}
	return rc, nil
}
