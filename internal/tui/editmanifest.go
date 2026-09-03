// editmanifest.go implements the `o` hotkey: open the project's admin.toml in
// the desktop's default handler for its type, from any tab. The Config tab is
// a read-only readout of resolved values (configtab.go), so the one thing it
// cannot do is change one — this is the write path, and it is deliberately
// not scoped to that tab: the manifest is what every tab is rendering, so the
// key that edits it works wherever the question comes up.
package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/timing"
)

// openManifestMsg reports that the launch of the default handler for a path
// either started (err is nil) or failed (err names why); name is the handler
// binary and target is a human label for what was opened (the manifest, a
// key file, a directory) so the status line can say what did or didn't run.
type openManifestMsg struct {
	name   string
	target string
	err    error
}

// openManifestCmd launches the platform's default handler for repoRoot's
// admin.toml and returns immediately — nothing suspends the TUI, since the
// desktop owns the window that opens. The file is not pre-created: whatever
// app opens it writes a new file on save, and a repo that has no manifest
// should not gain an empty one just because the key was pressed.
func openManifestCmd(repoRoot string) tea.Cmd {
	if repoRoot == "" {
		return func() tea.Msg { return openManifestMsg{err: errors.New("no repo root")} }
	}
	return openPathCmd(filepath.Join(repoRoot, manifest.Filename), manifest.Filename)
}

// openPathCmd launches the platform's default handler for an arbitrary path
// (a file or a directory) and returns immediately — the same fire-and-forget
// shape as openManifestCmd, generalized so the Config tab's rows (a key file,
// the worktree root, a populate pattern) can reuse it. label is what the
// status line names as opened.
func openPathCmd(path, label string) tea.Cmd {
	c := openPathCommand(path)
	if c == nil {
		return func() tea.Msg { return openManifestMsg{target: label, err: errors.New("no path")} }
	}
	name := c.Args[0]
	return func() tea.Msg {
		if err := c.Start(); err != nil {
			return openManifestMsg{name: name, target: label, err: err}
		}
		go func() { _ = c.Wait() }()
		return openManifestMsg{name: name, target: label}
	}
}

// openPathCommand builds the default-handler invocation openPathCmd starts,
// split out so the argv is testable without actually launching anything: nil
// when there is no path to open. `open` on darwin, `xdg-open` everywhere
// else — the same runtime.GOOS switch shape as openURLCmd (helpers.go) and
// copyClipboardCmd (commands.go). Works on directories as well as files —
// `open`/`xdg-open` both hand a directory to the desktop's file manager.
func openPathCommand(path string) *exec.Cmd {
	if path == "" {
		return nil
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	c := exec.Command(name, path)
	c.Dir = filepath.Dir(path)
	return c
}

// manifestStamp is a cheap fingerprint of admin.toml's on-disk state, used to
// tell a real edit to the manifest apart from every other reason the
// fsnotify watcher fires (a source-file save, a build writing into the tree,
// a branch switch). The zero value means "no manifest" and compares unequal
// to any real file, so create/delete of admin.toml is itself a change with
// no separate ok flag needed.
type manifestStamp struct {
	mod  time.Time
	size int64
}

// statManifest fingerprints repoRoot's admin.toml. It returns the zero
// manifestStamp when repoRoot is empty or the file can't be stat'd (missing,
// permission error) — a legitimate state, not an error to propagate, since
// the caller only ever compares stamps for equality.
func statManifest(repoRoot string) manifestStamp {
	if repoRoot == "" {
		return manifestStamp{}
	}
	fi, err := os.Stat(filepath.Join(repoRoot, manifest.Filename))
	if err != nil {
		return manifestStamp{}
	}
	return manifestStamp{mod: fi.ModTime(), size: fi.Size()}
}

// loadManifest re-reads and re-resolves repoRoot's admin.toml into the fields
// every tab renders from. New() calls it for the first read and the fsnotify
// watcher's fsChangedMsg calls it again on every external save — nothing
// suspends any more, so there is no editor-exit signal to reload on, and a
// re-read is the only way to tell whether an edit landed. An edit can change
// anything downstream of the manifest (the tracker, the PRs tab's presence,
// the Work tab's commands, a row's [keys] bindings), so all of it is rebuilt
// together rather than patched.
//
// Every field is assigned on every call, including the failure paths, so a
// manifest that was valid and is now broken clears the values it used to
// declare instead of leaving the last good ones on screen under a new error.
func (m *Model) loadManifest() {
	start := timing.Start()
	defer timing.Phase("tui manifest load", start)

	m.manifest, m.manifestOK, m.manifestErr = nil, false, ""
	m.cmdOrigin, m.actionOrigin, m.keyOrigin, m.rowKeys = nil, nil, nil, nil

	// Stat before the read: if the file is rewritten between the stat and
	// the read below, this stamp is already stale against the file on disk,
	// so the NEXT fsChangedMsg still sees a difference and reloads again.
	// Statting after the read would instead record the newer file against
	// this (older) parse and permanently skip the reload that should follow.
	m.manifestStamp = statManifest(m.manifestDir())

	project, merged, sp, ok, err := loadWorkManifest(m.manifestDir())
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
