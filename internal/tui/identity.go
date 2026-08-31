package tui

import (
	"context"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// settingsMsg carries the parts of the merged config the dashboard keeps on the
// model — who you are, and which columns the Beads pane draws — plus whether this
// launch was the one that seeded the identity. merged/global/local carry the
// whole config three ways (merged, global file alone, local file alone) so
// the Config tab (admin-3rgp.21) can say which file — or neither — declared
// each field, not just its final resolved value.
type settingsMsg struct {
	id      config.Identity
	beads   config.Beads
	seeded  bool
	merged  config.Config
	global  config.Config
	local   config.Config
	loadErr string
}

// loadConfigCmd resolves who you are, seeding the global config on first run.
//
// Seeding happens before the load, not instead of it, so the very first launch in
// a fresh install classifies branches immediately rather than showing every one of
// them as Unsorted until somebody goes and writes a config file. The two sources
// are the ones already sitting in the environment — the `gh` account the dashboard
// authenticates with, and the email git is committing under — which between them
// answer both rungs of the ownership question for the common case of one person on
// one machine.
//
// Everything past that first run is the config's business: seeding never overwrites
// an existing [identity], so an email added by hand (a work address, an agent's
// address) is never re-guessed away on the next launch.
func loadConfigCmd(ctx context.Context, repoRoot string) tea.Cmd {
	return func() tea.Msg {
		seeded, _ := config.SeedIdentity(viewerLogin(ctx), gitEmail(ctx))
		cfg, err := config.Load(repoRoot)
		if err != nil {
			// A config that will not parse must not silently mean "nothing is yours":
			// that files every branch under Unsorted with no hint why. The identity
			// stays zero and the Config tab shows the parse error instead of a value.
			return settingsMsg{loadErr: err.Error()}
		}
		// Reading each file alone (in addition to the merge above) is what
		// lets the Config tab say which file declared a field rather than
		// only its final value. Either read failing here is already the
		// same failure config.Load would have hit, so it can't happen
		// without err above already firing — global/local are read only for
		// display, never let a real load error through silently.
		global, _ := config.LoadGlobal()
		local, _ := config.LoadLocal(repoRoot)
		return settingsMsg{id: cfg.Identity, beads: cfg.Beads, seeded: seeded, merged: cfg, global: global, local: local}
	}
}

// viewerLogin is the authenticated GitHub account, or "" when `gh` is not logged
// in. Read directly rather than from Remote.ViewerLogin because seeding runs at
// launch, before any fetch has landed.
func viewerLogin(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "gh", "api", "user", "-q", ".login").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitEmail is the email git would author a commit with here — the repo-local
// value when one is set, else the global one, which is exactly the address the
// branches on this machine carry.
func gitEmail(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "config", "--get", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
