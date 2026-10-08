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
	"github.com/McBrideMusings/admin-project-tool/internal/wthooks"
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

// maxNamedIgnored is how many worker-only ignored paths a row note names.
const maxNamedIgnored = 3

// ReapDecision is the reap rule over its inputs. A worker that has not
// finished yields no reason: there is nothing to report about it. A finished
// worker that is not safe to delete yields the reason it stays. workerOnly is
// the ignored paths workerSafety found that the primary checkout lacks; when
// the worktree is dirty because of them, the reason names them.
func ReapDecision(finished, clean, merged bool, workerOnly []string) (reap bool, reason string) {
	switch {
	case !finished:
		return false, ""
	case !clean && len(workerOnly) > 0:
		named := workerOnly
		more := ""
		if len(named) > maxNamedIgnored {
			more = fmt.Sprintf(" (+%d more)", len(named)-maxNamedIgnored)
			named = named[:maxNamedIgnored]
		}
		return false, "finished, not removed: worktree has ignored files not in the primary checkout: " + strings.Join(named, ", ") + more
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
	if ctx.Err() != nil {
		// Killed by the deadline: the exit says nothing about the pane.
		return false, ctx.Err()
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
func workerFinished(ctx context.Context, e *livework.Entry, t config.Terminal) (finished bool, note string) {
	w, handle := e.Worker, e.Handle
	if workerWorktreeGone(w) {
		// The done predicate runs in the worktree, so it cannot run at all.
		// The directory is gone (removed by hand); workerSafety decides
		// whether the branch is safe to drop.
		return true, ""
	}
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
	if t.Status == "" || e.Via != "terminal" {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(ctx, doneTimeout)
	defer cancel()
	gone, err := PaneGone(ctx, t.Status, handle)
	return err == nil && gone, ""
}

// workerWorktreeGone reports whether w's worktree directory no longer exists.
func workerWorktreeGone(w *livework.WorkerInfo) bool {
	_, err := os.Stat(w.Worktree)
	return os.IsNotExist(err)
}

// workerSafety reads the facts ReapDecision needs. A fact git cannot report
// reads as false, the direction that keeps the work. baseKnown is false when
// reapBase does not resolve at all (no origin remote, never fetched), which
// is a different problem from a branch that holds commits the base lacks.
func workerSafety(ctx context.Context, repoRoot string, w *livework.WorkerInfo) (clean, merged, baseKnown bool, workerOnly []string) {
	if w.Branch == "" {
		return false, false, true, nil
	}
	if workerWorktreeGone(w) {
		// Already removed (by a failed earlier reap or by hand): nothing on
		// disk left to lose.
		clean = true
		if !git.BranchExistsLocal(ctx, repoRoot, w.Branch) {
			// The branch went with it, so there is no work left either.
			return true, true, true, nil
		}
	} else if st, err := git.WorkingStatusIgnored(ctx, w.Worktree); err == nil {
		// Ignored files count: worktree removal deletes a worker-made .env
		// or build output as surely as tracked work. An ignored path that
		// also exists in the primary checkout (the seeded admin.toml) is not
		// the worker's to lose, and a dependency install directory is
		// regenerable.
		workerOnly = workerOnlyIgnored(repoRoot, st.IgnoredPaths)
		clean = st.Clean() && len(workerOnly) == 0
	}
	baseKnown = git.BranchExistsRemote(ctx, repoRoot, reapBaseBranch)
	if n, ok := git.CountAheadOf(ctx, repoRoot, reapBase, w.Branch); ok && baseKnown && n == 0 {
		return clean, true, true, workerOnly
	}
	// Not merged as far as the local refs say; origin/main may simply be
	// stale, so refresh it and ask once more.
	fetchOrigin(ctx, repoRoot)
	baseKnown = git.BranchExistsRemote(ctx, repoRoot, reapBaseBranch)
	if n, ok := git.CountAheadOf(ctx, repoRoot, reapBase, w.Branch); ok && baseKnown {
		merged = n == 0
	}
	return clean, merged, baseKnown, workerOnly
}

// regenerableIgnored reports whether an ignored path is a dependency install
// directory: a package manager recreates it, so deleting it loses nothing.
// Build output and .env files are not regenerable and stay blocking.
func regenerableIgnored(p string) bool {
	return filepath.Base(strings.TrimSuffix(p, "/")) == "node_modules"
}

// workerOnlyIgnored returns the ignored paths absent from the primary checkout
// at repoRoot, leaving out regenerable ones.
func workerOnlyIgnored(repoRoot string, paths []string) []string {
	var out []string
	for _, p := range paths {
		if regenerableIgnored(p) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(repoRoot, strings.TrimSuffix(p, "/"))); err != nil {
			out = append(out, p)
		}
	}
	return out
}

// reapKilled holds the live ids of hosted workers the reaper stopped because
// they had finished, so their dispatch reports a finished target rather than
// the killed one a person's x key makes.
var reapKilled sync.Map

// panesClosed holds the live ids whose pane a reap has already closed.
var panesClosed sync.Map

// preRemoveVetoed holds, per live entry (one per dispatch, so a later dispatch
// of the same target starts fresh), when pre_remove last vetoed that
// worker's reap. The reaper runs every tick, and a hook with side effects
// (draining a server, releasing a port) must not run once a second while it
// keeps saying no; it is asked again only after preRemoveRetry.
var preRemoveVetoed sync.Map

// preRemovePassed holds the live entries whose pre_remove has already passed,
// so a reap retried after a failed pane kill or worktree removal goes on
// without running the hook again.
var preRemovePassed sync.Map

// preRemoveRetry is how long a vetoed reap waits before running pre_remove
// again. A var so tests can shorten it.
var preRemoveRetry = 30 * time.Second

// ReapWorker retires e when its worker has finished and its work is safe to
// delete, reporting whether it did. When the worker finished but the work is
// not safe, everything stays in place and the reason lands on the entry's
// Note. Only a worker is ever reaped: an entry without WorkerInfo, or
// not running or waiting, is left alone.
func ReapWorker(ctx context.Context, repoRoot string, e *livework.Entry, t config.Terminal) bool {
	w := e.Worker
	if w == nil {
		return false
	}
	st, ok := livework.Default.State(e.ID)
	if !ok || !st.Reapable() {
		return false
	}
	finished, note := workerFinished(ctx, e, t)
	if !finished {
		stopped := false
		if note == "" && w.Done != "" && t.Status != "" && e.Via == "terminal" {
			// A worker that declares done is finished only when done passes;
			// a pane that has gone without it is a worker that died. Leave
			// the row for a person to inspect and kill.
			sctx, cancel := context.WithTimeout(ctx, doneTimeout)
			defer cancel()
			if gone, err := PaneGone(sctx, t.Status, e.Handle); err == nil && gone {
				note = "pane closed before done passed"
				stopped = true
			}
		}
		switch {
		case stopped:
			livework.Default.SetState(e.ID, livework.StateStopped, nil)
		case st == livework.StateFinished:
			livework.Default.SetState(e.ID, livework.StateRunning, nil)
		}
		livework.Default.SetNote(e.ID, note)
		return false
	}
	clean, merged, baseKnown, workerOnly := workerSafety(ctx, repoRoot, w)
	if !baseKnown {
		holdFinished(e.ID, "finished, not removed: "+reapBase+" does not exist (no origin remote, or never fetched)")
		return false
	}
	reap, reason := ReapDecision(true, clean, merged, workerOnly)
	if !reap && !merged && workerWorktreeGone(w) {
		reason = "finished, not removed: worktree is gone and branch has commits not on " + reapBase
	}
	if !reap {
		holdFinished(e.ID, reason)
		return false
	}
	if _, closed := panesClosed.Load(e.ID); !closed {
		// pre_remove (ADR-0024) can veto, so it runs before the pane closes and
		// a veto leaves the worker as it was.
		if !preRemoveAllows(ctx, repoRoot, e) {
			return false
		}
		if err := closeFinishedWorker(ctx, e, t); err != nil {
			holdFinished(e.ID, "finished, not removed: "+err.Error())
			return false
		}
		// A later tick retrying a failed worktree or branch removal must not
		// run kill again on a pane that is already closed.
		panesClosed.Store(e.ID, true)
	}
	if workerWorktreeGone(w) {
		// A directory removed without `git worktree remove` stays registered
		// and keeps git from deleting its branch.
		if err := git.PruneWorktrees(ctx, repoRoot); err != nil {
			holdFinished(e.ID, "finished, not removed: worktree: "+err.Error())
			return false
		}
	} else {
		if err := git.RemoveWorktree(ctx, repoRoot, w.Worktree, false); err != nil {
			holdFinished(e.ID, "finished, not removed: worktree: "+err.Error())
			return false
		}
	}
	// -D, not -d: -d judges "merged" against the main checkout's HEAD, which
	// may not have pulled yet. workerSafety already proved the branch holds
	// nothing origin/main lacks.
	if !git.BranchExistsLocal(ctx, repoRoot, w.Branch) {
		// Already deleted by hand.
	} else if err := git.DeleteBranch(ctx, repoRoot, w.Branch, true); err != nil {
		holdFinished(e.ID, "finished, not removed: branch: "+err.Error())
		return false
	}
	panesClosed.Delete(e.ID)
	preRemoveVetoed.Delete(e)
	preRemovePassed.Delete(e)
	livework.Default.SetNote(e.ID, "")
	livework.Default.SetState(e.ID, livework.StateExited, nil)
	ReleaseWorkerSlot(e.ID)
	return true
}

// preRemoveAllows runs pre_remove on the worker's worktree and reports
// whether the reap may go on. A veto holds the worker with the hook's reason
// on its row, and for preRemoveRetry after one the hook is not run again: the
// row keeps the last reason. Once the hook passes it is never run again for e.
func preRemoveAllows(ctx context.Context, repoRoot string, e *livework.Entry) bool {
	if _, ok := preRemovePassed.Load(e); ok {
		return true
	}
	if at, ok := preRemoveVetoed.Load(e); ok && time.Since(at.(time.Time)) < preRemoveRetry {
		return false
	}
	err := wthooks.PreRemove(git.WithQuiet(ctx), repoRoot, e.Worker.Worktree, false)
	if err == nil {
		preRemoveVetoed.Delete(e)
		preRemovePassed.Store(e, true)
		return true
	}
	preRemoveVetoed.Store(e, time.Now())
	reason := err.Error()
	var he *wthooks.Error
	if errors.As(err, &he) {
		reason = he.Line()
	}
	holdFinished(e.ID, "finished, not removed: "+reason)
	return false
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
		if gone, err := PaneGone(sctx, t.Status, handle); err == nil && gone {
			return nil
		}
	}
	return fmt.Errorf("closing pane %s: %w", handle, killErr)
}

// holdFinished records why a finished worker was not removed. The worker's
// work is over, so the row says finished rather than running, and it stays
// reapable so a later tick retries once the cause clears.
func holdFinished(id, note string) {
	livework.Default.SetNote(id, note)
	livework.Default.SetState(id, livework.StateFinished, nil)
}
