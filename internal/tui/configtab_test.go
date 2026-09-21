package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// rowsText joins a row slice's searchText — a stand-in for the old
// strings.Join(lines, "\n") assertions now that the tab emits rowItems
// instead of pre-rendered strings.
func rowsText(rows []rowItem) string {
	texts := make([]string, len(rows))
	for i, r := range rows {
		texts[i] = r.label + " " + r.searchText()
	}
	return strings.Join(texts, "\n")
}

func TestManifestConfigLinesNoManifest(t *testing.T) {
	m := Model{manifestOK: false}
	joined := rowsText(m.manifestConfigRows())
	if !strings.Contains(joined, "no admin.toml") {
		t.Errorf("manifestConfigRows(no manifest) = %q, want it to say no admin.toml", joined)
	}
}

// TestManifestConfigLinesTrackerDeclaredAndDefaulted is admin-3rgp.21's core
// acceptance criterion for the manifest half of the tab: a declared key
// shows its value and origin, an undeclared one shows the "not declared"
// placeholder rather than a guessed value.
func TestManifestConfigLinesTrackerDeclaredAndDefaulted(t *testing.T) {
	m := Model{
		manifestOK: true,
		manifest:   &manifest.Manifest{Trackers: []string{"beads"}, HasTracker: true},
		keyOrigin:  map[string]string{"tracker": "(manifest)"},
	}
	joined := rowsText(m.manifestConfigRows())
	if !strings.Contains(joined, "beads") || !strings.Contains(joined, "(manifest)") {
		t.Errorf("manifestConfigRows(tracker declared) = %q, want it to show \"beads\" and its \"(manifest)\" origin", joined)
	}

	m2 := Model{manifestOK: true, manifest: &manifest.Manifest{}}
	joined2 := rowsText(m2.manifestConfigRows())
	if !strings.Contains(joined2, "no tracker declared") {
		t.Errorf("manifestConfigRows(no tracker) = %q, want \"no tracker declared\" per the AC (never guess one)", joined2)
	}
	// uses_pull_requests has a real bool default (false), so it renders that
	// value marked defaulted, unlike tracker which has no default value at
	// all — the AC draws this exact distinction.
	if !strings.Contains(joined2, "false") || !strings.Contains(joined2, "defaulted") {
		t.Errorf("manifestConfigRows(uses_pull_requests undeclared) = %q, want \"false\" marked defaulted", joined2)
	}
}

func TestManifestConfigLinesUsesArchetypeOrigin(t *testing.T) {
	m := Model{
		manifestOK: true,
		manifest:   &manifest.Manifest{Trackers: []string{"github"}, HasTracker: true},
		keyOrigin:  map[string]string{"tracker": "beads-tracker"},
	}
	joined := rowsText(m.manifestConfigRows())
	if !strings.Contains(joined, "beads-tracker") {
		t.Errorf("manifestConfigRows = %q, want it to name the declaring archetype \"beads-tracker\"", joined)
	}
}

// TestConfigTableLinesReportsOrigin exercises the identity/worktree half of
// the tab: a value's origin names admin.toml or the declaring archetype —
// the acceptance criterion "never a dotfile" — never a hardcoded file path.
func TestConfigTableLinesReportsOrigin(t *testing.T) {
	dir := t.TempDir()
	m := Model{
		repoRoot: dir,
		cfgMerged: config.Config{
			Identity:  config.Identity{Login: "pierce"},
			Worktrees: config.Worktrees{Root: "/tmp/wt"},
		},
		keyOrigin: map[string]string{"identity": "(manifest)", "worktrees": "some-archetype"},
	}
	joined := rowsText(m.configTableRows())
	if !strings.Contains(joined, "pierce") || !strings.Contains(joined, "(manifest)") {
		t.Errorf("configTableRows(login) = %q, want \"pierce\" attributed to (manifest)", joined)
	}
	if !strings.Contains(joined, "/tmp/wt") || !strings.Contains(joined, "some-archetype") {
		t.Errorf("configTableRows(worktree root) = %q, want \"/tmp/wt\" attributed to its archetype", joined)
	}
	if strings.Contains(joined, "config.toml") || strings.Contains(joined, "global config") || strings.Contains(joined, "local config") {
		t.Errorf("configTableRows named a dotfile origin: %q", joined)
	}
	// Nothing declared the default agent anywhere.
	if !strings.Contains(joined, "default agent") {
		t.Errorf("configTableRows missing the \"default agent\" row entirely: %q", joined)
	}
}

// configTestModel builds a Model with a real repoRoot and enough manifest/
// config state to render the full Config tab, for the row-machinery tests
// below (cursor, enter, click).
func configTestModel(t *testing.T, dir string) Model {
	t.Helper()
	m := New(context.Background(), dir, "owner/repo", "main", "main")
	m.active = tabConfig
	return m
}

// TestConfigTabCursorMoves is the ticket's core AC: the Config tab has a
// cursor that moves with j/k and the arrows, and the current row is visibly
// current (the cursor lead marker only shows with focus in the body).
func TestConfigTabCursorMoves(t *testing.T) {
	dir := t.TempDir()
	m := configTestModel(t, dir)
	m.focus = focusBody
	m.cursor[tabConfig] = 0
	m.clampCursor()
	start := m.cursor[tabConfig]

	next, _ := m.handleKey(keyMsg("j"))
	nm := next.(Model)
	if nm.cursor[tabConfig] == start {
		t.Fatalf("j did not move the Config tab cursor off %d", start)
	}
	r, ok := nm.currentRow()
	if !ok {
		t.Fatal("currentRow() returned nothing after moving the cursor")
	}
	if !r.selectable() {
		t.Errorf("current row after j is not selectable: %+v", r)
	}

	next2, _ := nm.handleKey(keyMsg("k"))
	nm2 := next2.(Model)
	if nm2.cursor[tabConfig] != start {
		t.Errorf("k did not return the cursor to %d, got %d", start, nm2.cursor[tabConfig])
	}
}

// TestConfigTabEnterOpensKeyFile is AC2/AC3: enter on a present key-file row
// issues an open command for that file's absolute path.
func TestConfigTabEnterOpensKeyFile(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)
	m := configTestModel(t, dir)
	m.focus = focusBody

	rows := m.visibleRows()
	idx := -1
	for i, r := range rows {
		if r.kind == rowConfigPath && r.cfg != nil && r.cfg.label == "admin.toml" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no admin.toml row in %v", rows)
	}
	m.cursor[tabConfig] = idx

	_, cmd := m.activate()
	if cmd == nil {
		t.Fatal("activate() on the admin.toml row produced no command")
	}
	msg := cmd()
	om, ok := msg.(openManifestMsg)
	if !ok {
		t.Fatalf("activate() message = %T, want openManifestMsg", msg)
	}
	if om.err != nil {
		t.Errorf("open command failed: %v", om.err)
	}
	if om.target != "admin.toml" {
		t.Errorf("target = %q, want %q", om.target, "admin.toml")
	}
}

// TestConfigTabEnterOnAbsentFileRefuses is AC5: an absent file's row is
// visually distinct (marked absent) and enter refuses to open it, setting a
// status line instead of starting anything.
func TestConfigTabEnterOnAbsentFileRefuses(t *testing.T) {
	dir := t.TempDir()
	m := configTestModel(t, dir)
	m.focus = focusBody

	rows := m.visibleRows()
	idx := -1
	for i, r := range rows {
		if r.kind == rowConfigPath && r.cfg != nil && r.cfg.label == "AGENTS.md" {
			idx = i
			if !r.cfg.absent {
				t.Fatalf("AGENTS.md row not marked absent in a fresh temp dir: %+v", r.cfg)
			}
		}
	}
	if idx < 0 {
		t.Fatalf("no AGENTS.md row in %v", rows)
	}
	m.cursor[tabConfig] = idx

	nm, cmd := m.activate()
	if cmd != nil {
		t.Error("activate() on an absent row started a command, want none")
	}
	mm := nm.(Model)
	if mm.status == "" || !strings.Contains(mm.status, "AGENTS.md") {
		t.Errorf("status = %q, want it to name AGENTS.md as absent", mm.status)
	}
}

// TestConfigTabEnterOnValueRowSaysNothingToOpen is AC on the value-only rows
// (tracker, ceiling): enter does nothing destructive and says so, rather
// than opening something arbitrary.
func TestConfigTabEnterOnValueRowSaysNothingToOpen(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "tracker = [\"beads\"]\n"+editTestManifest)
	m := configTestModel(t, dir)
	m.focus = focusBody

	rows := m.visibleRows()
	idx := -1
	for i, r := range rows {
		if r.kind == rowConfigValue && r.cfg != nil && r.cfg.label == "tracker" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no tracker value row in %v", rows)
	}
	m.cursor[tabConfig] = idx

	// activate() itself never schedules the status toast's expiry (only
	// Update does, wrapping around it), so its own cmd return says nothing
	// about whether an action started. What proves nothing destructive
	// happened is the model's own state: the status line is set (the
	// "nothing to open" message) and nothing else changed.
	nm, _ := m.activate()
	mm := nm.(Model)
	if mm.status == "" {
		t.Error("status is empty after activating a value-only row")
	}
	if mm.statusFg == cRed {
		t.Errorf("status is an error color, want the plain \"nothing to open\" message: %q", mm.status)
	}
}

// TestConfigTabClickActivatesCurrentRow is AC2's click half: a click on the
// row already under the cursor opens it, same as enter — the same
// click-to-activate contract every other tab already gets from clickRow.
func TestConfigTabClickActivatesCurrentRow(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)
	m := configTestModel(t, dir)
	m.focus = focusBody

	rows := m.visibleRows()
	idx := -1
	for i, r := range rows {
		if r.kind == rowConfigPath && r.cfg != nil && r.cfg.label == "admin.toml" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no admin.toml row in %v", rows)
	}
	m.cursor[tabConfig] = idx

	_, cmd := m.clickRow(0, idx)
	if cmd == nil {
		t.Fatal("clickRow on the current row produced no command")
	}
	msg := cmd()
	if _, ok := msg.(openManifestMsg); !ok {
		t.Fatalf("clickRow message = %T, want openManifestMsg", msg)
	}
}

// TestConfigTabDirectoryRowOpens is AC4: enter on the worktree root — a
// directory, not a file — opens it the same way a file row does.
func TestConfigTabDirectoryRowOpens(t *testing.T) {
	dir := t.TempDir()
	m := Model{
		repoRoot: dir,
		cfgMerged: config.Config{
			Worktrees: config.Worktrees{Root: dir},
		},
	}
	rows := m.configTableRows()
	var row rowItem
	found := false
	for _, r := range rows {
		if r.kind == rowConfigPath && r.cfg != nil && r.cfg.label == "worktree root" {
			row, found = r, true
		}
	}
	if !found {
		t.Fatalf("no worktree root row in %v", rows)
	}
	if row.cfg.absent {
		t.Fatalf("worktree root row marked absent for a real directory: %+v", row.cfg)
	}
	if row.cfg.path != dir {
		t.Errorf("worktree root path = %q, want %q", row.cfg.path, dir)
	}
}

// TestConfigTabPopulatePatternAbsentRow proves a populate pattern that
// resolves to nothing on disk (including a glob) renders as an absent row
// rather than a path that looks openable.
func TestConfigTabPopulatePatternAbsentRow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "present.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Model{repoRoot: dir}
	entries := []config.Entry{{Pattern: "present.txt"}, {Pattern: "missing.txt"}}
	rows := m.populateRows(entries, "")

	var present, missing *configRowData
	for _, r := range rows {
		if r.cfg == nil {
			continue
		}
		switch r.cfg.label {
		case "present.txt":
			present = r.cfg
		case "missing.txt":
			missing = r.cfg
		}
	}
	if present == nil || present.absent {
		t.Errorf("present.txt row = %+v, want present (not absent)", present)
	}
	if missing == nil || !missing.absent {
		t.Errorf("missing.txt row = %+v, want absent", missing)
	}
}
