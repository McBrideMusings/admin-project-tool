// Package archetype loads directory-format archetypes — archetype.toml plus
// an optional scripts/ directory — off an io/fs abstraction and merges their
// partial manifests into a project's manifest.Manifest, per ADR-0002 (merge
// semantics: partial manifests, list order, last wins, no inheritance) and
// ADR-0009 (a project's admin.toml references; the archetype implements).
//
// Loading an archetype requires no interpreter and no subprocess: this
// package only reads TOML and stats a directory. A scripts/ directory is
// located, never executed — nothing here calls os/exec.
package archetype

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Source is one slot in a SearchPath: a filesystem rooted at the directory
// that directly contains archetype subdirectories, labeled for error text and
// the merge log.
type Source struct {
	Label string
	FS    fs.FS
}

// SearchPath is the ordered list of places to look for an archetype, low to
// high precedence — the same name in a later slot wins.
type SearchPath []Source

// DefaultSearchPath is the three-slot search path this ticket introduces,
// later winning by name:
//
//  1. embedded()                          — reserved, ships empty today
//  2. <homeDir>/.admin/archetypes/         — installed archetypes
//  3. <repoDir>/.admin/archetypes/         — project-local archetypes
//
// homeDir and repoDir are parameters rather than read from the environment
// inside this package, so callers (and tests) can point them at real
// directories without mutating process state.
func DefaultSearchPath(homeDir, repoDir string) SearchPath {
	homeArchetypes := HomeArchetypesDir(homeDir)
	repoArchetypes := filepath.Join(repoDir, ".admin", "archetypes")
	return SearchPath{
		{Label: "embedded", FS: embedded()},
		{Label: homeArchetypes, FS: os.DirFS(homeArchetypes)},
		{Label: repoArchetypes, FS: os.DirFS(repoArchetypes)},
	}
}

// HomeArchetypesDir is where the archetype deployment path (Deploy, in
// deploy.go) writes and where DefaultSearchPath's home slot reads —
// ~/.admin/archetypes/, kept in one place so the two can never drift apart.
func HomeArchetypesDir(homeDir string) string {
	return filepath.Join(homeDir, ".admin", "archetypes")
}

// embedded is the reserved slot for archetypes promoted directly into the
// binary in the future. It is deliberately NOT a go:embed directive: embedding
// an empty or absent directory is a compile error, and today the binary ships
// no built-in archetypes at all (they install to ~/.admin/archetypes/ via a
// separate deployment path). This returns an always-empty fs.FS instead, so
// the slot exists in the search order without requiring any files on disk.
func embedded() fs.FS { return emptyFS{} }

// emptyFS is an fs.FS with nothing in it. It implements fs.ReadDirFS directly
// so fs.ReadDir never has to fall back through Open+File.ReadDir, and every
// Open reports fs.ErrNotExist the same way a missing directory would.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (emptyFS) ReadDir(string) ([]fs.DirEntry, error) {
	return nil, nil
}

// List returns every archetype name visible anywhere on the search path,
// mapped to the Source that wins for that name (later slots overwrite
// earlier ones). A slot whose root directory does not exist on disk
// contributes nothing and is not an error — that is the normal state for
// ~/.admin/archetypes/ or <repo>/.admin/archetypes/ before either is ever
// created. Any other read error (e.g. permission denied) is returned rather
// than silently treated as empty.
func (sp SearchPath) List() (map[string]Source, error) {
	result := map[string]Source{}
	for _, src := range sp {
		if src.FS == nil {
			continue
		}
		entries, err := fs.ReadDir(src.FS, ".")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("%s: %w", src.Label, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			result[e.Name()] = src
		}
	}
	return result, nil
}

// Find returns the winning Source's filesystem for name — the last slot on
// the search path that has a name/ directory — and false if no slot has one.
// A slot that errors while being checked (missing root directory included)
// is treated as not having name, matching List's "missing slot means empty"
// rule.
func (sp SearchPath) Find(name string) (fs.FS, string, bool) {
	var (
		found fs.FS
		label string
		ok    bool
	)
	for _, src := range sp {
		if src.FS == nil {
			continue
		}
		info, err := fs.Stat(src.FS, name)
		if err != nil || !info.IsDir() {
			continue
		}
		found, label, ok = src.FS, src.Label, true
	}
	return found, label, ok
}
