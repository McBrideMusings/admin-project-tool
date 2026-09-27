// Package reload is the ONE place a rebuild request (SIGUSR1, or an 'r'
// keypress from a live terminal) and its PID-file bookkeeping live.
//
// This package holds two shapes on one piece of state, because dev
// commands come in two shapes:
//
//   - RunWithReload(cmd, target, ...) — this package owns the loop. For a
//     process that stays up on its own (a server, a bundler): it streams the
//     child's output and, on reload, tears the child down and relaunches it.
//     kind="docker-dev" and kind="docker-reload-loop" use this shape.
//
//   - SetupReload(target) + IsRebuildRequested()/ClearRebuildRequest() — the
//     CALLER owns the loop. For a build-install-launch cycle (the Apple dev
//     loops admin-3rgp.12 ports) where "reload" means running the whole cycle
//     again, not restarting one child.
//
// Keeping a separate rebuild-requested flag and SIGUSR1 handler per caller is
// exactly the duplication this repo's CLAUDE.md records as past pain. Both
// shapes live in this one package instead, so a future caller never has to
// choose between adopting this package and writing a second copy of its own.
//
// The `server-reload` kind finds a running dev loop through the PID records
// both shapes write under PIDPrefix (Signal).
package reload

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PIDPrefix is the base path for every PID record this package writes: one
// file per running loop, "<PIDPrefix>-<target>-<pid>.pid". The pid in the
// name keeps two projects that both run a `dev` target from overwriting each
// other's record.
const PIDPrefix = "/tmp/admin-run"

// Reporter is the small slice of luahost.Host's output surface this package
// needs (Warn/Ok/Err) — a structural interface rather than an import of
// internal/luahost, so this package stays free of the Lua VM / project-root /
// log-context machinery that lives there and has nothing to do with reload
// state. *luahost.Host already satisfies this with no adapter needed.
type Reporter interface {
	Warn(msg string)
	Ok(msg string)
	Err(msg string)
}

// Record is what one PID file holds: the loop's process, the target name it
// was started under, and which project and dashboard row it belongs to.
//
// Root is the project root. Live is the live-work id the dashboard keys the
// dispatching command's row on (internal/kinds.multiTargetLiveID) — the
// command name, or "<command>:<target>" — so a Work-row key binding can
// signal exactly the loop behind that row. Both come from SetOwner; Root
// falls back to the loop's own working directory when no owner was set.
type Record struct {
	PID    int    `json:"pid"`
	Target string `json:"target"`
	Root   string `json:"root,omitempty"`
	Live   string `json:"live,omitempty"`
}

var (
	ownerMu   sync.Mutex
	ownerRoot string
	ownerLive string
)

// SetOwner records which project and command this process is running, for
// every PID record it writes afterwards. The first call wins: a command's
// steps can dispatch further commands (`@`-refs), and the loop belongs to
// the command that was actually invoked, not a nested one.
func SetOwner(root, live string) {
	ownerMu.Lock()
	defer ownerMu.Unlock()
	if ownerRoot != "" || ownerLive != "" {
		return
	}
	ownerRoot, ownerLive = root, live
}

func owner() (root, live string) {
	ownerMu.Lock()
	defer ownerMu.Unlock()
	return ownerRoot, ownerLive
}

// writeRecord writes this process's Record for target under prefix and
// returns the path, for the caller to remove on exit. dir is the loop's
// working directory, used as Root when SetOwner never ran.
func writeRecord(prefix, target, dir string) (string, error) {
	root, live := owner()
	if root == "" {
		root = dir
	}
	rec := Record{PID: os.Getpid(), Target: target, Root: root, Live: live}
	path := fmt.Sprintf("%s-%s-%d.pid", prefix, target, rec.PID)
	return path, writeRecordFile(path, rec)
}

func writeRecordFile(path string, rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// removeRecord removes path if it exists — best-effort, errors swallowed.
func removeRecord(path string) {
	_ = os.Remove(path)
}

// readRecord reads and parses the Record stored at path.
func readRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("malformed pid file %s: %w", path, err)
	}
	if rec.PID <= 0 {
		return Record{}, fmt.Errorf("malformed pid file %s: no pid", path)
	}
	return rec, nil
}

// globPIDFiles returns every "<prefix>-*.pid" path.
func globPIDFiles(prefix string) ([]string, error) {
	return filepath.Glob(prefix + "-*.pid")
}

// MatchTarget matches the loops of the project at root whose target name
// contains filter — all of that project's loops when filter is empty. It is
// `admin reload [filter]`'s rule.
func MatchTarget(root, filter string) func(Record) bool {
	return func(rec Record) bool {
		return rec.Root == root && strings.Contains(rec.Target, filter)
	}
}

// MatchRow matches the loops behind one dashboard row: the project at root,
// and either the row's own live id or, for a command row, any of its
// targets' ("<live>:<target>").
func MatchRow(root, live string) func(Record) bool {
	return func(rec Record) bool {
		if rec.Root != root {
			return false
		}
		return rec.Live == live || strings.HasPrefix(rec.Live, live+":")
	}
}
