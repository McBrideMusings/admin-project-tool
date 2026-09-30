package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// settingsMsg carries the parts of the resolved config the dashboard keeps on
// the model — which columns the Beads pane draws. cfg is the
// whole merged config.Config (worktree root, populate, beads,
// workflow) so the Config tab (admin-3rgp.21) can render every field from one
// resolved source, plus which archetype (or admin.toml itself) declared it —
// see keyOrigin in work.go/model.go.
type settingsMsg struct {
	beads   config.Beads
	cfg     config.Config
	loadErr string
}

// loadConfigCmd resolves repoRoot's admin.toml tables, merged with its declared
// archetypes. Who you are is not among them: it comes from `gh api user`
// (loadIdentityCmd / fetchIdentityCmd in commands.go).
func loadConfigCmd(ctx context.Context, repoRoot string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.Load(repoRoot)
		if err != nil {
			// A config that will not parse must not pass as an empty one: the Config
			// tab shows the parse error instead of values.
			return settingsMsg{loadErr: err.Error()}
		}
		return settingsMsg{beads: cfg.Beads, cfg: cfg}
	}
}
