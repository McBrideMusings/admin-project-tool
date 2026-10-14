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
		shell:           shell{width: 120, height: 40},
		manifest:        m,
		manifestOK:      true,
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
// query-sourced workflow appears in the list; a row-sourced one does
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
			"review": {"kind": "workflow", "over": "row"},
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
		t.Errorf("row-sourced workflow command was kept, want it dropped")
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

// TestRunningCommandLightsOnlyItsOwnRow is the social-chess case: dev and
// setup share the db-ensure step, and running dev must show dev as running
// and leave setup idle.
func TestRunningCommandLightsOnlyItsOwnRow(t *testing.T) {
	freshLivework(t)
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"setup": {Desc: "install and migrate", Steps: []string{"install-deps", "db-ensure"}},
			"dev":   {Desc: "run the dev stack", Steps: []string{"db-ensure", "dev"}},
		},
		Actions: map[string]manifest.Action{
			"install-deps": {"kind": "shell"},
			"db-ensure":    {"kind": "shell"},
			"dev":          {"kind": "shell"},
		},
	}
	if err := livework.Default.Add(&livework.Entry{ID: "dev", Name: "dev", Handle: "pty-dev"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	live := map[string]bool{}
	for _, r := range workTestModel(m).workRows() {
		if r.kind == rowCommand && r.work != nil {
			live[r.work.cmdName] = r.work.live != nil
		}
	}
	if !live["dev"] {
		t.Error("dev row is not live while dev runs")
	}
	if live["setup"] {
		t.Error("setup row is live while only dev runs — it shares the db-ensure step, not the run")
	}

	// An entry keyed on the shared step is not either command's run.
	freshLivework(t)
	if err := livework.Default.Add(&livework.Entry{ID: "db-ensure", Name: "db-ensure", Handle: "pty-db"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, r := range workTestModel(m).workRows() {
		if r.kind == rowCommand && r.work != nil && r.work.live != nil {
			t.Errorf("%s row is live from an entry keyed on its step db-ensure", r.work.cmdName)
		}
	}
}

// TestWorkerAppearsOnlyInWorkersTab is admin-spjp.9's acceptance check: a
// genuine agent worker (Worker != nil, the row a bead binding or a workflow
// target produces) lands in the Workers tab's rows and in none of the
// Commands tab's.
func TestWorkerAppearsOnlyInWorkersTab(t *testing.T) {
	freshLivework(t)
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"build": {Desc: "build the binary", Steps: []string{"build"}},
		},
		Actions: map[string]manifest.Action{"build": {"kind": "shell"}},
	}
	mm := workTestModel(m)

	worker := &livework.Entry{
		ID: "test-work-rows-worker", Name: "test-work-rows-worker",
		Handle: "pane-worker", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "some-branch", Worktree: "/tmp/some-branch"},
	}
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add: %v", err)
	}
	livework.Default.SetState(worker.ID, livework.StateWaiting, nil)
	livework.Default.SetActivity(worker.ID, "needs input")

	for _, r := range mm.workRows() {
		if r.kind == rowWorker {
			t.Errorf("Commands tab holds a worker row: %+v", r)
		}
	}
	found := 0
	for _, r := range mm.workerRows() {
		if r.kind == rowCommand || r.kind == rowCommandTarget {
			t.Errorf("Workers tab holds a command row: %+v", r)
		}
		if r.kind == rowWorker && r.work.worker.ID == worker.ID {
			found++
		}
	}
	if found != 1 {
		t.Errorf("Workers tab holds the worker %d times, want exactly 1", found)
	}
}

// TestWorkRowsFoldsAProcessEntryIntoItsCommandRow is the AC's "running a
// command from the tab dispatches it, and the row switches to its live
// status" as read back through the registry: a LongRunning action is
// recorded under its own action name with no WorkerInfo (internal/kinds/
// pane.go's recordLiveWork), and that must show up ON the command row, not
// as a second row of its own.
func TestWorkRowsFoldsAProcessEntryIntoItsCommandRow(t *testing.T) {
	freshLivework(t)
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"dev-server": {Desc: "start the dev server", Steps: []string{"dev-server"}},
		},
		Actions: map[string]manifest.Action{"dev-server": {"kind": "vite-dev"}},
	}
	mm := workTestModel(m)

	e := &livework.Entry{
		ID: "dev-server", Name: "dev-server-action",
		Handle: "pane 9", Via: "terminal",
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	livework.Default.SetActivity(e.ID, "localhost:5173")

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
	freshLivework(t)
	idle := &workRowData{cmdName: "build", cmdDesc: "compile the binary", cmdKind: "shell"}
	idleLine := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: idle}, false, 60)
	if !strings.Contains(idleLine, "compile the binary") {
		t.Errorf("idle command row did not show its description: %q", idleLine)
	}

	entry := &livework.Entry{
		ID: "build", Name: "build",
		Handle: "pane 3", Via: "terminal", StartedAt: time.Now(),
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
	livework.Default.SetActivity(entry.ID, "compiling")
	live := &workRowData{cmdName: "build", live: entry}
	liveLine := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: live}, false, 60)
	if strings.Contains(liveLine, "compile the binary") {
		t.Errorf("running command row still showed the idle description: %q", liveLine)
	}
	if !strings.Contains(liveLine, "running") {
		t.Errorf("running command row did not show its live state: %q", liveLine)
	}
	if !strings.Contains(liveLine, "compiling") {
		t.Errorf("running command row did not show its activity: %q", liveLine)
	}
}

// TestCommandRowItemPrefersLiveEntryLogPath is admin-rc82's Work-tab
// acceptance criterion: the detail pane shows the path from the running
// command (livework.Default.LogPath, set by hostedpty.go's announce sniffer)
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

	freshLivework(t)
	live := &livework.Entry{ID: "build", Name: "build", Handle: "pty-build"}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add(%s): %v", live.ID, err)
	}
	livework.Default.SetLogPath(live.ID, "/live/current.log")
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

// TestTargetRowsResolvesLiveEntryByCommandName proves a target row resolves
// the entry keyed "<command>:<target>" — the shape
// internal/kinds.multiTargetLiveID writes (see detach_test.go's
// TestMultiTargetPerTargetDetachDashboardProducerDispatchesThroughLauncher)
// — even when the command ("dev") steps to a differently named action
// ("pick"), and never an entry keyed on that action.
func TestTargetRowsResolvesLiveEntryByCommandName(t *testing.T) {
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

	live := &livework.Entry{ID: "dev:cf", Name: "dev:cf"}
	other := &livework.Entry{ID: "pick:prod", Name: "pick:prod"}
	byName := map[string]*livework.Entry{"dev:cf": live, "pick:prod": other}

	parent := mm.commandRowItem(workCmdRow{name: "dev", cmd: m.Custom["dev"]}, byName)
	rows := targetRows(parent, byName, "")
	got := map[string]*livework.Entry{}
	for _, r := range rows {
		got[r.work.targetKey] = r.work.live
	}
	if got["cf"] != live {
		t.Errorf("cf target row's live entry = %v, want the entry keyed \"dev:cf\"", got["cf"])
	}
	if got["prod"] != nil {
		t.Errorf("prod target row resolved %v, want nothing — \"pick:prod\" is keyed on the action, not the command", got["prod"])
	}
}

// In collapse mode workRows marks every group it hides as closed, and leaves
// the open one (workOpenCmd) and every group in expanded mode unmarked, so
// renderGroupRow can tell a closed group from an empty one (admin-3rgp.72.35).
func TestWorkRowsMarksHiddenGroupsClosed(t *testing.T) {
	two := map[string]any{"kind": "multi-target", "a": map[string]any{"run": "a"}, "b": map[string]any{"run": "b"}}
	m := &manifest.Manifest{
		Custom:  map[string]manifest.Command{"dev": {Desc: "dev", Steps: []string{"dev"}}, "logs": {Desc: "logs", Steps: []string{"logs"}}},
		Actions: map[string]manifest.Action{"dev": two, "logs": two},
	}
	closed := func(mm Model) map[string]bool {
		got := map[string]bool{}
		for _, r := range mm.workRows() {
			if r.kind == rowCommand && r.group {
				got[r.work.cmdName] = r.work.closed
			}
		}
		return got
	}
	mm := workTestModel(m)
	if got := closed(mm); got["dev"] || got["logs"] {
		t.Errorf("expanded mode closed = %v, want no group closed", got)
	}
	mm.workCollapsed, mm.workOpenCmd = true, "dev"
	if got := closed(mm); got["dev"] || !got["logs"] {
		t.Errorf("collapsed with dev open: closed = %v, want only logs closed", got)
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

	rows := mm.workerRows()
	headerIdx := -1
	for i, r := range rows {
		if r.kind == rowHeader && r.label == "Workers" {
			headerIdx = i
		}
	}
	if headerIdx < 0 {
		t.Fatalf("no Workers header in workerRows: %+v", rows)
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
// workers branch in workerRows), so dispatching a new worker cannot move it —
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

	before := mm.workerRows()
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
		Handle: "pane-stays-put", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "some-branch", Worktree: "/tmp/some-branch"},
	}
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add: %v", err)
	}
	livework.Default.SetState(worker.ID, livework.StateWaiting, nil)
	livework.Default.SetActivity(worker.ID, "needs input")

	after := mm.workerRows()
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
		t.Fatalf("dispatched worker's row not found in workerRows")
	}
	if after[headerAfter+1].kind != rowWorker {
		t.Errorf("row directly after the Workers header = %+v, want a rowWorker now that one is live", after[headerAfter+1])
	}
}

// TestWorkRowsNoCommandsStillLegible is AC 5: a manifest declaring no
// commands still gets a Commands header plus its own empty-state line, and
// the whole-view rowCenter fallback that used to stand in for this is gone.
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
	if rows[0].kind != rowHeader || rows[0].label != "Commands" {
		t.Fatalf("rows[0] = %+v, want the Commands header", rows[0])
	}
	if len(rows) < 2 || rows[1].kind != rowText || rows[1].label == "" {
		t.Fatalf("row after Commands header = %+v, want a non-blank empty-state rowText", rows[1])
	}
	for _, r := range rows {
		if r.kind == rowCenter {
			t.Errorf("found a rowCenter row, want the whole-view fallback deleted: %+v", r)
		}
	}
	for _, r := range rows {
		if r.kind == rowHeader && r.label == "Workers" {
			t.Errorf("Commands tab carries a Workers header: %+v", r)
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
		Handle: "pane-no-activity", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "no-activity", Worktree: "/tmp/no-activity"},
	}
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rows := mm.workerRows()
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

// TestWorkerBandFieldsAlwaysHasNowRow supersedes admin-spjp.4's old rule (no
// labelled field with no value): a worker's field block always shows the
// same five rows — log/branch/worktree/session/now — whatever the worker's
// own Activity is. An empty Activity still gets its "now" row; it is simply
// blank after the label.
func TestWorkerBandFieldsAlwaysHasNowRow(t *testing.T) {
	freshLivework(t)
	e := &livework.Entry{
		ID: "test-worker-detail-no-activity", Name: "test-worker-detail-no-activity",
		Handle:    "pty-detail-no-activity",
		StartedAt: time.Now(),
	}
	if err := livework.Default.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	out := bandFieldsWorker(e, 60)
	if len(out) != 5 {
		t.Fatalf("bandFieldsWorker produced %d rows, want exactly 5 (log/branch/worktree/session/now)", len(out))
	}
	found := false
	for _, line := range out {
		if strings.Contains(line, "now") {
			found = true
		}
	}
	if !found {
		t.Errorf("bandFieldsWorker with empty Activity dropped the \"now\" row entirely, want it present (fixed slot): %v", out)
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
