// keybar_binding_test.go pins admin-l6kb.4's acceptance criteria: the
// footer keybar merges [keys.<row-kind>] bindings for the row kind under
// the cursor, so it advertises exactly what rowKeyDispatch would do — a
// bound key relabels its built-in pair in place, an unbound-for-this-row
// key gets appended, a command with no description falls back to its own
// name, and the cursor leaving a bound row restores the built-in labels.
package tui

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestKeybarShowsEffectiveBinding proves a rebound `enter` on a bead row
// relabels the built-in "start work" pair in place — same index, same
// Display glyph, only the Label changes.
func TestKeybarShowsEffectiveBinding(t *testing.T) {
	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest: &manifest.Manifest{
			Custom: map[string]manifest.Command{
				"start-triage": {Desc: "triage this bead"},
			},
		},
		beads: []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {"enter": "start-triage"},
		},
	}
	trackerCursorTo(t, &m, "proj-1")

	builtin := builtinPairs(keymap.Bead)
	got := m.rowPairs(keymap.Bead)
	if len(got) != len(builtin) {
		t.Fatalf("rowPairs len = %d, want %d (builtin count unchanged)", len(got), len(builtin))
	}
	// enter is index 1 in keymap.Builtins(Bead).
	if got[1][0] != "↵" {
		t.Errorf("rebound enter Display = %q, want \"↵\" (position preserved)", got[1][0])
	}
	if got[1][1] != "triage this bead" {
		t.Errorf("rebound enter Label = %q, want \"triage this bead\"", got[1][1])
	}
	for i, p := range got {
		if i == 1 {
			continue
		}
		if p != builtin[i] {
			t.Errorf("pair %d = %v, want unchanged builtin %v", i, p, builtin[i])
		}
	}
}

// TestKeybarAppendsUnboundKey proves `o` bound on a bead row — which has no
// built-in `o` (a bead has no page to open) — is appended after the
// built-ins, landing before the trailing `←→ tab` keymap() appends.
func TestKeybarAppendsUnboundKey(t *testing.T) {
	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest: &manifest.Manifest{
			Custom: map[string]manifest.Command{
				"open-notes": {Desc: "open notes"},
			},
		},
		beads: []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {"o": "open-notes"},
		},
	}
	trackerCursorTo(t, &m, "proj-1")

	builtin := builtinPairs(keymap.Bead)
	got := m.rowPairs(keymap.Bead)
	if len(got) != len(builtin)+1 {
		t.Fatalf("rowPairs len = %d, want %d (builtin + 1 appended)", len(got), len(builtin)+1)
	}
	last := got[len(got)-1]
	if last != ([2]string{"o", "open notes"}) {
		t.Errorf("appended pair = %v, want {\"o\", \"open notes\"}", last)
	}

	full := m.keymap()
	tabPair := full[len(full)-1]
	if tabPair != ([2]string{"←→", "tab"}) {
		t.Fatalf("last pair of full keymap = %v, want {\"←→\", \"tab\"}", tabPair)
	}
	if full[len(full)-2] != last {
		t.Errorf("appended binding at %v, want it directly before ←→ tab", full[len(full)-2])
	}
}

// TestKeybarFallsBackToCommandName proves a bound command with an empty
// Desc labels the key with the command's own name.
func TestKeybarFallsBackToCommandName(t *testing.T) {
	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest: &manifest.Manifest{
			Custom: map[string]manifest.Command{
				"open-notes": {},
			},
		},
		beads: []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {"o": "open-notes"},
		},
	}
	trackerCursorTo(t, &m, "proj-1")

	got := m.rowPairs(keymap.Bead)
	last := got[len(got)-1]
	if last != ([2]string{"o", "open-notes"}) {
		t.Errorf("appended pair = %v, want {\"o\", \"open-notes\"} (name fallback)", last)
	}
}

// TestKeybarRestoresBuiltinsOnUnboundKind proves moving the cursor off a
// bound bead row onto a commit row (no [keys.commit] declared) restores
// the built-in labels for the commit pane.
func TestKeybarRestoresBuiltinsOnUnboundKind(t *testing.T) {
	m := Model{
		active:     tabLocal,
		pane:       paneCommits,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest:   &manifest.Manifest{},
		local:      localData{commits: []git.Commit{{SHA: "abc123", Subject: "msg"}}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {"enter": "start-triage"},
		},
	}

	got := m.rowPairs(keymap.Commit)
	want := builtinPairs(keymap.Commit)
	if len(got) != len(want) {
		t.Fatalf("rowPairs len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pair %d = %v, want builtin %v", i, got[i], want[i])
		}
	}
}

// TestKeybarIdenticalWithoutKeysTable proves the no-[keys]-table fast exit:
// with rowKeys nil (what buildRowKeys returns for a manifest with no
// [keys] table), rowPairs returns exactly builtinPairs — no currentRow()
// call, no allocation difference an observer could see.
func TestKeybarIdenticalWithoutKeysTable(t *testing.T) {
	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest:   &manifest.Manifest{},
		beads:      []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		// rowKeys intentionally left nil.
	}

	got := m.rowPairs(keymap.Bead)
	want := builtinPairs(keymap.Bead)
	if len(got) != len(want) {
		t.Fatalf("rowPairs len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pair %d = %v, want builtin %v", i, got[i], want[i])
		}
	}

	fullGot := m.keymap()
	fullWant := append(builtinPairs(keymap.Bead), [2]string{"o", "config"}, [2]string{"←→", "tab"})
	if len(fullGot) != len(fullWant) {
		t.Fatalf("keymap() len = %d, want %d", len(fullGot), len(fullWant))
	}
	for i := range fullWant {
		if fullGot[i] != fullWant[i] {
			t.Errorf("keymap()[%d] = %v, want %v", i, fullGot[i], fullWant[i])
		}
	}
}

// TestKeybarKeepsQuitAndTabWithBindings proves the drop-from-the-middle
// invariant survives a keymap lengthened by several row bindings: q quit
// (the last builtin pair on the Beads tab) and ←→ tab (always last) both
// stay visible across a spread of widths.
func TestKeybarKeepsQuitAndTabWithBindings(t *testing.T) {
	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest: &manifest.Manifest{
			Custom: map[string]manifest.Command{
				"cmd-a": {Desc: "alpha action"},
				"cmd-b": {Desc: "bravo action"},
				"cmd-c": {Desc: "charlie action"},
				"cmd-d": {Desc: "delta action"},
			},
		},
		beads: []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {
				"1": "cmd-a",
				"2": "cmd-b",
				"3": "cmd-c",
				"4": "cmd-d",
			},
		},
	}
	trackerCursorTo(t, &m, "proj-1")

	for width := 40; width <= 200; width += 10 {
		bar := m.keybar(width)
		if !strings.Contains(bar, "q") || !strings.Contains(bar, "quit") {
			t.Errorf("width %d: keybar missing q/quit: %q", width, bar)
		}
		if !strings.Contains(bar, "←→") || !strings.Contains(bar, "tab") {
			t.Errorf("width %d: keybar missing ←→/tab: %q", width, bar)
		}
	}
}

// TestKeybarKeepsLastTwoOnPanesWithoutQuit is the half of the drop rule that
// naming `q quit` cannot cover. Branch, Commit and Stash carry no quit pair at
// all (internal/keymap/keymap.go), so a protected set built only from index 0,
// the last index and a literal {"q","quit"} match leaves their second-to-last
// pair — `r refresh`, directly ahead of `←→ tab` — free to be dropped, which
// the old drop-`pairs[len-3]` loop had always kept. Every bindable row kind has
// to keep its last two pairs at every width, quit pair or not.
func TestKeybarKeepsLastTwoOnPanesWithoutQuit(t *testing.T) {
	for _, tc := range []struct {
		name string
		pane localPane
	}{
		{"branches", paneBranches},
		{"commits", paneCommits},
		{"stashes", paneStashes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := seededModel()
			m.active, m.pane = tabLocal, tc.pane
			full := m.keymap()
			if len(full) < 2 {
				t.Fatalf("%s: keymap has %d pairs, want at least 2", tc.name, len(full))
			}
			wantLabel := full[len(full)-2][1]
			for _, width := range []int{20, 30, 40, 60, 80, 100, 200} {
				bar := stripANSI(m.keybar(width))
				if !strings.Contains(bar, wantLabel) {
					t.Errorf("width %d: keybar dropped the second-to-last pair %q: %q", width, wantLabel, bar)
				}
				if !strings.Contains(bar, "tab") {
					t.Errorf("width %d: keybar dropped the tab hint: %q", width, bar)
				}
			}
		})
	}
}
