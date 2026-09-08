package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
)

// TestLoadWithoutManifestStillResolvesArchetypes covers the repo that has
// nothing to build. The base archetype holds the machine-wide populate
// patterns, and Load used to stat <root>/admin.toml and return a zero Config
// before reaching them — so claude-skills, which has no build and no
// admin.toml, populated none of its worktrees and said nothing about it.
func TestLoadWithoutManifestStillResolvesArchetypes(t *testing.T) {
	installed := t.TempDir()
	archDir := filepath.Join(installed, "base")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archBody := "[[populate]]\npattern = \".claude/skills/*\"\nmode = \"symlink\"\n"
	if err := os.WriteFile(filepath.Join(archDir, "archetype.toml"), []byte(archBody), 0o644); err != nil {
		t.Fatal(err)
	}
	defer archetype.UseInstalledDir(installed)()

	root := t.TempDir()
	if _, err := os.Stat(filepath.Join(root, "admin.toml")); !os.IsNotExist(err) {
		t.Fatalf("fixture must have no admin.toml, stat said: %v", err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Populate) != 1 || cfg.Populate[0].Pattern != ".claude/skills/*" {
		t.Fatalf("populate = %+v, want the base archetype's one pattern", cfg.Populate)
	}
	if cfg.Populate[0].Mode != Symlink {
		t.Errorf("populate[0].Mode = %q, want symlink", cfg.Populate[0].Mode)
	}
	// Base sets no [worktrees] table, so the env-or-default root still stands.
	if cfg.Worktrees.Root != "" {
		t.Errorf("worktrees.root = %q, want empty so WorktreeRoot falls back", cfg.Worktrees.Root)
	}
}
