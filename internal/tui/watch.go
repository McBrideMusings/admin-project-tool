package tui

import (
	"context"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// fsDebounce is how long the watcher waits for a quiet period before signaling a
// change, so an editor's multi-write save fires a single reload, not one per write.
const fsDebounce = 250 * time.Millisecond

// watcherReadyMsg carries the started watcher and its coalesced-signal channel
// (both nil if the watcher could not start, which just leaves live updates off).
// The model holds the *Watcher so it can Close it on quit.
type watcherReadyMsg struct {
	w  *fsnotify.Watcher
	ch <-chan struct{}
}

// fsChangedMsg means the working tree changed on disk since the last reload.
type fsChangedMsg struct{}

// startWatchCmd builds an fsnotify watcher over the directories that hold tracked
// files and starts a goroutine that coalesces event bursts into one signal per
// quiet period. Any failure yields a nil channel — the dashboard still works, it
// just won't live-update (the r key remains the manual refresh).
func startWatchCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		dirs, err := git.TrackedDirs(ctx)
		if err != nil || len(dirs) == 0 {
			return watcherReadyMsg{}
		}
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return watcherReadyMsg{}
		}
		// Best effort per dir (macOS kqueue uses one FD per dir, so some Adds can
		// fail on a huge tree). If not one dir could be watched, give up the watcher
		// rather than run a goroutine that will never fire — r still refreshes.
		added := 0
		for _, d := range dirs {
			if err := w.Add(d); err == nil {
				added++
			}
		}
		if added == 0 {
			_ = w.Close()
			return watcherReadyMsg{}
		}
		ch := make(chan struct{}, 1)
		go debounceLoop(w, ch)
		return watcherReadyMsg{w: w, ch: ch}
	}
}

// debounceLoop drains raw fsnotify events and emits one struct{} on ch after each
// quiet period. It also starts watching newly-created directories so changes in
// them are seen, and ignores anything under a .git directory.
func debounceLoop(w *fsnotify.Watcher, ch chan<- struct{}) {
	// Closing ch on exit (watcher Close on quit) unblocks the waiting waitForFSCmd
	// goroutine so it returns instead of leaking.
	defer close(ch)
	var timerC <-chan time.Time
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			// A CHMOD-only event is an attribute change (atime, xattr), not an edit:
			// something reading a tracked file on a loop emits one every few hundred
			// ms, which outruns the debounce and reloads git state continuously.
			if isGitPath(ev.Name) || ev.Op == fsnotify.Chmod {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					_ = w.Add(ev.Name)
				}
			}
			timerC = time.After(fsDebounce)
		case <-timerC:
			select {
			case ch <- struct{}{}:
			default: // a reload is already pending; drop this signal
			}
			timerC = nil
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
		}
	}
}

// waitForFSCmd blocks until the watcher signals a change, then yields a message.
// Update re-issues it after each event to keep listening. A nil channel disables it.
func waitForFSCmd(ch <-chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return fsChangedMsg{}
	}
}

// isGitPath reports whether p sits inside (or is) a .git directory. This is the
// runtime complement to git.TrackedDirs (which already excludes .git from the
// initial watch set): it keeps newly-created .git children from being watched.
func isGitPath(p string) bool {
	sep := string(os.PathSeparator)
	return strings.Contains(p, sep+".git"+sep) || strings.HasSuffix(p, sep+".git")
}
