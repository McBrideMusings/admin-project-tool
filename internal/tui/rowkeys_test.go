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
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
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
		manifest: &manifest.Manifest{
			Custom: map[string]manifest.Command{"mycmd": {Steps: []string{"mycmd"}}},
		},
		beads: []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
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

// TestRowKeyDispatchRefusesReservedKey proves rowKeyDispatch refuses a
// reserved key before consulting m.rowKeys at all — acceptance criterion 3
// of admin-gz7g. The binding here is hand-installed directly onto
// m.rowKeys, bypassing buildRowKeys/validate.RowKeys entirely, to stand in
// for the ticket's "somehow got installed" case: even then, dispatch must
// still refuse rather than let the binding pre-empt the global "q" behavior
// in handleKey's built-in switch.
func TestRowKeyDispatchRefusesReservedKey(t *testing.T) {
	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest:   &manifest.Manifest{},
		beads:      []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {"q": "mycmd"},
		},
	}
	trackerCursorTo(t, &m, "proj-1")

	_, cmd, ok := m.rowKeyDispatch("q")
	if ok {
		t.Fatal("rowKeyDispatch(\"q\") on a reserved key = hit, want miss even with a binding installed")
	}
	if cmd != nil {
		t.Errorf("rowKeyDispatch returned ok=false but a non-nil Cmd")
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

// TestRowKeyDispatchOnWorkCommandRow proves a [keys.command] binding on the
// Work tab dispatches its command with the command row's live-work id as
// ADMIN_ROW_ID — the id a reload loop records as its owner — and that the
// footer advertises it with the bound command's desc.
func TestRowKeyDispatchOnWorkCommandRow(t *testing.T) {
	freshLivework(t)
	gotEnv := stubStartHostedRun(t)

	m := twoCommandModel()
	m.manifest.Custom["reload"] = manifest.Command{Desc: "reload", Steps: []string{"reload"}}
	m.manifest.Actions["reload"] = manifest.Action{"kind": "server-reload"}
	m.rowKeys = map[keymap.RowKind]map[string]string{keymap.Command: {"r": "reload"}}
	m.cursor[tabWork] = rowIndex(t, m, isCmdRow("build"))

	want := [][2]string{{"↑↓", "move"}, {"↵", "dispatch"}, {"c", "collapse"}, {"q/esc", "quit"}, {"r", "reload"}}
	if got := m.workFooterPairs(); !pairsEqual(got, want) {
		t.Errorf("footer pairs = %v, want %v", got, want)
	}

	_, cmd, ok := m.rowKeyDispatch("r")
	if !ok || cmd == nil {
		t.Fatalf("rowKeyDispatch(\"r\") on a bound command row = (ok=%v, cmd=%v), want a dispatch", ok, cmd)
	}
	if started, isStart := cmd().(workDispatchStartedMsg); !isStart || started.name != "reload" {
		t.Fatalf("dispatch Cmd produced %#v, want workDispatchStartedMsg for reload", started)
	}
	for _, kv := range []string{"ADMIN_ROW_KIND=command", "ADMIN_ROW_ID=build"} {
		if !containsEnv(*gotEnv, kv) {
			t.Errorf("env %v missing %s", *gotEnv, kv)
		}
	}
}

// TestRowIDWorkRowsAreLiveIDs proves each Work row kind's ADMIN_ROW_ID is the
// id its run is keyed on: the command name, "<command>:<target>", or the
// worker's entry id.
func TestRowIDWorkRowsAreLiveIDs(t *testing.T) {
	cases := []struct {
		r    rowItem
		kind keymap.RowKind
		want string
	}{
		{rowItem{kind: rowCommand, work: &workRowData{cmdName: "dev"}}, keymap.Command, "dev"},
		{rowItem{kind: rowCommandTarget, work: &workRowData{cmdName: "dev", targetKey: "web"}}, keymap.CommandTarget, "dev:web"},
		{rowItem{kind: rowWorker, work: &workRowData{worker: &livework.Entry{ID: "agent-7"}}}, keymap.Worker, "agent-7"},
	}
	for _, c := range cases {
		if got, ok := rowKindOf(c.r); !ok || got != c.kind {
			t.Errorf("rowKindOf(%v) = (%q, %v), want (%q, true)", c.r.kind, got, ok, c.kind)
		}
		if got := rowID(c.r, c.kind); got != c.want {
			t.Errorf("rowID(%v) = %q, want %q", c.r.kind, got, c.want)
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

// rowKeyWorkflowManifest is the fixture the four rowKeyDispatch/workflow
// tests share: [keys.bead].enter bound to "review-branch", a single-step
// kind="workflow" over="selection" command — with agent set explicitly so
// the immediate-dispatch path never depends on this machine's own
// ~/.config/repo/config.toml — plus a plain shell command ("build") to
// prove a non-workflow binding is unaffected.
func rowKeyWorkflowManifest(agent string) *manifest.Manifest {
	action := manifest.Action{"kind": "workflow", "over": "selection"}
	if agent != "" {
		action["agent"] = agent
	}
	return &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"review-branch": {Desc: "review a picked bead", Steps: []string{"review"}},
			"build":         {Desc: "compile", Steps: []string{"build"}},
		},
		Actions: map[string]manifest.Action{
			"review": action,
			"build":  {"kind": "shell"},
		},
	}
}

// TestRowKeyDispatchWorkflowImmediateOnCursorRow proves the empty-selection
// path: enter on a bead bound to a selection-sourced workflow, with nothing
// selected, dispatches immediately over just the row under the cursor — no
// overlay, one target, through the same dispatchWorkflowCmd the confirm
// overlay itself uses — and the status line names the command and the bead
// id.
func TestRowKeyDispatchWorkflowImmediateOnCursorRow(t *testing.T) {
	gotTargets := stubDispatchWorkflow(t, nil)

	m := workTestModel(rowKeyWorkflowManifest("claude"))
	m.active = tabBeads
	m.beads = []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}}
	m.trackerSelected = map[string]bool{}
	m.rowKeys = map[keymap.RowKind]map[string]string{
		keymap.Bead: {"enter": "review-branch"},
	}
	trackerCursorTo(t, &m, "proj-1")

	mm, cmd, ok := m.rowKeyDispatch("enter")
	if !ok {
		t.Fatal("rowKeyDispatch(\"enter\") on a workflow-bound bead row = miss, want hit")
	}
	rm := mm.(Model)
	if rm.wfOpen {
		t.Errorf("wfOpen = true on the immediate (empty-selection) path, want no overlay")
	}
	if !strings.Contains(rm.status, "review-branch") || !strings.Contains(rm.status, "proj-1") {
		t.Errorf("status = %q, want it to name the command and the bead id", rm.status)
	}
	if cmd == nil {
		t.Fatal("rowKeyDispatch returned ok=true with a nil Cmd")
	}
	msg := cmd()
	done, ok := msg.(workflowDispatchDoneMsg)
	if !ok {
		t.Fatalf("dispatch Cmd produced a %T, want workflowDispatchDoneMsg", msg)
	}
	if done.name != "review-branch" {
		t.Errorf("dispatched command = %q, want review-branch", done.name)
	}
	if len(*gotTargets) != 1 || (*gotTargets)[0].Name != "proj-1" {
		t.Fatalf("dispatched targets = %v, want exactly [proj-1]", *gotTargets)
	}
}

// TestRowKeyDispatchWorkflowSelectionOpensConfirmOverlay proves the
// nonempty-selection path: the same binding, with three beads selected,
// opens the same confirm overlay the space-then-w picker uses — naming all
// three targets and dispatching nothing until the overlay itself confirms
// (workflowConfirmKey, unchanged by this ticket).
func TestRowKeyDispatchWorkflowSelectionOpensConfirmOverlay(t *testing.T) {
	stubDispatchWorkflow(t, nil)

	m := workTestModel(rowKeyWorkflowManifest("claude"))
	m.active = tabBeads
	m.beads = []bd.Bead{
		{ID: "proj-1", Title: "one", Status: "open"},
		{ID: "proj-2", Title: "two", Status: "open"},
		{ID: "proj-3", Title: "three", Status: "open"},
	}
	m.trackerSelected = map[string]bool{"proj-1": true, "proj-2": true, "proj-3": true}
	m.rowKeys = map[keymap.RowKind]map[string]string{
		keymap.Bead: {"enter": "review-branch"},
	}
	trackerCursorTo(t, &m, "proj-1")

	mm, cmd, ok := m.rowKeyDispatch("enter")
	if !ok {
		t.Fatal("rowKeyDispatch(\"enter\") on a workflow-bound bead row = miss, want hit")
	}
	rm := mm.(Model)
	if !rm.wfOpen {
		t.Fatalf("wfOpen = false, want the confirm overlay open for a nonempty selection")
	}
	if !rm.wfFromSelection {
		t.Errorf("wfFromSelection = false, want true for a selection-originated confirm")
	}
	if len(rm.wfTargets) != 3 {
		t.Fatalf("wfTargets = %v, want all 3 selected beads", rm.wfTargets)
	}
	if cmd != nil {
		t.Errorf("rowKeyDispatch returned a non-nil cmd opening the confirm overlay, want nil (nothing dispatched yet)")
	}
}

// TestRowKeyDispatchWorkflowNoAgentReportsRedStatus proves the immediate
// path's misconfiguration handling: with no agent configured on the action
// (and no [workflow].agent to fall back on), the immediate (cursor-row)
// path reports a red status line and dispatches nothing — the field the
// confirm overlay would have shown has nowhere else to go on this path.
func TestRowKeyDispatchWorkflowNoAgentReportsRedStatus(t *testing.T) {
	dispatched := false
	orig := runDispatchWorkflow
	runDispatchWorkflow = func(repoRoot, name string, action manifest.Action, targets []kinds.Target, _ kinds.DispatchSurface) []kinds.TargetResult {
		dispatched = true
		return nil
	}
	t.Cleanup(func() { runDispatchWorkflow = orig })

	m := workTestModel(rowKeyWorkflowManifest(""))
	m.active = tabBeads
	m.beads = []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}}
	m.trackerSelected = map[string]bool{}
	m.rowKeys = map[keymap.RowKind]map[string]string{
		keymap.Bead: {"enter": "review-branch"},
	}
	trackerCursorTo(t, &m, "proj-1")

	mm, cmd, ok := m.rowKeyDispatch("enter")
	if !ok {
		t.Fatal("rowKeyDispatch(\"enter\") on a workflow-bound bead row = miss, want hit")
	}
	if cmd != nil {
		t.Errorf("rowKeyDispatch returned a non-nil cmd with no agent configured, want nil")
	}
	rm := mm.(Model)
	if rm.statusFg != cRed {
		t.Errorf("statusFg = %v, want cRed", rm.statusFg)
	}
	if !strings.Contains(rm.status, "no agent configured") {
		t.Errorf("status = %q, want it to say no agent is configured", rm.status)
	}
	if dispatched {
		t.Error("runDispatchWorkflow was called with no agent configured, want nothing dispatched")
	}
}

// TestRowKeyDispatchNonWorkflowCommandStillRunsPlain proves a binding whose
// command is declared but is NOT a selection-sourced workflow (a plain
// shell command here) is unaffected by this ticket's new branch — it still
// runs through dispatchCommandCmd exactly as admin-l6kb.2 left it.
func TestRowKeyDispatchNonWorkflowCommandStillRunsPlain(t *testing.T) {
	freshLivework(t)
	gotEnv := stubStartHostedRun(t)

	m := workTestModel(rowKeyWorkflowManifest("claude"))
	m.active = tabBeads
	m.beads = []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}}
	m.rowKeys = map[keymap.RowKind]map[string]string{
		keymap.Bead: {"b": "build"},
	}
	trackerCursorTo(t, &m, "proj-1")

	mm, cmd, ok := m.rowKeyDispatch("b")
	if !ok {
		t.Fatal("rowKeyDispatch(\"b\") on a plain-command-bound bead row = miss, want hit")
	}
	_ = mm
	if cmd == nil {
		t.Fatal("rowKeyDispatch returned ok=true with a nil Cmd")
	}
	msg := cmd()
	started, ok := msg.(workDispatchStartedMsg)
	if !ok {
		t.Fatalf("dispatch Cmd produced a %T, want workDispatchStartedMsg", msg)
	}
	if started.name != "build" {
		t.Errorf("dispatched command = %q, want build", started.name)
	}
	if !containsEnv(*gotEnv, "ADMIN_ROW_ID=proj-1") {
		t.Errorf("env %v missing ADMIN_ROW_ID=proj-1", *gotEnv)
	}
}

// TestRowKeyDispatchAttachesWhenCommandAlreadyLive proves admin-c2zl: a
// bound key pressed against a command that already has a live entry
// attaches to it instead of starting a second run — the double-press case.
// The command name ("rowkeylive") and its live entry's own name are unique
// to this test so it cannot collide with the "build" fixture other tests in
// this package register on the same process-global livework.Default
// registry (see TestActivateCommandAttachesWhenLive's own comment on the
// same pattern in work_enter_test.go).
//
// MUTATION EVIDENCE: delete the liveEntryFor/attachEntry branch this ticket
// added to rowKeyDispatch (fall through to dispatchCommandCmd
// unconditionally) and this test fails: "rowKeyDispatch dispatched a second
// run instead of attaching, attached=false".
func TestRowKeyDispatchAttachesWhenCommandAlreadyLive(t *testing.T) {
	freshLivework(t)
	gotEnv := stubStartHostedRun(t)

	attached := false
	live := &livework.Entry{
		ID: "rowkeylive", Name: "rowkeylive", State: livework.StateRunning,
		Handle: "pane-rowkeylive", Via: "terminal",
		Ops: livework.Ops{Attach: func() error { attached = true; return nil }},
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := Model{
		active:     tabBeads,
		width:      120,
		height:     40,
		manifestOK: true,
		manifest: &manifest.Manifest{
			Custom:  map[string]manifest.Command{"rowkeylive": {Steps: []string{"rowkeylive"}}},
			Actions: map[string]manifest.Action{"rowkeylive": {"kind": "shell"}},
		},
		beads: []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}},
		rowKeys: map[keymap.RowKind]map[string]string{
			keymap.Bead: {"b": "rowkeylive"},
		},
	}
	trackerCursorTo(t, &m, "proj-1")

	// Press the bound key twice — the double-press case the ticket names.
	if _, _, ok := m.rowKeyDispatch("b"); !ok {
		t.Fatal("rowKeyDispatch(\"b\") first press = miss, want hit")
	}
	_, cmd, ok := m.rowKeyDispatch("b")
	if !ok {
		t.Fatal("rowKeyDispatch(\"b\") second press = miss, want hit")
	}
	if !attached {
		t.Error("rowKeyDispatch dispatched a second run instead of attaching, attached=false")
	}
	if cmd != nil {
		t.Errorf("rowKeyDispatch returned a non-nil Cmd for an attach, want nil (attach is synchronous)")
	}
	if *gotEnv != nil {
		t.Errorf("startHostedRun was called (env=%v) — a live command must never dispatch a second run", *gotEnv)
	}
}

// dogfoodImplementManifest is admin-7wy5's exact declaration — the one this
// repo's own (untracked) admin.toml carries: [commands.implement] routing
// to a kind="workflow" action with over="selection" and agent="claude" set
// on the action itself, bound to enter on bead rows. Named separately from
// rowKeyWorkflowManifest (whose command is "review-branch") so a failure in
// either test names the dogfood shape unambiguously.
func dogfoodImplementManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"implement": {Desc: "Dispatch an agent over the selected bead(s) (ADR-0018 dogfood)", Steps: []string{"implement"}},
		},
		Actions: map[string]manifest.Action{
			"implement": {"kind": "workflow", "over": "selection", "agent": "claude"},
		},
	}
}

// TestRowPairsDogfoodEnterAdvertisesImplementDesc is admin-7wy5's acceptance
// criterion 2: with [keys.bead] enter = "implement" bound (the dogfood
// declaration), the Beads-tab footer's ↵ pair carries the implement
// command's own desc instead of the built-in "start work" label — the
// ADR-0018 claim ("the footer shows the effective binding") exercised
// against this ticket's specific declaration rather than a synthetic one.
func TestRowPairsDogfoodEnterAdvertisesImplementDesc(t *testing.T) {
	m := workTestModel(dogfoodImplementManifest())
	m.active = tabBeads
	m.beads = []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}}
	m.rowKeys = map[keymap.RowKind]map[string]string{
		keymap.Bead: {"enter": "implement"},
	}
	trackerCursorTo(t, &m, "proj-1")

	got := m.rowPairs(keymap.Bead)
	if got[1][0] != "↵" {
		t.Fatalf("enter pair Display = %q, want \"↵\" (position preserved)", got[1][0])
	}
	if got[1][1] != "Dispatch an agent over the selected bead(s) (ADR-0018 dogfood)" {
		t.Errorf("enter pair Label = %q, want the implement command's own desc, not the built-in \"start work\"", got[1][1])
	}
}

// TestRowKeyDispatchDogfoodEnterRoutesToWorkflowNotBuiltinWorktree is
// admin-7wy5's acceptance criterion 2's other half: with the dogfood
// declaration bound and nothing selected, pressing enter on a bead row is a
// rowKeyDispatch HIT that routes into rowKeyWorkflow (proven by the
// resulting Cmd producing a workflowDispatchDoneMsg, the type only
// rowKeyWorkflow's immediate-dispatch path ever produces) — never falling
// through to dispatchCommandCmd's workDispatchStartedMsg, which is what the
// pre-existing built-in "start work" open-a-worktree path would have run
// before this binding existed.
func TestRowKeyDispatchDogfoodEnterRoutesToWorkflowNotBuiltinWorktree(t *testing.T) {
	gotTargets := stubDispatchWorkflow(t, nil)

	m := workTestModel(dogfoodImplementManifest())
	m.active = tabBeads
	m.beads = []bd.Bead{{ID: "proj-1", Title: "one", Status: "open"}}
	m.trackerSelected = map[string]bool{}
	m.rowKeys = map[keymap.RowKind]map[string]string{
		keymap.Bead: {"enter": "implement"},
	}
	trackerCursorTo(t, &m, "proj-1")

	mm, cmd, ok := m.rowKeyDispatch("enter")
	if !ok {
		t.Fatal("rowKeyDispatch(\"enter\") on the dogfood binding = miss, want hit")
	}
	rm := mm.(Model)
	if rm.wfOpen {
		t.Errorf("wfOpen = true on the immediate (empty-selection) path, want no overlay")
	}
	if cmd == nil {
		t.Fatal("rowKeyDispatch returned ok=true with a nil Cmd")
	}
	msg := cmd()
	done, ok := msg.(workflowDispatchDoneMsg)
	if !ok {
		t.Fatalf("dispatch Cmd produced a %T, want workflowDispatchDoneMsg (rowKeyWorkflow) — a %T would mean the built-in worktree path ran instead", msg, msg)
	}
	if done.name != "implement" {
		t.Errorf("dispatched command = %q, want implement", done.name)
	}
	if len(*gotTargets) != 1 || (*gotTargets)[0].Name != "proj-1" {
		t.Fatalf("dispatched targets = %v, want exactly [proj-1]", *gotTargets)
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

// TestRowKeyDispatchPlainCommandIgnoresTheSelection pins the boundary of
// ADR-0018's "acts on the selection" consequence, which reads as a general
// rule about "a bound key" and is not one. Only a binding resolving to a
// single-step kind="workflow" action with over="selection" takes a list of
// targets; every other shape dispatches once against the cursor row, however
// many rows are selected, because ADMIN_ROW_KIND/ADMIN_ROW_ID/ADMIN_ROW_URL
// carry one row and have no plural form.
//
// Without this, nothing distinguishes "deliberately cursor-only" from "the
// selection branch was never wired for plain commands" — the review of the
// epic diff read the ADR literally and could not tell which it was.
func TestRowKeyDispatchPlainCommandIgnoresTheSelection(t *testing.T) {
	freshLivework(t)
	gotEnv := stubStartHostedRun(t)

	m := workTestModel(rowKeyWorkflowManifest("claude"))
	m.active = tabBeads
	m.beads = []bd.Bead{
		{ID: "proj-1", Title: "one", Status: "open"},
		{ID: "proj-2", Title: "two", Status: "open"},
		{ID: "proj-3", Title: "three", Status: "open"},
	}
	m.trackerSelected = map[string]bool{"proj-1": true, "proj-2": true, "proj-3": true}
	m.rowKeys = map[keymap.RowKind]map[string]string{
		keymap.Bead: {"b": "build"},
	}
	trackerCursorTo(t, &m, "proj-2")

	mm, cmd, ok := m.rowKeyDispatch("b")
	if !ok {
		t.Fatal("rowKeyDispatch(\"b\") on a plain-command-bound bead row = miss, want hit")
	}
	rm := mm.(Model)
	if rm.wfOpen {
		t.Error("wfOpen = true: a plain command must not raise the selection confirm overlay")
	}
	if cmd == nil {
		t.Fatal("rowKeyDispatch returned ok=true with a nil Cmd")
	}
	msg := cmd()
	if _, ok := msg.(workDispatchStartedMsg); !ok {
		t.Fatalf("dispatch Cmd produced a %T, want workDispatchStartedMsg", msg)
	}
	if !containsEnv(*gotEnv, "ADMIN_ROW_ID=proj-2") {
		t.Errorf("env %v missing ADMIN_ROW_ID=proj-2 — a plain command acts on the CURSOR row, not the selection", *gotEnv)
	}
	for _, other := range []string{"proj-1", "proj-3"} {
		if containsEnv(*gotEnv, "ADMIN_ROW_ID="+other) {
			t.Errorf("env %v carries ADMIN_ROW_ID=%s — only the cursor row may reach a plain command", *gotEnv, other)
		}
	}
}
