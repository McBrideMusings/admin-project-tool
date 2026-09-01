package archetype

import (
	"os"
	"path/filepath"
	"testing"
)

// writeArchetypeDir creates <root>/<name>/archetype.toml (and, if scripts is
// true, an empty <root>/<name>/scripts/ dir) under a fresh t.TempDir().
func writeArchetypeDir(t *testing.T, root, name, tomlBody string, scripts bool) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(tomlBody), 0o644); err != nil {
		t.Fatalf("write archetype.toml: %v", err)
	}
	if scripts {
		if err := os.MkdirAll(filepath.Join(dir, ScriptsDir), 0o755); err != nil {
			t.Fatalf("mkdir scripts: %v", err)
		}
	}
}

func TestRepoArchetypeOverridesHome(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	homeArch := filepath.Join(home, ".admin", "archetypes")
	repoArch := filepath.Join(repo, ".admin", "archetypes")

	writeArchetypeDir(t, homeArch, "docker", "[commands.build]\ndesc = \"home\"\nsteps = [\"x\"]\n", false)
	writeArchetypeDir(t, repoArch, "docker", "[commands.build]\ndesc = \"repo\"\nsteps = [\"x\"]\n", false)

	sp := DefaultSearchPath(home, repo)

	fsys, label, ok := sp.Find("docker")
	if !ok {
		t.Fatal("Find(docker) = not found, want found")
	}
	if label != repoArch {
		t.Errorf("Find(docker) label = %q, want repo slot %q", label, repoArch)
	}

	arch, err := LoadFrom(fsys, "docker", label)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := arch.Partial.Overrides["build"].Desc; got != "repo" {
		t.Errorf("winning archetype desc = %q, want %q (repo must win over home)", got, "repo")
	}
}

func TestMissingDirectoriesAreNotAnError(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	// Neither home nor repo has a .admin/archetypes/ directory at all.
	sp := DefaultSearchPath(home, repo)

	list, err := sp.List()
	if err != nil {
		t.Fatalf("List: %v, want nil error for absent directories", err)
	}
	if len(list) != 0 {
		t.Errorf("List = %v, want empty", list)
	}

	if _, _, ok := sp.Find("anything"); ok {
		t.Error("Find(anything) = found, want not found when no archetype directories exist")
	}
}

func TestDefaultSearchPathOrder(t *testing.T) {
	home := "/home/example"
	repo := "/repo/example"
	sp := DefaultSearchPath(home, repo)

	if len(sp) != 3 {
		t.Fatalf("DefaultSearchPath has %d slots, want 3", len(sp))
	}
	if sp[0].Label != "embedded" {
		t.Errorf("slot 0 label = %q, want %q", sp[0].Label, "embedded")
	}
	if sp[1].Label != filepath.Join(home, ".admin", "archetypes") {
		t.Errorf("slot 1 label = %q, want home archetypes dir", sp[1].Label)
	}
	if sp[2].Label != filepath.Join(repo, ".admin", "archetypes") {
		t.Errorf("slot 2 label = %q, want repo archetypes dir", sp[2].Label)
	}

	// The embedded slot ships empty: it contributes no names to List().
	list, err := sp.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List = %v, want empty (embedded slot ships empty, other slots don't exist)", list)
	}
}

// TestSearchPathForReadsInstalledDirSeam proves SearchPathFor's installed
// slot comes from InstalledArchetypesDir (via UseInstalledDir) rather than
// resolving its own home directory, and that its three-slot order matches
// DefaultSearchPath's.
func TestSearchPathForReadsInstalledDirSeam(t *testing.T) {
	installed := t.TempDir()
	repo := t.TempDir()
	defer UseInstalledDir(installed)()

	sp, err := SearchPathFor(repo)
	if err != nil {
		t.Fatalf("SearchPathFor: %v", err)
	}

	want := searchPath(installed, repo)
	if len(sp) != len(want) {
		t.Fatalf("SearchPathFor has %d slots, want %d", len(sp), len(want))
	}
	for i := range sp {
		if sp[i].Label != want[i].Label {
			t.Errorf("slot %d label = %q, want %q", i, sp[i].Label, want[i].Label)
		}
	}
	if sp[1].Label != installed {
		t.Errorf("installed slot label = %q, want the InstalledArchetypesDir override %q", sp[1].Label, installed)
	}
}
