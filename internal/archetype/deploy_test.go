package archetype

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeployCopiesArchetypeDirectories(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "archetypes") // does not exist yet

	writeArchetypeDir(t, src, "simple", "project_name = \"x\"\n", false)
	writeArchetypeDir(t, src, "apple", "project_name = \"y\"\n", true)
	if err := os.WriteFile(filepath.Join(src, "apple", "scripts", "build.py"), []byte("# body\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	// A legacy sibling file and a plain, non-archetype directory must both be
	// skipped — only a directory containing archetype.toml counts.
	if err := os.WriteFile(filepath.Join(src, "simple.py"), []byte("# legacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "not-an-archetype"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	names, err := Deploy(src, dest)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if want := []string{"apple", "simple"}; !equalStrings(names, want) {
		t.Errorf("Deploy names = %v, want %v", names, want)
	}

	for _, name := range []string{"simple", "apple"} {
		if _, err := os.Stat(filepath.Join(dest, name, Filename)); err != nil {
			t.Errorf("deployed %s missing archetype.toml: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "apple", "scripts", "build.py")); err != nil {
		t.Errorf("deployed apple missing scripts/build.py: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "simple.py")); err == nil {
		t.Error("legacy sibling file simple.py was deployed, want skipped")
	}
	if _, err := os.Stat(filepath.Join(dest, "not-an-archetype")); err == nil {
		t.Error("non-archetype directory was deployed, want skipped")
	}
}

func TestDeployIsAFullResync(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	// A stale archetype (and a stray file) already sit in dest from a
	// previous deploy of a source that has since dropped them.
	writeArchetypeDir(t, dest, "stale", "project_name = \"old\"\n", false)
	if err := os.WriteFile(filepath.Join(dest, "junk.txt"), []byte("leftover"), 0o644); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	writeArchetypeDir(t, src, "simple", "project_name = \"x\"\n", false)

	names, err := Deploy(src, dest)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if want := []string{"simple"}; !equalStrings(names, want) {
		t.Errorf("Deploy names = %v, want %v", names, want)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale")); err == nil {
		t.Error("stale archetype survived a redeploy, want removed")
	}
	if _, err := os.Stat(filepath.Join(dest, "junk.txt")); err == nil {
		t.Error("stray file in dest survived a redeploy, want removed")
	}
}

func TestDeployThenSearchPathFindsIt(t *testing.T) {
	src := t.TempDir()
	home := t.TempDir()
	repo := t.TempDir() // no repo-local archetypes at all

	writeArchetypeDir(t, src, "docker", "[commands.build]\ndesc = \"d\"\nsteps = [\"x\"]\n", false)

	if _, err := Deploy(src, HomeArchetypesDir(home)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	sp := DefaultSearchPath(home, repo)
	list, err := sp.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, ok := list["docker"]; !ok {
		t.Fatalf("List = %v, want a deployed 'docker' archetype visible with no rebuild", list)
	}
}

func TestDeployEmptySourceYieldsNoArchetypes(t *testing.T) {
	src := t.TempDir() // exists, but nothing in it looks like an archetype
	dest := t.TempDir()

	// A stale file already sits in dest from a previous deploy. An empty
	// source is still a full resync, not a skipped no-op — an "optimization"
	// that short-circuits when there's nothing to deploy would silently
	// leave stale content behind.
	stale := filepath.Join(dest, "leftover.txt")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	names, err := Deploy(src, dest)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("Deploy names = %v, want empty", names)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale file survived a redeploy from an empty source, want removed")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
