package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// issuesTab is the Issues tab as its own model (ADR-0026): its rows and its
// cursor. Everything it knows about the repository comes through the handles
// its constructor asked for — the sync's issues and the local git read that
// tells it which issues already have a branch.
type issuesTab struct {
	declared []source
	sync     syncSource
	local    localSource
	// nwo is the repository's owner/name, which turns an issue number into its
	// URL.
	nwo string

	cursor int
}

// newIssuesTab builds the tab, declaring the sync source by asking for its
// handle.
func newIssuesTab(f feeds, nwo string) issuesTab {
	d := sourceSet{f: f}
	t := issuesTab{sync: d.sync(), local: d.local(), nwo: nwo}
	t.declared = d.declared
	return t
}

func (t *issuesTab) sources() []source { return t.declared }

func (t *issuesTab) cursorRef() *int { return &t.cursor }

func (t *issuesTab) rows(sh *shell) []rowItem { return t.issueRows(sh) }

// modal is false: the tab has no overlay of its own.
func (t *issuesTab) modal() bool { return false }

// key reports every key unhandled: the shell's row bindings and built-ins own
// them all.
func (t *issuesTab) key(*shell, tea.KeyMsg) (bool, tea.Cmd) { return false, nil }

func (t *issuesTab) body(sh *shell, width, height int) ([]string, []hitTarget) {
	return sh.renderRows(sh.filterRows(t.rows(sh)), t.cursor, width, height)
}

func (t *issuesTab) footer(int) (string, bool) { return "", false }

// count is the tab-bar badge: every open issue.
func (t *issuesTab) count() int { return len(t.sync.issues()) }

func (t *issuesTab) loaded() bool { return t.sync.loaded() }

// freshness is the age of the issues section the last sync stamped.
func (t *issuesTab) freshness() seg { return cachedAgeSeg(t.sync.issuesFetchedAt()) }

func (t *issuesTab) update(*shell, tea.Msg) tea.Cmd { return nil }
