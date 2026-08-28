package archetype

import (
	"os"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func projectManifest(t *testing.T, toml string) *manifest.Manifest {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/"+manifest.Filename, []byte(toml), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}
	m, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("manifest.Load: %v", err)
	}
	return m
}

func TestLaterArchetypeWinsOnConflict(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "a", `
[commands.build]
desc  = "from a"
steps = ["step-a"]
`, false)
	writeArchetypeDir(t, root, "b", `
[commands.build]
desc  = "from b"
steps = ["step-b"]
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["a", "b"]
`)

	merged, log, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := merged.Overrides["build"].Desc; got != "from b" {
		t.Errorf("Overrides[build].Desc = %q, want %q (b listed after a must win)", got, "from b")
	}

	found := false
	for _, line := range log {
		if strings.Contains(line, "'build'") && strings.Contains(line, "archetype 'a'") && strings.Contains(line, "archetype 'b'") {
			found = true
		}
	}
	if !found {
		t.Errorf("merge log = %v, want a line naming the 'build' collision between archetype 'a' and 'b'", log)
	}
}

func TestManifestOverridesArchetype(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "docker", `
[commands.build]
desc  = "archetype build"
steps = ["docker-build"]
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["docker"]

[commands.build]
desc  = "project build"
steps = ["custom-build"]
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := merged.Overrides["build"]
	if got.Desc != "project build" {
		t.Errorf("Overrides[build].Desc = %q, want %q (project [commands] beats every archetype)", got.Desc, "project build")
	}
	if len(got.Steps) != 1 || got.Steps[0] != "custom-build" {
		t.Errorf("Overrides[build].Steps = %v, want [custom-build]", got.Steps)
	}
}

func TestNoArchetypesResolvesCleanly(t *testing.T) {
	// No archetype directories exist anywhere on the search path.
	home := t.TempDir()
	repo := t.TempDir()
	sp := DefaultSearchPath(home, repo)

	project := projectManifest(t, `
project_name = "widget"

[commands.build]
desc  = "just the project"
steps = ["compile"]
`)

	merged, log, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v, want a manifest with no archetypes to resolve cleanly", err)
	}
	if len(merged.Overrides) != 1 || merged.Overrides["build"].Desc != "just the project" {
		t.Errorf("Overrides = %v, want just the project's own 'build'", merged.Overrides)
	}
	if len(log) != 0 {
		t.Errorf("merge log = %v, want empty — nothing to collide with", log)
	}
	if merged.ProjectName != "widget" {
		t.Errorf("ProjectName = %q, want %q", merged.ProjectName, "widget")
	}
}
