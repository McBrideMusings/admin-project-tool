package herdr

import (
	"context"
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
	info, err := d.Info(context.Background(), "pane-1")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Status != agent.StatusWorking {
		t.Errorf("Status = %q, want %q", info.Status, agent.StatusWorking)
	}
	if info.Activity != "doing the thing" {
		t.Errorf("Activity = %q, want trimmed %q", info.Activity, "doing the thing")
	}
}

func TestInfoEmptyStatusBecomesUnknown(t *testing.T) {
	stubRun(t, []byte(`{"result":{"agent":{"agent":"claude"}}}`), nil)
	d := New()
	info, err := d.Info(context.Background(), "pane-1")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Status != agent.StatusUnknown {
		t.Errorf("Status = %q, want %q for an unreported status word", info.Status, agent.StatusUnknown)
	}
}
