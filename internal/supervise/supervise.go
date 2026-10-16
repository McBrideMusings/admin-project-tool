// Package supervise makes every process admin starts for an action findable
// and killable after it has left the process group admin started it in.
//
// Dev launchers routinely spawn children with Node's `detached: true`, which
// calls setsid(); those children escape any group kill and survive both a
// normal stop and the death of admin. So each action run is stamped with
// ADMIN_RUN_ID and ADMIN_CHECKOUT in its environment — inherited through
// fork/exec, detachment and reparenting — and a run is torn down by finding
// every process carrying its id (ports.SweepTagged), not by walking groups.
//
// A run has a watcher: a separate `admin __supervise` process, in its own
// session, holding the read end of a pipe whose write end only the owning
// admin process keeps. When that pipe reaches EOF — admin exited for any
// reason, including SIGKILL — the watcher sweeps the run's id.
package supervise

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/ports"
)

// Executable returns the admin binary the watcher re-enters. It is nil until
// Enable is called, and while nil no watcher process starts and nothing is
// logged to disk: a test binary that exercises a spawn site must not
// re-execute itself as a "watcher". The admin binary enables it once, in
// cmd.Execute.
var Executable func() (string, error)

// Enable turns on watcher processes and the sweep log for this process.
func Enable() { Executable = os.Executable }

// NewRunID returns a random id, unique per action run.
func NewRunID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("r%x", os.Getpid())
	}
	return "r" + hex.EncodeToString(b)
}

// SetEnv returns env with ADMIN_RUN_ID and ADMIN_CHECKOUT set to id and root,
// replacing any inherited values rather than appending duplicates.
func SetEnv(env []string, id, root string) []string {
	return append(ports.StripTag(env), ports.EnvRunID+"="+id, ports.EnvCheckout+"="+root)
}

// RunIDFromEnv returns the ADMIN_RUN_ID in env, "" when absent.
func RunIDFromEnv(env []string) string { return ports.EnvValue(env, ports.EnvRunID) }

func absRoot(root string) string {
	if root == "" {
		root, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

// LogPath is where every run's watcher and sweeps append their log:
// ~/.admin/state/supervise.log, beside the live-work state. It is not under
// the project, so a sweep never dirties a worktree it is cleaning up.
func LogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".admin", "state", "supervise.log")
}

// Run is one action run: a fresh id stamped onto the command it was started
// for, and a watcher process that sweeps the id when this process's pipe
// closes — Stop, or this process's exit for any reason.
type Run struct {
	id   string
	pipe *os.File // write end of the watcher's pipe; nil without a watcher
	once sync.Once
	off  bool // the action opted out (supervise = false)
}

var (
	mu   sync.Mutex
	runs = map[string]*Run{}
)

// ID returns the run's id.
func (r *Run) ID() string { return r.id }

// Start begins a new run for c, which has not been started yet: it stamps a
// fresh ADMIN_RUN_ID and ADMIN_CHECKOUT onto c's environment (the process's
// own environment when c.Env is nil) and starts the run's watcher. root is
// the project root — the directory holding admin.toml — and is what
// ADMIN_CHECKOUT records; "" falls back to the working directory. This is the
// only place a command is tagged. When the watcher cannot start the run still
// works, without crash safety; the problem goes to stderr.
//
// supervised is the action's `supervise` key (default true). When false the
// action opts out — it starts a daemon meant to outlive admin — so c gets no
// tag (an inherited one is removed too, or an enclosing run's sweep would
// still reach it), no watcher, and the returned Run does nothing.
func Start(c *exec.Cmd, root string, supervised bool) *Run {
	root = absRoot(root)
	r := &Run{id: NewRunID()}
	env := c.Env
	if env == nil {
		env = os.Environ()
	}
	if !supervised {
		c.Env = ports.StripTag(env)
		r.off = true
		return r
	}
	c.Env = SetEnv(env, r.id, root)
	if err := r.startWatcher(root); err != nil {
		fmt.Fprintln(os.Stderr, "admin: "+err.Error())
	}
	mu.Lock()
	runs[r.id] = r
	mu.Unlock()
	return r
}

func (r *Run) startWatcher(root string) error {
	if Executable == nil {
		return nil
	}
	exe, err := Executable()
	if err != nil {
		return fmt.Errorf("supervise: locating admin: %w", err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("supervise: pipe: %w", err)
	}
	logf, _ := openLog()
	c := exec.Command(exe, "__supervise", r.id, root,
		strconv.Itoa(os.Getpid()), strconv.Itoa(syscall.Getpgrp()))
	c.ExtraFiles = []*os.File{pr}
	c.Env = ports.StripTag(os.Environ())
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if logf != nil {
		c.Stdout, c.Stderr = logf, logf
	}
	err = c.Start()
	_ = pr.Close()
	if logf != nil {
		_ = logf.Close()
	}
	if err != nil {
		_ = pw.Close()
		return fmt.Errorf("supervise: starting watcher: %w", err)
	}
	go func() { _ = c.Wait() }()
	r.pipe = pw
	return nil
}

// Stop ends the run: it sweeps every process carrying the id, then closes the
// watcher's pipe so the watcher does a final sweep and exits. It runs once
// per run; later calls wait for the first and return.
func (r *Run) Stop() {
	r.once.Do(func() {
		if r.off {
			return // an unsupervised run: nothing was tagged
		}
		Sweep(r.id, "stop")
		mu.Lock()
		delete(runs, r.id)
		mu.Unlock()
		if r.pipe != nil {
			_ = r.pipe.Close()
		}
	})
}

// StopID stops the run with this id if this process started it; a no-op for
// "" or an unknown id. Callers on a UI path run it in a goroutine.
func StopID(id string) {
	mu.Lock()
	r := runs[id]
	mu.Unlock()
	if r != nil {
		r.Stop()
	}
}

// Sweep kills every process carrying run id, logging each decision to the
// sweep log. why names the trigger in the log.
func Sweep(id, why string) {
	var logger func(string, ...any)
	if f, err := openLog(); err == nil {
		defer f.Close()
		logger = lineLogger(f)
	}
	ports.SweepTagged(fmt.Sprintf("run=%s (%s)", id, why),
		func(t ports.Tagged) bool { return t.RunID == id },
		ports.SweepOptions{Logf: logger})
}

// lineLogger writes one timestamped line per call to w.
func lineLogger(w io.Writer) func(string, ...any) {
	return func(format string, args ...any) {
		fmt.Fprintf(w, "%s pid=%d %s\n", time.Now().Format("2006-01-02T15:04:05.000Z07:00"), os.Getpid(), fmt.Sprintf(format, args...))
	}
}

func openLog() (*os.File, error) {
	if Executable == nil {
		return nil, fmt.Errorf("sweep log disabled")
	}
	path := LogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

// Watch is the body of `admin __supervise <id> <root> <owner-pid>
// <owner-pgid>`: block until fd 3 (the owner's pipe) reaches EOF, then sweep
// the run id, never touching the owner's pid or process group.
func Watch(id, root string, ownerPID, ownerPGID int) error {
	if id == "" {
		return fmt.Errorf("supervise: empty run id")
	}
	// The watcher lives in its own session; a hangup or interrupt aimed at
	// the owner's terminal must not stop it before its sweep.
	signal.Ignore(syscall.SIGHUP, syscall.SIGINT)
	pipe := os.NewFile(3, "supervise-pipe")
	if pipe == nil {
		return fmt.Errorf("supervise: no pipe on fd 3")
	}
	_, _ = io.Copy(io.Discard, pipe)
	logf := lineLogger(os.Stdout)
	logf("supervise: owner pid %d gone or run %s stopped; sweeping", ownerPID, id)
	ports.SweepTagged(fmt.Sprintf("run=%s (watcher)", id),
		func(t ports.Tagged) bool { return t.RunID == id },
		ports.SweepOptions{
			ProtectPIDs:  []int{ownerPID},
			ProtectPGIDs: []int{ownerPGID},
			Logf:         logf,
		})
	return nil
}
