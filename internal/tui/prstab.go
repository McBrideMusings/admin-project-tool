package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// prsTab is the PRs tab as its own model (ADR-0026): its rows and its cursor.
// Everything it knows about the repository comes through the handles its
// constructor asked for — the sync's open PRs and the local git read that tells
// it which PRs have a worktree.
type prsTab struct {
	declared []source
	sync     syncSource
	local    localSource
	// nwo is the repository's owner/name, which turns a PR number into its URL.
	nwo string

	cursor int
}

// newPRsTab builds the tab, declaring the sync source by asking for its handle.
func newPRsTab(f feeds, nwo string) prsTab {
	d := sourceSet{f: f}
	t := prsTab{sync: d.sync(), local: d.local(), nwo: nwo}
	t.declared = d.declared
	return t
}

func (t *prsTab) sources() []source { return t.declared }

func (t *prsTab) cursorRef() *int { return &t.cursor }

func (t *prsTab) rows(sh *shell) []rowItem { return t.prRows(sh) }

// modal is false: the tab has no overlay of its own.
func (t *prsTab) modal() bool { return false }

// key reports every key unhandled: the shell's row bindings and built-ins own
// them all.
func (t *prsTab) key(*shell, tea.KeyMsg) (bool, tea.Cmd) { return false, nil }

func (t *prsTab) body(sh *shell, width, height int) ([]string, []hitTarget) {
	return sh.renderRows(sh.filterRows(t.rows(sh)), t.cursor, width, height)
}

func (t *prsTab) footer(int) (string, bool) { return "", false }

// count is the tab-bar badge: every open PR.
func (t *prsTab) count() int { return len(t.sync.prs()) }

func (t *prsTab) loaded() bool { return t.sync.loaded() }

// freshness is the age of the PRs section the last sync stamped.
func (t *prsTab) freshness() seg { return cachedAgeSeg(t.sync.prsFetchedAt()) }

func (t *prsTab) update(*shell, tea.Msg) tea.Cmd { return nil }
