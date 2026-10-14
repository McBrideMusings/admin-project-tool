package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// configTab is the Config tab as its own model (ADR-0026): its rows, its
// cursor and the keys it owns. The manifest and what it declares come through
// src, the one handle its constructor asked for; the merged config.Config
// arrives as the settingsMsg update takes.
type configTab struct {
	declared []source
	src      configSource

	cursor int
	// cfg is the resolved config.Config — repoRoot's admin.toml
	// [worktrees]/[[populate]]/[workflow] tables merged with its declared
	// archetypes. Zero until settingsMsg lands; cfgErr is its load error.
	cfg    config.Config
	cfgErr string
}

// newConfigTab builds the tab, asking for the handle on the manifest.
func newConfigTab(f feeds) configTab {
	d := sourceSet{f: f}
	t := configTab{src: d.config()}
	t.declared = d.declared
	return t
}

func (t *configTab) sources() []source { return t.declared }

func (t *configTab) cursorRef() *int { return &t.cursor }

func (t *configTab) rows(sh *shell) []rowItem { return t.configRows(sh) }

// modal is always false: the tab has no overlay of its own.
func (t *configTab) modal() bool { return false }

// key handles no keys of its own: the cursor keys and ↵ are the shell's list
// machinery, and ↵ reaches activate, which hands a config row back to
// openRow.
func (t *configTab) key(*shell, string) (bool, tea.Cmd) { return false, nil }

// body draws the filtered row list.
func (t *configTab) body(sh *shell, width, height int) ([]string, []hitTarget) {
	return sh.renderRows(sh.filterRows(t.rows(sh)), t.cursor, width, height)
}

func (t *configTab) footer() (string, bool) { return "", false }

// count is zero: the tab bar draws no badge for this tab.
func (t *configTab) count() int { return 0 }

// loaded is always true: the manifest is read synchronously at launch.
func (t *configTab) loaded() bool { return true }

// freshness reads "cached": nothing the tab draws carries a fetch time.
func (t *configTab) freshness() seg { return seg{t: "cached", fg: cFaint} }

// update takes the merged config.
func (t *configTab) update(_ *shell, msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(settingsMsg); ok {
		t.cfg, t.cfgErr = msg.cfg, msg.loadErr
	}
	return nil
}

// openRow is ↵ or a second click on a config row: a value row has nothing to
// open, a path row opens with the platform's default handler unless the path
// is absent.
func (t *configTab) openRow(sh *shell, r rowItem) tea.Cmd {
	switch r.kind {
	case rowConfigValue:
		sh.setStatus("nothing to open on this row", cDim)
	case rowConfigPath:
		if r.cfg == nil {
			return nil
		}
		if r.cfg.absent {
			sh.setStatus(r.cfg.label+" does not exist", cRed)
			return nil
		}
		return openPathCmd(r.cfg.path, r.cfg.label)
	}
	return nil
}
