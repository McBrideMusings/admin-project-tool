package kinds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func hostedAction(extra map[string]any) manifest.Action {
	a := manifest.Action{"kind": "workflow", "run": "true", "locale": "hosted"}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

// TestHostedWorkersRunOnDashboardPtyOneSlotAtATime is admin-spjp.5's
// acceptance check: with no [terminal] and no agent driver at all, a hosted
// dashboard dispatch runs each worker on a pty in its own worktree at 24x80,
// records it as a worker entry on a hosted pty (Via empty, id <action>-<target>), and
// frees the ceiling-1 slot only when the first process has exited.
func TestHostedWorkersRunOnDashboardPtyOneSlotAtATime(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 1\n")
	out := filepath.Join(t.TempDir(), "out.log")
	t.Setenv("HOSTED_OUT", out)
	run := `{ echo "start-$ADMIN_TARGET"; pwd; stty size; sleep 0.4; echo "end-$ADMIN_TARGET"; } >> "$HOSTED_OUT"`

	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "hostedseq", hostedAction(nil),
			[]Target{{Name: "a", Run: run}, {Name: "b", Run: run}}, SurfaceDashboard)
	}()

	deadline := time.Now().Add(10 * time.Second)
	var e *livework.Entry
	for time.Now().Before(deadline) {
		if got, ok := livework.Default.Get("hostedseq-a"); ok {
			e = got
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e == nil {
		t.Fatal("no live-work entry hostedseq-a while the first worker runs")
	}
	if e.Kind != "hosted-pty" || e.Via != "" || e.Worker == nil || e.Ops.Write == nil || e.Ops.Resize == nil {
		t.Errorf("entry = kind %q via %q worker %v write %v, want a hosted pty carrying WorkerInfo",
			e.Kind, e.Via, e.Worker, e.Ops.Write != nil)
	}
	if _, ok := livework.Default.Get("hostedseq-b"); ok {
		t.Error("second worker was registered while the first still held the only slot")
	}

	select {
	case results := <-done:
		for _, r := range results {
			if r.Err != nil {
				t.Errorf("target %q: %v", r.Target, r.Err)
			}
		}
		wa, wb := results[0].WorktreePath, results[1].WorktreePath
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		real := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
		want := strings.Join([]string{
			"start-a", real(wa), "24 80", "end-a",
			"start-b", real(wb), "24 80", "end-b",
		}, "\n") + "\n"
		got := strings.ReplaceAll(string(raw), "\r", "")
		if got != want {
			t.Errorf("worker output:\n%s\nwant:\n%s", got, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("DispatchTargets never returned")
	}
	if st, _ := livework.Default.State("hostedseq-a"); st != livework.StateExited {
		t.Errorf("hostedseq-a state = %q, want exited", st)
	}
}

func TestHostedWorkerNonzeroExitFailsTheTarget(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "")
	res := DispatchTargets(repoRoot, "hostedfail", hostedAction(nil), []Target{{Name: "t1", Run: "exit 3"}}, SurfaceDashboard)
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "exit status 3") {
		t.Fatalf("Err = %v, want the run's exit status", res[0].Err)
	}
	if strings.Contains(res[0].Err.Error(), "exit 3") {
		t.Errorf("Err = %v, echoes the run string", res[0].Err)
	}
	if st, _ := livework.Default.State("hostedfail-t1"); st != livework.StateFailed {
		t.Errorf("state = %q, want failed", st)
	}
}

// TestHostedShellSurfaceRunsInlineOneAtATime: from a plain shell no pty is
// hosted and no entry recorded; each agent gets the terminal in turn.
func TestHostedShellSurfaceRunsInlineOneAtATime(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 3\n")
	var mu sync.Mutex
	var running int
	var calls []string
	orig := runInlineWorker
	runInlineWorker = func(argv []string, dir string, env []string) error {
		mu.Lock()
		running++
		if running > 1 {
			t.Error("two inline agents ran at once")
		}
		calls = append(calls, fmt.Sprintf("%s [%s] @ %s", argv[len(argv)-1], strings.Join(env, " "), filepath.Base(dir)))
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { runInlineWorker = orig })

	res := DispatchTargets(repoRoot, "hostedshell", hostedAction(nil),
		[]Target{{Name: "a", Run: "do a"}, {Name: "b", Run: "do b"}}, SurfaceShell)
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("target %q: %v", r.Target, r.Err)
		}
	}
	want := []string{
		"do a [ADMIN_TARGET=a ADMIN_WORKER_WORKTREE=" + res[0].WorktreePath + "] @ " + filepath.Base(res[0].WorktreePath),
		"do b [ADMIN_TARGET=b ADMIN_WORKER_WORKTREE=" + res[1].WorktreePath + "] @ " + filepath.Base(res[1].WorktreePath),
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("inline calls = %q, want %q", calls, want)
	}
	if _, ok := livework.Default.Get("hostedshell:a"); ok {
		t.Error("shell surface recorded a live-work entry")
	}
}

// TestHostedDispatchHonoursCeiling: five hosted targets under a ceiling of 2
// start two workers, hold the other three until a process exits, and never
// have more than two running at once.
func TestHostedDispatchHonoursCeiling(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "[workflow]\nceiling = 2\n")

	var mu sync.Mutex
	var running, peak, started int
	exits := make(chan chan error, 5)
	orig := startHostedWorker
	startHostedWorker = func(id string, argv []string, dir string, env []string, rows, cols int, _ *livework.WorkerInfo) (<-chan error, error) {
		exit := make(chan error, 1)
		mu.Lock()
		running++
		started++
		if running > peak {
			peak = running
		}
		mu.Unlock()
		exits <- exit
		return exit, nil
	}
	t.Cleanup(func() { startHostedWorker = orig })

	targets := make([]Target, 5)
	for i := range targets {
		targets[i] = Target{Name: fmt.Sprintf("t%d", i), Run: "x"}
	}
	done := make(chan []TargetResult, 1)
	go func() { done <- DispatchTargets(repoRoot, "hostedceil", hostedAction(nil), targets, SurfaceDashboard) }()

	finish := func() {
		exit := <-exits
		mu.Lock()
		running--
		mu.Unlock()
		exit <- nil
	}
	startedNow := func() int { mu.Lock(); defer mu.Unlock(); return started }

	deadline := time.Now().Add(30 * time.Second)
	for startedNow() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := startedNow(); got != 2 {
		t.Fatalf("started before any worker exited = %d, want 2 (ceiling)", got)
	}
	for range targets {
		finish()
	}

	select {
	case results := <-done:
		for _, r := range results {
			if r.Err != nil {
				t.Errorf("target %q: %v", r.Target, r.Err)
			}
		}
	case <-time.After(30 * time.Second):
		t.Fatal("DispatchTargets never returned")
	}
	mu.Lock()
	defer mu.Unlock()
	if started != 5 || peak != 2 {
		t.Errorf("started = %d, peak concurrent = %d, want 5 and 2", started, peak)
	}
}

// TestHostedWorkerKilledFailsTheTarget: a worker killed from the dashboard is
// not reported as a finished target.
func TestHostedWorkerKilledFailsTheTarget(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "")
	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "hostedkill", hostedAction(nil), []Target{{Name: "t1", Run: "sleep 30"}}, SurfaceDashboard)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := livework.Default.Get("hostedkill-t1"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := livework.Default.Kill("hostedkill-t1"); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "killed") {
			t.Errorf("Err = %v, want a killed error", res[0].Err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("DispatchTargets never returned after Kill")
	}
}

func TestValidateWorkflowsChecksRunDoneAndRemovedKeys(t *testing.T) {
	ok := map[string]manifest.Action{"w": {"kind": "workflow", "over": "row", "run": "go $ADMIN_TARGET", "done": "true"}}
	if err := ValidateWorkflows(ok); err != nil {
		t.Errorf("valid workflow: %v", err)
	}
	noRun := map[string]manifest.Action{"nr": {"kind": "workflow", "over": "row"}}
	if err := ValidateWorkflows(noRun); err == nil || !strings.Contains(err.Error(), `action "nr"`) || !strings.Contains(err.Error(), "'run'") {
		t.Errorf("no run: ValidateWorkflows = %v, want an error naming the action and run", err)
	}
	blankDone := map[string]manifest.Action{"bd": {"kind": "workflow", "over": "row", "run": "x", "done": "  "}}
	if err := ValidateWorkflows(blankDone); err == nil || !strings.Contains(err.Error(), "'done'") {
		t.Errorf("blank done: ValidateWorkflows = %v", err)
	}
	for _, key := range []string{"agent", "mode", "prompt"} {
		old := map[string]manifest.Action{"o": {"kind": "workflow", "over": "row", "run": "x", key: "v"}}
		if err := ValidateWorkflows(old); err == nil || !strings.Contains(err.Error(), "'"+key+"' is not a workflow key") {
			t.Errorf("key %s: ValidateWorkflows = %v, want it named", key, err)
		}
	}
}

// TestReapWorkerStopsHostedWorker: a hosted worker whose done predicate passes
// is reaped like a window worker, its pty killed through the entry's own Kill
// instead of a launcher, and the dispatch returns.
func TestReapWorkerStopsHostedWorker(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "")
	addOrigin(t, repoRoot)
	action := hostedAction(nil)
	targets := []Target{{Name: "h0", Run: "sleep 30", Done: "true"}}

	done := make(chan []TargetResult, 1)
	go func() { done <- DispatchTargets(repoRoot, "hostedreap", action, targets, SurfaceDashboard) }()

	var e *livework.Entry
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && e == nil; time.Sleep(10 * time.Millisecond) {
		e, _ = livework.Default.Get("hostedreap-h0")
	}
	if e == nil {
		t.Fatal("no live-work entry hostedreap-h0")
	}
	if ended, _ := ReapWorker(context.Background(), repoRoot, e, config.Terminal{}); !ended {
		line, _ := livework.Default.Line(e.ID)
		t.Fatalf("ReapWorker did not reap a finished hosted worker (line %q)", line)
	}
	select {
	case res := <-done:
		if res[0].Err != nil {
			t.Errorf("a reaped worker reported %v, want a finished target", res[0].Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hosted worker still running after its reap")
	}
	if _, err := os.Stat(e.Worker.Worktree); err == nil {
		t.Error("worktree still on disk after the reap")
	}
}
