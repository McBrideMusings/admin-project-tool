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
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

func TestKeepReason(t *testing.T) {
	tests := []struct {
		name       string
		clean      bool
		workerOnly []string
		unpushed   int
		counted    bool
		want       string
	}{
		{"clean and on origin removes", true, nil, 0, true, ""},
		{"dirty keeps", false, nil, 0, true, "uncommitted"},
		{"worker-only ignored names up to 3", false, []string{"a/.env", "b/dist/", "c/out/", "d/x/"}, 0, true, "a/.env, b/dist/, c/out/ (+1 more)"},
		{"commits on no origin branch keep", true, nil, 2, true, "2 commit(s) on no origin branch"},
		{"uncountable keeps", true, nil, 0, false, "could not tell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KeepReason(tt.clean, tt.workerOnly, tt.unpushed, tt.counted)
			if tt.want == "" && got != "" {
				t.Errorf("reason = %q, want empty", got)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("reason = %q, want it to contain %q", got, tt.want)
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

// commitIn commits one new file in dir.
func commitIn(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	wfGitT(t, dir, "add", "-A")
	wfGitT(t, dir, "commit", "-qm", "work")
}

// assertEnded checks e ended, its worktree and branch are removed or kept as
// removed says, and its outcome contains want.
func assertEnded(t *testing.T, repoRoot string, e *livework.Entry, ended bool, outcome string, removed bool, want string) {
	t.Helper()
	if !ended {
		line, _ := livework.Default.Line(e.ID)
		t.Fatalf("ReapWorker did not end the worker; row line = %q", line)
	}
	if st, _ := livework.Default.State(e.ID); st != livework.StateExited {
		t.Errorf("state = %s, want exited", st)
	}
	if !strings.Contains(outcome, want) {
		t.Errorf("outcome = %q, want it to contain %q", outcome, want)
	}
	_, statErr := os.Stat(e.Worker.Worktree)
	if removed {
		if !os.IsNotExist(statErr) {
			t.Errorf("worktree %s still exists", e.Worker.Worktree)
		}
		if branchExists(t, repoRoot, e.Worker.Branch) {
			t.Errorf("branch %s still exists", e.Worker.Branch)
		}
		return
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		t.Errorf("stat worktree: %v", statErr)
	}
	if !branchExists(t, repoRoot, e.Worker.Branch) {
		t.Errorf("branch %s deleted though its work was kept", e.Worker.Branch)
	}
}

func TestReapWorker(t *testing.T) {
	tests := []struct {
		name    string
		done    string
		prepare func(t *testing.T, w *livework.WorkerInfo)
		ended   bool
		removed bool
		want    string
	}{
		{name: "done passes, clean: worktree removed", done: "true", ended: true, removed: true, want: "finished: worktree and branch removed"},
		{
			name: "done passes, dirty: ends, worktree kept", done: "true", ended: true, want: "uncommitted",
			prepare: func(t *testing.T, w *livework.WorkerInfo) {
				if err := os.WriteFile(filepath.Join(w.Worktree, "scratch.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "done passes, commit on no origin branch: ends, worktree kept", done: "true", ended: true, want: "1 commit(s) on no origin branch",
			prepare: func(t *testing.T, w *livework.WorkerInfo) { commitIn(t, w.Worktree) },
		},
		{
			// An open PR's branch: pushed, never merged. Nothing is only local.
			name: "done passes, branch pushed but unmerged: worktree removed", done: "true", ended: true, removed: true,
			prepare: func(t *testing.T, w *livework.WorkerInfo) {
				commitIn(t, w.Worktree)
				wfGitT(t, w.Worktree, "push", "-q", "origin", w.Branch)
			},
		},
		{
			name: "detached HEAD with a commit on no origin branch: kept", done: "true", ended: true, want: "on no origin branch",
			prepare: func(t *testing.T, w *livework.WorkerInfo) {
				wfGitT(t, w.Worktree, "checkout", "-q", "--detach")
				commitIn(t, w.Worktree)
			},
		},
		{name: "done exits nonzero: keeps running", done: "false"},
		{name: "done times out: keeps running", done: "sleep 5"},
		{name: "done sees ADMIN_TARGET", done: `test "$ADMIN_TARGET" = t0`, ended: true, removed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot := setupWorkflowRepo(t, terminalTOML)
			addOrigin(t, repoRoot)
			prev := doneTimeout
			doneTimeout = 300 * time.Millisecond
			t.Cleanup(func() { doneTimeout = prev })

			e := dispatchWorker(t, repoRoot, tt.done)
			if tt.prepare != nil {
				tt.prepare(t, e.Worker)
			}
			ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
			if !tt.ended {
				if ended {
					t.Fatalf("ReapWorker ended a worker whose done never passed: %q", outcome)
				}
				if st, _ := livework.Default.State(e.ID); st != livework.StateRunning {
					t.Errorf("state = %s, want running", st)
				}
				if _, err := os.Stat(e.Worker.Worktree); err != nil {
					t.Errorf("worktree touched though the session runs: %v", err)
				}
				return
			}
			assertEnded(t, repoRoot, e, ended, outcome, tt.removed, tt.want)
		})
	}
}

// A pane a person closed ends the worker even though done never passed: the
// session is gone. Its work is removed when origin holds all of it.
func TestReapWorkerPaneClosedBeforeDone(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	done, runs := countingDone(t)
	e := dispatchWorker(t, repoRoot, done)
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "false", Status: "false"})
	assertEnded(t, repoRoot, e, ended, outcome, true, "pane closed: worktree and branch removed")
	if n := runs(); n != 0 {
		t.Errorf("done ran %d times for a worker whose pane had gone, want 0", n)
	}
}

// Same, but the worktree holds a commit origin lacks: the row still ends, and
// the work stays on disk.
func TestReapWorkerPaneClosedKeepsLocalWork(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "false")
	commitIn(t, e.Worker.Worktree)
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true", Status: "false"})
	assertEnded(t, repoRoot, e, ended, outcome, false, "pane closed: worktree left at "+e.Worker.Worktree)
}

// A pre_remove veto (ADR-0024) keeps the worktree and branch, and its one-line
// output is the outcome's reason; the session still ends.
func TestReapWorkerPreRemoveVeto(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML+"[worktrees.hooks]\npre_remove = 'echo draining; echo still-serving; exit 1'\n")
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "true")
	killed := filepath.Join(t.TempDir(), "killed")
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "touch " + killed})
	assertEnded(t, repoRoot, e, ended, outcome, false, "pre_remove hook failed (exit 1): still-serving")
	if _, err := os.Stat(killed); err != nil {
		t.Error("pane not closed though done passed")
	}
	if strings.Contains(outcome, "\n") {
		t.Errorf("outcome = %q, want one line", outcome)
	}
}

// A pane close that fails leaves the worker running with the reason on its
// row; the next tick tries again.
func TestReapWorkerFailedCloseKeepsRunning(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "true")
	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "false"}); ended {
		t.Fatal("ended a worker whose pane would not close")
	}
	if line, _ := livework.Default.Line(e.ID); !strings.Contains(line, "closing its session failed") {
		t.Errorf("row line = %q, want the close failure", line)
	}
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, true, "finished")
}

// With no done predicate, only the pane going ends the worker.
func TestReapWorkerNoDoneUsesPaneStatus(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "")

	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true", Status: "true"}); ended {
		t.Fatal("ended a worker whose pane is still alive")
	}
	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"}); ended {
		t.Fatal("ended a worker with no done and no status command")
	}
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "false", Status: "false"})
	assertEnded(t, repoRoot, e, ended, outcome, true, "pane closed")
}

// A queued target starts once a worker's session ends, even when that
// worker's worktree is kept.
func TestReapFreesSlotForQueuedTarget(t *testing.T) {
	noDoneGate(t)
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 1\n\n"+terminalTOML)
	addOrigin(t, repoRoot)
	var spawned int32
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		return fmt.Sprintf("pane-%d", atomic.AddInt32(&spawned, 1)), nil
	})

	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "slot", workflowAction(),
			[]Target{{Name: "t0", Run: "x", Done: "false"}, {Name: "t1", Run: "x"}}, SurfaceDashboard)
	}()
	for atomic.LoadInt32(&spawned) < 1 {
		time.Sleep(time.Millisecond)
	}
	e, ok := livework.Default.Get(workerLiveID("slot", "t0"))
	if !ok {
		t.Fatal("t0 not registered")
	}

	// Still running: the slot stays held.
	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"}); ended {
		t.Fatal("ended a running worker")
	}
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&spawned); got != 1 {
		t.Fatalf("spawned while a running worker held the slot = %d, want 1", got)
	}

	if err := os.WriteFile(filepath.Join(e.Worker.Worktree, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.Worker.Done = "true"
	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"}); !ended {
		t.Fatal("did not end the finished worker")
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

// TestPaneGoneExitCode exercises the real /bin/sh exec path: 0 is alive, 1-125
// is the command's own "gone", and the shell's could-not-run and signal codes
// are an err that names the exit, never gone.
func TestPaneGoneExitCode(t *testing.T) {
	for _, tc := range []struct {
		cmd     string
		gone    bool
		wantErr string
	}{
		{cmd: "exit 0"},
		{cmd: "sleep 3 >&2 & exit 0"},
		{cmd: "exit 1", gone: true},
		{cmd: "exit 125", gone: true},
		{cmd: "exit 126", wantErr: "exit 126"},
		{cmd: `no-such-launcher-xyz agent get "$ADMIN_HANDLE"`, wantErr: "exit 127"},
		{cmd: "exit 130", wantErr: "exit 130"},
		{cmd: "kill -TERM $$", wantErr: "killed by"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		gone, err := PaneGone(ctx, tc.cmd, "handle")
		cancel()
		if gone != tc.gone {
			t.Errorf("PaneGone(%q) gone = %v, want %v", tc.cmd, gone, tc.gone)
		}
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("PaneGone(%q) err = %v, want nil", tc.cmd, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("PaneGone(%q) err = %v, want one containing %q", tc.cmd, err, tc.wantErr)
		}
	}
}

// A status command that cannot run (its launcher binary is missing) says
// nothing about the pane: the worker keeps running and its row shows why.
func TestReapWorkerBrokenStatusKeepsRunning(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "")

	ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true", Status: "no-such-launcher-xyz"})
	if ended {
		t.Fatal("ended a worker because its status command was not found")
	}
	if st, _ := livework.Default.State(e.ID); st != livework.StateRunning {
		t.Errorf("state = %v, want running", st)
	}
	if line, _ := livework.Default.Line(e.ID); !strings.Contains(line, "exit 127") {
		t.Errorf("row line = %q, want the status failure", line)
	}
}

// A status command killed by its deadline exits nonzero too; that is a wedged
// launcher, not a closed pane.
func TestPaneGoneTimeoutIsNotGone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// The pipeline leaves sleep holding stderr after sh is killed; the probe
	// must still return near its deadline.
	start := time.Now()
	gone, err := PaneGone(ctx, "sleep 5 | cat", "handle")
	if gone || err == nil {
		t.Errorf("PaneGone(timed out) = %v, %v; want false and an error", gone, err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("PaneGone(timed out) took %v, want it bounded by the deadline", took)
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

// A hosted worker has no launcher pane, so [terminal].status says nothing
// about it: a failing status does not end it.
func TestReapWorkerHostedIgnoresPaneStatus(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "false")
	e.Via = ""
	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true", Status: "false"}); ended {
		t.Fatal("ended a hosted worker on a launcher's pane status")
	}
}

// A worktree removal that fails keeps the worktree and says why; the session
// still ends, and kill runs once.
func TestReapWorkerRemovalFailureKeepsWorktree(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "true")
	killLog := filepath.Join(t.TempDir(), "kills")
	// A locked worktree refuses `worktree remove`.
	wfGitT(t, repoRoot, "worktree", "lock", e.Worker.Worktree)
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: `echo k >> ` + killLog})
	assertEnded(t, repoRoot, e, ended, outcome, false, "removing it failed")
	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: `echo k >> ` + killLog}); ended {
		t.Error("ended an already-ended worker again")
	}
	out, err := os.ReadFile(killLog)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "k"); n != 1 {
		t.Errorf("kill ran %d times, want 1", n)
	}
}

// Ignored files (.env, build output) keep the worktree: removing it would
// delete them even though git status calls the tree clean.
func TestReapWorkerKeepsIgnoredFiles(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	excludes := filepath.Join(t.TempDir(), "ignore")
	if err := os.WriteFile(excludes, []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wfGitT(t, repoRoot, "config", "core.excludesFile", excludes)
	e := dispatchWorker(t, repoRoot, "true")
	if err := os.WriteFile(filepath.Join(e.Worker.Worktree, ".env"), []byte("K=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, false, ".env")
	if _, err := os.Stat(filepath.Join(e.Worker.Worktree, ".env")); err != nil {
		t.Errorf(".env gone: %v", err)
	}
}

// A branch pushed from elsewhere reads as on origin once the reaper fetches,
// though the local tracking refs were stale.
func TestReapWorkerFetchesOrigin(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "true")
	w := e.Worker
	commitIn(t, w.Worktree)
	wfGitT(t, w.Worktree, "push", "-q", "origin", w.Branch+":main")
	// The push refreshed the tracking ref; put it back to the stale value.
	wfGitT(t, repoRoot, "update-ref", "refs/remotes/origin/main", "main")

	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, true, "removed")
}

// With no origin at all, a branch that adds nothing beyond the local default
// branch loses nothing; one that adds a commit is kept.
func TestReapWorkerNoOrigin(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	e := dispatchWorker(t, repoRoot, "true")
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, true, "removed")

	repoRoot = setupWorkflowRepo(t, terminalTOML)
	e = dispatchWorker(t, repoRoot, "true")
	commitIn(t, e.Worker.Worktree)
	ended, outcome = ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, false, "on no origin branch")
}

// A done predicate that times out shows why on the row; one that merely exits
// nonzero (not finished yet) shows nothing.
func TestReapWorkerReportsDoneTimeout(t *testing.T) {
	prev := doneTimeout
	doneTimeout = 200 * time.Millisecond
	t.Cleanup(func() { doneTimeout = prev })

	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	logPath := filepath.Join(t.TempDir(), "sync.log")
	t.Cleanup(data.UseSyncLogPath(logPath))
	e := dispatchWorker(t, repoRoot, "sleep 5")
	ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	if note, _ := livework.Default.Line(e.ID); !strings.Contains(note, "timed out") {
		t.Errorf("row line = %q, want the timeout reason", note)
	}
	// The run that could not answer is the one a person reads the log for, so
	// its line names the worker and the exit code too.
	b, _ := os.ReadFile(logPath)
	for _, want := range []string{" done FAIL ", "timed out", "worker=" + e.ID, "exit=-1"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("sync log = %q, want %q", b, want)
		}
	}
}

// Every ended worker writes its outcome to the sync log.
func TestReapWorkerLogsOutcome(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	logPath := filepath.Join(t.TempDir(), "sync.log")
	t.Cleanup(data.UseSyncLogPath(logPath))
	e := dispatchWorker(t, repoRoot, "true")
	_, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), " reap ") || !strings.Contains(string(b), outcome) {
		t.Errorf("sync log = %q, want a reap line carrying %q", b, outcome)
	}
}

// Killing a worker whose pane already closed still exits the row and frees its
// ceiling slot.
func TestKillWorkerWithGonePaneReleasesSlot(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[terminal]\nspawn = \"true\"\nattach = \"true\"\nkill = \"false\"\nstatus = \"false\"\n")
	e := dispatchWorker(t, repoRoot, "false")
	released := make(chan struct{})
	holdWorkerSlot(e.ID, func() { close(released) })
	if err := e.Ops.Kill(); err != nil {
		t.Fatalf("Kill on a gone pane: %v", err)
	}
	select {
	case <-released:
	default:
		t.Error("slot not released")
	}
	if st, _ := livework.Default.State(e.ID); st != livework.StateExited {
		t.Errorf("state = %s, want exited", st)
	}
}

// An ignored path the primary checkout also has (a seeded copy) does not keep
// the worktree.
func TestReapWorkerIgnoresSeededIgnoredFiles(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	excludes := filepath.Join(t.TempDir(), "ignore")
	if err := os.WriteFile(excludes, []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wfGitT(t, repoRoot, "config", "core.excludesFile", excludes)
	if err := os.WriteFile(filepath.Join(repoRoot, ".env"), []byte("K=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := dispatchWorker(t, repoRoot, "true")
	if err := os.WriteFile(filepath.Join(e.Worker.Worktree, ".env"), []byte("K=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, true, "removed")
}

// A dependency install directory the primary checkout lacks is regenerable,
// so it does not keep the worktree, including one nested under a directory
// git would otherwise report as a single ignored path.
func TestReapWorkerIgnoresWorkerOnlyNodeModules(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	excludes := filepath.Join(t.TempDir(), "ignore")
	if err := os.WriteFile(excludes, []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wfGitT(t, repoRoot, "config", "core.excludesFile", excludes)
	e := dispatchWorker(t, repoRoot, "true")
	nm := filepath.Join(e.Worker.Worktree, "packages", "x", "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nm, "pkg.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, true, "removed")
}

// A worker whose worktree a person removed by hand has no directory to run
// done in; it ends, and a branch with nothing local-only goes too.
func TestReapWorkerWorktreeRemovedByHand(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "true")
	if err := os.RemoveAll(e.Worker.Worktree); err != nil {
		t.Fatal(err)
	}
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, true, "removed")
}

// Same, but the branch holds a commit origin lacks: the branch stays.
func TestReapWorkerWorktreeGoneBranchUnpushed(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "true")
	commitIn(t, e.Worker.Worktree)
	if err := os.RemoveAll(e.Worker.Worktree); err != nil {
		t.Fatal(err)
	}
	ended, outcome := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"})
	assertEnded(t, repoRoot, e, ended, outcome, false, "on no origin branch")
}

// Worktree and branch both removed by hand: nothing is left to lose.
func TestReapWorkerWorktreeAndBranchGone(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	e := dispatchWorker(t, repoRoot, "true")
	wfGitT(t, repoRoot, "worktree", "remove", "--force", e.Worker.Worktree)
	wfGitT(t, repoRoot, "branch", "-D", e.Worker.Branch)
	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{Kill: "true"}); !ended {
		note, _ := livework.Default.Line(e.ID)
		t.Fatalf("did not end; row line = %q", note)
	}
}

// TestDispatchRefusesATargetWithALiveWorker: a second dispatch over a target
// that already has a live worker opens no pane and says which worker holds it.
func TestDispatchRefusesATargetWithALiveWorker(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	spawns := 0
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		spawns++
		return "pane-1", nil
	})
	targets := []Target{{Name: "t0", Run: "x"}}
	if res := DispatchTargets(repoRoot, "reap", workflowAction(), targets, SurfaceDashboard); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	res := DispatchTargets(repoRoot, "reap", workflowAction(), targets, SurfaceDashboard)
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "already has a worker (running)") {
		t.Fatalf("second dispatch err = %v, want the live-worker refusal", res[0].Err)
	}
	if spawns != 1 {
		t.Fatalf("spawned %d panes, want 1", spawns)
	}
	livework.Default.SetState(workerLiveID("reap", "t0"), livework.StateExited, nil)
	if res := DispatchTargets(repoRoot, "reap", workflowAction(), targets, SurfaceDashboard); res[0].Err != nil {
		t.Fatalf("redispatch after the worker exited: %v", res[0].Err)
	}
}

// noDoneGate runs every worker's done predicate on every reap for t, for a
// test that changes the predicate between two back-to-back reaps.
func noDoneGate(t *testing.T) {
	t.Helper()
	prev := doneInterval
	doneInterval = 0
	t.Cleanup(func() { doneInterval = prev })
}

// countingDone is a done predicate that never passes and appends one line to
// the returned file per run.
func countingDone(t *testing.T) (done string, runs func() int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs")
	return fmt.Sprintf("echo x >> %q; false", path), func() int {
		b, _ := os.ReadFile(path)
		return strings.Count(string(b), "x")
	}
}

// The done predicate runs once per doneInterval, however often the dashboard
// tick reaps, and the answer between runs is the last one.
func TestReapWorkerRunsDoneOncePerInterval(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	addOrigin(t, repoRoot)
	done, runs := countingDone(t)
	e := dispatchWorker(t, repoRoot, done)
	alive := config.Terminal{Kill: "true", Status: "true"}
	for range 5 {
		if ended, _ := ReapWorker(context.Background(), repoRoot, e, alive); ended {
			t.Fatal("ended a worker whose done never passed")
		}
	}
	if n := runs(); n != 1 {
		t.Fatalf("done ran %d times over 5 reaps inside one interval, want 1", n)
	}

	prev := doneInterval
	doneInterval = 50 * time.Millisecond
	t.Cleanup(func() { doneInterval = prev })
	time.Sleep(60 * time.Millisecond)
	ReapWorker(context.Background(), repoRoot, e, alive)
	if n := runs(); n != 2 {
		t.Fatalf("done ran %d times after the interval passed, want 2", n)
	}
}
