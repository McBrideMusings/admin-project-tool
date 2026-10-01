package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
// records it as a hosted-command entry (Via empty, id <action>:<target>), and
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
		if got, ok := livework.Default.Get("hostedseq:a"); ok {
			e = got
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e == nil {
		t.Fatal("no live-work entry hostedseq:a while the first worker runs")
	}
	if e.Kind != "hosted-pty" || e.Via != "" || e.Worker != nil || e.Ops.Write == nil || e.Ops.Resize == nil {
		t.Errorf("entry = kind %q via %q worker %v write %v, want the hosted-command shape",
			e.Kind, e.Via, e.Worker, e.Ops.Write != nil)
	}
	if _, ok := livework.Default.Get("hostedseq:b"); ok {
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
	if st, _ := livework.Default.State("hostedseq:a"); st != livework.StateExited {
		t.Errorf("hostedseq:a state = %q, want exited", st)
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
	if st, _ := livework.Default.State("hostedfail:t1"); st != livework.StateFailed {
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
		"do a [ADMIN_TARGET=a] @ " + filepath.Base(res[0].WorktreePath),
		"do b [ADMIN_TARGET=b] @ " + filepath.Base(res[1].WorktreePath),
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("inline calls = %q, want %q", calls, want)
	}
	if _, ok := livework.Default.Get("hostedshell:a"); ok {
		t.Error("shell surface recorded a live-work entry")
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
		if _, ok := livework.Default.Get("hostedkill:t1"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := livework.Default.Kill("hostedkill:t1"); err != nil {
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
	ok := map[string]manifest.Action{"w": {"kind": "workflow", "over": "selection", "run": "go $ADMIN_TARGET", "done": "true"}}
	if err := ValidateWorkflows(ok); err != nil {
		t.Errorf("valid workflow: %v", err)
	}
	noRun := map[string]manifest.Action{"nr": {"kind": "workflow", "over": "selection"}}
	if err := ValidateWorkflows(noRun); err == nil || !strings.Contains(err.Error(), `action "nr"`) || !strings.Contains(err.Error(), "'run'") {
		t.Errorf("no run: ValidateWorkflows = %v, want an error naming the action and run", err)
	}
	blankDone := map[string]manifest.Action{"bd": {"kind": "workflow", "over": "selection", "run": "x", "done": "  "}}
	if err := ValidateWorkflows(blankDone); err == nil || !strings.Contains(err.Error(), "'done'") {
		t.Errorf("blank done: ValidateWorkflows = %v", err)
	}
	for _, key := range []string{"agent", "mode", "prompt"} {
		old := map[string]manifest.Action{"o": {"kind": "workflow", "over": "selection", "run": "x", key: "v"}}
		if err := ValidateWorkflows(old); err == nil || !strings.Contains(err.Error(), "'"+key+"' is not a workflow key") {
			t.Errorf("key %s: ValidateWorkflows = %v, want it named", key, err)
		}
	}
}
