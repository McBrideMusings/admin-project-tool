package kinds

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
	"github.com/McBrideMusings/admin-project-tool/internal/config"
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

// terminalTOML is a placeholder [terminal] table: dispatch now requires one
// declared (ADR-0020/0021) before it will even attempt a target, and every
// test below stubs spawnPane/selectDriver rather than actually shelling out
// to these commands — their content is never executed.
const terminalTOML = "[terminal]\nspawn = \"true\"\nattach = \"true\"\nkill = \"true\"\n"

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

// fakeDriver is the test double for internal/agent.Driver: each method
// defers to a caller-supplied func when set, else a harmless default (Start/
// Prompt succeed doing nothing, Info reports idle).
type fakeDriver struct {
	startFn  func(handle, kind string, args []string) error
	promptFn func(handle, text string, until []agent.Status) error
	infoFn   func(handle string) (agent.Info, error)
}

func (f *fakeDriver) Start(handle, kind string, args []string) error {
	if f.startFn == nil {
		return nil
	}
	return f.startFn(handle, kind, args)
}

func (f *fakeDriver) Prompt(handle, text string, until []agent.Status) error {
	if f.promptFn == nil {
		return nil
	}
	return f.promptFn(handle, text, until)
}

func (f *fakeDriver) Read(handle string, lines int) (string, error) { return "", nil }

func (f *fakeDriver) Info(handle string) (agent.Info, error) {
	if f.infoFn == nil {
		return agent.Info{Status: agent.StatusIdle}, nil
	}
	return f.infoFn(handle)
}

// stubAgent replaces spawnPane and selectDriver for the duration of the
// test — spawn stands in for the [terminal] launcher's pane-opening step,
// start/prompt back the fakeDriver selectDriver hands back — restoring the
// real implementations on cleanup, the same save-swap-restore-on-Cleanup
// pattern the rest of this package uses for its other package-var seams
// (selfExecutable, removeWorktreeFn).
func stubAgent(t *testing.T,
	spawn func(cwd string) (string, error),
	start func(handle, kind string, args []string) error,
	prompt func(handle, text string, until []agent.Status) error,
) *fakeDriver {
	t.Helper()
	origSpawn, origSelect := spawnPane, selectDriver
	spawnPane = func(cwd string, ctx launchCtx, term config.Terminal) (string, error) { return spawn(cwd) }
	fd := &fakeDriver{startFn: start, promptFn: prompt}
	selectDriver = func() (agent.Driver, bool) { return fd, true }
	t.Cleanup(func() { spawnPane, selectDriver = origSpawn, origSelect })
	return fd
}

// -- tests ------------------------------------------------------------------

// TestDispatchTargetsCreatesWorktreesSeededWithPopulate is the first
// acceptance criterion: dispatching over N targets creates N worktrees,
// each seeded with the configured populate files.
func TestDispatchTargetsCreatesWorktreesSeededWithPopulate(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"default-agent\"\n\n"+
		terminalTOML+"\n[[populate]]\npattern = \".env\"\nmode = \"copy\"\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var paneN int32
	stubAgent(t,
		func(cwd string) (string, error) {
			n := atomic.AddInt32(&paneN, 1)
			return fmt.Sprintf("pane-%d", n), nil
		},
		nil, nil,
	)

	targets := []Target{{Name: "t1"}, {Name: "t2"}, {Name: "t3"}}
	results := DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, targets, SurfaceDashboard)

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("target %q: unexpected error: %v", r.Target, r.Err)
		}
		if r.Handle == "" {
			t.Errorf("target %q: no handle recorded", r.Target)
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
		[]Target{{Name: "t1"}, {Name: "t2"}}, SurfaceDashboard)

	for _, r := range results {
		if r.Err == nil || !strings.Contains(r.Err.Error(), "no agent configured") {
			t.Errorf("target %q: err = %v, want a 'no agent configured' error", r.Target, r.Err)
		}
	}
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Errorf("a worktree root was created (%v) for a dispatch with no agent configured", err)
	}
}

// TestDispatchTargetsNoTerminalConfiguredFailsEveryTargetNamingIt is the
// launcher-side counterpart: an agent is configured but no [terminal]
// launcher is declared, so every target fails naming that specifically
// (rather than a generic dispatch error), and nothing is created.
func TestDispatchTargetsNoTerminalConfiguredFailsEveryTargetNamingIt(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n")
	wtRoot := filepath.Join(filepath.Dir(repoRoot), "wt")

	results := DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}}, SurfaceDashboard)

	for _, r := range results {
		if r.Err == nil || !strings.Contains(r.Err.Error(), "[terminal]") {
			t.Errorf("target %q: err = %v, want it to name [terminal]", r.Target, r.Err)
		}
	}
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Errorf("a worktree root was created (%v) for a dispatch with no [terminal] launcher", err)
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
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"config-default-agent\"\n\n"+terminalTOML)

	var gotKind string
	var mu sync.Mutex
	stubAgent(t,
		func(cwd string) (string, error) { return "pane-1", nil },
		func(handle, kind string, args []string) error {
			mu.Lock()
			gotKind = kind
			mu.Unlock()
			return nil
		},
		nil,
	)

	action := manifest.Action{"kind": "workflow", "agent": "action-override-agent"}
	results := DispatchTargets(repoRoot, "sweep", action, []Target{{Name: "t1"}}, SurfaceDashboard)

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
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\nceiling = 2\n\n"+terminalTOML)

	var current, max int32
	release := make(chan struct{})
	var paneN int32
	stubAgent(t,
		func(cwd string) (string, error) {
			n := atomic.AddInt32(&paneN, 1)
			return fmt.Sprintf("pane-%d", n), nil
		},
		nil,
		func(handle, text string, until []agent.Status) error {
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
		targets[i] = Target{Name: fmt.Sprintf("t%d", i), Prompt: "go"}
	}

	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, targets, SurfaceDashboard)
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
// the worktree at a base nobody chose, and must never spawn a pane or start
// an agent at all once the worktree is gone.
//
// MUTATION EVIDENCE (actually run, then reverted): short-circuiting the
// `if !create.IsRegisteredWorktree(...)` guard in dispatchOne (`if false &&
// ...`) produced this real failure:
//
//	expected an error for a worktree removed before dispatch, got nil
func TestDispatchOneFailsLoudlyWhenWorktreeVanished(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n\n"+terminalTOML)
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
		func(cwd string) (string, error) { agentStarted = true; return "pane-1", nil },
		nil, nil,
	)

	cfg, err := config.Load(repoRoot)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	drv, _ := selectDriver()
	res := dispatchOne(repoRoot, "sweep", "a", cfg.Terminal, drv, Target{Name: "vanish-me"}, wt)

	if res.Err == nil {
		t.Fatal("expected an error for a worktree removed before dispatch, got nil")
	}
	if !strings.Contains(res.Err.Error(), "no longer exists") {
		t.Errorf("error = %v, want it to say the worktree no longer exists", res.Err)
	}
	if agentStarted {
		t.Error("spawnPane was called for a worktree that no longer exists")
	}
	if _, err := os.Stat(wt.Path); err == nil {
		t.Error("a worktree was recreated at the removed path — dispatchOne must never do this")
	}
}

// TestDispatchTargetsRecordsLiveWorkWithPaneHandle is the acceptance
// criterion "each running target appears in the live registry with its
// pane handle": the registry entry exists (keyed by the launcher handle)
// while the agent's blocking prompt/wait step is still in flight, not only
// after DispatchTargets has returned for every target.
func TestDispatchTargetsRecordsLiveWorkWithPaneHandle(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n\n"+terminalTOML)

	seenRunning := make(chan struct{}, 1)
	release := make(chan struct{})
	stubAgent(t,
		func(cwd string) (string, error) { return "pane-live-1", nil },
		nil,
		func(handle, text string, until []agent.Status) error {
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
		done <- DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1", Prompt: "go"}}, SurfaceDashboard)
	}()

	<-seenRunning
	// Keyed on the derived agent session name, not the launcher handle.
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

// stubAgentInfo sets fd.infoFn to a fixed status word for the duration of
// the test, matching the old stubAgentStatus's shape (a herdr status word
// in, an agent.Info with that status out).
func stubAgentInfo(fd *fakeDriver, status string) {
	fd.infoFn = func(handle string) (agent.Info, error) {
		return agent.Info{Status: agent.Status(status)}, nil
	}
}

// TestDispatchOneRecordsWorkerInfo proves a workflow target's live-work
// entry carries its branch and worktree — the acceptance criterion "an
// agent worker has a branch and a worktree" — and that Attach/Kill
// are both wired, not left nil.
func TestDispatchOneRecordsWorkerInfo(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n\n"+terminalTOML)
	fd := stubAgent(t,
		func(cwd string) (string, error) { return "pane-info-1", nil },
		nil, nil,
	)
	stubAgentInfo(fd, "idle")

	res := DispatchTargets(repoRoot, "review", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}}, SurfaceDashboard)
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
	if entry.Ops.Attach == nil || entry.Ops.Kill == nil {
		t.Errorf("entry.Ops = %+v, want Attach/Kill both wired", entry.Ops)
	}
}

// TestDispatchOneReportsWaitingWhenAgentBlocked proves the acceptance
// criterion "an entry waiting for input is distinguishable from one still
// working without attaching": when the agent's settled status is
// "blocked" (herdr's own word for an approval/question prompt),
// dispatchOne records StateWaiting, not StateExited — Prompt returning with
// no error is not by itself proof the target finished.
//
// MUTATION EVIDENCE (actually run, then reverted): replacing the
// `info.Status == agent.StatusBlocked` check in dispatchOne with a
// nonsense status produced this real failure:
//
//	entry.State = "exited", want "waiting" for a blocked agent
func TestDispatchOneReportsWaitingWhenAgentBlocked(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n\n"+terminalTOML)
	fd := stubAgent(t,
		func(cwd string) (string, error) { return "pane-wait-1", nil },
		nil, nil, // settles with no error, like a blocked agent
	)
	stubAgentInfo(fd, "blocked")

	res := DispatchTargets(repoRoot, "sweep", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}}, SurfaceDashboard)
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
	repoRoot := setupWorkflowRepo(t, "[workflow]\nagent = \"a\"\n\n"+terminalTOML)
	fd := stubAgent(t,
		func(cwd string) (string, error) { return "pane-idle-1", nil },
		nil, nil,
	)
	stubAgentInfo(fd, "idle")

	res := DispatchTargets(repoRoot, "sweep2", manifest.Action{"kind": "workflow"}, []Target{{Name: "t1"}}, SurfaceDashboard)
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

// TestDispatchTargetsDogfoodActionAgentWithNoWorkflowDefault is admin-7wy5's
// acceptance criterion 4: dispatch the exact action shape this repo's own
// admin.toml declares — kind="workflow", over="selection", agent="claude"
// set on the action, with NO [workflow].agent configured anywhere in the
// project — through DispatchTargets under stubAgent, and prove the
// resolved agent kind is "claude" with no real herdr pane touched (stubbed
// spawnPane/selectDriver never call a real [terminal] launcher or herdr
// CLI, matching the ticket's fourth acceptance criterion in words:
// "Dispatch is exercised against a stubbed agent, not a real herdr pane, in
// tests").
func TestDispatchTargetsDogfoodActionAgentWithNoWorkflowDefault(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML) // no [workflow] table at all

	var gotKind string
	var mu sync.Mutex
	stubAgent(t,
		func(cwd string) (string, error) { return "pane-dogfood-1", nil },
		func(handle, kind string, args []string) error {
			mu.Lock()
			gotKind = kind
			mu.Unlock()
			return nil
		},
		nil,
	)

	action := manifest.Action{"kind": "workflow", "over": "selection", "agent": "claude"}
	results := DispatchTargets(repoRoot, "implement", action, []Target{{Name: "proj-1"}}, SurfaceDashboard)

	if results[0].Err != nil {
		t.Fatalf("unexpected error: %v", results[0].Err)
	}
	mu.Lock()
	got := gotKind
	mu.Unlock()
	if got != "claude" {
		t.Errorf("agent kind = %q, want claude", got)
	}
	if results[0].Handle != "pane-dogfood-1" {
		t.Errorf("Handle = %q, want the handle the stub returned (proof only the stub ran)", results[0].Handle)
	}
}
