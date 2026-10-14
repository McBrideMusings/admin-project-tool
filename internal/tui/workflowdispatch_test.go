package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// stubDispatchWorkflow swaps runDispatchWorkflow for the duration of one
// test, recording exactly what it was called with — this is what proves a
// dispatch ran over exactly the intended targets without a single real `git
// worktree add`.
func stubDispatchWorkflow(t *testing.T, results []kinds.TargetResult) *[]kinds.Target {
	t.Helper()
	var got []kinds.Target
	orig := runDispatchWorkflow
	runDispatchWorkflow = func(repoRoot, name string, action manifest.Action, targets []kinds.Target, _ kinds.DispatchSurface) []kinds.TargetResult {
		got = targets
		return results
	}
	t.Cleanup(func() { runDispatchWorkflow = orig })
	return &got
}

// queryWorkflowModel is a Work-tab model declaring one query workflow,
// "retire", over label:stale, with beads already loaded.
func queryWorkflowModel(beads []bd.Bead) Model {
	m := workTestModel(&manifest.Manifest{
		Custom: map[string]manifest.Command{"retire": {Desc: "Retire stale beads", Steps: []string{"retire"}}},
		Actions: map[string]manifest.Action{"retire": {
			"kind": "workflow",
			"over": map[string]any{"query": "label:stale"},
			"run":  `claude "/retire $ADMIN_TARGET"`,
		}},
	})
	m.feeds.beads.have = true
	m.feeds.beads.beads = beads
	return m
}

// TestQueryWorkflowListsEachMatchedItemAsATarget is the frame
// commands-workflow-selected.txt:8-12: the workflow is a group row the cursor
// skips, with one target row per matched tracker item beneath it.
func TestQueryWorkflowListsEachMatchedItemAsATarget(t *testing.T) {
	m := queryWorkflowModel([]bd.Bead{
		{ID: "a-1", Labels: []string{"stale"}},
		{ID: "a-2"},
		{ID: "a-3", Labels: []string{"stale"}},
	})
	rows := m.commandsTab.rows(&m.shell)

	var got []string
	for _, r := range rows {
		switch r.kind {
		case rowCommand:
			if r.selectable() {
				t.Errorf("group row %q is selectable, want the cursor to skip it", r.label)
			}
		case rowCommandTarget:
			got = append(got, r.work.targetKey+"|"+r.work.targetDesc)
		}
	}
	want := []string{"a-1|dispatch claude over a-1", "a-3|dispatch claude over a-3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("target rows = %v, want %v", got, want)
	}
	if hdr := rows[0].rightSegs[0].t; strings.TrimSpace(hdr) != "2" {
		t.Errorf("Commands count = %q, want 2 (one per matched item)", hdr)
	}
}

// TestQueryWorkflowWithNoMatchSaysSo proves a query matching nothing draws
// one unselectable line under the group naming the query, not a bare header.
func TestQueryWorkflowWithNoMatchSaysSo(t *testing.T) {
	m := queryWorkflowModel([]bd.Bead{{ID: "a-1"}})
	rows := m.commandsTab.rows(&m.shell)
	last := rows[len(rows)-1]
	if last.kind != rowText || last.label != "  └ no items match label:stale" {
		t.Errorf("last row = %v %q, want the rowText %q", last.kind, last.label, "  └ no items match label:stale")
	}
}

// TestCollapsedCursorPassesAQueryWorkflowWithNoMatch proves a collapsed
// list's cursor moves straight past a query group that matched nothing — it
// has no target to land on — instead of spending the keypress opening it.
func TestCollapsedCursorPassesAQueryWorkflowWithNoMatch(t *testing.T) {
	m := queryWorkflowModel([]bd.Bead{{ID: "a-1"}})
	m.manifest.Custom["aaa"] = manifest.Command{Steps: []string{"aaa"}}
	m.manifest.Custom["zzz"] = manifest.Command{Steps: []string{"zzz"}}
	m.manifest.Actions["aaa"] = manifest.Action{"kind": "shell"}
	m.manifest.Actions["zzz"] = manifest.Action{"kind": "shell"}
	m.active = tabWork
	m.commandsTab.collapsed = true
	m.commandsTab.openCmd = "aaa"
	m.commandsTab.cursor = indexOfCommandRow(m.visibleRows(), "aaa")

	m.commandsTab.moveCursorWork(&m.shell, 1)
	r := m.visibleRows()[m.commandsTab.cursor]
	if r.work == nil || r.work.cmdName != "zzz" {
		t.Errorf("cursor landed on %q, want zzz", r.label)
	}
}

// TestEnterOnQueryWorkflowTargetDispatchesThatItem proves ↵ on one matched
// item dispatches an agent over that item alone.
func TestEnterOnQueryWorkflowTargetDispatchesThatItem(t *testing.T) {
	got := stubDispatchWorkflow(t, []kinds.TargetResult{{Target: "a-3"}})
	m := queryWorkflowModel([]bd.Bead{
		{ID: "a-1", Labels: []string{"stale"}},
		{ID: "a-3", Labels: []string{"stale"}},
	})
	var wd *workRowData
	for _, r := range m.commandsTab.rows(&m.shell) {
		if r.kind == rowCommandTarget && r.work.targetKey == "a-3" {
			wd = r.work
		}
	}
	if wd == nil {
		t.Fatal("no target row for a-3")
	}

	cmd := m.commandsTab.activateCommandTarget(&m.shell, wd)
	updated := m
	if cmd == nil {
		t.Fatal("activateCommandTarget returned a nil Cmd, want a dispatch")
	}
	if _, ok := cmd().(workflowDispatchDoneMsg); !ok {
		t.Fatal("dispatch Cmd did not produce a workflowDispatchDoneMsg")
	}
	if len(*got) != 1 || (*got)[0].Name != "a-3" {
		t.Errorf("dispatched %v, want exactly a-3", *got)
	}
	if s := updated.status; !strings.Contains(s, "retire") || !strings.Contains(s, "a-3") {
		t.Errorf("status = %q, want it to name the command and the item", s)
	}
}

// TestWorkflowDispatchDoneReportsFailures proves a mix of ok/failed target
// results is distinguished on the status line rather than reported as one
// blanket outcome.
func TestWorkflowDispatchDoneReportsFailures(t *testing.T) {
	m := &Model{}
	m.workflowDispatchDone(workflowDispatchDoneMsg{
		name: "retire-merged",
		results: []kinds.TargetResult{
			{Target: "a-1"},
			{Target: "a-2", Err: errors.New("boom")},
		},
	})
	if !strings.Contains(m.status, "1 finished") || !strings.Contains(m.status, "1 failed") {
		t.Errorf("status = %q, want it to report 1 finished and 1 failed", m.status)
	}
}
