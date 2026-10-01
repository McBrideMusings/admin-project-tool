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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// reapBase is the ref a worker's branch must be fully contained in.
const (
	reapBaseBranch = "main"
	reapBase       = "origin/" + reapBaseBranch
)

// doneTimeout bounds one done-predicate run, so a wedged predicate reads as
// "not finished" rather than stalling the tick.
var doneTimeout = 5 * time.Second

// fetchInterval is the least time between two fetches of one repo's origin
// by the reaper, so a finished worker that stays unmerged does not trigger a
// fetch on every dashboard tick.
var fetchInterval = 30 * time.Second

var (
	fetchMu   sync.Mutex
	fetchedAt = map[string]time.Time{}
)

// fetchOrigin refreshes repoRoot's remote-tracking refs, at most once per
// fetchInterval. Landing a branch moves origin/main on the remote; without
// this the local origin/main never learns the branch is merged.
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

// ReapDecision is the reap rule over its three inputs. A worker that has not
// finished yields no reason: there is nothing to report about it. A finished
// worker that is not safe to delete yields the reason it stays.
func ReapDecision(finished, clean, merged bool) (reap bool, reason string) {
	switch {
	case !finished:
		return false, ""
	case !clean:
		return false, "finished, not removed: worktree has uncommitted changes or ignored files"
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
// exits nonzero is "not finished"; so is a worker with no done predicate and
// no [terminal].status to ask. note is non-empty when the predicate could not
// give an answer at all (it timed out or would not start), so the row says
// why it is not progressing instead of looking merely unfinished.
func workerFinished(ctx context.Context, w *livework.WorkerInfo, handle string, t config.Terminal) (finished bool, note string) {
	if w.Done != "" {
		ctx, cancel := context.WithTimeout(ctx, doneTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", w.Done)
		cmd.Dir = w.Worktree
		cmd.Env = append(os.Environ(), "ADMIN_TARGET="+w.Target, "ADMIN_HANDLE="+handle)
		cmd.WaitDelay = time.Second
		err := cmd.Run()
		if err == nil {
			return true, ""
		}
		var exitErr *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return false, fmt.Sprintf("done predicate timed out after %s", doneTimeout)
		case !errors.As(err, &exitErr):
			return false, "done predicate failed to run: " + err.Error()
		}
		return false, ""
	}
	if t.Status == "" {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(ctx, doneTimeout)
	defer cancel()
	gone, err := PaneGone(ctx, t.Status, handle)
	return err == nil && gone, ""
}

// workerSafety reads the facts ReapDecision needs. A fact git cannot report
// reads as false, the direction that keeps the work. baseKnown is false when
// reapBase does not resolve at all (no origin remote, never fetched), which
// is a different problem from a branch that holds commits the base lacks.
func workerSafety(ctx context.Context, repoRoot string, w *livework.WorkerInfo) (clean, merged, baseKnown bool) {
	if w.Branch == "" {
		return false, false, true
	}
	if _, err := os.Stat(w.Worktree); os.IsNotExist(err) {
		// Already removed (by a failed earlier reap or by hand): nothing on
		// disk left to lose.
		clean = true
	} else if st, err := git.WorkingStatusIgnored(ctx, w.Worktree); err == nil {
		// Ignored files count: worktree removal deletes a worker-made .env
		// or build output as surely as tracked work. An ignored path that
		// also exists in the primary checkout (the seeded admin.toml,
		// node_modules) is not the worker's to lose.
		clean = st.Clean() && !hasWorkerOnlyIgnored(repoRoot, st.IgnoredPaths)
	}
	baseKnown = git.BranchExistsRemote(ctx, repoRoot, reapBaseBranch)
	if n, ok := git.CountAheadOf(ctx, repoRoot, reapBase, w.Branch); ok && baseKnown && n == 0 {
		return clean, true, true
	}
	// Not merged as far as the local refs say; origin/main may simply be
	// stale, so refresh it and ask once more.
	fetchOrigin(ctx, repoRoot)
	baseKnown = git.BranchExistsRemote(ctx, repoRoot, reapBaseBranch)
	if n, ok := git.CountAheadOf(ctx, repoRoot, reapBase, w.Branch); ok && baseKnown {
		merged = n == 0
	}
	return clean, merged, baseKnown
}

// hasWorkerOnlyIgnored reports whether any ignored path is absent from the
// primary checkout at repoRoot.
func hasWorkerOnlyIgnored(repoRoot string, paths []string) bool {
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(repoRoot, strings.TrimSuffix(p, "/"))); err != nil {
			return true
		}
	}
	return false
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
	finished, note := workerFinished(ctx, w, e.Handle, t)
	if !finished {
		if note == "" && w.Done != "" && t.Status != "" {
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
	clean, merged, baseKnown := workerSafety(ctx, repoRoot, w)
	if !baseKnown {
		livework.Default.SetNote(e.ID, "finished, not removed: "+reapBase+" does not exist (no origin remote, or never fetched)")
		return false
	}
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
