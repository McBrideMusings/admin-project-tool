// livenesspoll.go is the Work tab's per-tick liveness round: once per tick it
// hands every running or waiting entry to kinds.ProbeLiveness, which asks the
// project's [terminal].status whether each launcher pane still exists and the
// agent driver whether each worker's agent is blocked on a prompt or working.
// Nothing else notices a pane the user closed by hand, or an agent that has
// sat on an approval prompt for minutes: an entry only transitions when
// something reports having watched the real thing settle.
//
// The round runs off the render path and comes back as one livenessMsg, and
// Update's handler is the only thing that applies it (kinds.ApplyLiveness),
// so every livework write stays on the Update goroutine. A worker whose pane
// is gone is left for reappoll.go: its work may still be on disk.
package tui

import (
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
)

// livenessMsg carries a completed round back into Update.
type livenessMsg struct {
	results []kinds.Liveness
}

// livenessInflight makes an overlapping tick a no-op: a round whose queries
// are still running when the next tick fires is not doubled up.
var livenessInflight atomic.Bool

// pollLivenessCmd runs one round over the live entries. statusCmd is the
// project's [terminal].status, "" when it declares none — then only agents
// are asked about.
func pollLivenessCmd(statusCmd string) tea.Cmd {
	return func() tea.Msg {
		if !livenessInflight.CompareAndSwap(false, true) {
			return nil
		}
		defer livenessInflight.Store(false)
		results := kinds.ProbeLiveness(kinds.LiveEntries(), statusCmd, kinds.PaneAndAgent)
		if len(results) == 0 {
			return nil
		}
		return livenessMsg{results: results}
	}
}
