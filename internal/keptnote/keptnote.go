// Package keptnote records why the worker reaper left a worktree on disk, for
// the reasons git cannot show again: a pre_remove veto (ADR-0024) or a failed
// `git worktree remove`. Reasons that are git state — uncommitted changes,
// unpushed commits, ignored files the primary checkout lacks — are never
// written here; the Repo tab reads those live.
//
// The note is one file in the worktree's own git dir
// (.git/worktrees/<name>/admin-kept.json), so git deletes it with the worktree
// and nothing outlives what it describes. It carries the HEAD it was written
// at, and Read ignores it once HEAD has moved: a commit, reset or checkout
// changed what the veto or failure judged. It reads as a past event — when the
// reaper left the worktree and why — never as a verdict on the worktree now.
package keptnote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fileName is the note's name inside the worktree's git dir.
const fileName = "admin-kept.json"

// Note is one recorded keep.
type Note struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
	Head   string    `json:"head"`
}

// gitDir is the linked worktree's private git dir, read from the `gitdir:`
// line of its .git file. A primary checkout (whose .git is a directory) has no
// private git dir and gets an error: the reaper never removes it.
func gitDir(worktree string) (string, error) {
	b, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return "", err
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return "", fmt.Errorf("%s/.git has no gitdir line", worktree)
	}
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(worktree, dir)
	}
	return dir, nil
}

// Write records reason against the worktree at the HEAD head, replacing any
// earlier note.
func Write(worktree, head, reason string) error {
	dir, err := gitDir(worktree)
	if err != nil {
		return err
	}
	b, err := json.Marshal(Note{Reason: reason, At: time.Now(), Head: head})
	if err != nil {
		return err
	}
	// A temp file of its own, so two admin processes reaping at once never
	// rename each other's half-written bytes into place.
	f, err := os.CreateTemp(dir, fileName+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, fileName))
}

// Clear removes the worktree's note, if it has one.
func Clear(worktree string) error {
	dir, err := gitDir(worktree)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, fileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Read returns the worktree's note when it was written at head, the worktree's
// HEAD now. A note written at another HEAD, or one that does not parse, is not
// returned. Read never deletes: its head can come from a snapshot older than a
// note just written, and the next Write or the worktree's removal replaces the
// file anyway. ok is false when there is no current note.
func Read(worktree, head string) (n Note, ok bool) {
	dir, err := gitDir(worktree)
	if err != nil {
		return Note{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return Note{}, false
	}
	if json.Unmarshal(b, &n) != nil || n.Head == "" || n.Head != head {
		return Note{}, false
	}
	return n, true
}
