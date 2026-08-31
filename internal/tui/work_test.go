package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// workTestModel builds a bare Model carrying a hand-built merged manifest —
// no filesystem, no archetype resolution — so declaredCommands/commandKind/
// workRows can be tested against exact, known input.
func workTestModel(m *manifest.Manifest) Model {
	return Model{
		manifest:   m,
		manifestOK: true,
		width:      120,
		height:     40,
	}
}

func TestDeclaredCommandsOrdersByGroupThenPriorityThenName(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"test":   {Desc: "run tests", Steps: []string{"test"}, HasGroup: true, Group: 2, HasPriority: true, Priority: 10},
			"deploy": {Desc: "deploy", Steps: []string{"deploy"}, HasGroup: true, Group: 1, HasPriority: true, Priority: 30},
			"build":  {Desc: "build", Steps: []string{"build"}, HasGroup: true, Group: 1, HasPriority: true, Priority: 10},
		},
		Actions: map[string]manifest.Action{
			"test": {"kind": "shell"}, "deploy": {"kind": "shell"}, "build": {"kind": "shell"},
		},
	}
	mm := workTestModel(m)
	got := mm.declaredCommands()
	var names []string
	for _, c := range got {
		names = append(names, c.name)
	}
	want := []string{"build", "deploy", "test"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v (group then priority then name)", names, want)
	}
}

func TestDeclaredCommandsHonorsDeclaredOrder(t *testing.T) {
	m := &manifest.Manifest{
		Order: []string{"zeta", manifest.Spacer, "alpha"},
		Custom: map[string]manifest.Command{
			"alpha": {Desc: "a", Steps: []string{"a"}},
			"zeta":  {Desc: "z", Steps: []string{"z"}},
			"beta":  {Desc: "b", Steps: []string{"b"}},
		},
		Actions: map[string]manifest.Action{
			"a": {"kind": "shell"}, "z": {"kind": "shell"}, "b": {"kind": "shell"},
		},
	}
	mm := workTestModel(m)
	got := mm.declaredCommands()
	var names []string
	for _, c := range got {
		names = append(names, c.name)
	}
	// Declared order first (spacer skipped), then whatever it left out,
	// alphabetically.
	want := []string{"zeta", "alpha", "beta"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", names, want)
	}
}

func TestDeclaredCommandsDropsHidden(t *testing.T) {
	m := &manifest.Manifest{
		HideCommands: []string{"internal"},
		Custom: map[string]manifest.Command{
			"internal": {Desc: "hidden", Steps: []string{"x"}},
			"build":    {Desc: "build", Steps: []string{"x"}},
		},
		Actions: map[string]manifest.Action{"x": {"kind": "shell"}},
	}
	mm := workTestModel(m)
	for _, c := range mm.declaredCommands() {
		if c.name == "internal" {
			t.Errorf("hide_commands entry %q was not dropped", c.name)
		}
	}
}

// TestDeclaredCommandsFiltersWorkflowBySource is the epic's own rule: "a
// query-sourced workflow appears in the list; a selection-sourced one does
// not" — kinds.WorkflowTarget.AppearsInCommandList is the single source of
// truth and this proves the Work tab actually consults it.
func TestDeclaredCommandsFiltersWorkflowBySource(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"retire-merged": {Desc: "retire", Steps: []string{"retire"}},
			"review-branch": {Desc: "review", Steps: []string{"review"}},
		},
		Actions: map[string]manifest.Action{
			"retire": {"kind": "workflow", "over": map[string]any{"query": "label:stale"}},
			"review": {"kind": "workflow", "over": "selection"},
		},
	}
	mm := workTestModel(m)
	got := mm.declaredCommands()
	names := map[string]bool{}
	for _, c := range got {
		names[c.name] = true
	}
	if !names["retire-merged"] {
		t.Errorf("query-sourced workflow command was dropped, want it kept")
	}
	if names["review-branch"] {
		t.Errorf("selection-sourced workflow command was kept, want it dropped")
	}
}

func TestCommandKind(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"build": {"kind": "go"},
			"a":     {}, // no kind declared -> defaults to shell
			"b":     {},
		},
	}
	mm := workTestModel(m)
	cases := []struct {
		name string
		cmd  manifest.Command
		want string
	}{
		{"single declared kind", manifest.Command{Steps: []string{"build"}}, "go"},
		{"single defaulted kind", manifest.Command{Steps: []string{"a"}}, "shell"},
		{"multi-step", manifest.Command{Steps: []string{"a", "b"}}, "sequence"},
		{"undeclared step", manifest.Command{Steps: []string{"missing"}}, "?"},
		{"no steps", manifest.Command{}, "?"},
	}
	for _, c := range cases {
		if got := mm.commandKind(c.cmd); got != c.want {
			t.Errorf("%s: commandKind = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestComputeOriginsPrefersManifestOverArchetype exercises the real
// archetype-loading half of computeOrigins against a temp directory, since
// that path does real file I/O that a hand-built manifest can't stand in for.
func TestComputeOriginsPrefersManifestOverArchetype(t *testing.T) {
	dir := t.TempDir()
	archDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[commands.build]\ndesc = \"from archetype\"\nsteps = [\"build\"]\n\n" +
		"[actions.build]\nkind = \"shell\"\nrun = \"echo hi\"\n"
	if err := os.WriteFile(filepath.Join(archDir, "archetype.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := archetype.SearchPath{{Label: "test", FS: os.DirFS(dir)}}

	project := &manifest.Manifest{
		Archetypes: []string{"demo"},
		Custom: map[string]manifest.Command{
			// The project overrides "build" itself — computeOrigins should
			// say "(manifest)", not "demo", per ADR-0009's "the manifest
			// references, the archetype implements" and the display rule
			// that follows from it.
			"build": {Desc: "from manifest", Steps: []string{"build"}},
		},
	}
	cmds, actions, _ := computeOrigins(project, sp)
	if cmds["build"] != "(manifest)" {
		t.Errorf(`cmds["build"] = %q, want "(manifest)"`, cmds["build"])
	}
	// The action itself is only declared by the archetype.
	if actions["build"] != "demo" {
		t.Errorf(`actions["build"] = %q, want "demo"`, actions["build"])
	}
}

// TestComputeOriginsTracksScalarKeyOrigin is admin-3rgp.21's Config-tab-facing
// half of computeOrigins: tracker/uses_pull_requests get the same
// archetype-vs-manifest origin tracking as commands and actions, and a key
// neither side declares is simply absent from the map (never a guessed
// origin for a value that was never set).
func TestComputeOriginsTracksScalarKeyOrigin(t *testing.T) {
	dir := t.TempDir()
	archDir := filepath.Join(dir, "beads-tracker")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "tracker = \"beads\"\nuses_pull_requests = false\n"
	if err := os.WriteFile(filepath.Join(archDir, "archetype.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := archetype.SearchPath{{Label: "test", FS: os.DirFS(dir)}}

	// The project overrides tracker itself, so that key's origin should read
	// "(manifest)" while uses_pull_requests still reads back to the
	// archetype that's the only one to declare it.
	project := &manifest.Manifest{
		Archetypes: []string{"beads-tracker"},
		Tracker:    "github", HasTracker: true,
	}
	_, _, keys := computeOrigins(project, sp)
	if keys["tracker"] != "(manifest)" {
		t.Errorf(`keys["tracker"] = %q, want "(manifest)"`, keys["tracker"])
	}
	if keys["uses_pull_requests"] != "beads-tracker" {
		t.Errorf(`keys["uses_pull_requests"] = %q, want "beads-tracker"`, keys["uses_pull_requests"])
	}
	if _, declared := keys["python_min"]; declared {
		t.Errorf(`keys["python_min"] present (%q); no such tracked key exists, want absent`, keys["python_min"])
	}
}

func TestLiveEntryForMatchesAnyStep(t *testing.T) {
	c := manifest.Command{Steps: []string{"lint", "build"}}
	e := &livework.Entry{ID: "build", Name: "build", State: livework.StateRunning}
	byName := map[string]*livework.Entry{"build": e}
	if got := liveEntryFor(c, byName); got != e {
		t.Errorf("liveEntryFor did not find the entry backing the second step")
	}
	if got := liveEntryFor(manifest.Command{Steps: []string{"other"}}, byName); got != nil {
		t.Errorf("liveEntryFor matched a step that isn't in byName: %v", got)
	}
}

// TestWorkRowsOneListOneCursor is the ticket's own central claim: commands
// and workers share one list under one cursor, in a stable order, and the
// cursor can move across the boundary between them.
func TestWorkRowsOneListOneCursor(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Desc: "build the binary", Steps: []string{"build"}},
		},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	}
	mm := workTestModel(m)
	mm.active = tabWork

	// A genuine agent worker has no command behind it and must still land in
	// the same list, after every command — this is the row a workflow target
	// produces (internal/livework's own doc comment), so use a unique,
	// test-scoped ID that no other test in this package can collide with.
	worker := &livework.Entry{
		ID: "test-work-rows-worker", Name: "test-work-rows-worker",
		State: livework.StateWaiting, Activity: "needs input", PaneID: "pane-worker",
		Worker: &livework.WorkerInfo{Branch: "some-branch", Worktree: "/tmp/some-branch"},
	}
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rows := mm.workRows()
	var kinds []rowKind
	for _, r := range rows {
		kinds = append(kinds, r.kind)
	}
	// Commands section header, the one command, Workers section header
	// (blank spacer optional), the worker row.
	foundCmd, foundWorker := -1, -1
	for i, r := range rows {
		if r.kind == rowCommand {
			foundCmd = i
		}
		if r.kind == rowWorker && r.work.worker.ID == worker.ID {
			foundWorker = i
		}
	}
	if foundCmd < 0 {
		t.Fatalf("no command row in %v", kinds)
	}
	if foundWorker < 0 {
		t.Fatalf("no worker row in %v", kinds)
	}
	if foundWorker < foundCmd {
		t.Errorf("worker row (%d) came before the command row (%d) — commands must come first", foundWorker, foundCmd)
	}

	// One cursor traverses both: moving down from the command row must reach
	// the worker row without any special-casing at the boundary.
	mm.cursor[tabWork] = foundCmd
	mm.moveCursor(1)
	r, ok := mm.currentRow()
	if !ok || r.kind != rowWorker {
		t.Errorf("moving down from the last command did not land on the worker row")
	}
}

// TestWorkRowsFoldsAProcessEntryIntoItsCommandRow is the AC's "running a
// command from the tab dispatches it, and the row switches to its live
// status" as read back through the registry: a LongRunning action is
// recorded under its own action name with no WorkerInfo (internal/kinds/
// pane.go's recordLiveWork), and that must show up ON the command row, not
// as a second row of its own.
func TestWorkRowsFoldsAProcessEntryIntoItsCommandRow(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"dev-server": {Desc: "start the dev server", Steps: []string{"dev-server"}},
		},
		Actions: map[string]manifest.Action{"dev-server": {"kind": "vite-dev"}},
	}
	mm := workTestModel(m)

	e := &livework.Entry{
		ID: "test-fold-process-entry", Name: "dev-server",
		State: livework.StateRunning, Activity: "localhost:5173", PaneID: "pane 9",
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// livework.Default is a process-wide singleton (internal/livework's own
	// doc comment: "one registry per process"), so other tests in this
	// package may have already left entries in it — this only asserts on
	// e's own row, never on the absence of every other one.
	rows := mm.workRows()
	found := false
	for _, r := range rows {
		if r.kind == rowCommand && r.work.cmdName == "dev-server" {
			found = true
			if r.work.live == nil {
				t.Fatalf("dev-server's row has no live entry, want the running one")
			}
			if r.work.live.ID != e.ID {
				t.Errorf("dev-server's row live entry = %q, want %q", r.work.live.ID, e.ID)
			}
		}
		if r.kind == rowWorker && r.work.worker.ID == e.ID {
			t.Errorf("the process-kind entry (no WorkerInfo) got its own row: %+v", r.work)
		}
	}
	if !found {
		t.Fatalf("no command row for dev-server")
	}
}

func TestWorkTabAbsentWithoutManifest(t *testing.T) {
	dir := t.TempDir() // no admin.toml here
	m := New(context.Background(), dir, "", "main", "main")
	if m.manifestOK {
		t.Fatalf("manifestOK = true in a directory with no admin.toml")
	}
	for _, tb := range m.visibleTabs() {
		if tb == tabWork {
			t.Errorf("tabWork is visible with no manifest present")
		}
	}
	if m.active == tabWork {
		t.Errorf("active tab is Work with no manifest present")
	}
}

func TestWorkTabPresentAndActiveWithManifest(t *testing.T) {
	dir := t.TempDir()
	body := "project_name = \"demo\"\narchetypes = []\n\n" +
		"[commands.build]\ndesc = \"build it\"\nsteps = [\"build\"]\n\n" +
		"[actions.build]\nkind = \"shell\"\nrun = \"echo hi\"\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), dir, "", "main", "main")
	if !m.manifestOK {
		t.Fatalf("manifestOK = false with a valid admin.toml present (err: %s)", m.manifestErr)
	}
	if m.active != tabWork {
		t.Errorf("active tab = %v, want tabWork — AC: opens on the Work tab when a manifest exists", m.active)
	}
	found := false
	for _, tb := range m.visibleTabs() {
		if tb == tabWork {
			found = true
		}
	}
	if !found {
		t.Errorf("tabWork is not in visibleTabs() with a manifest present")
	}
}

func TestRenderWorkRowShowsDescriptionWhenIdleAndLiveStateWhenRunning(t *testing.T) {
	idle := &workRowData{cmdName: "build", cmdDesc: "compile the binary", cmdKind: "shell"}
	idleLine := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: idle}, false, 60)
	if !strings.Contains(idleLine, "compile the binary") {
		t.Errorf("idle command row did not show its description: %q", idleLine)
	}

	live := &workRowData{
		cmdName: "build",
		live: &livework.Entry{
			ID: "build", Name: "build", State: livework.StateRunning,
			Activity: "compiling", PaneID: "pane 3", StartedAt: time.Now(),
		},
	}
	liveLine := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: live}, false, 60)
	if strings.Contains(liveLine, "compile the binary") {
		t.Errorf("running command row still showed the idle description: %q", liveLine)
	}
	if !strings.Contains(liveLine, "running") {
		t.Errorf("running command row did not show its live state: %q", liveLine)
	}
	if !strings.Contains(liveLine, "pane 3") {
		t.Errorf("running command row did not show its pane: %q", liveLine)
	}
}
