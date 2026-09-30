package herdr

import (
	"context"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
)

// stubRun swaps the package's exec seam for the duration of the test,
// capturing every argv passed to it.
func stubRun(t *testing.T, out []byte, err error) *[][]string {
	t.Helper()
	var calls [][]string
	orig := run
	run = func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{}, args...))
		return out, err
	}
	t.Cleanup(func() { run = orig })
	return &calls
}

func TestStartBuildsExpectedArgv(t *testing.T) {
	calls := stubRun(t, nil, nil)
	d := New()
	if err := d.Start("pane-1", "claude", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := (*calls)[0]
	want := []string{"agent", "start", "pane-1", "--kind", "claude", "--pane", "pane-1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestStartWithArgsAppendsDoubleDashSeparator(t *testing.T) {
	calls := stubRun(t, nil, nil)
	d := New()
	if err := d.Start("pane-1", "claude", []string{"--foo", "bar"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := (*calls)[0]
	want := []string{"agent", "start", "pane-1", "--kind", "claude", "--pane", "pane-1", "--", "--foo", "bar"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestPromptBareWaitWithNoUntil(t *testing.T) {
	calls := stubRun(t, nil, nil)
	d := New()
	if err := d.Prompt("pane-1", "do the thing", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	got := (*calls)[0]
	want := []string{"agent", "prompt", "pane-1", "do the thing", "--wait"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestPromptRepeatsUntilFlag(t *testing.T) {
	calls := stubRun(t, nil, nil)
	d := New()
	until := []agent.Status{agent.StatusIdle, agent.StatusDone, agent.StatusBlocked}
	if err := d.Prompt("pane-1", "go", until); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	got := (*calls)[0]
	want := []string{
		"agent", "prompt", "pane-1", "go", "--wait",
		"--until", "idle", "--until", "done", "--until", "blocked",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestReadBuildsArgvAndDecodesText(t *testing.T) {
	calls := stubRun(t, []byte(`{"result":{"read":{"text":"hello world"}}}`), nil)
	d := New()
	text, err := d.Read("pane-1", 50)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
	got := (*calls)[0]
	want := []string{"agent", "read", "pane-1", "--lines", "50"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestInfoDecodesResultAgent(t *testing.T) {
	stubRun(t, []byte(`{"result":{"agent":{
		"agent":"claude",
		"agent_status":"working",
		"agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"sess-1"},
		"workspace_id":"ws-1",
		"tab_id":"tab-1",
		"pane_id":"pane-1",
		"cwd":"/repo",
		"interactive_ready":true,
		"terminal_title_stripped":"  doing the thing  "
	}}}`), nil)
	d := New()
	info, err := d.Info("pane-1")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Kind != "claude" {
		t.Errorf("Kind = %q, want claude", info.Kind)
	}
	if info.Status != agent.StatusWorking {
		t.Errorf("Status = %q, want %q", info.Status, agent.StatusWorking)
	}
	if info.Activity != "doing the thing" {
		t.Errorf("Activity = %q, want trimmed %q", info.Activity, "doing the thing")
	}
	if !info.Ready {
		t.Error("Ready = false, want true")
	}
	if info.Session == nil {
		t.Fatal("Session = nil, want a populated SessionRef")
	}
	if info.Session.Workspace != "ws-1" || info.Session.Tab != "tab-1" || info.Session.Pane != "pane-1" || info.Session.Session != "sess-1" {
		t.Errorf("Session = %+v, want workspace/tab/pane/session all populated", info.Session)
	}
	if info.CWD != "/repo" {
		t.Errorf("CWD = %q, want /repo", info.CWD)
	}
}

func TestInfoEmptyStatusBecomesUnknown(t *testing.T) {
	stubRun(t, []byte(`{"result":{"agent":{"agent":"claude"}}}`), nil)
	d := New()
	info, err := d.Info("pane-1")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Status != agent.StatusUnknown {
		t.Errorf("Status = %q, want %q for an unreported status word", info.Status, agent.StatusUnknown)
	}
}

func TestAgentNameSanitizesHandle(t *testing.T) {
	cases := map[string]string{
		"pane-1":       "pane-1",
		"PANE_1":       "pane_1",
		"pane/1:weird": "pane-1-weird",
		"123abc":       "a-123abc",
		"":             "agent",
		"---":          "agent",
	}
	for in, want := range cases {
		if got := agentName(in); got != want {
			t.Errorf("agentName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentNameTruncatesTo32(t *testing.T) {
	long := strings.Repeat("a", 50)
	got := agentName(long)
	if len(got) > 32 {
		t.Errorf("agentName returned %d chars, want <= 32", len(got))
	}
}
