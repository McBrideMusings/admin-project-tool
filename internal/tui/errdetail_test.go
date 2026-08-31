package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// checkoutRefusal is the shape that motivated the overlay: git names the refusal on
// line one and the files it is actually about on the lines after, which is the half
// a one-line status bar can never show.
func checkoutRefusal() error {
	return newTestCmdError("error: Your local changes to the following files would be overwritten by checkout:\n" +
		"\tinternal/tui/view.go\n" +
		"Please commit your changes or stash them before you switch branches.\n" +
		"Aborting")
}

func newTestCmdError(out string) error {
	return &git.CmdError{Err: errors.New("exit status 1"), Headline: strings.SplitN(out, "\n", 2)[0], Output: out}
}

func TestFailedActionKeepsDetailReachable(t *testing.T) {
	m := seededModel()

	updated, _ := m.Update(actionMsg{text: "checkout failed", err: checkoutRefusal()})
	m = updated.(Model)

	if strings.Count(m.status, "\n") != 0 {
		t.Errorf("status line spans multiple lines, the flood this fixes:\n%s", m.status)
	}
	if m.statusHint != "e for detail" {
		t.Errorf("status line does not offer the detail view: hint=%q status=%q", m.statusHint, m.status)
	}
	// The affordance has to survive a message far longer than the terminal is wide.
	m.width = 60
	if !strings.Contains(m.View(), "e for detail") {
		t.Errorf("a long refusal truncated the detail affordance off the status line\n%s", m.View())
	}
	m.width = 120
	if strings.Contains(m.status, "view.go") {
		t.Errorf("status line leaked the detail lines it is meant to park: %q", m.status)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(Model)
	if !m.errOpen {
		t.Fatal("e did not open the error-detail overlay")
	}

	out := m.View()
	for _, want := range []string{"checkout failed", "internal/tui/view.go", "Aborting", "any key"} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q\n%s", want, out)
		}
	}
	if got := strings.Count(out, "\n") + 1; got != m.height {
		t.Errorf("overlay view is %d lines, want exactly %d — the frame must not scroll", got, m.height)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(Model)
	if m.errOpen {
		t.Error("a key press did not dismiss the overlay")
	}
}

// A plain error has nothing behind its message, so `e` must not open an overlay
// that only repeats the status bar — and a later plain failure must clear the
// detail an earlier one left behind.
func TestPlainErrorOffersNoDetail(t *testing.T) {
	m := seededModel()

	updated, _ := m.Update(actionMsg{text: "clipboard unavailable", err: errors.New("no pbcopy")})
	m = updated.(Model)
	if strings.Contains(m.status, "e for detail") {
		t.Errorf("status offers a detail view for an error with no detail: %q", m.status)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(Model)
	if m.errOpen {
		t.Error("e opened an empty overlay")
	}

	updated, _ = m.Update(actionMsg{text: "checkout failed", err: checkoutRefusal()})
	m = updated.(Model)
	updated, _ = m.Update(actionMsg{text: "pull failed", err: errors.New("exit status 1")})
	m = updated.(Model)
	if m.errDetail != "" {
		t.Errorf("a later failure left the previous failure's detail behind: %q", m.errDetail)
	}
}

func TestWrapCellsFoldsRatherThanTruncates(t *testing.T) {
	long := "fatal: could not create work tree dir '/Users/someone/very/long/path/to/a/worktree': File exists"
	got := wrapCells(long, 40)
	if len(got) < 2 {
		t.Fatalf("wrapCells did not fold a %d-cell line into a 40-cell box: %v", len(long), got)
	}
	if strings.Join(got, "") != long {
		t.Errorf("wrapCells lost or reordered text:\n%q\n%q", strings.Join(got, ""), long)
	}
	for _, l := range got {
		if vlen(l) > 40 {
			t.Errorf("wrapped line is %d cells, over the 40-cell box: %q", vlen(l), l)
		}
	}
	// A rune wider than the whole box must still terminate.
	if got := wrapCells("日本", 1); strings.Join(got, "") != "日本" {
		t.Errorf("wrapCells mangled a line narrower than one rune: %v", got)
	}
}
