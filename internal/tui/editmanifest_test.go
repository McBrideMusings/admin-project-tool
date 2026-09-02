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
// the platform's default-handler binary followed by the target path —
// `open` on darwin, `xdg-open` everywhere else.
func TestOpenCommandUsesPlatformHandlerAndManifestPath(t *testing.T) {
	dir := t.TempDir()
	wantPath := filepath.Join(dir, manifest.Filename)

	c := openPathCommand(wantPath)
	if c == nil {
		t.Fatal("openPathCommand returned nil for a real path")
	}
	wantName := "xdg-open"
	if runtime.GOOS == "darwin" {
		wantName = "open"
	}
	if c.Args[0] != wantName {
		t.Errorf("argv[0] = %q, want %q on %s", c.Args[0], wantName, runtime.GOOS)
	}
	if len(c.Args) < 2 || c.Args[1] != wantPath {
		t.Errorf("argv = %v, want handler followed by %q", c.Args, wantPath)
	}
}

// TestOpenCommandNilOnEmptyRoot proves the key does nothing destructive when
// there is no path to open.
func TestOpenCommandNilOnEmptyRoot(t *testing.T) {
	if c := openPathCommand(""); c != nil {
		t.Errorf("openPathCommand(\"\") = %v, want nil", c)
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
	if len(m.trackers()) != 0 {
		t.Fatalf("trackers = %v before the edit, want undeclared", m.trackers())
	}

	writeTestManifest(t, dir, "tracker = [\"beads\"]\n"+editTestManifest)
	updated, _ := m.Update(fsChangedMsg{})
	mm := updated.(Model)
	if !mm.hasTracker("beads") {
		t.Errorf("trackers = %v after the fs change, want [\"beads\"]", mm.trackers())
	}
	if !mm.manifestOK || mm.manifestErr != "" {
		t.Errorf("manifestOK = %v, err = %q after a valid external edit", mm.manifestOK, mm.manifestErr)
	}
	if mm.status == "" {
		t.Error("status is empty after a real manifest change, want the reload announced")
	}
}

// TestFSChangeWithUnchangedManifestIsInert proves the fsnotify watcher's
// reload is gated on admin.toml's own stat: a fsChangedMsg that fires for
// some other reason (a source-file save, a build writing into the tree) must
// not re-parse the manifest or overwrite whatever the status line was
// already showing.
func TestFSChangeWithUnchangedManifestIsInert(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)

	m := New(context.Background(), dir, "owner/repo", "main", "main")
	m.setStatus("sentinel status", cDim)
	m.manifestErr = "sentinel err" // only a re-parse would clobber this

	updated, _ := m.Update(fsChangedMsg{})
	mm := updated.(Model)
	if mm.status != "sentinel status" {
		t.Errorf("status = %q after an unrelated fs change, want it untouched", mm.status)
	}
	if mm.manifestErr != "sentinel err" {
		t.Errorf("manifestErr = %q after an unrelated fs change, want it untouched (no re-parse)", mm.manifestErr)
	}
}

// TestOpenManifestFailureWithNoRepoRoot proves the no-repo-root failure path
// (openCommand returns nil before a handler name is ever chosen) renders a
// clean status line instead of the "open failed: : no repo root" artifact a
// naive `name + ": " + err` concatenation produces when name is empty.
func TestOpenManifestFailureWithNoRepoRoot(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, editTestManifest)
	m := New(context.Background(), dir, "owner/repo", "main", "main")

	cmd := openManifestCmd("")
	msg := cmd()
	updated, _ := m.Update(msg)
	mm := updated.(Model)

	if !strings.Contains(mm.status, "no repo root") {
		t.Errorf("status = %q, want it to mention the failure reason", mm.status)
	}
	if strings.Contains(mm.status, "open failed: :") || strings.Contains(mm.status, ": :") {
		t.Errorf("status = %q, contains an empty-name artifact", mm.status)
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
