package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func writeTestManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const editTestManifest = "project_name = \"demo\"\narchetypes = []\n\n" +
	"[commands.build]\ndesc = \"build it\"\nsteps = [\"build\"]\n\n" +
	"[actions.build]\nkind = \"shell\"\nrun = \"echo hi\"\n"

// TestEditorCommandUsesEditorAndManifestPath pins the argv `o` suspends into:
// $EDITOR's own words followed by the repo's admin.toml.
func TestEditorCommandUsesEditorAndManifestPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDITOR", "code --wait")

	c := editorCommand(dir)
	if c == nil {
		t.Fatal("editorCommand returned nil for a real repo root")
	}
	want := []string{"code", "--wait", filepath.Join(dir, manifest.Filename)}
	if strings.Join(c.Args, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", c.Args, want)
	}
}

// TestEditorCommandFallsBackToVi proves an unset $EDITOR still opens something
// rather than failing the key silently.
func TestEditorCommandFallsBackToVi(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDITOR", "")

	c := editorCommand(dir)
	if c == nil {
		t.Fatal("editorCommand returned nil for a real repo root")
	}
	if c.Args[0] != "vi" {
		t.Errorf("argv[0] = %q, want %q with $EDITOR unset", c.Args[0], "vi")
	}
}

// TestOKeyOpensTheManifestFromEveryTab proves the key is not scoped to one
// tab: every tab the dashboard can draw returns an editor command for `o`.
func TestOKeyOpensTheManifestFromEveryTab(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)

	for _, tb := range []tab{tabWork, tabLocal, tabPRs, tabIssues, tabBeads, tabConfig} {
		m := New(context.Background(), dir, "owner/repo", "main", "main")
		m.active = tb
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
		if cmd == nil {
			t.Errorf("tab %v: `o` produced no command", tb)
		}
	}
}

// TestEditorReturnRereadsTheManifest proves the reload on editor exit is a
// real re-read of the file, not a refresh of the copy loaded at launch: the
// manifest is rewritten behind the model's back and the new value shows up.
func TestEditorReturnRereadsTheManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)

	m := New(context.Background(), dir, "owner/repo", "main", "main")
	if m.trackerKind() != "" {
		t.Fatalf("tracker = %q before the edit, want undeclared", m.trackerKind())
	}

	writeTestManifest(t, dir, "tracker = \"beads\"\n"+editTestManifest)
	updated, _ := m.Update(editorDoneMsg{})
	mm := updated.(Model)
	if mm.trackerKind() != "beads" {
		t.Errorf("tracker = %q after the editor returned, want %q", mm.trackerKind(), "beads")
	}
	if !mm.manifestOK || mm.manifestErr != "" {
		t.Errorf("manifestOK = %v, err = %q after a valid edit", mm.manifestOK, mm.manifestErr)
	}
}

// TestEditorReturnSurfacesABrokenManifest proves an edit that breaks the file
// clears the values it used to declare and leaves the cursor on a tab that
// still exists — the Work tab goes away with the manifest.
func TestEditorReturnSurfacesABrokenManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)

	m := New(context.Background(), dir, "owner/repo", "main", "main")
	if m.active != tabWork {
		t.Fatalf("active = %v before the edit, want tabWork", m.active)
	}

	writeTestManifest(t, dir, "project_name = \"demo\"\n[commands.build]\nkind = \"shell\"\n")
	updated, _ := m.Update(editorDoneMsg{})
	mm := updated.(Model)
	if mm.manifestOK {
		t.Errorf("manifestOK = true after the edit broke the manifest")
	}
	if mm.manifestErr == "" {
		t.Errorf("manifestErr is empty after the edit broke the manifest")
	}
	if mm.active == tabWork {
		t.Errorf("active tab is still Work after the Work tab stopped being drawn")
	}
}
