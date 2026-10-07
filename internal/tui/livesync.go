// livesync.go reads the live-work entries other admin processes shared
// (internal/livework's persist.go) into this dashboard's registry: once at
// launch, so the first frame already shows a worker `admin worktree -x`
// started, and on every Work tick after. Every poller that judges an entry —
// panepoll, agentpoll, the reaper — then treats a loaded entry exactly like
// one this process dispatched.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// liveWorkSyncMsg carries a failed sync back into Update for the status line.
type liveWorkSyncMsg struct{ err error }

// syncLiveWorkCmd syncs livework.Default with repoRoot's shared entries.
func syncLiveWorkCmd(repoRoot string) tea.Cmd {
	return func() tea.Msg {
		if err := livework.Default.Sync(repoRoot); err != nil {
			return liveWorkSyncMsg{err: err}
		}
		return nil
	}
}
