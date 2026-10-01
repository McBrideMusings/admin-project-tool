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
	// Every test dispatches under reused action/target names ("sweep", "t1"),
	// and Registry.Add refuses a live duplicate: each test gets its own registry.
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
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

// workflowAction is the smallest valid workflow action: a run string.
func workflowAction() manifest.Action {
	return manifest.Action{"kind": "workflow", "run": "work-on $ADMIN_TARGET"}
}

// stubSpawn replaces spawnPane for the duration of the test — spawn stands in
// for the [terminal] launcher's pane-opening step — restoring the real
// implementation on cleanup, the same save-swap-restore-on-Cleanup pattern
// the rest of this package uses for its other package-var seams.
func stubSpawn(t *testing.T, spawn func(cmdLine, cwd string, ctx launchCtx) (string, error)) {
	t.Helper()
	origSpawn := spawnPane
	spawnPane = func(cmdLine, cwd string, ctx launchCtx, term config.Terminal) (string, error) {
		return spawn(cmdLine, cwd, ctx)
	}
	t.Cleanup(func() { spawnPane = origSpawn })
}

// -- tests ------------------------------------------------------------------

// TestDispatchTargetsCreatesWorktreesSeededWithPopulate is the first
// acceptance criterion: dispatching over N targets creates N worktrees,
// each seeded with the configured populate files.
func TestDispatchTargetsCreatesWorktreesSeededWithPopulate(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML+"\n[[populate]]\npattern = \".env\"\nmode = \"copy\"\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var paneN int32
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		n := atomic.AddInt32(&paneN, 1)
		return fmt.Sprintf("pane-%d", n), nil
	})

	targets := []Target{{Name: "t1", Run: "x"}, {Name: "t2", Run: "x"}, {Name: "t3", Run: "x"}}
	results := DispatchTargets(repoRoot, "sweep", workflowAction(), targets, SurfaceDashboard)

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

// TestDispatchTargetsSpawnGetsSharedGroupAndIndex: one dispatch of three
// targets spawns three times with one ADMIN_GROUP and ADMIN_INDEX 0, 1, 2; a
// second dispatch gets a different group.
func TestDispatchTargetsSpawnGetsSharedGroupAndIndex(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	var mu sync.Mutex
	var envs [][]string
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		envs = append(envs, ctx.env(cmdLine, cwd))
		return fmt.Sprintf("pane-%d", len(envs)), nil
	})
	has := func(env []string, kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}
	group := func(env []string) string {
		for _, e := range env {
			if g, ok := strings.CutPrefix(e, "ADMIN_GROUP="); ok {
				return g
			}
		}
		return ""
	}

	targets := []Target{{Name: "g1", Run: "x"}, {Name: "g2", Run: "x"}, {Name: "g3", Run: "x"}}
	DispatchTargets(repoRoot, "sweep", workflowAction(), targets, SurfaceDashboard)
	if len(envs) != 3 {
		t.Fatalf("spawn called %d times, want 3", len(envs))
	}
	g := group(envs[0])
	if g == "" {
		t.Fatal("ADMIN_GROUP missing from spawn env")
	}
	seen := map[int]bool{}
	for _, env := range envs {
		if group(env) != g {
			t.Errorf("ADMIN_GROUP = %q, want %q for every target", group(env), g)
		}
		for i := 0; i < 3; i++ {
			if has(env, fmt.Sprintf("ADMIN_INDEX=%d", i)) {
				seen[i] = true
			}
		}
	}
	if len(seen) != 3 {
		t.Errorf("ADMIN_INDEX values seen = %v, want 0, 1, 2", seen)
	}

	envs = nil
	DispatchTargets(repoRoot, "sweep", workflowAction(), []Target{{Name: "g4", Run: "x"}}, SurfaceDashboard)
	if len(envs) != 1 || group(envs[0]) == g {
		t.Errorf("second dispatch group = %v, want one differing from %q", envs, g)
	}
}

// TestDispatchTargetsNoRunFailsEveryTargetWithoutTouchingGit is the "no run,
// no guessing" case: a target carrying no run command fails, naming the
// missing key — and nothing is created, since failing before doing any work
// beats creating N worktrees for a dispatch that was never going to run
// anything.
func TestDispatchTargetsNoRunFailsEveryTargetWithoutTouchingGit(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	wtRoot := filepath.Join(filepath.Dir(repoRoot), "wt")

	results := DispatchTargets(repoRoot, "sweep", workflowAction(),
		[]Target{{Name: "t1"}, {Name: "t2"}}, SurfaceDashboard)

	for _, r := range results {
		if r.Err == nil || !strings.Contains(r.Err.Error(), "no run command") {
			t.Errorf("target %q: err = %v, want a 'no run command' error", r.Target, r.Err)
		}
	}
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Errorf("a worktree root was created (%v) for a dispatch with no run command", err)
	}
}

// TestDispatchTargetsNoTerminalConfiguredFailsEveryTargetNamingIt is the
// launcher-side counterpart: a run is configured but no [terminal]
// launcher is declared, so every target fails naming that specifically
// (rather than a generic dispatch error), and nothing is created.
func TestDispatchTargetsNoTerminalConfiguredFailsEveryTargetNamingIt(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "")
	wtRoot := filepath.Join(filepath.Dir(repoRoot), "wt")

	results := DispatchTargets(repoRoot, "sweep", workflowAction(), []Target{{Name: "t1", Run: "x"}}, SurfaceDashboard)

	for _, r := range results {
		if r.Err == nil || !strings.Contains(r.Err.Error(), "[terminal]") {
			t.Errorf("target %q: err = %v, want it to name [terminal]", r.Target, r.Err)
		}
	}
	if _, err := os.Stat(wtRoot); !os.IsNotExist(err) {
		t.Errorf("a worktree root was created (%v) for a dispatch with no [terminal] launcher", err)
	}
}

// TestDispatchTargetsWorkerHoldsSlotUntilReleased: a window worker keeps its
// ceiling slot after its spawn returns, so with a ceiling of 2 the third
// target does not spawn until one of the first two is released.
//
// MUTATION EVIDENCE (actually run, then reverted): freeing the slot as soon
// as dispatchOne returns spawned all three at once:
//
//	spawned before any release = 3, want 2 (ceiling)
func TestDispatchTargetsWorkerHoldsSlotUntilReleased(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 2\n\n"+terminalTOML)

	var spawned int32
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		return fmt.Sprintf("pane-%d", atomic.AddInt32(&spawned, 1)), nil
	})

	targets := []Target{{Name: "t0", Run: "x"}, {Name: "t1", Run: "x"}, {Name: "t2", Run: "x"}}
	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "sweep", workflowAction(), targets, SurfaceDashboard)
	}()

	for atomic.LoadInt32(&spawned) < 2 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&spawned); got != 2 {
		t.Fatalf("spawned before any release = %d, want 2 (ceiling)", got)
	}
	select {
	case <-done:
		t.Fatal("DispatchTargets returned while a target was still queued behind held slots")
	default:
	}

	ReleaseWorkerSlot(workerLiveID("sweep", "t0"))
	results := <-done
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("target %q: unexpected error: %v", r.Target, r.Err)
		}
	}
	if got := atomic.LoadInt32(&spawned); got != 3 {
		t.Errorf("spawned after release = %d, want 3", got)
	}
}

// TestKillFreesWorkerSlot: killing a worker through its live-work entry frees
// the slot it holds, so a queued target starts under a ceiling of 1.
func TestKillFreesWorkerSlot(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 1\n\n"+terminalTOML)
	var spawned int32
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		return fmt.Sprintf("pane-%d", atomic.AddInt32(&spawned, 1)), nil
	})

	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "kill", workflowAction(),
			[]Target{{Name: "t0", Run: "x"}, {Name: "t1", Run: "x"}}, SurfaceDashboard)
	}()
	for atomic.LoadInt32(&spawned) < 1 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&spawned); got != 1 {
		t.Fatalf("spawned with the only slot held = %d, want 1", got)
	}

	entry, ok := livework.Default.Get(workerLiveID("kill", "t0"))
	if !ok {
		t.Fatal("t0 not in the live registry")
	}
	if err := entry.Ops.Kill(); err != nil {
		t.Fatal(err)
	}
	results := <-done
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("target %q: %v", r.Target, r.Err)
		}
	}
}

// TestRegistryFailureFreesSlot: a worker whose live-work entry cannot be added
// (its id is already running) closes its pane and frees its slot.
func TestRegistryFailureFreesSlot(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 1\n\n"+terminalTOML)
	err := livework.Default.Add(&livework.Entry{
		ID: workerLiveID("dup", "t0"), Name: "dup", Kind: "workflow", Handle: "existing", Via: "terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) { return "pane-x", nil })

	results := DispatchTargets(repoRoot, "dup", workflowAction(),
		[]Target{{Name: "t0", Run: "x"}, {Name: "t1", Run: "x"}}, SurfaceDashboard)
	if results[0].Err == nil {
		t.Error("t0: want the duplicate-entry error")
	}
	if results[1].Err != nil {
		t.Errorf("t1: %v", results[1].Err)
	}
}

// TestDispatchTargetsSpawnFailureFreesSlot: a target whose spawn fails holds
// nothing, so the next target still starts under a ceiling of 1.
func TestDispatchTargetsSpawnFailureFreesSlot(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 1\n\n"+terminalTOML)
	var n int32
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		if atomic.AddInt32(&n, 1) == 1 {
			return "", errors.New("no pane")
		}
		return "pane-2", nil
	})

	results := DispatchTargets(repoRoot, "sweep", workflowAction(),
		[]Target{{Name: "t0", Run: "x"}, {Name: "t1", Run: "x"}}, SurfaceDashboard)
	if results[0].Err == nil {
		t.Error("t0: want the spawn error on the result")
	}
	if results[1].Err != nil {
		t.Errorf("t1: %v", results[1].Err)
	}
}

// TestDispatchOneFailsLoudlyWhenWorktreeVanished is operational knowledge
// item 2: a worktree created earlier can be removed by something outside
// this dispatch (a sweeper, a person) before its turn to run arrives.
// dispatchOne must fail that target loudly rather than silently recreating
// the worktree at a base nobody chose, and must never spawn a pane at all once the worktree is gone.
//
// MUTATION EVIDENCE (actually run, then reverted): short-circuiting the
// `if !create.IsRegisteredWorktree(...)` guard in dispatchOne (`if false &&
// ...`) produced this real failure:
//
//	expected an error for a worktree removed before dispatch, got nil
func TestDispatchOneFailsLoudlyWhenWorktreeVanished(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, ""+terminalTOML)
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

	spawned := false
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) { spawned = true; return "pane-1", nil })

	cfg, err := config.Load(repoRoot)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	res := dispatchOne(repoRoot, "sweep", "sweep-g", 0, cfg.Terminal, Target{Name: "vanish-me", Run: "x"}, wt, func() {})

	if res.Err == nil {
		t.Fatal("expected an error for a worktree removed before dispatch, got nil")
	}
	if !strings.Contains(res.Err.Error(), "no longer exists") {
		t.Errorf("error = %v, want it to say the worktree no longer exists", res.Err)
	}
	if spawned {
		t.Error("spawnPane was called for a worktree that no longer exists")
	}
	if _, err := os.Stat(wt.Path); err == nil {
		t.Error("a worktree was recreated at the removed path — dispatchOne must never do this")
	}
}

// TestDispatchTargetsSpawnsRunWithTargetEnv is the launch contract: the
// launcher receives the target's run string as ADMIN_CMD, the target id as
// ADMIN_TARGET and the target's own worktree as ADMIN_CWD.
func TestDispatchTargetsSpawnsRunWithTargetEnv(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	var gotCmd, gotTarget, gotCwd string
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		gotCmd, gotTarget, gotCwd = cmdLine, ctx.Target, cwd
		return "pane-1", nil
	})

	action := manifest.Action{"kind": "workflow", "run": "claude '/implement $ADMIN_TARGET afk'"}
	res := DispatchTargets(repoRoot, "implement", action, []Target{NewTarget(action, "proj-1")}, SurfaceDashboard)

	if res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if want := "export ADMIN_TARGET=proj-1; claude '/implement $ADMIN_TARGET afk'"; gotCmd != want {
		t.Errorf("ADMIN_CMD = %q, want %q: the run string behind an export of its target", gotCmd, want)
	}
	if gotTarget != "proj-1" {
		t.Errorf("ADMIN_TARGET = %q, want proj-1", gotTarget)
	}
	if gotCwd != res[0].WorktreePath {
		t.Errorf("ADMIN_CWD = %q, want the target's worktree %q", gotCwd, res[0].WorktreePath)
	}
}

// TestSpawnLauncherExportsAdminTarget runs the real launcher shell and reads
// back the environment [terminal].spawn sees.
func TestSpawnLauncherExportsAdminTarget(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env")
	term := config.Terminal{Spawn: `printf '%s|%s|%s' "$ADMIN_CMD" "$ADMIN_TARGET" "$ADMIN_CWD" > ` + out + `; echo h1`}
	lc := launchCtx{Label: "implement", Target: "proj-7"}

	handle, err := spawnLauncher("do-it", "/wt/proj-7", lc, term)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if handle != "h1" || string(got) != "do-it|proj-7|/wt/proj-7" {
		t.Errorf("handle = %q, env = %q; want h1 and do-it|proj-7|/wt/proj-7", handle, got)
	}
}

// TestDispatchTargetsRecordsLiveWorkWithPaneHandle: each dispatched target
// appears in the live registry, running, with its pane handle.
func TestDispatchTargetsRecordsLiveWorkWithPaneHandle(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, terminalTOML)
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) { return "pane-live-1", nil })

	res := DispatchTargets(repoRoot, "sweep", workflowAction(), []Target{{Name: "t1", Run: "x"}}, SurfaceDashboard)
	if res[0].Err != nil {
		t.Fatal(res[0].Err)
	}

	entry, ok := livework.Default.Get(workerLiveID("sweep", "t1"))
	if !ok {
		t.Fatal("live-work entry not present after dispatch")
	}
	if entry.Handle != "pane-live-1" {
		t.Errorf("entry.Handle = %q, want pane-live-1", entry.Handle)
	}
	if entry.State != livework.StateRunning {
		t.Errorf("entry.State = %q, want running", entry.State)
	}
}

// TestDispatchOneRecordsWorkerInfo proves a workflow target's live-work
// entry carries its branch and worktree — the acceptance criterion "an
// agent worker has a branch and a worktree" — and that Attach/Kill
// are both wired, not left nil.
func TestDispatchOneRecordsWorkerInfo(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, ""+terminalTOML)
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) { return "pane-info-1", nil })

	res := DispatchTargets(repoRoot, "review", workflowAction(), []Target{{Name: "t1", Run: "x"}}, SurfaceDashboard)
	if res[0].Err != nil {
		t.Fatalf("DispatchTargets: %v", res[0].Err)
	}

	entry, ok := livework.Default.Get(workerLiveID("review", "t1"))
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

// killLogTerminal is a [terminal] table whose kill appends the pane handle to
// a file, so a test can see which panes DispatchTargets closed.
func killLogTerminal(killLog string) string {
	return "[terminal]\nspawn = \"true\"\nattach = \"true\"\nkill = \"echo $ADMIN_HANDLE >> " + killLog + "\"\n"
}

func killedHandles(t *testing.T, killLog string) []string {
	t.Helper()
	b, err := os.ReadFile(killLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// TestDispatchTargetsTargetsDifferingAfterChar32GetOwnEntries: two targets
// whose names share their first 32 sanitized characters must be two live-work
// entries, each with its own pane handle.
func TestDispatchTargetsTargetsDifferingAfterChar32GetOwnEntries(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, ""+terminalTOML)
	var mu sync.Mutex
	n := 0
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("pane-%d", n), nil
	})

	targets := []Target{{Name: "admin-3rgp-79-fix-login-timeout-a", Run: "x"}, {Name: "admin-3rgp-79-fix-login-timeout-b", Run: "x"}}
	results := DispatchTargets(repoRoot, "implement", workflowAction(), targets, SurfaceDashboard)

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("target %q: %v", r.Target, r.Err)
		}
	}
	if got := len(livework.Default.List()); got != 2 {
		t.Fatalf("registry holds %d entries, want 2", got)
	}
	for _, tg := range targets {
		e, ok := livework.Default.Get(workerLiveID("implement", tg.Name))
		if !ok {
			t.Fatalf("no entry for %q", tg.Name)
		}
		if e.Handle == "" {
			t.Errorf("entry for %q has no handle", tg.Name)
		}
	}
	if a, b := workerLiveID("implement", targets[0].Name), workerLiveID("implement", targets[1].Name); a == b {
		t.Errorf("both targets map to ID %q", a)
	}
}

// TestDispatchTargetsBrokenManifestReportsParseError: a manifest that does not
// parse fails every target with that error, not "no run command".
func TestDispatchTargetsBrokenManifestReportsParseError(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow\nceiling = 1\n")

	res := DispatchTargets(repoRoot, "sweep", workflowAction(), []Target{{Name: "t1", Run: "x"}, {Name: "t2", Run: "x"}}, SurfaceDashboard)

	for _, r := range res {
		if r.Err == nil || !strings.Contains(r.Err.Error(), "loading config") {
			t.Errorf("target %q: err = %v, want a 'loading config' error", r.Target, r.Err)
		}
		if r.Err != nil && strings.Contains(r.Err.Error(), "no run command") {
			t.Errorf("target %q: parse error was replaced by %v", r.Target, r.Err)
		}
	}
}

// TestDispatchTargetsSpawnOnlyTerminalRefusesToDispatch: a [terminal] with
// spawn but no attach/kill would open panes nothing can close.
func TestDispatchTargetsSpawnOnlyTerminalRefusesToDispatch(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[terminal]\nspawn = \"true\"\n")
	spawned := false
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) { spawned = true; return "pane-x", nil })

	res := DispatchTargets(repoRoot, "sweep", workflowAction(), []Target{{Name: "t1", Run: "x"}}, SurfaceDashboard)

	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "attach") || !strings.Contains(res[0].Err.Error(), "kill") {
		t.Errorf("err = %v, want it to name the missing attach and kill", res[0].Err)
	}
	if spawned {
		t.Error("a pane was opened despite the incomplete [terminal]")
	}
}

// TestDispatchOneDuplicateLiveEntryClosesPane: Registry.Add refusing a live
// duplicate must close the pane this dispatch opened, and keep the original.
func TestDispatchOneDuplicateLiveEntryClosesPane(t *testing.T) {
	killLog := filepath.Join(t.TempDir(), "kills")
	repoRoot := setupWorkflowRepo(t, ""+killLogTerminal(killLog))
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) { return "pane-new", nil })
	if err := livework.Default.Add(&livework.Entry{ID: workerLiveID("sweep", "t1"), Handle: "pane-old"}); err != nil {
		t.Fatal(err)
	}

	res := DispatchTargets(repoRoot, "sweep", workflowAction(), []Target{{Name: "t1", Run: "x"}}, SurfaceDashboard)

	if res[0].Err == nil {
		t.Fatal("dispatch over a live duplicate succeeded")
	}
	if got := killedHandles(t, killLog); len(got) != 1 || got[0] != "pane-new" {
		t.Errorf("killed panes = %v, want [pane-new]", got)
	}
	if e, _ := livework.Default.Get(workerLiveID("sweep", "t1")); e == nil || e.Handle != "pane-old" {
		t.Errorf("original entry = %+v, want it untouched", e)
	}
}

// TestDispatchOneSpawnFailureReportsNoHandle: a launcher that cannot open a
// pane leaves no handle, no entry and nothing to kill.
func TestDispatchOneSpawnFailureReportsNoHandle(t *testing.T) {
	killLog := filepath.Join(t.TempDir(), "kills")
	repoRoot := setupWorkflowRepo(t, ""+killLogTerminal(killLog))
	stubSpawn(t, func(cmdLine, cwd string, ctx launchCtx) (string, error) { return "", errors.New("spawn boom") })

	res := DispatchTargets(repoRoot, "sweep", workflowAction(), []Target{{Name: "t1", Run: "x"}}, SurfaceDashboard)

	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "spawn boom") {
		t.Fatalf("err = %v, want the spawn failure", res[0].Err)
	}
	if res[0].Handle != "" {
		t.Errorf("Handle = %q, want empty", res[0].Handle)
	}
	if got := killedHandles(t, killLog); len(got) != 0 {
		t.Errorf("killed panes = %v, want none", got)
	}
	if n := len(livework.Default.List()); n != 0 {
		t.Errorf("registry holds %d entries, want 0", n)
	}
}

func TestRecordLiveWorkClosesPaneWhenRegistryRejects(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "killed")
	term := config.Terminal{Kill: `echo "$ADMIN_HANDLE" > ` + marker}
	id := "record-dup-" + t.Name()
	if err := recordLiveWork(id, "shell", "pane-1", term); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("kill ran for the accepted entry")
	}
	if err := recordLiveWork(id, "shell", "pane-2", term); err == nil {
		t.Fatal("second record of a live id: want rejection")
	}
	got, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(got)) != "pane-2" {
		t.Fatalf("kill marker = %q, %v; want pane-2", got, err)
	}
}
