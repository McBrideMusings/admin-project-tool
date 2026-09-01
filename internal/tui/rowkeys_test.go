// rowkeys_test.go pins admin-l6kb.2's own acceptance criteria: a HIT
// dispatches the named command through the same command-activation path a
// Commands-tab row uses, with ADMIN_ROW_KIND/ADMIN_ROW_ID/ADMIN_ROW_URL in
// the child's env; a MISS (wrong row kind, no binding, no [keys] table at
// all) leaves the built-in behavior completely untouched.
package tui

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// stubStartHostedRun replaces the package-level startHostedRun seam for the
// duration of one test and hands back a func that returns the env slice the
// most recent call captured — mirrors work_enter_test.go's own stubbing
// pattern so a stubbed dispatch never spawns a real pty.
func stubStartHostedRun(t *testing.T) *[]string {
	t.Helper()
	old := startHostedRun
	t.Cleanup(func() { startHostedRun = old })
	var gotEnv []string
	startHostedRun = func(m *manifest.Manifest, name string, args []string, rows, cols int, env []string) error {
		gotEnv = env
		return nil
	}
	return &gotEnv
}

// TestRowKeyDispatchHitOnBeadRow proves a HIT — [keys.bead] bound for the
// key under test — runs the named command through dispatchCommandCmd (the
// same path activateCommand uses) with ADMIN_ROW_KIND=bead and
// ADMIN_ROW_ID set to the bead's own id, and that a bead row (which never
// carries a url) sets no ADMIN_ROW_URL at all.
func TestRowKeyDispatchHitOnBeadRow(t *testing.T) {
	gotEnv := stubStartHostedRun(t)

	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest:   &manifest.Manifest{},
		beads:      []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {"o": "mycmd"},
		},
	}
	trackerCursorTo(t, &m, "proj-1")

	_, cmd, ok := m.rowKeyDispatch("o")
	if !ok {
		t.Fatal("rowKeyDispatch(\"o\") on a bound bead row = miss, want hit")
	}
	if cmd == nil {
		t.Fatal("rowKeyDispatch returned ok=true with a nil Cmd")
	}
	msg := cmd()
	started, ok := msg.(workDispatchStartedMsg)
	if !ok {
		t.Fatalf("dispatch Cmd produced a %T, want workDispatchStartedMsg", msg)
	}
	if started.name != "mycmd" {
		t.Errorf("dispatched command = %q, want \"mycmd\"", started.name)
	}

	env := *gotEnv
	if !containsEnv(env, "ADMIN_ROW_KIND=bead") {
		t.Errorf("env %v missing ADMIN_ROW_KIND=bead", env)
	}
	if !containsEnv(env, "ADMIN_ROW_ID=proj-1") {
		t.Errorf("env %v missing ADMIN_ROW_ID=proj-1", env)
	}
	if hasEnvKey(env, "ADMIN_ROW_URL") {
		t.Errorf("env %v carries ADMIN_ROW_URL for a bead row, which never has one", env)
	}
}

// TestRowKeyDispatchMissOnOtherRowKind proves a binding scoped to Bead does
// nothing on a row of a different kind — the AC's "the same key pressed on
// a row of any other kind still does exactly what it did before." A
// commit row under the cursor, with only [keys.bead] declared, must miss.
func TestRowKeyDispatchMissOnOtherRowKind(t *testing.T) {
	m := Model{
		active: tabLocal,
		pane:   paneCommits,
		width:  120,
		height: 40,
		local:  localData{commits: []git.Commit{{SHA: "abc123", Subject: "msg"}}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {"o": "mycmd"},
		},
	}

	if _, _, ok := m.rowKeyDispatch("o"); ok {
		t.Error("rowKeyDispatch(\"o\") hit on a commit row with only [keys.bead].o bound, want miss")
	}
}

// TestRowKeyDispatchNoKeysTableIsAlwaysAMiss proves the AC's "with no
// [keys] table in the manifest, every key in the dashboard behaves as it
// does today — including enter on a bead": a nil rowKeys lookup (what
// buildRowKeys returns for a manifest with no [keys] table) is a miss on
// every key, on every bindable row kind, with no further lookup needed to
// reach the built-in switch in handleKey.
func TestRowKeyDispatchNoKeysTableIsAlwaysAMiss(t *testing.T) {
	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest:   &manifest.Manifest{},
		beads:      []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		// rowKeys intentionally left nil: no [keys] table.
	}
	trackerCursorTo(t, &m, "proj-1")

	if _, _, ok := m.rowKeyDispatch("enter"); ok {
		t.Error("rowKeyDispatch(\"enter\") hit with no [keys] table declared, want miss")
	}
}

// TestRowKindOfWorkTabRowsAreUnbindable proves the AC's "a key bound for a
// row kind does nothing on the Work tab's own rows (command,
// commandTarget, worker)": none of the three map to a keymap.RowKind, so
// rowKeyDispatch can never look one up in the first place, regardless of
// what a [keys] table declares.
func TestRowKindOfWorkTabRowsAreUnbindable(t *testing.T) {
	for _, kind := range []rowKind{rowCommand, rowCommandTarget, rowWorker} {
		if _, ok := rowKindOf(rowItem{kind: kind}); ok {
			t.Errorf("rowKindOf(%v) = ok, want unbindable (Work tab's own rows)", kind)
		}
	}
}

// TestRowEnvURLPresentOnlyWhenTheRowCarriesOne proves ADMIN_ROW_URL is
// appended when the row has one (an issue row) and genuinely absent — not
// merely empty — when it doesn't (a bead row), matching the acceptance
// criteria's exact wording.
func TestRowEnvURLPresentOnlyWhenTheRowCarriesOne(t *testing.T) {
	withURL := rowEnv(rowItem{kind: rowIssue, issueNum: 5, url: "https://example.com/issues/5"}, keymap.Issue)
	if !containsEnv(withURL, "ADMIN_ROW_URL=https://example.com/issues/5") {
		t.Errorf("issue row env %v missing ADMIN_ROW_URL", withURL)
	}

	withoutURL := rowEnv(rowItem{kind: rowBead, beadID: "proj-1"}, keymap.Bead)
	if hasEnvKey(withoutURL, "ADMIN_ROW_URL") {
		t.Errorf("bead row env %v carries ADMIN_ROW_URL, want none", withoutURL)
	}
}

// TestRowIDStashCutsRefFromTheCombinedLabel proves the stash caveat: a
// stash rowItem's label is "<ref>  <subject>" (model.go's localCurrent,
// two-space separator, no dedicated ref field), and ADMIN_ROW_ID must be
// just the ref.
func TestRowIDStashCutsRefFromTheCombinedLabel(t *testing.T) {
	got := rowID(rowItem{kind: rowStash, label: "stash@{0}  work in progress"}, keymap.Stash)
	if got != "stash@{0}" {
		t.Errorf("rowID(stash) = %q, want \"stash@{0}\"", got)
	}
}

func containsEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func hasEnvKey(env []string, key string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return true
		}
	}
	return false
}
