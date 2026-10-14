// reap.go ends a worker's session once it is over: the dashboard tick calls
// ReapWorker for every running or waiting worker. A worker is a live agent
// session tasked with something, so its row lasts exactly as long as that
// session does.
//
// The session is over when its launcher pane has gone (closed by a person, or
// the agent exited) or a hosted worker's process has exited, or when the
// action's `done` predicate passes, in which case the reaper closes the
// session itself. Either way the row ends and the ceiling slot frees. The
// worktree and branch are a separate question: they are removed only when
// nothing in them exists outside origin, checked here in Go so no manifest
// can loosen it; otherwise they stay on disk for the Repo tab, and the reap's
// outcome says why.
package kinds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/keptnote"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/wthooks"
)

// doneTimeout bounds one done-predicate run, so a wedged predicate reads as
// "not finished" rather than stalling the tick.
var doneTimeout = 5 * time.Second

// fetchInterval is the least time between two fetches of one repo's origin
// by the reaper, so a run of reaps does not fetch once per worker.
var fetchInterval = 30 * time.Second

var (
	fetchMu   sync.Mutex
	fetchedAt = map[string]time.Time{}
)

// fetchOrigin refreshes repoRoot's remote-tracking refs, at most once per
// fetchInterval. A branch pushed from another machine, or landed on the
// remote, is on origin before the local tracking refs know it.
func fetchOrigin(ctx context.Context, repoRoot string) {
	// Held across the fetch so a concurrent worker waits for fresh refs
	// instead of re-checking against stale ones.
	fetchMu.Lock()
	defer fetchMu.Unlock()
	if time.Since(fetchedAt[repoRoot]) < fetchInterval {
		return
	}
	git.FetchPrune(ctx, repoRoot)
	fetchedAt[repoRoot] = time.Now()
}

// maxNamedIgnored is how many worker-only ignored paths a keep reason names.
const maxNamedIgnored = 3

// KeepReason is the removal rule over its inputs: why an ended worker's
// worktree and branch stay on disk, "" when removing them loses nothing.
// workerOnly is the ignored paths the primary checkout lacks; unpushed is how
// many commits exist on no origin ref, and counted is false when that could
// not be read, which keeps the work.
func KeepReason(clean bool, workerOnly []string, unpushed int, counted bool) string {
	switch {
	case !clean && len(workerOnly) > 0:
		named := workerOnly
		more := ""
		if len(named) > maxNamedIgnored {
			more = fmt.Sprintf(" (+%d more)", len(named)-maxNamedIgnored)
			named = named[:maxNamedIgnored]
		}
		return "ignored files not in the primary checkout: " + strings.Join(named, ", ") + more
	case !clean:
		return "uncommitted changes"
	case !counted:
		return "could not tell which commits origin holds"
	case unpushed > 0:
		return fmt.Sprintf("%d commit(s) on no origin branch", unpushed)
	}
	return ""
}

// PaneGone runs a [terminal].status command with ADMIN_HANDLE set to handle.
// Exit 0 says the pane is there and an exit from 1 to 125 says it is gone:
// those are the only answers the command itself gives. Everything else is err
// and says nothing definite about the pane: 126 and 127 (the shell could not
// run the command — not executable, not found), 128 and above or a signalled
// shell (killed by a signal), a timeout, a shell that would not start. The
// error carries the exit code and the first line of the command's stderr.
func PaneGone(ctx context.Context, statusCmd, handle string) (gone bool, err error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", statusCmd)
	cmd.Env = append(os.Environ(), "ADMIN_HANDLE="+handle)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// A grandchild left holding the stderr pipe after the deadline kills sh
	// must not hold Run past it.
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	if runErr == nil || errors.Is(runErr, exec.ErrWaitDelay) {
		// ErrWaitDelay means sh exited 0 and a grandchild still held stderr:
		// the command's answer was "alive".
		return false, nil
	}
	if ctx.Err() != nil {
		// Killed by the deadline: the exit says nothing about the pane.
		return false, fmt.Errorf("status command timed out: %w", ctx.Err())
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return false, fmt.Errorf("status command: %w", runErr)
	}
	code := exitErr.ExitCode()
	if code >= 1 && code <= 125 {
		return true, nil
	}
	msg := fmt.Sprintf("status command failed to run (exit %d)", code)
	if code < 0 {
		msg = "status command killed by " + exitErr.String()
	}
	if s, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); s != "" {
		msg += ": " + s
	}
	return false, errors.New(msg)
}

// doneInterval is the least time between two runs of one worker's done
// predicate. The reaper is called on every 1s dashboard tick, while a
// predicate commonly asks GitHub (`gh pr view`) a question whose answer moves
// on the scale of minutes; run every tick, a handful of workers exhaust the
// account's GraphQL rate limit.
var doneInterval = 60 * time.Second

// doneRun is the outcome of one worker's last done-predicate run, reused until
// the next run is due.
type doneRun struct {
	at       time.Time
	finished bool
	note     string
}

// doneRuns holds each worker entry's last doneRun, keyed by the entry pointer
// so a re-dispatch under the same id starts with no history.
var doneRuns sync.Map

// doneDue reports whether e's done predicate runs now: it never has, or its
// last run is doneInterval old.
func doneDue(e *livework.Entry, now time.Time) (last doneRun, due bool) {
	v, ok := doneRuns.Load(e)
	if !ok {
		return doneRun{}, true
	}
	last = v.(doneRun)
	return last, now.Sub(last.at) >= doneInterval
}

// workerSessionGone reports whether e's session has ended on its own, and how:
// a window worker's launcher pane has gone ([terminal].status), or a hosted
// worker's process has exited (Ops.Exited). A launcher with no status command
// reads as not gone; a status probe that could not answer reads as not gone
// and returns probeErr, the reason the row shows.
func workerSessionGone(ctx context.Context, e *livework.Entry, t config.Terminal) (gone bool, why string, probeErr error) {
	if e.Via != "terminal" {
		if e.Ops.Exited == nil {
			return false, "", nil
		}
		exited, err := e.Ops.Exited()
		if !exited {
			return false, "", nil
		}
		if err != nil {
			return true, "process exited (" + err.Error() + ")", nil
		}
		return true, "process exited", nil
	}
	if t.Status == "" {
		return false, "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, doneTimeout)
	defer cancel()
	gone, err := PaneGone(ctx, t.Status, e.Handle)
	if err != nil {
		return false, "", err
	}
	return gone, "pane closed", nil
}

// workerFinished reports whether a worker whose session is still running has
// completed its task: its done predicate passed, or its worktree was removed
// by hand. A worker with no done predicate is never finished this way; its
// session ends when its pane does. note is non-empty when the predicate could
// not give an answer at all (it timed out or would not start), so the row says
// why it is not progressing. Between runs of the predicate (doneDue) the last
// run's answer stands.
func workerFinished(ctx context.Context, repoRoot string, e *livework.Entry) (finished bool, note string) {
	w := e.Worker
	if workerWorktreeGone(w) {
		// The done predicate runs in the worktree, so it cannot run at all.
		return true, ""
	}
	if w.Done == "" {
		return false, ""
	}
	last, due := doneDue(e, time.Now())
	if !due {
		return last.finished, last.note
	}
	start := time.Now()
	finished, code, note := runDone(ctx, e)
	doneRuns.Store(e, doneRun{at: start, finished: finished, note: note})
	// LogSync prints either the detail or the error, so a run that could not
	// answer carries its fields inside the error text.
	detail := fmt.Sprintf("worker=%s target=%s exit=%d finished=%t", e.ID, w.Target, code, finished)
	var logErr error
	if note != "" {
		logErr = fmt.Errorf("%s: %s", note, detail)
	}
	data.LogSync(filepath.Base(repoRoot), "done", time.Since(start), detail, logErr)
	return finished, note
}

// runDone runs e's done predicate once in its worktree. code is the shell's
// exit status, -1 when it never produced one.
func runDone(ctx context.Context, e *livework.Entry) (finished bool, code int, note string) {
	w := e.Worker
	ctx, cancel := context.WithTimeout(ctx, doneTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", w.Done)
	cmd.Dir = w.Worktree
	cmd.Env = append(os.Environ(), "ADMIN_TARGET="+w.Target, "ADMIN_HANDLE="+e.Handle)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err == nil {
		return true, 0, ""
	}
	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return false, -1, fmt.Sprintf("done predicate timed out after %s", doneTimeout)
	case !errors.As(err, &exitErr):
		return false, -1, "done predicate failed to run: " + err.Error()
	}
	return false, exitErr.ExitCode(), ""
}

// workerWorktreeGone reports whether w's worktree directory no longer exists.
func workerWorktreeGone(w *livework.WorkerInfo) bool {
	_, err := os.Stat(w.Worktree)
	return os.IsNotExist(err)
}

// workerKeepReason reads the facts KeepReason needs and returns its answer. A
// fact git cannot report reads in the direction that keeps the work. Commits
// are counted from the worktree's HEAD (a detached checkout holds commits no
// branch names) and from the branch, against every origin ref and the local
// default branch.
func workerKeepReason(ctx context.Context, repoRoot string, w *livework.WorkerInfo) string {
	def := git.DefaultBranch(ctx, repoRoot)
	if def == "" {
		return "no default branch to compare against"
	}
	clean := true
	var workerOnly []string
	treeGone := workerWorktreeGone(w)
	if !treeGone {
		st, err := git.WorkingStatusIgnored(ctx, w.Worktree)
		if err != nil {
			return "could not read its git status"
		}
		// Ignored files count: worktree removal deletes a worker-made .env
		// or build output as surely as tracked work.
		workerOnly = git.UnsharedIgnored(repoRoot, st.IgnoredPaths)
		clean = st.Clean() && len(workerOnly) == 0
	}
	count := func() (int, bool) {
		n, ok := 0, true
		if !treeGone {
			h, hok := git.CountUnpushedRev(ctx, w.Worktree, "HEAD", def)
			n, ok = max(n, h), ok && hok
		}
		if w.Branch != "" && git.BranchExistsLocal(ctx, repoRoot, w.Branch) {
			b, bok := git.CountUnpushed(ctx, repoRoot, w.Branch, def)
			n, ok = max(n, b), ok && bok
		}
		return n, ok
	}
	n, ok := count()
	if clean && (!ok || n > 0) {
		// The local tracking refs may simply be stale; refresh and ask again.
		fetchOrigin(ctx, repoRoot)
		n, ok = count()
	}
	return KeepReason(clean, workerOnly, n, ok)
}

// removeWorkerTree removes an ended worker's worktree and local branch when
// that loses nothing and pre_remove (ADR-0024) allows it. kept is why they
// stay, "" when both are gone.
//
// A keep the Repo tab can read back from git (uncommitted, unpushed, ignored
// files) is not recorded. A pre_remove veto or a failed removal is an event
// git cannot show again, so it goes to the worktree's kept note
// (internal/keptnote), which the Repo tab and `admin worktree list` show.
func removeWorkerTree(ctx context.Context, repoRoot string, w *livework.WorkerInfo) (kept string) {
	if reason := workerKeepReason(ctx, repoRoot, w); reason != "" {
		// Any earlier note described an event this state reason supersedes.
		if !workerWorktreeGone(w) {
			if err := keptnote.Clear(w.Worktree); err != nil {
				data.LogSync(filepath.Base(repoRoot), "kept-note", 0, "", fmt.Errorf("clearing %s: %w", w.Worktree, err))
			}
		}
		return reason
	}
	if workerWorktreeGone(w) {
		// A directory removed without `git worktree remove` stays registered
		// and keeps git from deleting its branch.
		if err := git.PruneWorktrees(ctx, repoRoot); err != nil {
			return "pruning its worktree record failed: " + err.Error()
		}
	} else {
		// The HEAD the hook and the removal judge, read before either can
		// move it, so a note is never stamped with a state it did not see.
		head, headErr := git.HeadSHA(ctx, w.Worktree)
		if err := wthooks.PreRemove(git.WithQuiet(ctx), repoRoot, w.Worktree, false); err != nil {
			reason := err.Error()
			var he *wthooks.Error
			if errors.As(err, &he) {
				reason = he.Line()
			}
			return recordKept(repoRoot, w.Worktree, head, headErr, reason)
		}
		if err := git.RemoveWorktree(ctx, repoRoot, w.Worktree, false); err != nil {
			return recordKept(repoRoot, w.Worktree, head, headErr, "removing it failed: "+err.Error())
		}
	}
	// -D, not -d: -d judges "merged" against the main checkout's HEAD.
	// workerKeepReason already proved origin holds every commit.
	if w.Branch != "" && git.BranchExistsLocal(ctx, repoRoot, w.Branch) {
		if err := git.DeleteBranch(ctx, repoRoot, w.Branch, true); err != nil {
			return "deleting branch " + w.Branch + " failed: " + err.Error()
		}
	}
	return ""
}

// recordKept writes reason to the worktree's kept note at head (headErr is the
// error reading it) and returns reason. A note that cannot be written is
// logged; the reap's outcome line still carries the reason.
func recordKept(repoRoot, worktree, head string, headErr error, reason string) string {
	err := headErr
	if err == nil {
		err = keptnote.Write(worktree, head, reason)
	}
	detail := fmt.Sprintf("worktree=%s head=%s reason=%q", worktree, head, reason)
	if err != nil {
		err = fmt.Errorf("writing kept note: %w: %s", err, detail)
	}
	data.LogSync(filepath.Base(repoRoot), "kept-note", 0, detail, err)
	return reason
}

// reapKilled holds the live ids of hosted workers the reaper stopped because
// they had finished, so their dispatch reports a finished target rather than
// the killed one a person's x key makes.
var reapKilled sync.Map

// ReapWorker ends e's session when it is over, reporting whether it did and,
// when it did, one line saying what happened to the worktree. A session is
// over when its launcher pane has gone or its hosted process has exited
// (workerSessionGone), or when its task is finished
// (workerFinished), in which case the reaper closes the pane first; a close
// that fails leaves the worker running with the reason on its row, retried
// next tick. Only a running or waiting worker is judged.
func ReapWorker(ctx context.Context, repoRoot string, e *livework.Entry, t config.Terminal) (ended bool, outcome string) {
	w := e.Worker
	if w == nil {
		return false, ""
	}
	st, ok := livework.Default.State(e.ID)
	if !ok || !st.Reapable() {
		return false, ""
	}
	start := time.Now()
	gone, why, probeErr := workerSessionGone(ctx, e, t)
	if !gone {
		finished, note := workerFinished(ctx, repoRoot, e)
		if !finished {
			if probeErr != nil {
				note = strings.TrimPrefix(note+"; "+probeErr.Error(), "; ")
			}
			livework.Default.SetNote(e.ID, note)
			return false, ""
		}
		if err := closeFinishedWorker(ctx, e, t); err != nil {
			livework.Default.SetNote(e.ID, "finished, but closing its session failed: "+err.Error())
			return false, ""
		}
		why = "finished"
	}
	kept := removeWorkerTree(ctx, repoRoot, w)
	doneRuns.Delete(e)
	livework.Default.SetNote(e.ID, "")
	livework.Default.SetState(e.ID, livework.StateExited, nil)
	ReleaseWorkerSlot(e.ID)

	outcome = fmt.Sprintf("%s %s: worktree and branch removed", e.ID, why)
	if kept != "" {
		outcome = fmt.Sprintf("%s %s: worktree left at %s (%s)", e.ID, why, w.Worktree, kept)
	}
	data.LogSync(filepath.Base(repoRoot), "reap", time.Since(start), outcome, nil)
	return true, outcome
}

// closeFinishedWorker stops a finished worker's session: the launcher's kill
// for a window worker, the entry's own Kill for a hosted one, whose pty has no
// launcher behind it.
func closeFinishedWorker(ctx context.Context, e *livework.Entry, t config.Terminal) error {
	if e.Via != "terminal" {
		if e.Ops.Kill == nil {
			return fmt.Errorf("hosted worker %s has no kill", e.ID)
		}
		reapKilled.Store(e.ID, true)
		if err := e.Ops.Kill(); err != nil {
			reapKilled.Delete(e.ID)
			return err
		}
		return nil
	}
	return closeFinishedPane(ctx, e.Handle, t)
}

// closeFinishedPane runs [terminal].kill on handle. A pane that is already
// gone makes kill fail; that is success, since closing it was the goal.
func closeFinishedPane(ctx context.Context, handle string, t config.Terminal) error {
	killErr := runLauncherShell(t.Kill, handle)
	if killErr == nil {
		return nil
	}
	if t.Status != "" {
		sctx, cancel := context.WithTimeout(ctx, doneTimeout)
		defer cancel()
		gone, err := PaneGone(sctx, t.Status, handle)
		if err == nil && gone {
			return nil
		}
		if err != nil {
			return fmt.Errorf("closing pane %s: %w (and %v)", handle, killErr, err)
		}
	}
	return fmt.Errorf("closing pane %s: %w", handle, killErr)
}
