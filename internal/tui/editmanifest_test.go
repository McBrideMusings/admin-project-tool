package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestOpenCommandUsesPlatformHandlerAndManifestPath pins the argv `o` starts:
// the platform's default-handler binary followed by the repo's admin.toml —
// `open` on darwin, `xdg-open` everywhere else.
func TestOpenCommandUsesPlatformHandlerAndManifestPath(t *testing.T) {
	dir := t.TempDir()

	c := openCommand(dir)
	if c == nil {
		t.Fatal("openCommand returned nil for a real repo root")
	}
	wantName := "xdg-open"
	if runtime.GOOS == "darwin" {
		wantName = "open"
	}
	if c.Args[0] != wantName {
		t.Errorf("argv[0] = %q, want %q on %s", c.Args[0], wantName, runtime.GOOS)
	}
	wantPath := filepath.Join(dir, manifest.Filename)
	if len(c.Args) < 2 || c.Args[1] != wantPath {
		t.Errorf("argv = %v, want handler followed by %q", c.Args, wantPath)
	}
}

// TestOpenCommandNilOnEmptyRoot proves the key does nothing destructive when
// there is no repo root to resolve the manifest against.
func TestOpenCommandNilOnEmptyRoot(t *testing.T) {
	if c := openCommand(""); c != nil {
		t.Errorf("openCommand(\"\") = %v, want nil", c)
	}
}

// TestOKeyOpensTheManifestFromEveryTab proves the key is not scoped to one
// tab: every tab the dashboard can draw returns an open command for `o`.
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

// TestOpenManifestFailureSetsStatus proves a launch failure surfaces on the
// status line naming the command that could not run, rather than a silent
// no-op.
func TestOpenManifestFailureSetsStatus(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)
	m := New(context.Background(), dir, "owner/repo", "main", "main")

	updated, _ := m.Update(openManifestMsg{name: "xdg-open", err: exec.ErrNotFound})
	mm := updated.(Model)
	if mm.status == "" {
		t.Fatal("status is empty after a failed launch")
	}
	if !strings.Contains(mm.status, "xdg-open") {
		t.Errorf("status = %q, want it to name the missing command", mm.status)
	}
}

// TestFSChangeRereadsTheManifest proves the fsnotify watcher's reload is a
// real re-read of the file, not a refresh of the copy loaded at launch: the
// manifest is rewritten behind the model's back and the new value shows up
// once the debounced fsChangedMsg arrives. Nothing suspends the TUI to edit
// admin.toml any more, so this watcher event is the only reload signal left.
func TestFSChangeRereadsTheManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)

	m := New(context.Background(), dir, "owner/repo", "main", "main")
	if m.trackerKind() != "" {
		t.Fatalf("tracker = %q before the edit, want undeclared", m.trackerKind())
	}

	writeTestManifest(t, dir, "tracker = \"beads\"\n"+editTestManifest)
	updated, _ := m.Update(fsChangedMsg{})
	mm := updated.(Model)
	if mm.trackerKind() != "beads" {
		t.Errorf("tracker = %q after the fs change, want %q", mm.trackerKind(), "beads")
	}
	if !mm.manifestOK || mm.manifestErr != "" {
		t.Errorf("manifestOK = %v, err = %q after a valid external edit", mm.manifestOK, mm.manifestErr)
	}
}

// TestFSChangeSurfacesABrokenManifest proves an external edit that breaks the
// file clears the values it used to declare and leaves the cursor on a tab
// that still exists — the Work tab goes away with the manifest.
func TestFSChangeSurfacesABrokenManifest(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)

	m := New(context.Background(), dir, "owner/repo", "main", "main")
	if m.active != tabWork {
		t.Fatalf("active = %v before the edit, want tabWork", m.active)
	}

	writeTestManifest(t, dir, "project_name = \"demo\"\n[commands.build]\nkind = \"shell\"\n")
	updated, _ := m.Update(fsChangedMsg{})
	mm := updated.(Model)
	if mm.manifestOK {
		t.Errorf("manifestOK = true after the external edit broke the manifest")
	}
	if mm.manifestErr == "" {
		t.Errorf("manifestErr is empty after the external edit broke the manifest")
	}
	if mm.active == tabWork {
		t.Errorf("active tab is still Work after the Work tab stopped being drawn")
	}
}
