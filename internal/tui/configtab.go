package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// configLoadedMsg carries a fresh read of both config files — fired at launch
// and again once the editor closes, since only a re-read says whether an edit
// actually landed.
type configLoadedMsg struct {
	global, local       string
	globalErr, localErr string
}

// loadConfigTextCmd re-reads the global and per-repo config files for
// display. Config is a handful of lines at most, so reading it whole on every
// load is simplest and cheap — no diffing, no watching.
func loadConfigTextCmd(repoRoot string) tea.Cmd {
	return func() tea.Msg {
		g, gerr := readConfigFile(config.GlobalPath())
		l, lerr := readConfigFile(config.RepoPath(repoRoot))
		return configLoadedMsg{global: g, globalErr: gerr, local: l, localErr: lerr}
	}
}

func readConfigFile(path string) (text, errMsg string) {
	if path == "" {
		return "", ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "no file yet — ↵ to create"
		}
		return "", err.Error()
	}
	return string(b), ""
}

// editorDoneMsg reports that the suspended $EDITOR process has returned.
type editorDoneMsg struct{ err error }

// editConfigCmd suspends the TUI and opens $EDITOR (falling back to vi) on
// path, creating its parent directory first so a per-repo config that doesn't
// exist yet can still be opened and saved into place.
func editConfigCmd(path string) tea.Cmd {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func() tea.Msg { return editorDoneMsg{err: err} }
	}
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		editor = []string{"vi"}
	}
	c := exec.Command(editor[0], append(editor[1:], path)...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return editorDoneMsg{err: err} })
}

// openConfigEditorCmd opens whichever file the Config tab currently shows.
func (m Model) openConfigEditorCmd() tea.Cmd {
	path := config.GlobalPath()
	if m.cfgLocal {
		path = config.RepoPath(m.repoRoot)
	}
	return editConfigCmd(path)
}

// renderConfigBody draws the Config tab: a toggle between the global and
// per-repo file (⇥ switches it), and the raw text of whichever is selected.
func (m Model) renderConfigBody(width, height int) []string {
	path := config.GlobalPath()
	text, errMsg := m.cfgGlobalText, m.cfgGlobalErr
	if m.cfgLocal {
		path = config.RepoPath(m.repoRoot)
		text, errMsg = m.cfgLocalText, m.cfgLocalErr
	}

	lines := []string{
		renderSegs(m.configToggleSegs(), nil),
		mutedStyle.Render("  " + path),
		"",
	}
	switch {
	case errMsg != "":
		lines = append(lines, mutedStyle.Render("  "+errMsg))
	case strings.TrimSpace(text) == "":
		lines = append(lines, mutedStyle.Render("  (empty)"))
	default:
		for line := range strings.SplitSeq(text, "\n") {
			lines = append(lines, "  "+line)
		}
	}
	return pad(lines, height)
}

func (m Model) configToggleSegs() []seg {
	global := seg{t: " Global ", fg: cDim}
	local := seg{t: " Local ", fg: cDim}
	if m.cfgLocal {
		local = seg{t: " Local ", fg: cBg, bg: cBlue}
	} else {
		global = seg{t: " Global ", fg: cBg, bg: cBlue}
	}
	return []seg{{t: " "}, global, {t: " "}, local}
}
