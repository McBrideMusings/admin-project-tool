package kinds

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// directorySize is the Go port of admin_lib/cleanup.py::directory_size.
// Symlinks count as the size of the link itself, matching `du` without
// `-L`; an unreadable child during the walk contributes 0 rather than
// aborting the whole sweep.
func directorySize(path string) int64 {
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() {
		return info.Size()
	}
	if !info.IsDir() {
		return 0
	}
	var total int64
	_ = filepath.Walk(path, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		total += fi.Size()
		return nil
	})
	return total
}

// formatBytes is the Go port of admin_lib/cleanup.py::format_bytes — a
// short, 1024-based human-readable byte count.
func formatBytes(n int64) string {
	f := float64(n)
	for _, unit := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 {
			return fmt.Sprintf("%.1f%s", f, unit)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1fPB", f)
}

// nukePath is the Go port of admin_lib/cleanup.py::nuke_path: remove a
// file/dir/symlink, logging its size first, returning bytes freed. A
// missing path is a silent no-op; a removal failure is warned and
// swallowed, matching Python's "one stuck file shouldn't abort the sweep".
func nukePath(h *luahost.Host, path, label string) int64 {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	sz := directorySize(path)
	labelText := path
	if label != "" {
		labelText = label + ": " + path
	}
	h.Info("  " + labelText + " (" + formatBytes(sz) + ")")
	if fi.Mode()&os.ModeSymlink != 0 || fi.Mode().IsRegular() {
		err = os.Remove(path)
	} else {
		err = os.RemoveAll(path)
	}
	if err != nil {
		h.Warn("    failed: " + err.Error())
		return 0
	}
	return sz
}

// cleanPathEntry is one (path, label) pair for cleanPaths.
type cleanPathEntry struct {
	Path  string
	Label string
}

// cleanPaths is the Go port of admin_lib/cleanup.py::clean_paths: nukes each
// entry, skipping empty paths and duplicates (by absolute path), and sums
// bytes freed.
func cleanPaths(h *luahost.Host, entries []cleanPathEntry) int64 {
	seen := map[string]bool{}
	var total int64
	for _, e := range entries {
		if e.Path == "" {
			continue
		}
		ap, err := filepath.Abs(e.Path)
		if err != nil {
			ap = e.Path
		}
		if seen[ap] {
			continue
		}
		seen[ap] = true
		total += nukePath(h, e.Path, e.Label)
	}
	return total
}

// dbQuickCheck is the Go port of admin_lib/cleanup.py::db_quick_check —
// `PRAGMA quick_check` on a SQLite DB, via the `sqlite3` CLI rather than a
// Go SQLite driver: this repo already leans on platform tools it doesn't
// vendor a binding for (ditto, lsof), and shelling out here avoids adding a
// cgo or pure-Go SQLite dependency for one PRAGMA. If `sqlite3` isn't on
// PATH, that reads as "unreadable" — the same fail-closed status Python
// returns for a corrupt-DB open — so a missing CLI can never be
// misread as "healthy" and silently drop a good database.
func dbQuickCheck(dbPath string) (healthy bool, status string) {
	if dbPath == "" {
		return false, "missing"
	}
	if _, err := os.Stat(dbPath); err != nil {
		return false, "missing"
	}
	sqlite3Path, err := exec.LookPath("sqlite3")
	if err != nil {
		return false, "unreadable: sqlite3 not found on PATH"
	}
	out, err := exec.Command(sqlite3Path, dbPath, "PRAGMA quick_check;").CombinedOutput()
	if err != nil {
		return false, "CORRUPT: " + strings.TrimSpace(string(out))
	}
	result := strings.TrimSpace(string(out))
	if result == "ok" {
		return true, "healthy"
	}
	if result == "" {
		result = "<no result>"
	}
	return false, "CORRUPT: " + result
}

// cleanWithDB is the Go port of admin_lib/cleanup.py::clean_with_db: always
// clean `paths`; drop the SQLite DB (plus its -shm/-wal companions) only
// when `force` is set or the quick-check fails.
func cleanWithDB(h *luahost.Host, paths []cleanPathEntry, dbPath string, force bool) int64 {
	h.Info("clearing caches and transcode outputs:")
	freed := cleanPaths(h, paths)

	healthy, status := dbQuickCheck(dbPath)
	shouldDrop := force || (!healthy && dbPath != "" && fileExists(dbPath))

	if shouldDrop {
		reason := status
		if force {
			reason = "--force"
		}
		h.Info("dropping DB (" + reason + "):")
		freed += nukePath(h, dbPath, "DB main")
		for _, ext := range []string{"-shm", "-wal"} {
			freed += nukePath(h, dbPath+ext, "DB"+ext)
		}
	} else if dbPath != "" && fileExists(dbPath) {
		h.Info("DB: " + status + " — keeping (pass --force to drop)")
	}

	h.Ok("clean done — freed " + formatBytes(freed))
	return freed
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolvedPathEntry resolves one `[clean_dev].paths[]` entry — {env=,
// default=, label=} or {path=, label=} — into an absolute path, matching
// admin_lib/cleanup.py::clean_dev_from_config's inline loop. An entry that
// resolves to an empty base is dropped, same as Python's `if not base:
// continue`.
func resolvedPathEntry(raw any) (cleanPathEntry, bool) {
	entry, ok := raw.(map[string]any)
	if !ok {
		return cleanPathEntry{}, false
	}
	var base string
	if envName, ok := entry["env"].(string); ok {
		base = os.Getenv(envName)
		if base == "" {
			base = manifest.ResolveComputed(cfgString(entry, "default", ""))
		}
	} else {
		base = manifest.ResolveComputed(cfgString(entry, "path", ""))
	}
	if base == "" {
		return cleanPathEntry{}, false
	}
	if !filepath.IsAbs(base) {
		if abs, err := filepath.Abs(base); err == nil {
			base = abs
		}
	}
	return cleanPathEntry{Path: base, Label: cfgString(entry, "label", "")}, true
}

// resolvedDBPath resolves `[clean_dev].db` — {path=} or {env=, default=,
// file=} — into an absolute path, matching
// admin_lib/cleanup.py::clean_dev_from_config's db-path branch.
func resolvedDBPath(db map[string]any) string {
	if p, ok := db["path"].(string); ok {
		return manifest.ResolveComputed(p)
	}
	file, ok := db["file"].(string)
	if !ok {
		return ""
	}
	var base string
	if envName, ok := db["env"].(string); ok {
		base = os.Getenv(envName)
	}
	if base == "" {
		base = manifest.ResolveComputed(cfgString(db, "default", ""))
	}
	if base == "" {
		return file
	}
	if !filepath.IsAbs(base) {
		if abs, err := filepath.Abs(base); err == nil {
			base = abs
		}
	}
	return filepath.Join(base, file)
}

// cleanDevFromConfig is the Go port of
// admin_lib/cleanup.py::clean_dev_from_config: reads the [clean_dev] table
// and calls cleanWithDB.
func cleanDevFromConfig(h *luahost.Host, cfg map[string]any, force bool) int64 {
	if len(cfg) == 0 {
		return 0
	}
	var paths []cleanPathEntry
	for _, raw := range cfgList(cfg, "paths") {
		if e, ok := resolvedPathEntry(raw); ok {
			paths = append(paths, e)
		}
	}
	dbPath := resolvedDBPath(cfgMap(cfg, "db"))
	return cleanWithDB(h, paths, dbPath, force)
}

// makeCleanDev is kind="clean-dev"'s factory — the Go port of
// admin_lib/interp.py::_make_clean_dev. `--force`/`--db` on the command line
// force-drops the DB regardless of its health, matching Python's args
// check. `pre_kill` (default true) dispatches the manifest's "kill" action
// first, when one is declared — mirroring `_kill_command_exists()` +
// `run_command("kill", args)`; a manifest with no "kill" command/action
// (kind="kill-port" is not yet ported — admin-3rgp.15) simply skips this
// step rather than erroring, exactly as Python's guard does when "kill"
// isn't registered at all.
func makeCleanDev(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "clean_dev")
	preKill := cfgBool(cfg, "pre_kill", true)

	return func(args []string) error {
		h := newHost(m)
		if preKill {
			if _, ok := resolveCommand(m, "kill"); ok {
				if err := surf.Command("kill", args); err != nil {
					h.Warn("kill: " + err.Error())
				}
			} else if _, ok := m.Actions["kill"]; ok {
				if err := RunAction(m, Default, "kill", args); err != nil {
					h.Warn("kill: " + err.Error())
				}
			}
		}
		force := false
		for _, a := range args {
			if a == "--force" || a == "--db" {
				force = true
			}
		}
		cleanDevFromConfig(h, cfg, force)
		return nil
	}, nil
}
