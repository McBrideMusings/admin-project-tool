package tui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// deadlineDriver records the deadline on the ctx queryAgentInfo hands Info.
type deadlineDriver struct {
	agent.Driver
	hasDeadline bool
	remaining   time.Duration
}

func (d *deadlineDriver) Info(ctx context.Context, _ string) (agent.Info, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.hasDeadline = true
		d.remaining = time.Until(dl)
	}
	return agent.Info{Status: agent.StatusWorking}, nil
}

// deadlineDrv is registered once for the whole test binary: agent.Register
// has no unregister, so a per-test registration would leave the first run's
// driver answering Select on a repeat run. It answers Select only while a
// test holds deadlineDriverActive true.
var (
	deadlineDrv          = &deadlineDriver{}
	deadlineDriverActive atomic.Bool
)

func init() {
	agent.Register("deadline-test", deadlineDriverActive.Load, deadlineDrv)
}

// TestPollPassesItsDeadlineToTheDriver proves the per-query ceiling reaches
// the driver through agent.Driver.Info itself, so one wedged agent cannot
// freeze the Work tab's status updates.
func TestPollPassesItsDeadlineToTheDriver(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	*deadlineDrv = deadlineDriver{}
	deadlineDriverActive.Store(true)
	t.Cleanup(func() { deadlineDriverActive.Store(false) })

	addWorkflowEntry(t, "deadline", livework.StateRunning)
	if msg := pollAgentStatusCmd()(); msg == nil {
		t.Fatal("pollAgentStatusCmd() = nil, want an agentStatusMsg")
	}
	if !deadlineDrv.hasDeadline {
		t.Fatal("driver.Info got a context with no deadline")
	}
	if deadlineDrv.remaining <= 0 || deadlineDrv.remaining > agentPollTimeout {
		t.Errorf("deadline in %v, want within (0, %v]", deadlineDrv.remaining, agentPollTimeout)
	}
}
