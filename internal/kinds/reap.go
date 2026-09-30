// reap.go retires a window worker once it has finished: the dashboard tick
// calls ReapWorker for every live worker row, and a worker whose work is safe
// to delete has its pane closed, its worktree and branch removed, and its
// ceiling slot freed so a queued target starts.
//
// What "finished" means comes from the manifest: the action's `done`
// predicate (exit 0 = finished), or, with no `done`, the launcher pane having
// gone. What "safe to delete" means does not: the worktree must be clean and
// the branch must hold no commit origin/main lacks, checked here in Go so no
// manifest can loosen it.
package kinds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// reapBase is the ref a worker's branch must be fully contained in.
const reapBase = "origin/main"

// doneTimeout bounds one done-predicate run, so a wedged predicate reads as
// "not finished" rather than stalling the tick.
var doneTimeout = 5 * time.Second

// ReapDecision is the reap rule over its three inputs. A worker that has not
// finished yields no reason: there is nothing to report about it. A finished
// worker that is not safe to delete yields the reason it stays.
func ReapDecision(finished, clean, merged bool) (reap bool, reason string) {
	switch {
	case !finished:
		return false, ""
	case !clean:
		return false, "finished, not removed: worktree has uncommitted changes"
	case !merged:
		return false, "finished, not removed: branch has commits not on " + reapBase
	}
	return true, ""
}

// PaneGone runs a [terminal].status command with ADMIN_HANDLE set to handle.
// A nonzero exit reports gone=true. Any other failure — a timeout, a shell
// that would not start — is err: it says nothing definite about the pane.
func PaneGone(ctx context.Context, statusCmd, handle string) (gone bool, err error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", statusCmd)
	cmd.Env = append(os.Environ(), "ADMIN_HANDLE="+handle)
	runErr := cmd.Run()
	if runErr == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return true, nil
	}
	return false, runErr
}

// workerFinished reports whether w's work is over. A done predicate that
// errors, times out, or exits nonzero is "not finished"; so is a worker with
// no done predicate and no [terminal].status to ask.
func workerFinished(ctx context.Context, w *livework.WorkerInfo, handle string, t config.Terminal) bool {
	if w.Done != "" {
		ctx, cancel := context.WithTimeout(ctx, doneTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", w.Done)
		cmd.Dir = w.Worktree
		cmd.Env = append(os.Environ(), "ADMIN_TARGET="+w.Target, "ADMIN_HANDLE="+handle)
		cmd.WaitDelay = time.Second
		return cmd.Run() == nil
	}
	if t.Status == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, doneTimeout)
	defer cancel()
	gone, err := PaneGone(ctx, t.Status, handle)
	return err == nil && gone
}

// workerSafety reads the two facts ReapDecision needs. A fact git cannot
// report reads as false, the direction that keeps the work.
func workerSafety(ctx context.Context, repoRoot string, w *livework.WorkerInfo) (clean, merged bool) {
	if w.Branch == "" {
		return false, false
	}
	if _, err := os.Stat(w.Worktree); os.IsNotExist(err) {
		// Already removed (by a failed earlier reap or by hand): nothing on
		// disk left to lose.
		clean = true
	} else if st, err := git.WorkingStatus(ctx, w.Worktree); err == nil {
		clean = st.Clean()
	}
	if n, ok := git.CountAheadOf(ctx, repoRoot, reapBase, w.Branch); ok {
		merged = n == 0
	}
	return clean, merged
}

// panesClosed holds the live ids whose pane a reap has already closed.
var panesClosed sync.Map

// ReapWorker retires e when its worker has finished and its work is safe to
// delete, reporting whether it did. When the worker finished but the work is
// not safe, everything stays in place and the reason lands on the entry's
// Note. Only a window worker is ever reaped: an entry without WorkerInfo, or
// not running or waiting, is left alone.
func ReapWorker(ctx context.Context, repoRoot string, e *livework.Entry, t config.Terminal) bool {
	w := e.Worker
	if w == nil {
		return false
	}
	if st, ok := livework.Default.State(e.ID); !ok || (st != livework.StateRunning && st != livework.StateWaiting) {
		return false
	}
	if !workerFinished(ctx, w, e.Handle, t) {
		note := ""
		if w.Done != "" && t.Status != "" {
			// A worker that declares done is finished only when done passes;
			// a pane that has gone without it is a worker that died. Leave
			// the row for a person to inspect and kill.
			sctx, cancel := context.WithTimeout(ctx, doneTimeout)
			defer cancel()
			if gone, err := PaneGone(sctx, t.Status, e.Handle); err == nil && gone {
				note = "pane closed before done passed"
			}
		}
		livework.Default.SetNote(e.ID, note)
		return false
	}
	clean, merged := workerSafety(ctx, repoRoot, w)
	reap, reason := ReapDecision(true, clean, merged)
	if !reap {
		livework.Default.SetNote(e.ID, reason)
		return false
	}
	if _, closed := panesClosed.Load(e.ID); !closed {
		if err := closeFinishedPane(ctx, e.Handle, t); err != nil {
			livework.Default.SetNote(e.ID, "finished, not removed: "+err.Error())
			return false
		}
		// A later tick retrying a failed worktree or branch removal must not
		// run kill again on a pane that is already closed.
		panesClosed.Store(e.ID, true)
	}
	if _, err := os.Stat(w.Worktree); err == nil {
		if err := git.RemoveWorktree(ctx, repoRoot, w.Worktree, false); err != nil {
			livework.Default.SetNote(e.ID, "finished, not removed: worktree: "+err.Error())
			return false
		}
	}
	// -D, not -d: -d judges "merged" against the main checkout's HEAD, which
	// may not have pulled yet. workerSafety already proved the branch holds
	// nothing origin/main lacks.
	if err := git.DeleteBranch(ctx, repoRoot, w.Branch, true); err != nil {
		livework.Default.SetNote(e.ID, "finished, not removed: branch: "+err.Error())
		return false
	}
	panesClosed.Delete(e.ID)
	livework.Default.SetNote(e.ID, "")
	livework.Default.SetState(e.ID, livework.StateExited, nil)
	ReleaseWorkerSlot(e.ID)
	return true
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
		if gone, err := PaneGone(sctx, t.Status, handle); err == nil && gone {
			return nil
		}
	}
	return fmt.Errorf("closing pane %s: %w", handle, killErr)
}
