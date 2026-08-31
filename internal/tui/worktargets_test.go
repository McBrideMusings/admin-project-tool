package tui

import (
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// multiTargetModel is a Model with one multi-target command ("dev", four
// targets), one single-target multi-target command ("solo"), and one plain
// shell command ("test") — enough to cover every branch the expander has.
func multiTargetModel(t *testing.T) Model {
	t.Helper()
	m := Model{
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
		cmdOrigin:    map[string]string{},
		actionOrigin: map[string]string{},
	}
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

func TestCollapsedCommandListsNoTargets(t *testing.T) {
	m := multiTargetModel(t)
	got := rowNames(m.workRows())
	want := []string{"cmd:dev", "cmd:solo", "cmd:test"}
	if len(got) != len(want) {
		t.Fatalf("collapsed rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collapsed rows = %v, want %v", got, want)
		}
	}
}

func TestExpandedCommandListsItsTargetsInPickerOrder(t *testing.T) {
	m := multiTargetModel(t)
	m.expandedCmd = "dev"
	got := rowNames(m.workRows())
	want := []string{
		"cmd:dev",
		"target:dev/ios", "target:dev/mac", "target:dev/server", "target:dev/tv",
		"cmd:solo", "cmd:test",
	}
	if len(got) != len(want) {
		t.Fatalf("expanded rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expanded rows = %v, want %v", got, want)
		}
	}
}

// TestSingleTargetCommandIsNotAnExpander: the CLI runs a lone target without
// prompting, so a row that would open onto exactly one child is a plain
// dispatch instead.
func TestSingleTargetCommandIsNotAnExpander(t *testing.T) {
	m := multiTargetModel(t)
	m.expandedCmd = "solo"
	for _, r := range m.workRows() {
		if r.kind == rowCommandTarget {
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

// TestEnterOnCommandWithTargetsTogglesInsteadOfRunning is the whole point:
// enter opens the submenu and dispatches nothing.
func TestEnterOnCommandWithTargetsTogglesInsteadOfRunning(t *testing.T) {
	m := multiTargetModel(t)
	wd := cmdRow(t, m, "dev")

	next, cmd := m.activateCommand(wd)
	if cmd != nil {
		t.Fatal("enter on a command with targets dispatched something")
	}
	if got := next.(Model).expandedCmd; got != "dev" {
		t.Fatalf("expandedCmd = %q, want \"dev\"", got)
	}

	// Enter again on the same row closes it.
	m2 := next.(Model)
	again, _ := m2.activateCommand(cmdRow(t, m2, "dev"))
	if got := again.(Model).expandedCmd; got != "" {
		t.Fatalf("second enter left expandedCmd = %q, want empty", got)
	}
}

// TestOnlyOneSubmenuOpenAtATime: opening a second expander closes the first.
func TestOnlyOneSubmenuOpenAtATime(t *testing.T) {
	m := multiTargetModel(t)
	m.manifest.Actions["deploy"] = manifest.Action{
		"kind":    "multi-target",
		"targets": map[string]any{"mac": "ship mac", "ios": "ship ios"},
	}
	m.manifest.Custom["deploy"] = manifest.Command{Desc: "ship it", Steps: []string{"deploy"}, Group: 1, Priority: 4}

	m.expandedCmd = "dev"
	deploy := cmdRow(t, m, "deploy")
	next, _ := m.activateCommand(deploy)
	if got := next.(Model).expandedCmd; got != "deploy" {
		t.Fatalf("expandedCmd = %q, want \"deploy\"", got)
	}
	for _, r := range next.(Model).workRows() {
		if r.kind == rowCommandTarget && r.work != nil && r.work.cmdName == "dev" {
			t.Fatal("dev stayed expanded while deploy opened")
		}
	}
}

// TestTargetRowDispatchesParentWithTargetArg checks the plumbing that makes
// a target row equivalent to `admin dev mac` — the arg the CLI picker would
// otherwise have to ask for.
func TestTargetRowDispatchesParentWithTargetArg(t *testing.T) {
	m := multiTargetModel(t)
	m.expandedCmd = "dev"
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
	exec := &cmdExecCommand{manifest: m.manifest, name: target.cmdName, args: []string{target.targetKey}}
	if len(exec.args) != 1 || exec.args[0] != "mac" {
		t.Fatalf("dispatch args = %v, want [mac]", exec.args)
	}
}

// TestToggleKeepsCursorOnTheRowThatWasPressed: collapsing a submenu above
// the cursor would otherwise slide a different command underneath it.
func TestToggleKeepsCursorOnTheRowThatWasPressed(t *testing.T) {
	m := multiTargetModel(t)
	m.manifest.Actions["deploy"] = manifest.Action{
		"kind":    "multi-target",
		"targets": map[string]any{"mac": "ship mac", "ios": "ship ios"},
	}
	m.manifest.Custom["deploy"] = manifest.Command{Desc: "ship it", Steps: []string{"deploy"}, Group: 1, Priority: 4}
	m.expandedCmd = "dev"

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
	cur := nm.workRows()[nm.cursor[nm.active]]
	if cur.kind != rowCommand || cur.work.cmdName != "deploy" {
		t.Fatalf("cursor landed on %v/%v, want the deploy command row", cur.kind, cur.work)
	}
}
