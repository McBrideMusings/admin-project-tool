package archetype

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Deploy copies every archetype directory found directly under sourceDir
// (this repository's archetypes/) into destDir (normally HomeArchetypesDir,
// ~/.admin/archetypes/), and returns the names it deployed, sorted.
//
// This is the archetype half of the two deployment paths admin-3rgp.29
// establishes. The other half is `go install`, which produces a binary that
// embeds none of this — see embedded() in search.go. The two are independent
// on purpose: deploying archetypes never requires rebuilding the binary, and
// building the binary never bundles an archetype into it.
//
// A source entry counts as an archetype only if it is a directory containing
// Filename (archetype.toml) — this is what excludes the legacy sibling files
// (e.g. archetypes/simple.py) still present alongside the directories that
// admin-3rgp.4 converted, without Deploy needing to know that history.
//
// destDir is wiped and rebuilt from scratch on every call, so a source
// archetype that was removed disappears from the deployment too, and running
// Deploy twice in a row is idempotent rather than an accumulating overlay —
// matching install.sh's existing `rm -rf $DEST_DIR/archetypes && cp -R` for
// the Python archetype directory it deploys today.
func Deploy(sourceDir, destDir string) ([]string, error) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", sourceDir, err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(sourceDir, e.Name(), Filename)); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	if err := os.RemoveAll(destDir); err != nil {
		return nil, fmt.Errorf("clear %s: %w", destDir, err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", destDir, err)
	}

	for _, name := range names {
		if err := copyTree(filepath.Join(sourceDir, name), filepath.Join(destDir, name)); err != nil {
			return nil, fmt.Errorf("deploy archetype %q: %w", name, err)
		}
	}
	return names, nil
}

// copyTree recursively copies src onto dst, preserving each file's mode.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
