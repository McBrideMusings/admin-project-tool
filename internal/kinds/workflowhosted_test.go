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

// stubHostedArgv swaps the hosted worker's command line for sh running script,
// with the target's prompt as $1, so a real pty runs a stand-in for claude.
func stubHostedArgv(t *testing.T, script string) {
	t.Helper()
	orig := hostedAgentArgv
	hostedAgentArgv = func(kind, mode, prompt string) ([]string, error) {
		return []string{"sh", "-c", script, "sh", prompt}, nil
	}
	t.Cleanup(func() { hostedAgentArgv = orig })
}

func hostedAction(extra map[string]any) manifest.Action {
	a := manifest.Action{"kind": "workflow", "agent": "claude", "locale": "hosted"}
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
	stubHostedArgv(t, `{ echo "start-$1"; pwd; stty size; sleep 0.4; echo "end-$1"; } >> "$HOSTED_OUT"`)

	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "hostedseq", hostedAction(nil),
			[]Target{{Name: "a", Prompt: "a"}, {Name: "b", Prompt: "b"}}, SurfaceDashboard)
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
	stubHostedArgv(t, "exit 3")
	res := DispatchTargets(repoRoot, "hostedfail", hostedAction(nil), []Target{{Name: "t1", Prompt: "x"}}, SurfaceDashboard)
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "exit status 3") {
		t.Fatalf("Err = %v, want the agent's exit status", res[0].Err)
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
	orig := runInlineAgent
	runInlineAgent = func(argv []string, dir string) error {
		mu.Lock()
		running++
		if running > 1 {
			t.Error("two inline agents ran at once")
		}
		calls = append(calls, fmt.Sprintf("%s @ %s", strings.Join(argv, " "), filepath.Base(dir)))
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { runInlineAgent = orig })

	res := DispatchTargets(repoRoot, "hostedshell", hostedAction(map[string]any{"mode": "print"}),
		[]Target{{Name: "a", Prompt: "do a"}, {Name: "b", Prompt: "do b"}}, SurfaceShell)
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("target %q: %v", r.Target, r.Err)
		}
	}
	want := []string{
		"claude -p do a @ " + filepath.Base(res[0].WorktreePath),
		"claude -p do b @ " + filepath.Base(res[1].WorktreePath),
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("inline calls = %q, want %q", calls, want)
	}
	if _, ok := livework.Default.Get("hostedshell:a"); ok {
		t.Error("shell surface recorded a live-work entry")
	}
}

func TestHostedRejectsWhatItCannotRun(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "")
	res := DispatchTargets(repoRoot, "hostedcodex", hostedAction(map[string]any{"agent": "codex"}),
		[]Target{{Name: "t1"}}, SurfaceShell)
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), `"codex" cannot run hosted`) {
		t.Errorf("non-claude agent: Err = %v", res[0].Err)
	}

	res = DispatchTargets(repoRoot, "modewindow",
		manifest.Action{"kind": "workflow", "agent": "claude", "mode": "print"}, []Target{{Name: "t1"}}, SurfaceDashboard)
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "mode applies to locale") {
		t.Errorf("mode on a window workflow: Err = %v", res[0].Err)
	}
}

func TestAgentArgv(t *testing.T) {
	cases := []struct {
		mode, prompt string
		want         []string
		wantErr      string
	}{
		{"", "fix it", []string{"claude", "fix it"}, ""},
		{"interactive", "", []string{"claude"}, ""},
		{"print", "fix it", []string{"claude", "-p", "fix it"}, ""},
		{"print", "  ", nil, "needs a prompt"},
		{"batch", "x", nil, `mode "batch"`},
	}
	for _, c := range cases {
		got, err := agentArgv("claude", c.mode, c.prompt)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("mode %q prompt %q: err = %v, want %q", c.mode, c.prompt, err, c.wantErr)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("mode %q prompt %q: got %q, %v want %q", c.mode, c.prompt, got, err, c.want)
		}
	}
}

func TestValidateWorkflowsChecksModeAndHostedAgent(t *testing.T) {
	ok := map[string]manifest.Action{"w": {"kind": "workflow", "over": "selection", "locale": "hosted", "mode": "print", "agent": "claude"}}
	if err := ValidateWorkflows(ok); err != nil {
		t.Errorf("valid hosted workflow: %v", err)
	}
	bad := map[string]manifest.Action{
		"m": {"kind": "workflow", "over": "selection", "mode": "batch"},
		"a": {"kind": "workflow", "over": "selection", "locale": "hosted", "agent": "codex"},
	}
	err := ValidateWorkflows(bad)
	if err == nil || !strings.Contains(err.Error(), `mode must be "interactive" or "print"`) ||
		!strings.Contains(err.Error(), "runs the claude command line only") {
		t.Errorf("ValidateWorkflows = %v, want both problems", err)
	}
}

// TestHostedWorkerKilledFailsTheTarget: a worker killed from the dashboard is
// not reported as a finished target.
func TestHostedWorkerKilledFailsTheTarget(t *testing.T) {
	repoRoot := setupWorkflowRepo(t, "")
	stubHostedArgv(t, "sleep 30")
	done := make(chan []TargetResult, 1)
	go func() {
		done <- DispatchTargets(repoRoot, "hostedkill", hostedAction(nil), []Target{{Name: "t1"}}, SurfaceDashboard)
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
