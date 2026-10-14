package tui

import (
	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
)

// loadBeads mounts m's tabs when nothing has mounted them yet, and gives the
// beads feed an answered poll of beads.
func loadBeads(m *Model, beads []bd.Bead) {
	if m.feeds.beads == nil {
		m.mountTabs(viewstate.State{})
	}
	m.feeds.beads.present, m.feeds.beads.have, m.feeds.beads.beads = true, true, beads
}
