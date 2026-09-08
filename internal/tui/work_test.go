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
		manifest:        m,
		manifestOK:      true,
		width:           120,
		height:          40,
		pendingDispatch: map[string]bool{},
		pendingAttach:   map[string]bool{},
		pendingTarget:   map[string]string{},
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
	body := "tracker = [\"beads\"]\nuses_pull_requests = false\n"
	if err := os.WriteFile(filepath.Join(archDir, "archetype.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := archetype.SearchPath{{Label: "test", FS: os.DirFS(dir)}}

	// The project overrides tracker itself, so that key's origin should read
	// "(manifest)" while uses_pull_requests still reads back to the
	// archetype that's the only one to declare it.
	project := &manifest.Manifest{
		Archetypes: []string{"beads-tracker"},
		Trackers:   []string{"github"}, HasTracker: true,
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

// TestComputeOriginsAttributesBaseAndManifestOverride is acceptance 13: a
// populate entry contributed only by the implicit base archetype attributes
// to "base" (never "(manifest)" or silently absent), and a project that
// declares its own [[populate]] still attributes to "(manifest)", matching
// every other manifest-level key's precedence display.
func TestComputeOriginsAttributesBaseAndManifestOverride(t *testing.T) {
	dir := t.TempDir()
	archDir := filepath.Join(dir, archetype.BaseName)
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[[populate]]\npattern = \".env\"\nmode = \"copy\"\n"
	if err := os.WriteFile(filepath.Join(archDir, "archetype.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := archetype.SearchPath{{Label: "test", FS: os.DirFS(dir)}}

	baseOnly := &manifest.Manifest{}
	_, _, keys := computeOrigins(baseOnly, sp)
	if keys["populate"] != archetype.BaseName {
		t.Errorf(`keys["populate"] = %q, want %q (implicit base, no archetypes declared)`, keys["populate"], archetype.BaseName)
	}

	declaring := &manifest.Manifest{
		Populate: []map[string]any{{"pattern": "CLAUDE.local.md", "mode": "symlink"}},
	}
	_, _, keys = computeOrigins(declaring, sp)
	if keys["populate"] != "(manifest)" {
		t.Errorf(`keys["populate"] = %q, want "(manifest)" when the project declares its own [[populate]]`, keys["populate"])
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

// TestWorkRowsNeverEmitsAProcessEntryAsAWorkerRow is admin-3rgp.72.2's own
// interim-state half of what TestWorkRowsFoldsAProcessEntryIntoItsCommandRow
// used to prove: a LongRunning action's live entry (no WorkerInfo —
// internal/kinds/pane.go's recordLiveWork) never shows up in workRows() at
// all now that the flat Commands list it used to fold into is gone — giving
// it back a row of its own (the design's "Running" section) is
// admin-3rgp.72.4's job, not this one's. liveWorkers/commandRowItem still
// resolve the entry correctly; see TestCommandRowItemPrefersLiveEntryLogPath
// and TestBuildPaletteEntriesNeverEmitsABareMultiTargetParent for that half.
func TestWorkRowsNeverEmitsAProcessEntryAsAWorkerRow(t *testing.T) {
	freshLivework(t)
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"dev-server": {Desc: "start the dev server", Steps: []string{"dev-server"}},
		},
		Actions: map[string]manifest.Action{"dev-server": {"kind": "vite-dev"}},
	}
	mm := workTestModel(m)

	e := &livework.Entry{
		ID: "test-fold-process-entry", Name: "dev-server",
		State: livework.StateRunning, Activity: "localhost:5173", Handle: "pane 9", Via: "terminal",
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	for _, r := range mm.workRows() {
		if r.kind == rowWorker && r.work.worker.ID == e.ID {
			t.Errorf("the process-kind entry (no WorkerInfo) got a Workers-section row: %+v", r.work)
		}
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
	freshLivework(t)
	idle := &workRowData{cmdName: "build", cmdDesc: "compile the binary", cmdKind: "shell"}
	idleLine := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: idle}, false, 60)
	if !strings.Contains(idleLine, "compile the binary") {
		t.Errorf("idle command row did not show its description: %q", idleLine)
	}

	entry := &livework.Entry{
		ID: "build", Name: "build", State: livework.StateRunning,
		Activity: "compiling", Handle: "pane 3", Via: "terminal", StartedAt: time.Now(),
	}
	// renderWorkRow reads state through livework.Default.State(e.ID) rather
	// than the struct field directly (admin-ddc8), so the entry must actually
	// be in the registry — see TestWhereColumnHostedCommandRowShowsHostedNeverPane.
	// This test used to pass only because another test in the package (e.g.
	// TestActivateCommandAttachesWhenLive) leaked a "build" entry into the
	// process-wide livework.Default and left it there (admin-hw2i); with
	// per-test isolation that leak is gone, so the entry has to be added here.
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}
	live := &workRowData{cmdName: "build", live: entry}
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

// TestCommandRowItemPrefersLiveEntryLogPath is admin-rc82's Work-tab
// acceptance criterion: the detail pane shows the path from the running
// command (livework.Entry.LogPath, set by hostedpty.go's announce sniffer)
// rather than the best-effort glob, whenever a live entry carries one.
func TestCommandRowItemPrefersLiveEntryLogPath(t *testing.T) {
	dir := t.TempDir()
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Desc: "build", Steps: []string{"build"}},
		},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	}
	mm := workTestModel(m)
	mm.repoRoot = dir

	// A glob-visible file exists too, so this test actually proves
	// preference, not just "there's nothing else to find".
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "tmp", "build.2020-01-01_000000.log")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	live := &livework.Entry{ID: "build", Name: "build", State: livework.StateRunning, LogPath: "/live/current.log"}
	byName := map[string]*livework.Entry{"build": live}

	row := mm.commandRowItem(workCmdRow{name: "build", cmd: m.Custom["build"]}, byName)
	if row.work.logPath != "/live/current.log" {
		t.Errorf("logPath = %q, want the live entry's LogPath %q", row.work.logPath, "/live/current.log")
	}
}

// TestCommandRowItemFallsBackToGlobWhenNoLiveEntry proves the reverse: a
// command with no live entry falls back to commandLogPath's filesystem
// glob, and a genuinely never-run command still reads as "none yet" (empty
// logPath).
func TestCommandRowItemFallsBackToGlobWhenNoLiveEntry(t *testing.T) {
	dir := t.TempDir()
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build":     {Desc: "build", Steps: []string{"build"}},
			"never-run": {Desc: "never run", Steps: []string{"never-run"}},
		},
		Actions: map[string]manifest.Action{
			"build":     {"kind": "shell"},
			"never-run": {"kind": "shell"},
		},
	}
	mm := workTestModel(m)
	mm.repoRoot = dir

	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	found := filepath.Join(dir, "tmp", "build.2026-01-01_000000.log")
	if err := os.WriteFile(found, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	row := mm.commandRowItem(workCmdRow{name: "build", cmd: m.Custom["build"]}, nil)
	if row.work.logPath != found {
		t.Errorf("logPath = %q, want the glob match %q", row.work.logPath, found)
	}

	neverRow := mm.commandRowItem(workCmdRow{name: "never-run", cmd: m.Custom["never-run"]}, nil)
	if neverRow.work.logPath != "" {
		t.Errorf("logPath for a never-run command = %q, want empty (\"none yet\")", neverRow.work.logPath)
	}
}

// TestTargetRowsResolvesLiveEntryByActionNameNotCommandName is admin-0cyu.1's
// regression case: the live-work registry keys a multi-target dispatch's
// entry on the ACTION name (internal/kinds.multiTargetLiveID, via
// kinds.TargetLiveKey), not the manifest COMMAND name that steps to it —
// nothing guarantees the two match. Command "dev" steps to action "pick",
// so a registry entry named "pick:cf" (exactly the shape
// internal/kinds/detach_test.go's
// TestMultiTargetPerTargetDetachDashboardProducerDispatchesThroughLauncher
// proves the real registry writes) must resolve on the "cf" target row even
// though byName["dev:cf"] — the command-name-keyed lookup round 1 shipped —
// would miss it entirely.
func TestTargetRowsResolvesLiveEntryByActionNameNotCommandName(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"dev": {Desc: "dev", Steps: []string{"pick"}},
		},
		Actions: map[string]manifest.Action{
			"pick": {
				"kind": "multi-target",
				"cf":   map[string]any{"run": "wrangler dev"},
				"prod": map[string]any{"run": "prod dev"},
			},
		},
	}
	mm := workTestModel(m)

	live := &livework.Entry{ID: "pick:cf", Name: "pick:cf", State: livework.StateRunning}
	byName := map[string]*livework.Entry{"pick:cf": live}

	parent := mm.commandRowItem(workCmdRow{name: "dev", cmd: m.Custom["dev"]}, byName)
	if parent.work.actionName != "pick" {
		t.Fatalf("parent actionName = %q, want %q (cmd.Steps[0])", parent.work.actionName, "pick")
	}

	rows := targetRows(parent, byName, "")
	var cfRow *workRowData
	for _, r := range rows {
		if r.work.targetKey == "cf" {
			cfRow = r.work
		}
	}
	if cfRow == nil {
		t.Fatalf("no target row for %q among %+v", "cf", rows)
	}
	if cfRow.live != live {
		t.Errorf("cf target row's live entry = %v, want the registry entry keyed \"pick:cf\" (byName[\"dev:cf\"] must NOT be what resolves it)", cfRow.live)
	}
}

// TestWorkRowsWorkersHeaderAlwaysPresent covers admin-spjp.1's core claim:
// the Workers header renders whether or not any worker is live, and the row
// directly under it matches whatever liveWorkers() actually returns right
// now — either the non-selectable empty-state line (zero workers) or a real
// rowWorker (at least one). livework.Default is a process-wide singleton
// shared by every test in this package (internal/livework's own doc
// comment) with no Remove/reset, so other tests may have already left
// worker entries in it; this asserts the row shape is correct for whichever
// count is actually live, never that the count itself is zero.
func TestWorkRowsWorkersHeaderAlwaysPresent(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Desc: "build the binary", Steps: []string{"build"}},
		},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	}
	mm := workTestModel(m)

	rows := mm.workRows()
	headerIdx := -1
	for i, r := range rows {
		if r.kind == rowHeader && r.label == "Workers" {
			headerIdx = i
		}
	}
	if headerIdx < 0 {
		t.Fatalf("no Workers header in workRows: %+v", rows)
	}
	if headerIdx+1 >= len(rows) {
		t.Fatalf("Workers header has no row after it")
	}
	next := rows[headerIdx+1]
	if len(liveWorkers()) == 0 {
		if next.kind != rowText || next.label == "" {
			t.Errorf("row after Workers header = %+v, want a non-blank rowText empty state", next)
		}
		if next.selectable() {
			t.Errorf("Workers empty-state row is selectable, want it non-selectable so the cursor can't land on it")
		}
	} else if next.kind != rowWorker {
		t.Errorf("row after Workers header = %+v, want a rowWorker (liveWorkers() is non-empty)", next)
	}
}

// TestWorkRowsWorkersHeaderStaysPutAcrossDispatch is AC 3: the Workers
// header sits at a fixed offset from the Commands section (built before the
// workers branch in workRows), so dispatching a new worker cannot move it —
// the empty-state line and the worker rows are alternatives at the same
// slot, never a reason for the header itself to shift.
func TestWorkRowsWorkersHeaderStaysPutAcrossDispatch(t *testing.T) {
	freshLivework(t)
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Desc: "build the binary", Steps: []string{"build"}},
		},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	}
	mm := workTestModel(m)

	before := mm.workRows()
	headerBefore := -1
	for i, r := range before {
		if r.kind == rowHeader && r.label == "Workers" {
			headerBefore = i
		}
	}
	if headerBefore < 0 {
		t.Fatalf("no Workers header before dispatch")
	}

	worker := &livework.Entry{
		ID: "test-header-stays-put-worker", Name: "test-header-stays-put-worker",
		State: livework.StateWaiting, Activity: "needs input", Handle: "pane-stays-put", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "some-branch", Worktree: "/tmp/some-branch"},
	}
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add: %v", err)
	}

	after := mm.workRows()
	headerAfter, workerIdx := -1, -1
	for i, r := range after {
		if r.kind == rowHeader && r.label == "Workers" {
			headerAfter = i
		}
		if r.kind == rowWorker && r.work.worker.ID == worker.ID {
			workerIdx = i
		}
	}
	if headerAfter != headerBefore {
		t.Errorf("Workers header moved from %d to %d when a worker was added", headerBefore, headerAfter)
	}
	if workerIdx < 0 {
		t.Fatalf("dispatched worker's row not found in workRows")
	}
	if after[headerAfter+1].kind != rowWorker {
		t.Errorf("row directly after the Workers header = %+v, want a rowWorker now that one is live", after[headerAfter+1])
	}
}

// TestWorkRowsNoCommandsStillLegible is AC 5's Work-tab-row half: a manifest
// declaring no commands still gets a legible Workers section — the flat
// Commands list's own empty state ("admin.toml declares no commands") moved
// into the palette box (admin-3rgp.72.2: see TestPaletteIdleContentEmptyMRU,
// whose "no commands run yet" row covers a manifest with nothing to run same
// as one with nothing recorded yet) — and the whole-view rowCenter fallback
// that used to stand in for all of this is gone.
func TestWorkRowsNoCommandsStillLegible(t *testing.T) {
	m := &manifest.Manifest{
		Custom:  map[string]manifest.Command{},
		Actions: map[string]manifest.Action{},
	}
	mm := workTestModel(m)

	rows := mm.workRows()
	if len(rows) == 0 {
		t.Fatalf("workRows returned nothing for a manifest with no commands")
	}
	if rows[0].kind != rowHeader || rows[0].label != "Workers" {
		t.Fatalf("rows[0] = %+v, want the Workers header — the flat Commands list is gone from workRows() entirely", rows[0])
	}
	for _, r := range rows {
		if r.kind == rowCenter {
			t.Errorf("found a rowCenter row, want the whole-view fallback deleted: %+v", r)
		}
		if r.kind == rowCommand || r.kind == rowCommandTarget {
			t.Errorf("found a %v row in workRows(), want the flat Commands list gone with no fallback: %+v", r.kind, r)
		}
	}
}

// TestWorkRowsWorkerLabelHasNoTrailingSpaceWhenActivityEmpty is admin-spjp.4's
// acceptance criterion for work.go's row label: a worker whose Activity has
// not been written yet (AgentInfo has not resolved, or the agent reports no
// current activity) must not carry a trailing space, since the same label
// doubles as the row's identity string (rowKey, model.go:120).
func TestWorkRowsWorkerLabelHasNoTrailingSpaceWhenActivityEmpty(t *testing.T) {
	freshLivework(t)
	m := &manifest.Manifest{Custom: map[string]manifest.Command{}, Actions: map[string]manifest.Action{}}
	mm := workTestModel(m)

	worker := &livework.Entry{
		ID: "test-work-rows-no-activity", Name: "test-work-rows-no-activity",
		State: livework.StateRunning, Activity: "", Handle: "pane-no-activity", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "no-activity", Worktree: "/tmp/no-activity"},
	}
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rows := mm.workRows()
	found := false
	for _, r := range rows {
		if r.kind == rowWorker && r.work.worker.ID == worker.ID {
			found = true
			if strings.HasSuffix(r.label, " ") {
				t.Errorf("label = %q, want no trailing space when Activity is empty", r.label)
			}
			if r.label != worker.Name {
				t.Errorf("label = %q, want exactly the worker name %q when Activity is empty", r.label, worker.Name)
			}
		}
	}
	if !found {
		t.Fatalf("no worker row found for %s", worker.ID)
	}
}

// TestWorkerDetailOmitsNowLabelWhenActivityEmpty is admin-spjp.4's other
// acceptance criterion: no worker detail pane renders a labelled field with
// no value.
func TestWorkerDetailOmitsNowLabelWhenActivityEmpty(t *testing.T) {
	freshLivework(t)
	e := &livework.Entry{
		ID: "test-worker-detail-no-activity", Name: "test-worker-detail-no-activity",
		State: livework.StateRunning, Activity: "", Handle: "pty-detail-no-activity",
		StartedAt: time.Now(),
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	out := workerDetail(e, 60, 20)
	for _, line := range out {
		if strings.Contains(line, "now") {
			t.Errorf("workerDetail rendered a %q line with empty Activity, want the now label suppressed entirely: %v", line, out)
		}
	}
}

// A linked worktree's admin.toml is the one the dashboard reads. git.RepoRoot
// hands back the MAIN worktree from inside a linked one, so before
// WithManifestRoot the Work tab was dropped for every worktree that declared a
// manifest, while `admin <cmd>` in that same directory ran its commands fine.
func TestWorkTabUsesTheWorktreeManifestNotTheMainCheckout(t *testing.T) {
	mainCheckout := t.TempDir() // stands in for repoRoot: no admin.toml here
	worktree := t.TempDir()
	body := "project_name = \"demo\"\narchetypes = []\n\n" +
		"[commands.build]\ndesc = \"build it\"\nsteps = [\"build\"]\n\n" +
		"[actions.build]\nkind = \"shell\"\nrun = \"echo hi\"\n"
	if err := os.WriteFile(filepath.Join(worktree, manifest.Filename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without the option the dashboard looks at the main checkout and finds nothing.
	bare := New(context.Background(), mainCheckout, "", "main", "main")
	if bare.manifestOK {
		t.Fatal("manifestOK = true for a checkout with no admin.toml")
	}
	for _, tb := range bare.visibleTabs() {
		if tb == tabWork {
			t.Fatal("tabWork drawn for a checkout with no admin.toml")
		}
	}

	m := New(context.Background(), mainCheckout, "", "main", "main", WithManifestRoot(worktree))
	if !m.manifestOK {
		t.Fatalf("manifestOK = false for the worktree's admin.toml (err: %s)", m.manifestErr)
	}
	if got := m.manifestDir(); got != worktree {
		t.Errorf("manifestDir() = %q, want the worktree %q", got, worktree)
	}
	found := false
	for _, tb := range m.visibleTabs() {
		if tb == tabWork {
			found = true
		}
	}
	if !found {
		t.Error("tabWork is absent even though the worktree declares a manifest")
	}
	if _, ok := m.manifest.Overrides["build"]; !ok {
		t.Error("the worktree manifest's build command did not survive the load")
	}
	// Every action's subprocess runs in Manifest.Root, so this is also what
	// makes a Work-tab run happen in the worktree rather than the main checkout.
	if m.manifest.Root != worktree {
		t.Errorf("manifest.Root = %q, want the worktree %q", m.manifest.Root, worktree)
	}
}

// An empty manifest root leaves the old behaviour alone, which is what every
// Model built without the option relies on.
func TestManifestDirFallsBackToRepoRoot(t *testing.T) {
	m := Model{repoRoot: "/repo"}
	if got := m.manifestDir(); got != "/repo" {
		t.Errorf("manifestDir() = %q, want the repoRoot fallback %q", got, "/repo")
	}
	m.manifestRoot = "/repo/worktrees/feature"
	if got := m.manifestDir(); got != "/repo/worktrees/feature" {
		t.Errorf("manifestDir() = %q, want the explicit manifest root", got)
	}
}
