// editmanifest.go implements the `o` hotkey: open the project's admin.toml in
// $EDITOR from any tab. The Config tab is a read-only readout of resolved
// values (configtab.go), so the one thing it cannot do is change one — this is
// the write path, and it is deliberately not scoped to that tab: the manifest
// is what every tab is rendering, so the key that edits it works wherever the
// question comes up.
package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// editorDoneMsg reports that the suspended $EDITOR process has returned.
type editorDoneMsg struct{ err error }

// openManifestCmd suspends the TUI and opens $EDITOR (falling back to vi) on
// repoRoot's admin.toml. The file is not pre-created: every editor writes a
// new file on save, and a repo that has no manifest should not gain an empty
// one just because the key was pressed.
func openManifestCmd(repoRoot string) tea.Cmd {
	c := editorCommand(repoRoot)
	if c == nil {
		return func() tea.Msg { return editorDoneMsg{err: os.ErrNotExist} }
	}
	return tea.ExecProcess(c, func(err error) tea.Msg { return editorDoneMsg{err: err} })
}

// editorCommand builds the $EDITOR invocation openManifestCmd suspends into,
// split out so the argv is testable without a running tea program. $EDITOR is
// split on spaces so a multi-word setting ("code --wait") keeps its flags; nil
// when there is no repo root to resolve the manifest against.
func editorCommand(repoRoot string) *exec.Cmd {
	if repoRoot == "" {
		return nil
	}
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		editor = []string{"vi"}
	}
	c := exec.Command(editor[0], append(editor[1:], filepath.Join(repoRoot, manifest.Filename))...)
	c.Dir = repoRoot
	return c
}

// loadManifest re-reads and re-resolves repoRoot's admin.toml into the fields
// every tab renders from. New() calls it for the first read and the editor
// return calls it again, since only a re-read says whether an edit landed —
// and an edit can change anything downstream of the manifest (the tracker, the
// PRs tab's presence, the Work tab's commands, a row's [keys] bindings), so
// all of it is rebuilt together rather than patched.
//
// Every field is assigned on every call, including the failure paths, so a
// manifest that was valid and is now broken clears the values it used to
// declare instead of leaving the last good ones on screen under a new error.
func (m *Model) loadManifest() {
	m.manifest, m.manifestOK, m.manifestErr = nil, false, ""
	m.cmdOrigin, m.actionOrigin, m.keyOrigin, m.rowKeys = nil, nil, nil, nil

	project, merged, sp, ok, err := loadWorkManifest(m.repoRoot)
	switch {
	case err != nil:
		m.manifestErr = err.Error()
	case ok:
		m.manifest = merged
		m.manifestOK = true
		m.cmdOrigin, m.actionOrigin, m.keyOrigin = computeOrigins(project, sp)
		m.rowKeys = buildRowKeys(merged)
	}
}
