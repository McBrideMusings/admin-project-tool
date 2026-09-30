package kinds

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

func TestReapDecision(t *testing.T) {
	tests := []struct {
		name                    string
		finished, clean, merged bool
		wantReap                bool
		wantReason              string
	}{
		{"finished clean merged reaps", true, true, true, true, ""},
		{"finished dirty stays", true, false, true, false, "uncommitted"},
		{"finished unmerged stays", true, true, false, false, "origin/main"},
		{"not finished says nothing", false, true, true, false, ""},
		{"not finished dirty says nothing", false, false, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reap, reason := ReapDecision(tt.finished, tt.clean, tt.merged)
			if reap != tt.wantReap {
				t.Errorf("reap = %v, want %v", reap, tt.wantReap)
			}
			if tt.wantReason == "" && reason != "" {
				t.Errorf("reason = %q, want empty", reason)
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", reason, tt.wantReason)
			}
		})
	}
}

// addOrigin gives repoRoot an origin whose main is its own, so origin/main
// resolves.
func addOrigin(t *testing.T, repoRoot string) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	wfGitT(t, repoRoot, "init", "-q", "--bare", bare)
	wfGitT(t, repoRoot, "remote", "add", "origin", bare)
	wfGitT(t, repoRoot, "push", "-q", "origin", "main")
}

// dispatchWorker dispatches one target whose done predicate is done and
// returns its live entry.
func dispatchWorker(t *testing.T, repoRoot, done string) *livework.Entry {
	t.Helper()
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) { return "pane-1", nil })
	res := DispatchTargets(repoRoot, "reap", workflowAction(),
		[]Target{{Name: "t0", Run: "x", Done: done}}, SurfaceDashboard)
	if res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	e, ok := livework.Default.Get(workerLiveID("reap", "t0"))
	if !ok {
		t.Fatal("worker not in the live registry")
	}
	return e
}

func branchExists(t *testing.T, repoRoot, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func TestReapWorker(t *testing.T) {
	tests := []struct {
		name       string
		done       string
		prepare    func(t *testing.T, w *livework.WorkerInfo)
		wantReaped bool
		wantNote   string
	}{
		{name: "done passes, clean, merged: reaped", done: "true", wantReaped: true},
		{
			name: "done passes, dirty: stays with reason", done: "true", wantNote: "uncommitted",
			prepare: func(t *testing.T, w *livework.WorkerInfo) {
				if err := os.WriteFile(filepath.Join(w.Worktree, "scratch.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "done passes, unmerged: stays with reason", done: "true", wantNote: "origin/main",
			prepare: func(t *testing.T, w *livework.WorkerInfo) {
				if err := os.WriteFile(filepath.Join(w.Worktree, "work.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				wfGitT(t, w.Worktree, "add", "-A")
				wfGitT(t, w.Worktree, "commit", "-qm", "work")
			},
		},
		{name: "done exits nonzero: stays", done: "false"},
		{name: "done times out: stays", done: "sleep 5"},
		{name: "done sees ADMIN_TARGET", done: `test "$ADMIN_TARGET" = t0`, wantReaped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot := setupWorkflowRepo(t, terminalTOML)
			addOrigin(t, repoRoot)
			prev := doneTimeout
			doneTimeout = 300 * time.Millisecond
			t.Cleanup(func() { doneTimeout = prev })

			e := dispatchWorker(t, repoRoot, tt.done)
			w := e.Worker
			if tt.prepare != nil {
				tt.prepare(t, w)
			}

			got := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
			if got != tt.wantReaped {
				t.Fatalf("ReapWorker = %v, want %v", got, tt.wantReaped)
			}
			_, statErr := os.Stat(w.Worktree)
			st, _ := livework.Default.State(e.ID)
			note, _ := livework.Default.Line(e.ID)
			if tt.wantReaped {
				if !os.IsNotExist(statErr) {
					t.Errorf("worktree %s still exists after reap", w.Worktree)
				}
				if branchExists(t, repoRoot, w.Branch) {
					t.Errorf("branch %s still exists after reap", w.Branch)
				}
				if st != livework.StateExited {
					t.Errorf("state = %s, want exited", st)
				}
				return
			}
			if statErr != nil {
				t.Errorf("worktree removed though the worker was not reaped: %v", statErr)
			}
			if !branchExists(t, repoRoot, w.Branch) {
				t.Errorf("branch %s deleted though the worker was not reaped", w.Branch)
			}
			if st != livework.StateRunning {
				t.Errorf("state = %s, want running", st)
			}
			if !strings.Contains(note, tt.wantNote) {
				t.Errorf("row line = %q, want it to contain %q", note, tt.wantNote)
			}
		})
	}
}

// With no done predicate, the pane having gone is the finish signal.
func TestReapWorkerNoDoneUsesPaneStatus(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "")

	alive := config.Terminal{Kill: "true", Status: "true"}
	if ReapWorker(context.Background(), repoRoot, e, alive) {
		t.Fatal("reaped a worker whose pane is still alive")
	}
	if ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"}) {
		t.Fatal("reaped a worker with no done and no status command")
	}
	gone := config.Terminal{Kill: "false", Status: "false"}
	if !ReapWorker(context.Background(), repoRoot, e, gone) {
		t.Fatal("did not reap a worker whose pane has gone")
	}
}

// A queued target starts after a reap frees its slot, and not while a
// finished-but-unlanded worker holds it.
func TestReapFreesSlotForQueuedTarget(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 1\n\n"+terminalTOML)
	addOrigin(t, repoRoot)
	var spawned int32
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		return fmt.Sprintf("pane-%d", atomic.AddInt32(&spawned, 1)), nil
	})

	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "slot", workflowAction(),
			[]Target{{Name: "t0", Run: "x", Done: "true"}, {Name: "t1", Run: "x"}}, SurfaceDashboard)
	}()
	for atomic.LoadInt32(&spawned) < 1 {
		time.Sleep(time.Millisecond)
	}
	e, ok := livework.Default.Get(workerLiveID("slot", "t0"))
	if !ok {
		t.Fatal("t0 not registered")
	}

	// Finished but dirty: unlanded work keeps the slot.
	if err := os.WriteFile(filepath.Join(e.Worker.Worktree, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"}) {
		t.Fatal("reaped a dirty worker")
	}
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&spawned); got != 1 {
		t.Fatalf("spawned while an unlanded worker held the slot = %d, want 1", got)
	}

	if err := os.Remove(filepath.Join(e.Worker.Worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	if !ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"}) {
		t.Fatal("did not reap the clean, merged worker")
	}
	for _, r := range <-done {
		if r.Err != nil {
			t.Errorf("target %q: %v", r.Target, r.Err)
		}
	}
	if got := atomic.LoadInt32(&spawned); got != 2 {
		t.Errorf("spawned after the reap = %d, want 2", got)
	}
}

// TestPaneGoneExitCode exercises the real /bin/sh exec path (not the
// paneStatusFn seam): a nonzero exit is the "gone" signal, a zero exit means
// alive, and neither case produces an err — err is reserved for a query
// that couldn't run to completion at all.
func TestPaneGoneExitCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	gone, err := PaneGone(ctx, "exit 1", "handle")
	if err != nil {
		t.Fatalf("PaneGone(exit 1) err = %v, want nil", err)
	}
	if !gone {
		t.Error("PaneGone(exit 1) gone = false, want true")
	}

	gone, err = PaneGone(ctx, "exit 0", "handle")
	if err != nil {
		t.Fatalf("PaneGone(exit 0) err = %v, want nil", err)
	}
	if gone {
		t.Error("PaneGone(exit 0) gone = true, want false")
	}
}

// TestPaneGonePassesHandleEnv proves ADMIN_HANDLE reaches the command,
// exactly the env shape ADR-0020's attach/kill contract already uses.
func TestPaneGonePassesHandleEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	gone, err := PaneGone(ctx, `test "$ADMIN_HANDLE" = "pane-42"`, "pane-42")
	if err != nil {
		t.Fatalf("PaneGone err = %v, want nil", err)
	}
	if gone {
		t.Error("PaneGone gone = true, want false (handle matched, command exited 0)")
	}
}

// A worker that declares done and whose pane has gone without done passing
// is left in place with a reason.
func TestReapWorkerDiedBeforeDone(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "false")
	if ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true", Status: "false"}) {
		t.Fatal("reaped a worker whose done never passed")
	}
	if note, _ := livework.Default.Line(e.ID); !strings.Contains(note, "pane closed before done") {
		t.Errorf("row line = %q, want the pane-closed reason", note)
	}
}

// A reap whose worktree removal failed retries on the next tick without
// running kill on the already-closed pane again.
func TestReapWorkerRetryDoesNotKillTwice(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "true")
	killLog := filepath.Join(t.TempDir(), "kills")
	term := config.Terminal{Kill: `echo k >> ` + killLog}

	// Make the removal fail: a locked worktree refuses `worktree remove`.
	wfGitT(t, repoRoot, "worktree", "lock", e.Worker.Worktree)
	if ReapWorker(context.Background(), repoRoot, e, term) {
		t.Fatal("reaped a locked worktree")
	}
	wfGitT(t, repoRoot, "worktree", "unlock", e.Worker.Worktree)
	if !ReapWorker(context.Background(), repoRoot, e, term) {
		t.Fatal("retry did not reap")
	}
	out, err := os.ReadFile(killLog)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "k"); n != 1 {
		t.Errorf("kill ran %d times, want 1", n)
	}
}
