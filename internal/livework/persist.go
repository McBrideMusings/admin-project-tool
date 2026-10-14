// persist.go shares launcher-dispatched entries between admin processes. A
// window worker or detached action runs in a launcher pane that outlives the
// admin process that opened it, so that process's in-memory registry is not
// enough: `admin worktree <input> -x claude` exits the moment its pane is up,
// and the dashboard has to learn about the worker from somewhere.
//
// Each such entry is one JSON file, <dir>/<repo-key>/<id>.json, written to a
// temp file in the same directory and renamed over the old one so a reader
// never sees half a file. The file exists exactly while the entry is live:
// SetState to exited or failed removes it, so a file that is gone means the
// entry is over, and no later update from any process brings it back. Every
// write and remove holds the repo dir's .lock (lockRepoDir). A hosted-pty
// entry (Via empty) is never written — its pty dies with the process that
// owns it, so no other process could reach it.
//
// Ops cannot be written down. A process that loads an entry another process
// wrote rebuilds them through the builder SetOpsBuilder installed
// (internal/kinds, from the entry's own repo root and handle).
package livework

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// record is an Entry's on-disk form: everything except the in-process parts
// (Ops, the hosted pty's Output and Screen, the polled Activity and Snapshot).
type record struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	Handle    string      `json:"handle"`
	Via       string      `json:"via"`
	Root      string      `json:"root"`
	StartedAt time.Time   `json:"started_at"`
	State     State       `json:"state"`
	Note      string      `json:"note,omitempty"`
	Worker    *WorkerInfo `json:"worker,omitempty"`
	LogPath   string      `json:"log_path,omitempty"`
}

func toRecord(e *Entry) record {
	return record{
		ID: e.ID, Name: e.Name, Kind: e.Kind, Handle: e.Handle, Via: e.Via, Root: e.Root,
		StartedAt: e.StartedAt, State: e.state, Note: e.note, Worker: e.Worker, LogPath: e.logPath,
	}
}

func (rec record) entry() *Entry {
	return &Entry{
		ID: rec.ID, Name: rec.Name, Kind: rec.Kind, Handle: rec.Handle, Via: rec.Via, Root: rec.Root,
		StartedAt: rec.StartedAt, state: rec.State, note: rec.Note, Worker: rec.Worker, logPath: rec.LogPath,
	}
}

// live reports whether s is a state a shared file holds: running or waiting.
// The set is closed, so a state this build does not know — one a file written
// by an older build carries — is over, never live.
func (s State) live() bool {
	return s == StateRunning || s == StateWaiting
}

// DefaultStateDir is where Default keeps its entries: ~/.admin/state/livework.
// "" when the home directory cannot be found, which leaves Default in-memory.
func DefaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".admin", "state", "livework")
}

// repoKey names root's directory under the state dir: the root's base name,
// for a person reading the directory, and a hash of the cleaned full path, so
// two checkouts with the same name stay apart.
func repoKey(root string) string {
	root = filepath.Clean(root)
	sum := sha256.Sum256([]byte(root))
	return filepath.Base(root) + "-" + hex.EncodeToString(sum[:])[:12]
}

// RepoDir is the directory holding root's shared entries in r, "" when r
// keeps nothing on disk.
func (r *Registry) RepoDir(root string) string {
	if r.dir == "" || root == "" {
		return ""
	}
	return filepath.Join(r.dir, repoKey(root))
}

func (r *Registry) entryPath(e *Entry) string {
	return filepath.Join(r.RepoDir(e.Root), e.ID+".json")
}

// shared reports whether e belongs on disk in r: r keeps a store, e names the
// repo it belongs to, and e lives in a launcher pane rather than this
// process's own pty.
func (r *Registry) shared(e *Entry) bool {
	return r.dir != "" && e.Root != "" && e.Via != ""
}

// opsBuilder rebuilds a loaded entry's Ops. See SetOpsBuilder.
var opsBuilder func(e *Entry) Ops

// SetOpsBuilder installs the function that gives an entry loaded from disk its
// attach and kill. internal/kinds installs it at init, since only it knows how
// to reach a project's [terminal] launcher.
func SetOpsBuilder(build func(e *Entry) Ops) {
	opsBuilder = build
}

// errClaimed reports that claimLocked found another process's live file.
type errClaimed struct{ rec record }

func (e errClaimed) Error() string {
	return fmt.Sprintf("live-work: entry %q is already %s in another admin process (handle %s)", e.rec.ID, e.rec.State, e.rec.Handle)
}

// claimLocked writes the file of e, an entry being added. The file is
// created exclusively, so of two processes adding one id at once exactly one
// wins; a file already there is replaced only when it holds no live entry.
// Called with r.mu held.
func (r *Registry) claimLocked(e *Entry) error {
	if !r.shared(e) {
		return nil
	}
	path := r.entryPath(e)
	unlock, err := lockRepoDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("livework: locking %s: %w", filepath.Dir(path), err)
	}
	defer unlock()
	err = writeFile(path, toRecord(e), true)
	if os.IsExist(err) {
		if rec, rerr := readRecord(path); rerr == nil && rec.State.live() {
			return errClaimed{rec}
		}
		err = writeFile(path, toRecord(e), false)
	}
	if err != nil {
		return fmt.Errorf("livework: writing %s: %w", path, err)
	}
	e.onDisk = true
	return nil
}

// writeLocked rewrites the file of e, an entry already added, or removes it
// once e is over. Called with r.mu held, so two writers in one process never
// interleave on one file, and under the repo dir's lock, so the check of what
// the file holds and the write that follows are one step for every process.
// Only a file holding e's own handle is rewritten or removed; a file that is
// gone after e was written is left gone. Either way e's change still applies
// in this process, and the next Sync reads a gone file as exited.
func (r *Registry) writeLocked(e *Entry) error {
	if !r.shared(e) {
		return nil
	}
	path := r.entryPath(e)
	unlock, err := lockRepoDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("livework: locking %s: %w", filepath.Dir(path), err)
	}
	defer unlock()
	rec, err := readRecord(path)
	exists := !os.IsNotExist(err)
	ours := err == nil && rec.Handle == e.Handle
	if !e.state.live() {
		if ours {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("livework: removing %s: %w", path, err)
			}
		}
		return nil
	}
	if !ours && (exists || e.onDisk) {
		return nil
	}
	if err := writeFile(path, toRecord(e), false); err != nil {
		return fmt.Errorf("livework: writing %s: %w", path, err)
	}
	e.onDisk = true
	return nil
}

// updateLocked is writeLocked for a change to an entry already added. The
// entry keeps working in this process when the write fails, so the failure is
// kept for the next Sync to report rather than returned to a setter no caller
// checks. Called with r.mu held.
func (r *Registry) updateLocked(e *Entry) {
	if err := r.writeLocked(e); err != nil {
		r.writeErrs = append(r.writeErrs, err)
	}
}

// lockRepoDir takes an exclusive flock on dir/.lock, serialising every
// create, rewrite and remove of dir's entry files across admin processes.
// Readers need no lock: a rename is already atomic for them. The returned
// func releases it.
func lockRepoDir(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// writeFile writes rec as JSON to path through a temp file in the same
// directory, so a reader never sees half a file. exclusive links the temp
// file into place, failing with an IsExist error when path already exists;
// otherwise it is renamed over whatever is there. Not synced: the file only
// has to outlive the process that wrote it, not the machine.
func writeFile(path string, rec record, exclusive bool) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if exclusive {
		return os.Link(tmp.Name(), path)
	}
	return os.Rename(tmp.Name(), path)
}

// readRecord reads one entry file. A file that is not a well-formed record is
// an error naming the file.
func readRecord(path string) (record, error) {
	var rec record
	data, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("livework: malformed entry file %s: %w", path, err)
	}
	if rec.ID == "" || rec.Handle == "" {
		return rec, fmt.Errorf("livework: malformed entry file %s: missing id or handle", path)
	}
	return rec, nil
}

// Sync brings r up to date with root's shared entries on disk. An entry
// another process added appears, with Ops rebuilt; an entry whose state or
// note another process changed takes the new values; a live entry of root's
// whose file is gone was ended by another process and reads exited. Sync
// never replaces an entry still live in this process, and never revives one
// this process saw end on the same handle. A malformed file is skipped and
// named in the returned error the first time Sync meets it; every other file
// still loads. A file holding a state that is not live is removed and named
// in the error (removeOverFile). The error also carries every write this registry failed since
// the last Sync.
func (r *Registry) Sync(root string) error {
	dir := r.RepoDir(root)
	if dir == "" {
		return nil
	}
	// Only an entry already on disk before the directory is read can be judged
	// gone by that read: one added meanwhile is missing from it only because
	// it is newer.
	r.mu.Lock()
	before := map[string]*Entry{}
	known := map[string]string{}
	for id, e := range r.entries {
		known[id] = e.Handle
		if e.Root == root && e.onDisk && e.state.live() {
			before[id] = e
		}
	}
	r.mu.Unlock()

	names, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("livework: reading %s: %w", dir, err)
	}
	var errs []error
	onDisk := map[string]record{}
	for _, de := range names {
		name := de.Name()
		if de.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		rec, err := readRecord(path)
		if err != nil {
			if !os.IsNotExist(err) && r.firstReport(path) { // IsNotExist: removed since ReadDir
				errs = append(errs, err)
			}
			continue
		}
		if !rec.State.live() {
			if err := removeOverFile(path); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		onDisk[rec.ID] = rec
	}
	// Rebuilding Ops loads the repo's config, so it happens outside the lock.
	fresh := map[string]*Entry{}
	for id, rec := range onDisk {
		if h, ok := known[id]; rec.State.live() && (!ok || h != rec.Handle) {
			e := rec.entry()
			e.onDisk = true
			if opsBuilder != nil {
				e.Ops = opsBuilder(e)
			}
			fresh[id] = e
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	errs = append(errs, r.writeErrs...)
	r.writeErrs = nil
	for id, e := range before {
		if _, ok := onDisk[id]; !ok && r.entries[id] == e && e.state.live() {
			e.state = StateExited
		}
	}
	for id, rec := range onDisk {
		if !rec.State.live() {
			continue
		}
		old, exists := r.entries[id]
		if exists && old.Handle == rec.Handle {
			// The same pane. Exited here means this process ended it and its
			// file outlived it; it stays ended.
			if old.state.live() && old.onDisk {
				old.state, old.note = rec.State, rec.Note
				if rec.LogPath != "" {
					old.logPath = rec.LogPath
				}
			}
			continue
		}
		if exists && old.state.live() {
			continue // this process's own live entry under that id
		}
		e := fresh[id]
		if e == nil {
			continue // appeared after the snapshot; the next Sync loads it
		}
		if !exists {
			r.order = append(r.order, id)
		}
		r.entries[id] = e
	}
	return errors.Join(errs...)
}

// removeOverFile removes the entry file at path when, under the repo dir's
// lock, it still holds a state that is not live. Nothing this build writes
// holds one, so such a file is left by an older build whose states this one
// dropped, and it would otherwise refuse every Add of its id. A removal comes
// back as an error naming the file, so Sync reports it; the file is gone
// after, so it is reported once.
func removeOverFile(path string) error {
	unlock, err := lockRepoDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("livework: locking %s: %w", filepath.Dir(path), err)
	}
	defer unlock()
	rec, err := readRecord(path)
	if err != nil || rec.State.live() {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("livework: removing %s (state %q): %w", path, rec.State, err)
	}
	return fmt.Errorf("livework: removed %s: state %q is not running or waiting", path, rec.State)
}

// firstReport reports whether Sync has not yet named path as malformed, and
// records that it now has, so a file that stays broken is reported once.
func (r *Registry) firstReport(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reported == nil {
		r.reported = map[string]bool{}
	}
	if r.reported[path] {
		return false
	}
	r.reported[path] = true
	return true
}
