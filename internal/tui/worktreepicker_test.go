package tui

import (
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// TestStartWorktreePickerListsWorktrees is admin-76tv's acceptance criterion
// for the dashboard half: `T` opens the picker listing the repo's worktrees
// — seededModel's own m.local.wts fixture — path and branch both present.
func TestStartWorktreePickerListsWorktrees(t *testing.T) {
	m := seededModel()

	got, _ := m.startWorktreePicker()
	mm := got.(Model)
	if !mm.wtPickerOpen {
		t.Fatalf("wtPickerOpen = false, want the picker open")
	}
	if len(mm.wtPickerOptions) != 1 || mm.wtPickerOptions[0].Path != "/repo/.worktrees/wt-branch" {
		t.Fatalf("wtPickerOptions = %+v, want exactly the fixture's one worktree", mm.wtPickerOptions)
	}
}

// TestStartWorktreePickerRefusesWithNoWorktrees mirrors
// TestStartWorkflowPickerRefusesEmptySelection's shape: nothing to pick from
// is a status line, not an empty overlay.
func TestStartWorktreePickerRefusesWithNoWorktrees(t *testing.T) {
	m := seededModel()
	m.local.wts = nil

	got, _ := m.startWorktreePicker()
	mm := got.(Model)
	if mm.wtPickerOpen {
		t.Fatalf("wtPickerOpen = true, want it to stay closed with no worktrees")
	}
	if mm.status == "" {
		t.Errorf("status is empty, want a message explaining the refusal")
	}
}

// TestWorktreePickerKeyMovesCursor mirrors
// TestWorkflowPickerKeyMovesCursor: up/down (or j/k) move wtPickerCursor,
// clamped at both ends, without closing the overlay.
func TestWorktreePickerKeyMovesCursor(t *testing.T) {
	m := Model{wtPickerOpen: true, wtPickerOptions: []git.Worktree{{Path: "/a"}, {Path: "/b"}, {Path: "/c"}}}

	got, _ := m.worktreePickerKey("down")
	m = got.(Model)
	if m.wtPickerCursor != 1 {
		t.Fatalf("wtPickerCursor = %d after down, want 1", m.wtPickerCursor)
	}
	got, _ = m.worktreePickerKey("down")
	m = got.(Model)
	got, _ = m.worktreePickerKey("down") // one past the end must clamp, not wrap
	m = got.(Model)
	if m.wtPickerCursor != 2 {
		t.Fatalf("wtPickerCursor = %d past the end, want clamped to 2", m.wtPickerCursor)
	}
	got, _ = m.worktreePickerKey("up")
	m = got.(Model)
	if m.wtPickerCursor != 1 {
		t.Fatalf("wtPickerCursor = %d after up, want 1", m.wtPickerCursor)
	}
	if !m.wtPickerOpen {
		t.Errorf("wtPickerOpen closed by plain cursor movement")
	}
}

// TestWorktreePickerKeyEnterSwitchesTarget is the dashboard's own acceptance
// criterion: enter on a candidate switches the model's target, and the
// header's targetLabel then names it — see view.go's headerLine.
func TestWorktreePickerKeyEnterSwitchesTarget(t *testing.T) {
	m := seededModel()
	got, _ := m.startWorktreePicker()
	m = got.(Model)

	got, _ = m.worktreePickerKey("enter")
	m = got.(Model)

	if m.wtPickerOpen {
		t.Errorf("wtPickerOpen = true, want the picker closed after a pick")
	}
	if m.wtTarget != "/repo/.worktrees/wt-branch" {
		t.Errorf("wtTarget = %q, want the picked worktree's path", m.wtTarget)
	}
	if m.wtTargetBranch != "wt-branch" {
		t.Errorf("wtTargetBranch = %q, want %q", m.wtTargetBranch, "wt-branch")
	}
	if label := m.targetLabel(); label != "wt-branch (wt-branch)" {
		t.Errorf("targetLabel() = %q, want %q", label, "wt-branch (wt-branch)")
	}
}

// TestWorktreePickerKeyEscCancelsWithoutSwitching mirrors
// TestWorkflowPickerKeyEscCancelsWithoutOpeningConfirm: esc (or any
// non-enter key) closes the picker having changed nothing.
func TestWorktreePickerKeyEscCancelsWithoutSwitching(t *testing.T) {
	m := seededModel()
	got, _ := m.startWorktreePicker()
	m = got.(Model)

	got, _ = m.worktreePickerKey("esc")
	m = got.(Model)

	if m.wtPickerOpen {
		t.Errorf("wtPickerOpen = true, want closed after esc")
	}
	if m.wtTarget != "" {
		t.Errorf("wtTarget = %q, want empty — esc must not switch the target", m.wtTarget)
	}
}

// TestTargetLabelEmptyUntilPicked proves the header shows nothing extra
// before any pick is made — the dashboard's original behavior for every
// project that never presses `T`.
func TestTargetLabelEmptyUntilPicked(t *testing.T) {
	m := seededModel()
	if label := m.targetLabel(); label != "" {
		t.Errorf("targetLabel() = %q, want empty before any pick", label)
	}
}
