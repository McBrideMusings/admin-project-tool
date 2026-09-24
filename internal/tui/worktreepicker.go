// worktreepicker.go is admin-76tv's dashboard half: a key binding that lists
// the repo's worktrees (path + branch, from `git worktree list --porcelain`)
// and lets the user pick one to point the dashboard at — same picker/overlay
// shape selectionworkflow.go's workflow picker already uses (a boxPanel list,
// ↑/↓ or j/k to move, enter to pick, everything else including esc to
// cancel), reused rather than reinvented. The picked worktree's path shows in
// the header (view.go's headerLine) until another pick changes it or the
// dashboard restarts.
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// startWorktreePicker opens the picker over the repo's worktrees — m.local.wts,
// the same list the Cleanup pane and the Branches pane's worktree column
// already read, loaded once by loadLocalCmd's own `git.Worktrees(ctx)` call
// rather than shelling out to git again here: a key handler runs
// synchronously inside Update, and a picker opening is not worth a second
// blocking subprocess call when the answer is already on the model. A repo
// with only the one worktree still opens it — an empty-looking list of one
// row is itself the answer to "what worktrees does this repo have", not a
// reason to refuse the key the way the workflow picker refuses an empty
// selection.
func (m Model) startWorktreePicker() (tea.Model, tea.Cmd) {
	if !m.haveLocal {
		m.setStatus("worktree list not loaded yet", cYellow)
		return m, nil
	}
	list := m.local.wts
	if len(list) == 0 {
		m.setStatus("no worktrees found", cYellow)
		return m, nil
	}
	m.wtPickerOpen = true
	m.wtPickerOptions = list
	m.wtPickerCursor = 0
	// Land the cursor on whatever is already the target (or the main
	// worktree, before any pick has been made) so reopening the picker
	// doesn't reset the selection back to the top every time.
	current := m.wtTarget
	if current == "" {
		current = m.repoRoot
	}
	for i, w := range list {
		if git.SamePath(w.Path, current) {
			m.wtPickerCursor = i
			break
		}
	}
	return m, nil
}

// worktreePickerKey handles every key while the picker overlay is up: ↑/↓
// (or j/k) move, enter picks, everything else (esc included) cancels having
// changed nothing — identical contract to workflowPickerKey.
func (m Model) worktreePickerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.wtPickerCursor > 0 {
			m.wtPickerCursor--
		}
		return m, nil
	case "down", "j":
		if m.wtPickerCursor < len(m.wtPickerOptions)-1 {
			m.wtPickerCursor++
		}
		return m, nil
	case "enter":
		w := m.wtPickerOptions[m.wtPickerCursor]
		m.wtPickerOpen = false
		m.wtPickerOptions = nil
		return m.pickWorktreeTarget(w)
	default:
		m.wtPickerOpen = false
		m.wtPickerOptions = nil
		m.setStatus("cancelled", cDim)
		return m, nil
	}
}

// pickWorktreeTarget switches the dashboard's target to w: the header shows
// it from here on (headerLine), and the Work tab's admin.toml lookup moves
// with it via manifestRoot — FindManifestRoot walks up from w.Path exactly
// as cmd.dashboardManifestRoot does for the CLI's own -w flag, so the Work
// tab lists exactly the commands `admin -w <this worktree> <name>` would
// run. A target with no admin.toml of its own falls back to manifestDir()'s
// existing "" -> repoRoot rule; the CLI's own fallback to the primary
// checkout's admin.toml (cmd/manifest.go) is a process-level concern the
// dashboard — already running as one process rooted at repoRoot — has no
// equivalent gap for.
func (m Model) pickWorktreeTarget(w git.Worktree) (tea.Model, tea.Cmd) {
	m.wtTarget = w.Path
	m.wtTargetBranch = w.Branch
	m.manifestRoot = FindManifestRoot(w.Path)
	m.loadManifest()
	m.setStatus("target: "+w.Path, cGreen)
	return m, nil
}

// targetLabel is what the header shows for the current target: "" once
// nothing has been picked (headerLine then shows nothing extra — the
// dashboard's ordinary repoRoot-rooted behavior), otherwise the picked
// worktree's directory name plus its branch when known.
func (m Model) targetLabel() string {
	if m.wtTarget == "" {
		return ""
	}
	name := m.wtTarget
	if i := lastSlash(name); i >= 0 {
		name = name[i+1:]
	}
	if m.wtTargetBranch != "" {
		return fmt.Sprintf("%s (%s)", name, m.wtTargetBranch)
	}
	return name
}

// lastSlash finds the last path separator without pulling in path/filepath
// for a one-line basename — every worktree path this package renders is
// already the absolute, forward-slash form git and os.Getwd() both use on
// every platform this dashboard ships for.
func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// renderWorktreePickerBody draws the picker as a centered box, the same
// layout math renderWorkflowPickerBody uses.
func (m Model) renderWorktreePickerBody(width, height int) []string {
	lines := make([][]seg, len(m.wtPickerOptions))
	for i, w := range m.wtPickerOptions {
		branch := w.Branch
		if branch == "" {
			branch = "(detached)"
		}
		lines[i] = []seg{{t: " " + w.Path, fg: cFg}, {t: "  " + branch, fg: cFaint}}
	}

	boxW := clampi(30, width-8, 96)
	if boxW > width {
		boxW = width
	}
	boxH := clampi(3, len(lines)+2, height)

	box := boxPanel{
		title: "Switch target worktree",
		width: boxW, height: boxH, focused: true,
		lines: lines, active: m.wtPickerCursor,
		footer: []seg{{t: "enter pick", fg: cCyan}, {t: "   esc cancel", fg: cFaint}},
	}
	rendered := box.render()

	lp := padN(clampi(0, (width-boxW)/2, width))
	top := clampi(0, (height-boxH)/2, height)
	out := make([]string, height)
	for i := top; i < height && i-top < len(rendered); i++ {
		out[i] = lp + rendered[i-top]
	}
	return out
}
