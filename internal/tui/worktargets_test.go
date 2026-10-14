package tui

import (
	"context"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
)

// multiTargetModel is a Model with one multi-target command ("dev", four
// targets), one single-target multi-target command ("solo"), and one plain
// shell command ("test") — enough to cover every branch the expander has.
func multiTargetModel(t *testing.T) Model {
	t.Helper()
	m := Model{
		feeds:      newFeeds(context.Background(), ""),
		manifestOK: true,
		manifest: &manifest.Manifest{
			Custom: map[string]manifest.Command{
				"dev":  {Desc: "dev build + run", Steps: []string{"dev"}, Group: 1, Priority: 1},
				"solo": {Desc: "only one target", Steps: []string{"solo"}, Group: 1, Priority: 2},
				"test": {Desc: "run tests", Steps: []string{"test"}, Group: 1, Priority: 3},
			},
			Actions: map[string]manifest.Action{
				"dev": {
					"kind": "multi-target",
					"targets": map[string]any{
						"mac":    "make mac",
						"ios":    "make ios",
						"tv":     "make tv",
						"server": "@dev-server",
					},
				},
				"solo": {
					"kind":    "multi-target",
					"targets": map[string]any{"mac": "make mac"},
				},
				"test": {"kind": "shell", "run": "go test ./..."},
			},
		},
		cmdOrigin:       map[string]string{},
		actionOrigin:    map[string]string{},
		pendingDispatch: map[string]bool{},
		pendingAttach:   map[string]bool{},
		pendingTarget:   map[string]string{},
	}
	m.mountTabs(viewstate.State{})
	return m
}

// rowNames flattens the Work tab's rows to "<kind>:<name>" so a test can
// assert the whole list shape in one comparison.
func rowNames(rows []rowItem) []string {
	var out []string
	for _, r := range rows {
		switch r.kind {
		case rowCommand:
			out = append(out, "cmd:"+r.work.cmdName)
		case rowCommandTarget:
			out = append(out, "target:"+r.work.cmdName+"/"+r.work.targetKey)
		}
	}
	return out
}

// TestEveryCommandsTargetsRenderUnconditionally: in the default expanded
// mode (workCollapsed's zero value, and the only mode before `c` existed —
// see work_collapse_test.go), a multi-target command's targets are always
// listed beneath it.
func TestEveryCommandsTargetsRenderUnconditionally(t *testing.T) {
	m := multiTargetModel(t)
	got := rowNames(m.workRows())
	want := []string{
		"cmd:dev",
		"target:dev/ios", "target:dev/mac", "target:dev/server", "target:dev/tv",
		"cmd:solo", "cmd:test",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
}

// TestSingleTargetCommandIsNotAnExpander: the CLI runs a lone target without
// prompting, so a row that would open onto exactly one child is a plain
// dispatch instead — "solo" carries no target group even though "dev"'s
// still renders unconditionally.
func TestSingleTargetCommandIsNotAnExpander(t *testing.T) {
	m := multiTargetModel(t)
	for _, r := range m.workRows() {
		if r.kind == rowCommandTarget && r.work != nil && r.work.cmdName == "solo" {
			t.Fatalf("single-target command expanded into %q", r.work.targetKey)
		}
		if r.kind == rowCommand && r.work != nil && r.work.cmdName == "solo" && len(r.work.targets) > 0 {
			t.Fatalf("single-target command carries targets %v", r.work.targets)
		}
	}
}

// cmdRow finds the rowCommand payload for name, or fails the test — the row
// list starts with a "Commands" header whose work payload is nil, so no test
// may index into it positionally.
func cmdRow(t *testing.T, m Model, name string) *workRowData {
	t.Helper()
	for _, r := range m.workRows() {
		if r.kind == rowCommand && r.work != nil && r.work.cmdName == name {
			return r.work
		}
	}
	t.Fatalf("no command row named %q", name)
	return nil
}

// TestMultiTargetParentRowIsUngroupUnselectable is
// TestEnterOnCommandWithTargetsDoesNotDispatch's replacement
// (admin-3rgp.72.11): a multi-target command's parent row has no single run
// to dispatch — its targets are already listed beneath it — so it is now a
// group the cursor can never land on at all (rowItem.group,
// selectable()), rather than a selectable row whose own enter handler used
// to no-op. Enter can no longer reach activateCommand with such a wd in the
// first place; see workgrid_test.go for the cursor-skip coverage.
func TestMultiTargetParentRowIsUngroupUnselectable(t *testing.T) {
	m := multiTargetModel(t)
	var parent rowItem
	found := false
	for _, r := range m.workRows() {
		if r.kind == rowCommand && r.work != nil && r.work.cmdName == "dev" {
			parent = r
			found = true
			break
		}
	}
	if !found {
		t.Fatal("dev's own command row is missing from workRows()")
	}
	if !parent.group {
		t.Fatal("dev has more than one target, so its row must be marked group")
	}
	if parent.selectable() {
		t.Fatal("a group row must not be selectable — the cursor must never land on it")
	}
}

// TestEveryCommandsTargetsRenderAtOnce: two multi-target commands both list
// their targets simultaneously — there is no "only one open" rule anymore.
func TestEveryCommandsTargetsRenderAtOnce(t *testing.T) {
	m := multiTargetModel(t)
	m.manifest.Actions["deploy"] = manifest.Action{
		"kind":    "multi-target",
		"targets": map[string]any{"mac": "ship mac", "ios": "ship ios"},
	}
	m.manifest.Custom["deploy"] = manifest.Command{Desc: "ship it", Steps: []string{"deploy"}, Group: 1, Priority: 4}

	var sawDevTarget, sawDeployTarget bool
	for _, r := range m.workRows() {
		if r.kind == rowCommandTarget && r.work != nil {
			switch r.work.cmdName {
			case "dev":
				sawDevTarget = true
			case "deploy":
				sawDeployTarget = true
			}
		}
	}
	if !sawDevTarget {
		t.Error("dev's targets are missing")
	}
	if !sawDeployTarget {
		t.Error("deploy's targets are missing")
	}
}

// TestTargetRowDispatchesParentWithTargetArg checks the plumbing that makes
// a target row equivalent to `admin dev mac` — the arg the CLI picker would
// otherwise have to ask for.
func TestTargetRowDispatchesParentWithTargetArg(t *testing.T) {
	m := multiTargetModel(t)
	var target *workRowData
	for _, r := range m.workRows() {
		if r.kind == rowCommandTarget && r.work != nil && r.work.targetKey == "mac" {
			target = r.work
		}
	}
	if target == nil {
		t.Fatal("no mac target row")
	}
	if target.cmdName != "dev" {
		t.Fatalf("target row cmdName = %q, want \"dev\"", target.cmdName)
	}
	old := startHostedRun
	defer func() { startHostedRun = old }()
	var gotName string
	var gotArgs []string
	startHostedRun = func(mf *manifest.Manifest, name string, args []string, rows, cols int, env []string) error {
		gotName, gotArgs = name, args
		return nil
	}

	_, cmd := m.activateCommandTarget(target)
	if cmd == nil {
		t.Fatal("activateCommandTarget returned a nil Cmd")
	}
	cmd()
	if gotName != "dev" {
		t.Fatalf("dispatch name = %q, want \"dev\"", gotName)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "mac" {
		t.Fatalf("dispatch args = %v, want [mac]", gotArgs)
	}
}

// TestEnterOnMultiTargetParentLeavesCursorPut: row count is now stable per
// session — nothing inserts or removes rows on enter — so the cursor stays
// exactly where it was pressed.
func TestEnterOnMultiTargetParentLeavesCursorPut(t *testing.T) {
	m := multiTargetModel(t)
	m.manifest.Actions["deploy"] = manifest.Action{
		"kind":    "multi-target",
		"targets": map[string]any{"mac": "ship mac", "ios": "ship ios"},
	}
	m.manifest.Custom["deploy"] = manifest.Command{Desc: "ship it", Steps: []string{"deploy"}, Group: 1, Priority: 4}

	rows := m.workRows()
	var deployIdx int
	for i, r := range rows {
		if r.kind == rowCommand && r.work != nil && r.work.cmdName == "deploy" {
			deployIdx = i
		}
	}
	deploy := cmdRow(t, m, "deploy")
	m.cursor[m.active] = deployIdx

	next, _ := m.activateCommand(deploy)
	nm := next.(Model)
	if nm.cursor[nm.active] != deployIdx {
		t.Fatalf("cursor moved to %d, want it to stay at %d", nm.cursor[nm.active], deployIdx)
	}
	cur := nm.workRows()[nm.cursor[nm.active]]
	if cur.kind != rowCommand || cur.work.cmdName != "deploy" {
		t.Fatalf("cursor sits on %v/%v, want the deploy command row", cur.kind, cur.work)
	}
}

// TestKillOnRunningTargetRowKillsIt is admin-2y6p's second bug: pressing k
// with the cursor on a RUNNING multi-target sub-target row (e.g. Command
// Test's "counter", or "dev mac" here) did nothing at all — a user report of
// "I can't kill anything, k does nothing" against a row the dashboard itself
// showed as "running". currentLiveEntry's switch covered rowWorker and
// rowCommand but never rowCommandTarget, so it returned nil for a target row
// regardless of whether wd.live was set, and killCurrentWork silently no-ops
// on a nil entry.
//
// MUTATION EVIDENCE: remove rowCommandTarget from currentLiveEntry's case
// list (back to `case rowCommand:` alone) and this test fails: killed stays
// false.
func TestKillOnRunningTargetRowKillsIt(t *testing.T) {
	freshLivework(t)
	killed := false
	live := &livework.Entry{
		ID: kinds.TargetLiveKey("dev", "mac"), Name: "dev:mac",
		Handle: "pty-dev-mac",
		Ops:    livework.Ops{Kill: func() error { killed = true; return nil }},
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m := multiTargetModel(t)
	rows := m.workRows()
	idx := -1
	for i, r := range rows {
		if r.kind == rowCommandTarget && r.work != nil && r.work.targetKey == "mac" && r.work.cmdName == "dev" {
			idx = i
		}
	}
	if idx == -1 {
		t.Fatal("no target row found for dev/mac")
	}
	if rows[idx].work.live != live {
		t.Fatalf("dev/mac target row's live entry = %v, want the registry entry this test added", rows[idx].work.live)
	}
	m.cursor[m.active] = idx

	if _, cmd := m.killCurrentWork(); cmd != nil {
		t.Errorf("killCurrentWork returned a non-nil Cmd, want nil (it acts synchronously via Ops.Kill)")
	}
	if !killed {
		t.Error("killCurrentWork on a running target row did nothing — Ops.Kill was never invoked")
	}
}
