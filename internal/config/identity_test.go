package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
)

// TestIdentityMatching pins the comparisons. Case-insensitive because git records
// the case a commit was authored with and the same person's address comes back
// capitalised differently from different machines.
func TestIdentityMatching(t *testing.T) {
	id := Identity{Login: "McBrideMusings", Emails: []string{"pierce@example.com"}}
	if !id.Owns("PIERCE@Example.com") {
		t.Error("an address differing only in case did not match")
	}
	if !id.IsMe("mcbridemusings") {
		t.Error("a login differing only in case did not match")
	}
	if id.Owns("") || id.IsMe("") {
		t.Error("an empty value matched — unknown and mine are different facts")
	}
	if (Identity{}).Known() {
		t.Error("an empty identity reported itself as usable")
	}
}

// writeManifest writes a minimal admin.toml (project_name plus whatever body
// the test wants) into dir and returns dir, for Load to resolve.
func writeManifest(t *testing.T, dir, body string) string {
	t.Helper()
	content := "project_name = \"t\"\n" + body
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestLoadNoManifestReturnsZeroConfig is the degrade path create.Worktree and
// the TUI both depend on: a directory with no admin.toml at all is not an
// error, it is simply nothing declared.
func TestLoadNoManifestReturnsZeroConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(no manifest) = %v, want nil error", err)
	}
	if cfg.Identity.Known() || len(cfg.Populate) > 0 {
		t.Errorf("Load(no manifest) = %+v, want a zero Config", cfg)
	}
}

// TestLoadDecodesIdentityWorktreesBeadsWorkflowPopulate is the core
// admin.toml-is-the-only-configuration-file behavior: every one of the five
// tables resolves from the manifest with no other file involved.
func TestLoadDecodesIdentityWorktreesBeadsWorkflowPopulate(t *testing.T) {
	dir := writeManifest(t, t.TempDir(), `
[identity]
login = "pierce"
emails = ["pierce@example.com", "pierce@work.example"]

[worktrees]
root = "/tmp/worktrees"

[beads]
columns = ["id", "title"]

[workflow]
agent = "claude"
ceiling = 4

[[populate]]
pattern = ".env"
mode = "copy"

[[populate]]
pattern = ".claude/skills/*"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Identity.Login != "pierce" || !cfg.Identity.Owns("pierce@work.example") {
		t.Errorf("identity = %+v", cfg.Identity)
	}
	if cfg.Worktrees.Root != "/tmp/worktrees" {
		t.Errorf("worktrees.root = %q", cfg.Worktrees.Root)
	}
	if len(cfg.Beads.Columns) != 2 || cfg.Beads.Columns[0] != "id" {
		t.Errorf("beads.columns = %v", cfg.Beads.Columns)
	}
	if cfg.Workflow.Agent != "claude" || cfg.Workflow.Ceiling != 4 {
		t.Errorf("workflow = %+v", cfg.Workflow)
	}
	if len(cfg.Populate) != 2 {
		t.Fatalf("populate = %+v, want 2 entries", cfg.Populate)
	}
	if cfg.Populate[0].Pattern != ".env" || cfg.Populate[0].Mode != Copy {
		t.Errorf("populate[0] = %+v", cfg.Populate[0])
	}
	// An entry with no mode defaults to symlink.
	if cfg.Populate[1].Mode != Symlink {
		t.Errorf("populate[1].Mode = %q, want the default symlink", cfg.Populate[1].Mode)
	}
}

// TestLoadMergesThroughArchetypes proves the ticket's central claim: these
// five tables merge through archetypes exactly like any other manifest key,
// with the project's own admin.toml winning over the archetype it declares.
func TestLoadMergesThroughArchetypes(t *testing.T) {
	installed := t.TempDir()
	archDir := filepath.Join(installed, "base")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archBody := "[workflow]\nagent = \"archetype-agent\"\nceiling = 2\n\n" +
		"[[populate]]\npattern = \".env\"\nmode = \"copy\"\n"
	if err := os.WriteFile(filepath.Join(archDir, "archetype.toml"), []byte(archBody), 0o644); err != nil {
		t.Fatal(err)
	}
	defer archetype.UseInstalledDir(installed)()

	dir := writeManifest(t, t.TempDir(), `
archetypes = ["base"]

[workflow]
agent = "project-agent"

[[populate]]
pattern = "admin.toml"
mode = "copy"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workflow.Agent != "project-agent" {
		t.Errorf("workflow.agent = %q, want the project's override to win", cfg.Workflow.Agent)
	}
	if cfg.Workflow.Ceiling != 2 {
		t.Errorf("workflow.ceiling = %d, want the archetype's value carried through", cfg.Workflow.Ceiling)
	}
	patterns := map[string]bool{}
	for _, e := range cfg.Populate {
		patterns[e.Pattern] = true
	}
	if !patterns[".env"] || !patterns["admin.toml"] {
		t.Errorf("populate = %+v, want both the archetype's and the project's patterns", cfg.Populate)
	}
}

// TestLoadIgnoresInstalledArchetypesWhenSearchPathIsEmpty proves the seam
// isolates Load from whatever the installed archetypes directory contains:
// with InstalledArchetypesDir pointed at a fresh empty directory, a manifest
// declaring N populate entries and no archetypes returns exactly N — never
// more, regardless of what ~/.admin/archetypes holds on the machine running
// the test.
func TestLoadIgnoresInstalledArchetypesWhenSearchPathIsEmpty(t *testing.T) {
	defer archetype.UseInstalledDir(t.TempDir())()

	dir := writeManifest(t, t.TempDir(), `
[[populate]]
pattern = "a"

[[populate]]
pattern = "b"

[[populate]]
pattern = "c"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Populate) != 3 {
		t.Fatalf("populate = %+v, want exactly 3 entries", cfg.Populate)
	}
}
