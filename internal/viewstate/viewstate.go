// Package viewstate persists the dashboard's four view toggles — Work tab
// collapse, Beads list shape, tracker sort, Branches mine-only — to a
// per-checkout .admin/view.toml, read through a cascade (admin-y5lc.1):
//
//  1. <project>/.admin/view.toml — the checkout whose admin.toml the
//     dashboard is reading.
//  2. <main checkout>/.admin/view.toml — only when the project is a linked
//     worktree; skipped for an ordinary checkout and for any project outside
//     a git repo at all.
//  3. ~/.admin/view.toml — hand-edited global defaults. The tool never
//     writes this file.
//  4. Built-in defaults — today's zero values.
//
// The cascade runs per key, not per file: a key one level leaves unset (or
// sets to something unrecognized) falls through to the next level, while a
// sibling key the same level does hold still wins there. A toggle only ever
// writes level 1, and only the one key that changed — values read from
// levels 2-4 are never copied down into it.
package viewstate

import (
	"context"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// Filename is the view-toggle file's name inside a checkout's .admin/ dir.
const Filename = "view.toml"

// State is the resolved set of view toggles, one concrete value per key
// regardless of which cascade level supplied it. The zero value is today's
// built-in defaults (level 4): BeadTree and TrackSort empty mean "use the
// enum's own zero value" to whoever maps these strings back onto tui's
// beadTreeMode/trackSort types.
type State struct {
	WorkCollapsed bool
	// BeadTree is "flat", "blocks" or "family".
	BeadTree string
	// TrackSort is "last-modified", "priority", "oldest-first",
	// "newest-first", "most-deps" or "by-type".
	TrackSort string
	MinesOnly bool
}

var allowedBeadTree = map[string]bool{"flat": true, "blocks": true, "family": true}

var allowedTrackSort = map[string]bool{
	"last-modified": true,
	"priority":      true,
	"oldest-first":  true,
	"newest-first":  true,
	"most-deps":     true,
	"by-type":       true,
}

// GlobalPath resolves the hand-edited global defaults file — level 3 of the
// cascade. It is a package-level var, the same seam
// archetype.InstalledArchetypesDir uses, so a test can point it at a
// hermetic temp path instead of inheriting whatever ~/.admin/view.toml
// exists on the machine running the test; production code should never call
// os.UserHomeDir for this file directly.
var GlobalPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".admin", Filename), nil
}

// UseGlobalPath points GlobalPath at a fixed path for the duration of a test
// and returns a restore func. Callers should always defer the returned
// restore.
func UseGlobalPath(path string) (restore func()) {
	prev := GlobalPath
	GlobalPath = func() (string, error) { return path, nil }
	return func() { GlobalPath = prev }
}

// rawFile is one level's file, decoded into a generic map rather than a
// typed struct so a bad value at one key — the wrong TOML type, or a string
// none of the enum values recognize — can be ignored for that key alone
// (falling through to the next level) instead of failing the whole level. A
// missing file, an unreadable one, or one that isn't valid TOML at all is
// "this level has nothing", never an error the cascade surfaces.
type rawFile = map[string]any

func readLevel(path string) rawFile {
	f := rawFile{}
	if path == "" {
		return f
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return f
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return rawFile{}
	}
	return f
}

func rawTable(f rawFile, table string) map[string]any {
	if v, ok := f[table].(map[string]any); ok {
		return v
	}
	return nil
}

func lookupBool(levels []rawFile, table, key string) (bool, bool) {
	for _, f := range levels {
		if tbl := rawTable(f, table); tbl != nil {
			if v, ok := tbl[key].(bool); ok {
				return v, true
			}
		}
	}
	return false, false
}

func lookupString(levels []rawFile, table, key string, allowed map[string]bool) (string, bool) {
	for _, f := range levels {
		if tbl := rawTable(f, table); tbl != nil {
			if v, ok := tbl[key].(string); ok && allowed[v] {
				return v, true
			}
		}
	}
	return "", false
}

// Load resolves the four view toggles for projectDir through the cascade
// described in the package doc. projectDir is the checkout whose admin.toml
// the dashboard is reading (Model.manifestDir()), not necessarily repoRoot.
func Load(ctx context.Context, projectDir string) State {
	levels := make([]rawFile, 0, 3)
	levels = append(levels, readLevel(filepath.Join(projectDir, ".admin", Filename)))

	if root, err := git.RepoRoot(ctx, projectDir); err == nil && root != "" {
		if filepath.Clean(root) != filepath.Clean(projectDir) {
			levels = append(levels, readLevel(filepath.Join(root, ".admin", Filename)))
		}
	}

	if gp, err := GlobalPath(); err == nil && gp != "" {
		levels = append(levels, readLevel(gp))
	}

	var s State
	if v, ok := lookupBool(levels, "work", "collapsed"); ok {
		s.WorkCollapsed = v
	}
	if v, ok := lookupString(levels, "beads", "tree", allowedBeadTree); ok {
		s.BeadTree = v
	}
	if v, ok := lookupString(levels, "tracker", "sort", allowedTrackSort); ok {
		s.TrackSort = v
	}
	if v, ok := lookupBool(levels, "branches", "mine_only"); ok {
		s.MinesOnly = v
	}
	return s
}

// SaveWorkCollapsed writes work.collapsed into projectDir's own
// .admin/view.toml — see saveKey.
func SaveWorkCollapsed(projectDir string, v bool) error {
	return saveKey(projectDir, "work", "collapsed", v)
}

// SaveBeadTree writes beads.tree into projectDir's own .admin/view.toml.
func SaveBeadTree(projectDir string, v string) error { return saveKey(projectDir, "beads", "tree", v) }

// SaveTrackSort writes tracker.sort into projectDir's own .admin/view.toml.
func SaveTrackSort(projectDir string, v string) error {
	return saveKey(projectDir, "tracker", "sort", v)
}

// SaveMinesOnly writes branches.mine_only into projectDir's own
// .admin/view.toml.
func SaveMinesOnly(projectDir string, v bool) error {
	return saveKey(projectDir, "branches", "mine_only", v)
}

// saveKey rewrites projectDir's own level-1 .admin/view.toml with
// table.key set to value, preserving every other key that file already
// holds and copying nothing down from levels 2-4 (Load reads those; saveKey
// never does). Creates .admin/ if missing. The write goes through a temp
// file + rename so a crash mid-write can't leave a half-written view.toml
// behind.
func saveKey(projectDir, table, key string, value any) error {
	// An empty projectDir means no persistence: joining "" would resolve
	// .admin/ against the process cwd and write view.toml there.
	if projectDir == "" {
		return nil
	}
	dir := filepath.Join(projectDir, ".admin")
	path := filepath.Join(dir, Filename)

	f := readLevel(path)
	tbl := rawTable(f, table)
	if tbl == nil {
		tbl = map[string]any{}
	}
	tbl[key] = value
	f[table] = tbl

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".view-*.toml")
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(tmp).Encode(f); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
