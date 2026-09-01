package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// settingsMsg carries the parts of the resolved config the dashboard keeps on
// the model — who you are, and which columns the Beads pane draws. cfg is the
// whole merged config.Config (identity, worktree root, populate, beads,
// workflow) so the Config tab (admin-3rgp.21) can render every field from one
// resolved source, plus which archetype (or admin.toml itself) declared it —
// see keyOrigin in work.go/model.go.
type settingsMsg struct {
	id      config.Identity
	beads   config.Beads
	cfg     config.Config
	loadErr string
}

// loadConfigCmd resolves who you are from repoRoot's admin.toml [identity]
// table, merged with its declared archetypes.
//
// There is no seeding step: identity now lives in the one file this repo
// commits (or, for every other project, the one manifest each project owns),
// and writing a machine-specific login/email into that file on first launch
// would be wrong the moment the manifest is shared. A project with no
// [identity] declared shows every branch as Unsorted until someone adds one
// by hand.
func loadConfigCmd(ctx context.Context, repoRoot string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.Load(repoRoot)
		if err != nil {
			// A config that will not parse must not silently mean "nothing is yours":
			// that files every branch under Unsorted with no hint why. The identity
			// stays zero and the Config tab shows the parse error instead of a value.
			return settingsMsg{loadErr: err.Error()}
		}
		return settingsMsg{id: cfg.Identity, beads: cfg.Beads, cfg: cfg}
	}
}
