package archetype

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// Filename is an archetype directory's manifest file.
const Filename = "archetype.toml"

// ScriptsDir is an archetype directory's optional sidecar scripts directory.
// It is only ever located, never executed, by this package.
const ScriptsDir = "scripts"

// BaseName is the archetype Resolve merges into every project implicitly,
// before any archetype the project declares and before the project's own
// manifest — retire-the-config-tiers' (admin-mb5q) answer to "where do the
// [[populate]] patterns every project needs live now that there is no global
// config tier". A project never lists "base" in `archetypes`; its presence is
// what makes it implicit.
const BaseName = "base"

// Archetype is one loaded archetype directory: its partial manifest plus
// where it was found.
type Archetype struct {
	// Name is the directory name — the canonical name, taken from the search
	// path rather than from any `name` key inside archetype.toml, so a
	// self-naming archetype.toml cannot silently rename itself.
	Name string
	// Source is the winning Source's label, for error text and merge logs.
	Source string
	// Scripts is the path (within fsys) to the scripts/ directory, or "" if
	// this archetype has none.
	Scripts string
	// Partial is archetype.toml parsed as a partial manifest (no
	// project_name required) — ADR-0002's "archetypes contribute partial
	// manifests".
	Partial *manifest.Manifest
}

// LoadFrom reads one archetype directory named name out of fsys. label is
// carried through to Archetype.Source and into any error for diagnostics.
// This performs no interpretation and no subprocess: a TOML decode, a
// manifest parse, and one Stat for the optional scripts/ directory.
func LoadFrom(fsys fs.FS, name, label string) (*Archetype, error) {
	tomlPath := path.Join(name, Filename)
	data, err := fs.ReadFile(fsys, tomlPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("archetype %q: no %s at %s (%s)", name, Filename, tomlPath, label)
		}
		return nil, fmt.Errorf("archetype %q: %w", name, err)
	}

	decoded := map[string]any{}
	if _, err := toml.Decode(string(data), &decoded); err != nil {
		return nil, fmt.Errorf("%s: invalid TOML: %w", tomlPath, err)
	}

	partial, err := manifest.ParsePartial(decoded, tomlPath)
	if err != nil {
		return nil, err
	}

	scripts := ""
	scriptsPath := path.Join(name, ScriptsDir)
	if info, err := fs.Stat(fsys, scriptsPath); err == nil && info.IsDir() {
		scripts = scriptsPath
	}

	return &Archetype{Name: name, Source: label, Scripts: scripts, Partial: partial}, nil
}

// Load searches sp for name (last slot wins) and loads it. An unknown name
// reports every slot label that was searched.
func Load(sp SearchPath, name string) (*Archetype, error) {
	fsys, label, ok := sp.Find(name)
	if !ok {
		labels := make([]string, 0, len(sp))
		for _, src := range sp {
			labels = append(labels, src.Label)
		}
		return nil, fmt.Errorf("unknown archetype: %s (looked in %s)", name, strings.Join(labels, ", "))
	}
	return LoadFrom(fsys, name, label)
}

// LoadBase looks up BaseName on sp and loads it, returning (nil, false, nil)
// when no slot has it rather than Load's "unknown archetype" error — a
// machine where archetypes/ was never deployed to ~/.admin/archetypes (or a
// test's bare search path) has no base, and that is a silent no-op, not a
// failure: Resolve must still resolve a project with no archetypes declared
// at all. Shared by Resolve (the implicit merge) and the TUI's
// computeOrigins (the matching origin attribution) so the two can never
// disagree about what base is.
func LoadBase(sp SearchPath) (*Archetype, bool, error) {
	fsys, label, ok := sp.Find(BaseName)
	if !ok {
		return nil, false, nil
	}
	arch, err := LoadFrom(fsys, BaseName, label)
	if err != nil {
		return nil, false, err
	}
	return arch, true, nil
}
