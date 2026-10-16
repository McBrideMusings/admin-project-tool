package kinds

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/supervise"
)

// emptyCmdErr: a blank `run` would otherwise reach /bin/sh -c "" and exit
// 0, which a factory would then report as success — refuse it explicitly
// instead of trusting the shell to fail on nothing.
const emptyCmdErr = "refusing to run an empty command"

// makeShell is kind="shell"'s factory, including the watch-kind piece
// (watch.go). `run` is resolved with manifest.ResolveEnv, the same call
// used on every command string: a manifest writes ${VAR} precisely so the
// value can live in .env instead of the file, and handing the placeholder
// to /bin/sh instead expands an unset name to "" and runs the command
// anyway.
func makeShell(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	run, _ := actionString(action, "run")
	rawURLs := actionURLs(action)
	footerVal, hasFooter := actionBool(action, "footer")
	ptyVal, _ := actionBool(action, "pty")
	noComplete, _ := actionBool(action, "no_complete")
	watchKind, hasWatch := actionString(action, "watch")
	return func(args []string) error {
		expanded, err := manifest.ResolveEnv(run)
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		cmd := strings.TrimSpace(expanded)
		h := newHost(m)
		if cmd == "" {
			h.Err(emptyCmdErr)
			return fmt.Errorf("action %q: %s", name, emptyCmdErr)
		}
		f := enterFrame(h, name, rawURLs, hasFooter, footerVal, m)
		defer frame.Deactivate(f)
		h.Info("running: " + cmd)
		// pty = true gives the child a real pseudo-terminal instead of a pipe,
		// which is the only way a child that prompts on stdin reads a whole
		// typed line at once. Everything else — frame, log tee, result
		// handling — is identical either way.
		runCmd := h.RunCmd
		if ptyVal {
			runCmd = h.RunCmdPTY
		}
		res, err := runCmd(cmd, luahost.RunCmdOptions{CaptureLog: true, Unsupervised: !superviseEnabled(action)})
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if !res.Ok {
			h.Err(name + " failed")
			return fmt.Errorf("action %q failed", name)
		}
		if hasWatch && watchKind != "" {
			runWatcher(h, watchKind, res.Output)
		}
		if !noComplete {
			h.Ok(name + " complete")
		}
		return nil
	}, nil
}

// makeShellPassthrough is kind="shell-passthrough"'s factory. Runner args
// are appended to `run`, each shell-quoted, so a caller-supplied argument
// can never be re-interpreted as shell syntax.
//
// `interactive = true` is a raw terminal handover: the child inherits this
// process's own stdin/stdout/stderr with no pipe anywhere in the path, no
// log tee, no frame and no output pump. That is the whole point of the knob
// and it cannot be approximated. A full-screen child — a TUI, an editor, a
// pager — needs a real terminal on stdout for two things it has no other way
// to get: the window size (a piped child is never sent SIGWINCH and reads no
// size, so it falls back to a fixed default and paints an island in the
// corner of a larger window) and color (every terminal library probes for a
// TTY and degrades to plain text without one). Routing such a child through
// RunCmd silently produces a small, monochrome fake of the program.
func makeShellPassthrough(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	run, _ := actionString(action, "run")
	rawURLs := actionURLs(action)
	footerVal, hasFooter := actionBool(action, "footer")
	interactive, _ := actionBool(action, "interactive")
	noComplete, _ := actionBool(action, "no_complete")
	return func(args []string) error {
		cmdline := run
		if len(args) > 0 {
			quoted := make([]string, len(args))
			for i, a := range args {
				quoted[i] = shellQuote(a)
			}
			cmdline += " " + strings.Join(quoted, " ")
		}
		expanded, err := manifest.ResolveEnv(cmdline)
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		resolved := strings.TrimSpace(expanded)
		h := newHost(m)
		if resolved == "" {
			h.Err(emptyCmdErr)
			return fmt.Errorf("action %q: %s", name, emptyCmdErr)
		}
		if interactive {
			// No frame and no banner: both would paint into a terminal this
			// process is about to hand over wholesale, and a full-screen child
			// clears them anyway. Nothing is logged, because there is no pipe
			// to tee from.
			return runInteractive(name, resolved, projectRoot(m), superviseEnabled(action))
		}
		f := enterFrame(h, name, rawURLs, hasFooter, footerVal, m)
		defer frame.Deactivate(f)
		h.Info("running: " + resolved)
		res, err := h.RunCmd(resolved, luahost.RunCmdOptions{CaptureLog: true, Unsupervised: !superviseEnabled(action)})
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if !res.Ok {
			h.Err(name + " failed")
			return fmt.Errorf("action %q failed", name)
		}
		if !noComplete {
			h.Ok(name + " complete")
		}
		return nil
	}, nil
}

// runInteractive hands the real terminal to cmdline and waits, running it in
// dir. The child inherits this process's own stdin, stdout and stderr file
// descriptors directly, so it sees a TTY, is sent SIGWINCH on resize, and can
// size and color itself exactly as if it had been typed at the shell.
//
// dir is the project root, the same working directory luahost.RunCmd gives
// the non-interactive path — an interactive action is still an action, and
// running it wherever `admin` was typed instead would make `run` mean two
// different things depending on one knob in the manifest.
//
// Nothing here may wrap those three descriptors — not a pipe, not a pty, not
// an io.Writer that tees to a log. Wrapping any of them reintroduces the bug
// this function exists to fix, and it fails silently: the child still runs,
// just small and monochrome.
func runInteractive(name, cmdline, dir string, supervised bool) error {
	c := exec.Command("/bin/sh", "-c", cmdline)
	c.Dir = dir
	supervise.Start(c, dir, supervised)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("action %q failed: exit %d", name, ee.ExitCode())
		}
		return fmt.Errorf("action %q: %w", name, err)
	}
	return nil
}
