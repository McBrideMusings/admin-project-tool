// Package wthooks runs the worktree lifecycle hooks ADR-0024 defines:
// post_create, post_start and pre_remove, declared as shell strings in
// [worktrees.hooks]. Every hook comes from the primary checkout's resolved
// manifest (config.Load on the repo root, the same load populate uses), never
// the worktree's own copy, and runs inside the worktree under /bin/sh -c with
// ADMIN_WT_PATH, ADMIN_WT_BRANCH and ADMIN_REPO_ROOT set.
//
// Every surface that deletes a worktree — `admin worktree --prune`, the
// dashboard's cleanup tab, the worker reaper — calls PreRemove first.
package wthooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// Hook names, as written in [worktrees.hooks].
const (
	HookPostCreate = "post_create"
	HookPostStart  = "post_start"
	HookPreRemove  = "pre_remove"
)

// Error is a hook that exited non-zero (or could not start). Output is the
// hook's combined stdout and stderr; Exit is -1 when the hook never ran.
type Error struct {
	Hook   string
	Exit   int
	Output string
	Err    error
	// Streamed is set when Output already reached stderr as the hook ran, so
	// the message leaves it out rather than print it twice.
	Streamed bool
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s hook failed (exit %d)", e.Hook, e.Exit)
	if e.Exit < 0 && e.Err != nil {
		msg = fmt.Sprintf("%s hook failed: %v", e.Hook, e.Err)
	}
	if out := strings.TrimSpace(e.Output); out != "" && !e.Streamed {
		msg += ":\n" + out
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Line is the error on one line, for a status field that holds no more: the
// headline plus the last line of the hook's output, which is where a script
// usually says why it failed.
func (e *Error) Line() string {
	msg := fmt.Sprintf("%s hook failed (exit %d)", e.Hook, e.Exit)
	if e.Exit < 0 && e.Err != nil {
		msg = fmt.Sprintf("%s hook failed: %v", e.Hook, e.Err)
	}
	if out := strings.TrimSpace(e.Output); out != "" {
		msg += ": " + out[strings.LastIndex(out, "\n")+1:]
	}
	return msg
}

// stderr is where hook progress and output go on a non-quiet ctx. A var so
// tests can capture it.
var stderr io.Writer = os.Stderr

// env is the hook environment: this process's own plus the three ADR-0024
// variables.
func env(repoRoot, path, branch string) []string {
	return append(os.Environ(),
		"ADMIN_WT_PATH="+path,
		"ADMIN_WT_BRANCH="+branch,
		"ADMIN_REPO_ROOT="+repoRoot,
	)
}

// Run runs hook's body synchronously inside the worktree at path and waits
// for it. On a non-quiet ctx (git.IsQuiet) the hook's output streams to
// stderr as it runs; on a quiet one (the dashboard, which owns the terminal)
// it is only captured. A non-zero exit returns *Error carrying the output.
func Run(ctx context.Context, hook, body, repoRoot, path, branch string) error {
	quiet := git.IsQuiet(ctx)
	if !quiet {
		fmt.Fprintf(stderr, "%s: running in %s: %s\n", hook, path, body)
	}
	var buf bytes.Buffer
	c := exec.CommandContext(ctx, "/bin/sh", "-c", body)
	c.Dir = path
	c.Env = env(repoRoot, path, branch)
	var out io.Writer = &buf
	if !quiet {
		out = io.MultiWriter(&buf, stderr)
	}
	c.Stdout, c.Stderr = out, out
	err := c.Run()
	if err == nil {
		if !quiet {
			fmt.Fprintf(stderr, "%s: exit 0\n", hook)
		}
		return nil
	}
	he := &Error{Hook: hook, Exit: -1, Output: buf.String(), Err: err, Streamed: !quiet}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		he.Exit = ee.ExitCode()
	}
	if !quiet {
		fmt.Fprintf(stderr, "%s: exit %d\n", hook, he.Exit)
	}
	return he
}

// Start runs hook's body detached inside the worktree at path: it returns as
// soon as the process starts, and the hook's output goes to a new
// "<hook>-<dir>.<timestamp>.log" under logDir — the same "<stem>.<ts>.log"
// shape luahost names run logs. The child gets its own session, so it
// outlives this process and a ctrl-c at the terminal does not reach it.
// Returns the log path.
func Start(hook, body, repoRoot, path, branch, logDir string) (string, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", fmt.Errorf("%s: creating log dir: %w", hook, err)
	}
	now := time.Now()
	ts := fmt.Sprintf("%s_%09d", now.Format("2006-01-02_150405"), now.Nanosecond())
	logPath := filepath.Join(logDir, hook+"-"+filepath.Base(path)+"."+ts+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return "", fmt.Errorf("%s: creating log: %w", hook, err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s: running in %s: %s\n", now.Format(time.RFC3339), hook, path, body)
	// The trailing echo records the exit status in the log, since nothing
	// waits on the detached child to report it.
	script := "(" + body + "\n); echo \"" + hook + ": exit $?\""
	c := exec.Command("/bin/sh", "-c", script)
	c.Dir = path
	c.Env = env(repoRoot, path, branch)
	c.Stdout, c.Stderr = f, f
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return logPath, &Error{Hook: hook, Exit: -1, Err: err}
	}
	// Reap the child when it exits so it never lingers as a zombie while this
	// process is still alive.
	go func() { _ = c.Wait() }()
	return logPath, nil
}

// PreRemove runs the primary checkout's pre_remove hook on the worktree at
// path, when its directory still exists, before a caller removes it from the
// repo containing dir. A non-nil return is a veto: the caller must leave the
// worktree and its branch in place and show the error, whose message carries
// the hook's output, as the reason. With force the hook still runs, and
// PreRemove returns nil whatever it exited with, so the removal goes ahead.
//
// Callers run it before every other step of their removal (closing a herdr
// workspace, stashing, killing a pane), so a veto leaves nothing half-done.
func PreRemove(ctx context.Context, dir, path string, force bool) error {
	if err := runPreRemove(ctx, dir, path); err != nil && !force {
		return err
	}
	return nil
}

func runPreRemove(ctx context.Context, dir, path string) error {
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return nil
	}
	repoRoot, err := git.RepoRoot(ctx, dir)
	if err != nil {
		return fmt.Errorf("%s: finding the primary checkout: %w", HookPreRemove, err)
	}
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return fmt.Errorf("%s: loading config: %w", HookPreRemove, err)
	}
	body := cfg.Worktrees.Hooks.PreRemove
	if body == "" {
		return nil
	}
	return Run(ctx, HookPreRemove, body, repoRoot, path, branchAt(ctx, repoRoot, path))
}

// branchAt is the branch checked out in the worktree at path, or "" when it
// is detached or not a worktree of repoRoot.
func branchAt(ctx context.Context, repoRoot, path string) string {
	wts, err := git.Worktrees(ctx, repoRoot)
	if err != nil {
		return ""
	}
	target := git.ResolvePath(path)
	for _, w := range wts {
		if git.ResolvePath(w.Path) == target {
			return w.Branch
		}
	}
	return ""
}
