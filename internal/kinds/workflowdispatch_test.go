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
	"sync/atomic"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/create"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// -- test setup -----------------------------------------------------------

func wfGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupWorkflowRepo builds a throwaway repo, isolates it from the real
// machine's worktree root (WORKTREE_ROOT → a temp dir), and writes the given
// config tables straight into the repo's own admin.toml so DispatchTargets'
// config.Load(repoRoot) call sees exactly what the test wants — never the
// developer's real repo config. Isolation from the machine's installed
// archetypes (~/.admin/archetypes/) comes from this package's TestMain,
// which points archetype.InstalledArchetypesDir at an empty temp directory
// for every test — config.Load no longer reads $HOME itself, so setting
// HOME here would no longer buy that isolation.
func setupWorkflowRepo(t *testing.T, configTOML string) (repoRoot string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("WORKTREE_ROOT", filepath.Join(base, "wt"))

	repoRoot = filepath.Join(base, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "admin.toml"),
		[]byte("project_name = \"t\"\n"+configTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	// internal/git's helpers run git in the process's current directory,
	// not in an explicit --git-dir/-C — the interpreter cd's to the
	// project root at boot and every caller relies on that. Without this,
	// DispatchTargets' git.AddNewBranch etc. would run against whatever
	// repo this test binary's process happened to start in (this very
	// worktree) instead of the throwaway repo below.
	t.Chdir(repoRoot)
	wfGitT(t, repoRoot, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wfGitT(t, repoRoot, "add", "-A")
	wfGitT(t, repoRoot, "commit", "-qm", "init")

	return repoRoot
}

// stubAgent replaces startAgent/runAgent for the duration of the test,
// restoring the real implementations on cleanup — the same
// save-swap-restore-on-Cleanup pattern the rest of this package uses for
// its other package-var seams (selfExecutable, removeWorktreeFn).
func stubAgent(t *testing.T,
	start func(dir, name, kind string) (string, error),
	run func(name, prompt string) error,
) {
	t.Helper()
	origStart, origRun := startAgent, runAgent
	startAgent, runAgent = start, run
	t.Cleanup(func() { startAgent, runAgent = origStart, origRun })
}

// -- tests ------------------------------------------------------------------

// TestDispatchTargetsCreatesWorktreesSeededWithPopulate is the first
// acceptance criterion: dispatching over N targets creates N worktrees,
// each seeded with the configured populate files.
func TestDispatchTargetsCreatesWorktreesSeededWithPopulate(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"default-agent\"\n\n"+
		"[[populate]]\npattern = \".env\"\nmode = \"copy\"\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var paneN int32
	stubAgent(t,
		func(dir, name, kind string) (string, error) {
			n := atomic.AddInt32(&paneN, 1)
			return fmt.Sprintf("pane-%d", n), nil
		},
		func(name, prompt string) error { return nil },
	)

	targets := []Target{{Name: "t1"}, {Name: "t2"}, {Name: "t3"}}
	results := DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, targets)

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("target %q: unexpected error: %v", r.Target, r.Err)
		}
		if r.PaneID == "" {
			t.Errorf("target %q: no pane ID recorded", r.Target)
		}
		envPath := filepath.Join(r.WorktreePath, ".env")
		body, err := os.ReadFile(envPath)
		if err != nil {
			t.Errorf("target %q: populated .env missing: %v", r.Target, err)
			continue
		}
		if string(body) != "SECRET=1\n" {
			t.Errorf("target %q: .env content = %q", r.Target, body)
		}
	}
}

// TestDispatchTargetsNoAgentConfiguredFailsEveryTargetWithoutTouchingGit is
// the "no agent, no guessing" case: with neither the action nor the config
// naming one, every target fails, naming the missing configuration — and
// nothing is created, since failing before doing any work beats creating N
// worktrees for a dispatch that was never going to run anything.
func TestDispatchTargetsNoAgentConfiguredFailsEveryTargetWithoutTouchingGit(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "")
	wtRoot := filepath.Join(filepath.Dir(repoRoot), "wt")

	results := DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"},
		[]Target{{Name: "t1"}, {Name: "t2"}})

	for _, r := range results {
		if r.Err == nil || !strings.Contains(r.Err.Error(), "no agent configured") {
			t.Errorf("target %q: err = %v, want a 'no agent configured' error", r.Target, r.Err)
		}
	}
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Errorf("a worktree root was created (%v) for a dispatch with no agent configured", err)
	}
}

// TestDispatchTargetsAgentOverrideWinsOverDefault is the acceptance
// criterion "a per-workflow agent override is used in preference to the
// default": the action names its own agent, and the project config names a
// different one.
//
// MUTATION EVIDENCE (actually run, then reverted): swapping ResolveAgent's
// two branches so cfg.Workflow.Agent is checked before the action's own
// key produced this real failure:
//
//	agent kind = "config-default-agent", want action-override-agent
func TestDispatchTargetsAgentOverrideWinsOverDefault(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"config-default-agent\"\n")

	var gotKind string
	var mu sync.Mutex
	stubAgent(t,
		func(dir, name, kind string) (string, error) {
			mu.Lock()
			gotKind = kind
			mu.Unlock()
			return "pane-1", nil
		},
		func(name, prompt string) error { return nil },
	)

	action := manifest.Action{"kind": "workflow", "agent": "action-override-agent"}
	results := DispatchTargets(repoRoot, "sweep", action, []Target{{Name: "t1"}})

	if results[0].Err != nil {
		t.Fatalf("unexpected error: %v", results[0].Err)
	}
	if gotKind != "action-override-agent" {
		t.Errorf("agent kind = %q, want action-override-agent", gotKind)
	}
}

// TestDispatchTargetsCeilingLimitsConcurrency is the acceptance criterion
// "no more than the ceiling run at once; the remainder queue and start as
// slots free": 5 targets under a ceiling of 2 never have more than 2
// running agents at the same instant, and all 5 eventually complete.
//
// MUTATION EVIDENCE (actually run, then reverted): changing
// `sem := make(chan struct{}, ceiling)` to `make(chan struct{}, len(targets))`
// (defeating the ceiling) produced this real failure:
//
//	observed max concurrency = 5, want <= 2 (ceiling)
func TestDispatchTargetsCeilingLimitsConcurrency(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\nceiling = 2\n")

	var current, max int32
	release := make(chan struct{})
	stubAgent(t,
		func(dir, name, kind string) (string, error) { return name, nil },
		func(name, prompt string) error {
			n := atomic.AddInt32(&current, 1)
			for {
				old := atomic.LoadInt32(&max)
				if n <= old || atomic.CompareAndSwapInt32(&max, old, n) {
					break
				}
			}
			<-release
			atomic.AddInt32(&current, -1)
			return nil
		},
	)

	targets := make([]Target, 5)
	for i := range targets {
		targets[i] = Target{Name: fmt.Sprintf("t%d", i)}
	}

	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, targets)
	}()

	// Let the ceiling's worth of targets actually start and block, then give
	// a defeated ceiling time to over-admit before releasing everything —
	// under the real ceiling the other 3 goroutines are parked on the
	// semaphore send and cannot advance no matter how long this waits, so
	// the extra time only matters for making a mutated (uncapped) sem
	// observably wrong rather than racing to check before it over-admits.
	for atomic.LoadInt32(&current) < 1 {
	}
	time.Sleep(100 * time.Millisecond)
	close(release)

	results := <-done
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("target %q: unexpected error: %v", r.Target, r.Err)
		}
	}
	if got := atomic.LoadInt32(&max); got > 2 {
		t.Errorf("observed max concurrency = %d, want <= 2 (ceiling)", got)
	}
}

// TestDispatchOneFailsLoudlyWhenWorktreeVanished is operational knowledge
// item 2: a worktree created earlier can be removed by something outside
// this dispatch (a sweeper, a person) before its turn to run arrives.
// dispatchOne must fail that target loudly rather than silently recreating
// the worktree at a base nobody chose, and must never call the agent at
// all once the worktree is gone.
//
// MUTATION EVIDENCE (actually run, then reverted): short-circuiting the
// `if !create.IsRegisteredWorktree(...)` guard in dispatchOne (`if false &&
// ...`) produced this real failure:
//
//	expected an error for a worktree removed before dispatch, got nil
func TestDispatchOneFailsLoudlyWhenWorktreeVanished(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n")
	ctx := context.Background()

	wt, err := create.Worktree(ctx, repoRoot, "vanish-me")
	if err != nil {
		t.Fatalf("creating worktree: %v", err)
	}
	// Simulate the sweeper: remove the worktree the way `git worktree
	// remove` would (deleting the checkout out from under a not-yet-
	// dispatched target), without going through this package's own
	// creation path again. git's own worktree admin data is keyed on the
	// symlink-resolved path (macOS registers /tmp/... worktrees under
	// /private/tmp/...), so removal must address it the same way or git
	// reports the exact path handed to it as "not a working tree".
	resolved, err := filepath.EvalSymlinks(wt.Path)
	if err != nil {
		t.Fatalf("resolving worktree path: %v", err)
	}
	wfGitT(t, repoRoot, "worktree", "remove", "--force", resolved)

	agentStarted := false
	stubAgent(t,
		func(dir, name, kind string) (string, error) { agentStarted = true; return "pane-1", nil },
		func(name, prompt string) error { return nil },
	)

	res := dispatchOne("sweep", "a", Target{Name: "vanish-me"}, wt)

	if res.Err == nil {
		t.Fatal("expected an error for a worktree removed before dispatch, got nil")
	}
	if !strings.Contains(res.Err.Error(), "no longer exists") {
		t.Errorf("error = %v, want it to say the worktree no longer exists", res.Err)
	}
	if agentStarted {
		t.Error("startAgent was called for a worktree that no longer exists")
	}
	if _, err := os.Stat(wt.Path); err == nil {
		t.Error("a worktree was recreated at the removed path — dispatchOne must never do this")
	}
}

// TestDispatchTargetsRecordsLiveWorkWithPaneHandle is the acceptance
// criterion "each running target appears in the live registry with its
// pane handle": the registry entry exists (keyed by the pane ID) while the
// agent's blocking prompt/wait step is still in flight, not only after
// DispatchTargets has returned for every target.
func TestDispatchTargetsRecordsLiveWorkWithPaneHandle(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n")

	seenRunning := make(chan struct{}, 1)
	release := make(chan struct{})
	stubAgent(t,
		func(dir, name, kind string) (string, error) { return "pane-live-1", nil },
		func(name, prompt string) error {
			select {
			case seenRunning <- struct{}{}:
			default:
			}
			<-release
			return nil
		},
	)

	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}})
	}()

	<-seenRunning
	// Keyed on the derived agent session name, not the pane ID — see
	// dispatchOne's own comment for why the entry's ID must survive a Retry
	// that opens a different pane.
	entry, ok := livework.Default.Get(agentSessionName("sweep", "t1"))
	if !ok {
		t.Fatal("live-work entry not present while the target's agent is still running")
	}
	if entry.Handle != "pane-live-1" {
		t.Errorf("entry.Handle = %q, want pane-live-1", entry.Handle)
	}
	if entry.State != livework.StateRunning {
		t.Errorf("entry.State = %q, want running while the target is in flight", entry.State)
	}
	close(release)
	<-done
}

// stubAgentStatus replaces the package-level agentStatus var for the
// duration of the test, restoring the real implementation on cleanup — the
// same pattern stubAgent uses for startAgent/runAgent.
func stubAgentStatus(t *testing.T, status func(name string) (string, error)) {
	t.Helper()
	orig := agentStatus
	agentStatus = status
	t.Cleanup(func() { agentStatus = orig })
}

// TestDispatchOneRecordsWorkerInfo proves a workflow target's live-work
// entry carries its branch and worktree — the acceptance criterion "an
// agent worker has a branch and a worktree" — and that Attach/Kill/Retry
// are all wired, not left nil.
func TestDispatchOneRecordsWorkerInfo(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n")
	stubAgent(t,
		func(dir, name, kind string) (string, error) { return "pane-info-1", nil },
		func(name, prompt string) error { return nil },
	)
	stubAgentStatus(t, func(name string) (string, error) { return "idle", nil })

	res := DispatchTargets(repoRoot, "review", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}})
	if res[0].Err != nil {
		t.Fatalf("DispatchTargets: %v", res[0].Err)
	}

	entry, ok := livework.Default.Get(agentSessionName("review", "t1"))
	if !ok {
		t.Fatal("live-work entry not found")
	}
	if entry.Worker == nil {
		t.Fatal("entry.Worker = nil, want a WorkerInfo for an agent worker")
	}
	if entry.Worker.Branch == "" {
		t.Error("entry.Worker.Branch is empty, want the target's branch")
	}
	if entry.Worker.Worktree == "" {
		t.Error("entry.Worker.Worktree is empty, want the target's worktree path")
	}
	if entry.Ops.Attach == nil || entry.Ops.Kill == nil || entry.Ops.Retry == nil {
		t.Errorf("entry.Ops = %+v, want Attach/Kill/Retry all wired", entry.Ops)
	}
}

// TestDispatchOneReportsWaitingWhenAgentBlocked proves the acceptance
// criterion "an entry waiting for input is distinguishable from one still
// working without attaching": when the agent's settled status is
// "blocked" (herdr's own word for an approval/question prompt),
// dispatchOne records StateWaiting, not StateExited — PromptAndWait
// returning with no error is not by itself proof the target finished.
//
// MUTATION EVIDENCE (actually run, then reverted): replacing the
// `status == "blocked"` check in dispatchOne with `status == "nonsense"`
// produced this real failure:
//
//	entry.State = "exited", want "waiting" for a blocked agent
func TestDispatchOneReportsWaitingWhenAgentBlocked(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n")
	stubAgent(t,
		func(dir, name, kind string) (string, error) { return "pane-wait-1", nil },
		func(name, prompt string) error { return nil }, // settles with no error, like a blocked agent
	)
	stubAgentStatus(t, func(name string) (string, error) { return "blocked", nil })

	res := DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}})
	if res[0].Err != nil {
		t.Fatalf("DispatchTargets: %v", res[0].Err)
	}

	entry, ok := livework.Default.Get(agentSessionName("sweep", "t1"))
	if !ok {
		t.Fatal("live-work entry not found")
	}
	if entry.State != livework.StateWaiting {
		t.Errorf("entry.State = %q, want %q for a blocked agent", entry.State, livework.StateWaiting)
	}
}

// TestDispatchOneReportsExitedWhenAgentIdle proves the other half: a target
// that settles idle (actually finished) is recorded StateExited, not
// StateWaiting.
func TestDispatchOneReportsExitedWhenAgentIdle(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n")
	stubAgent(t,
		func(dir, name, kind string) (string, error) { return "pane-idle-1", nil },
		func(name, prompt string) error { return nil },
	)
	stubAgentStatus(t, func(name string) (string, error) { return "idle", nil })

	res := DispatchTargets(repoRoot, "sweep2", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}})
	if res[0].Err != nil {
		t.Fatalf("DispatchTargets: %v", res[0].Err)
	}

	entry, ok := livework.Default.Get(agentSessionName("sweep2", "t1"))
	if !ok {
		t.Fatal("live-work entry not found")
	}
	if entry.State != livework.StateExited {
		t.Errorf("entry.State = %q, want %q for an idle agent", entry.State, livework.StateExited)
	}
}

// TestDispatchOneRetryRestartsFailedTarget proves the acceptance criterion
// "retrying restarts a failed entry" end to end: a target's first attempt
// fails, its live-work entry's Retry op is invoked through the registry
// (livework.Default.Retry, the same call the Work tab would make), and a
// second attempt runs against the SAME worktree, replacing the entry in
// place (same ID) with a successful run.
func TestDispatchOneRetryRestartsFailedTarget(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n")

	var attempt int32
	stubAgent(t,
		func(dir, name, kind string) (string, error) {
			n := atomic.AddInt32(&attempt, 1)
			return fmt.Sprintf("pane-retry-%d", n), nil
		},
		func(name, prompt string) error {
			if atomic.LoadInt32(&attempt) == 1 {
				return errors.New("boom")
			}
			return nil
		},
	)
	stubAgentStatus(t, func(name string) (string, error) { return "idle", nil })

	res := DispatchTargets(repoRoot, "retry-me", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}})
	if res[0].Err == nil {
		t.Fatal("want the first attempt to fail")
	}

	id := agentSessionName("retry-me", "t1")
	entry, ok := livework.Default.Get(id)
	if !ok {
		t.Fatal("live-work entry not found after first attempt")
	}
	if entry.State != livework.StateFailed {
		t.Fatalf("entry.State = %q, want %q before retry", entry.State, livework.StateFailed)
	}
	if entry.Handle != "pane-retry-1" {
		t.Fatalf("entry.Handle = %q, want pane-retry-1 before retry", entry.Handle)
	}

	if err := livework.Default.Retry(id); err != nil {
		t.Fatalf("Retry() = %v, want nil", err)
	}
	// dispatchOne's Retry op re-runs in a goroutine (matching how
	// DispatchTargets originally launched it) — wait for the second
	// attempt's pane to land rather than racing it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		entry, _ = livework.Default.Get(id)
		if entry.Handle == "pane-retry-2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("entry never advanced to the retried pane; last seen %+v", entry)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if entry.State != livework.StateExited {
		t.Errorf("entry.State = %q, want %q after a successful retry", entry.State, livework.StateExited)
	}
}

// TestDispatchTargetsDogfoodActionAgentWithNoWorkflowDefault is admin-7wy5's
// acceptance criterion 4: dispatch the exact action shape this repo's own
// admin.toml declares — kind="workflow", over="selection", agent="claude"
// set on the action, with NO [workflow].agent configured anywhere in the
// project — through DispatchTargets under stubAgent, and prove the
// resolved agent kind is "claude" with no real herdr pane touched (stubbed
// startAgent/runAgent never call shim.SplitPane/shim.StartAgent, so this
// exercises dispatch against the stub only, matching the ticket's fourth
// acceptance criterion in words: "Dispatch is exercised against a stubbed
// agent, not a real herdr pane, in tests").
func TestDispatchTargetsDogfoodActionAgentWithNoWorkflowDefault(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "") // no [workflow] table at all

	var gotKind string
	var mu sync.Mutex
	stubAgent(t,
		func(dir, name, kind string) (string, error) {
			mu.Lock()
			gotKind = kind
			mu.Unlock()
			return "pane-dogfood-1", nil
		},
		func(name, prompt string) error { return nil },
	)

	action := manifest.Action{"kind": "workflow", "over": "selection", "agent": "claude"}
	results := DispatchTargets(repoRoot, "implement", action, []Target{{Name: "proj-1"}})

	if results[0].Err != nil {
		t.Fatalf("unexpected error: %v", results[0].Err)
	}
	mu.Lock()
	got := gotKind
	mu.Unlock()
	if got != "claude" {
		t.Errorf("agent kind = %q, want claude", got)
	}
	if results[0].PaneID != "pane-dogfood-1" {
		t.Errorf("PaneID = %q, want the pane the stub returned (proof only the stub ran)", results[0].PaneID)
	}
}
