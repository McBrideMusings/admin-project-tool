package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestNewSeedsCollapsedFromViewToml pins admin-y5lc.1's acceptance: a
// project whose level-1 .admin/view.toml sets work.collapsed = true starts
// the dashboard already collapsed, with workOpenCmd seeded from the cursor
// row exactly as toggleWorkCollapse seeds it from a keypress — there is no
// keypress at launch, so New() has to do the same job itself.
func TestNewSeedsCollapsedFromViewToml(t *testing.T) {
	dir := t.TempDir()
	body := "project_name = \"demo\"\narchetypes = []\n\n" +
		"[commands.build]\ndesc = \"build it\"\nsteps = [\"build\"]\n\n" +
		"[actions.build]\nkind = \"shell\"\nrun = \"echo hi\"\n\n" +
		"[commands.test]\ndesc = \"test it\"\nsteps = [\"test\"]\n\n" +
		"[actions.test]\nkind = \"shell\"\nrun = \"echo hi\"\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".admin"), 0o755); err != nil {
		t.Fatal(err)
	}
	viewBody := "[work]\ncollapsed = true\n"
	if err := os.WriteFile(filepath.Join(dir, ".admin", "view.toml"), []byte(viewBody), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(context.Background(), dir, "", "main", "main")

	if !m.manifestOK {
		t.Fatalf("manifestOK = false (err: %s)", m.manifestErr)
	}
	if !m.workCollapsed {
		t.Fatal("workCollapsed = false, want true from view.toml's work.collapsed")
	}
	rows := m.visibleRows()
	i := m.cursor[m.active]
	if i < 0 || i >= len(rows) {
		t.Fatalf("cursor %d out of range of %d visible rows", i, len(rows))
	}
	if rows[i].kind != rowCommand {
		t.Fatalf("cursor row kind = %v, want rowCommand (row 0's header should have been skipped)", rows[i].kind)
	}
	// "build" sorts before "test" (group/priority tie, alphabetical name) —
	// declaredCommands' own order, which is what the zero-value cursor lands
	// the dashboard on.
	if m.workOpenCmd != "build" {
		t.Errorf("workOpenCmd = %q, want %q, the command the cursor started on", m.workOpenCmd, "build")
	}
}

// TestToggleWorkCollapseSavesToProjectLevelFile pins the write half: `c`
// persists the flip to <project>/.admin/view.toml, not merely to the
// in-memory field.
func TestToggleWorkCollapseSavesToProjectLevelFile(t *testing.T) {
	dir := t.TempDir()
	body := "project_name = \"demo\"\narchetypes = []\n\n" +
		"[commands.build]\ndesc = \"build it\"\nsteps = [\"build\"]\n\n" +
		"[actions.build]\nkind = \"shell\"\nrun = \"echo hi\"\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(context.Background(), dir, "", "main", "main")
	if m.workCollapsed {
		t.Fatal("workCollapsed = true before any toggle, want false (no view.toml exists yet)")
	}
	m.focus = focusBody
	m.toggleWorkCollapse()
	if !m.workCollapsed {
		t.Fatal("workCollapsed = false after toggleWorkCollapse, want true")
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".admin", "view.toml"))
	if err != nil {
		t.Fatalf("reading the saved view.toml: %v", err)
	}
	m2 := New(context.Background(), dir, "", "main", "main")
	if !m2.workCollapsed {
		t.Errorf("a fresh New() over the same dir did not read back the saved collapse — file contents: %s", raw)
	}
}
