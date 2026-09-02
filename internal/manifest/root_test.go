package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindRootWalksUp proves a project's manifest is found from any
// directory inside it, not only from the directory it sits in. That walk is
// what makes the project root a real value rather than a synonym for the
// invocation cwd — every action's subprocess runs in it.
//
// MUTATION EVIDENCE: replace the loop body with a single stat of start and
// this test fails at the nested directory.
func TestFindRootWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte(""), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	nested := filepath.Join(root, "internal", "kinds")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, start := range []string{root, nested} {
		got, ok := FindRoot(start)
		if !ok {
			t.Fatalf("FindRoot(%q) ok = false, want true", start)
		}
		if !samePath(t, got, root) {
			t.Errorf("FindRoot(%q) = %q, want %q", start, got, root)
		}
	}
}

// TestFindRootReportsNothingWhenThereIsNoManifest proves the walk terminates
// at the filesystem root rather than looping, and that "no project here" is
// reported rather than guessed.
func TestFindRootReportsNothingWhenThereIsNoManifest(t *testing.T) {
	// A TempDir has no admin.toml above it unless the machine's temp root
	// is inside a project, which it is not on any supported platform.
	if root, ok := FindRoot(t.TempDir()); ok {
		t.Fatalf("FindRoot = %q, true; want no manifest found", root)
	}
}

// TestFindRootStopsAtTheNearestManifest proves an inner project wins over an
// outer one — a worktree checked out inside another project must not be
// dispatched against the outer project's manifest.
func TestFindRootStopsAtTheNearestManifest(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "vendor", "thing")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, dir := range []string{outer, inner} {
		if err := os.WriteFile(filepath.Join(dir, Filename), []byte(""), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}
	got, ok := FindRoot(inner)
	if !ok || !samePath(t, got, inner) {
		t.Errorf("FindRoot(%q) = %q, %v; want the inner project", inner, got, ok)
	}
}

// TestFindRootIgnoresADirectoryNamedAdminToml guards the stat: a directory
// wearing the manifest's name is not a manifest, and treating it as one
// would root every action at the wrong place with no parse error to explain
// it.
func TestFindRootIgnoresADirectoryNamedAdminToml(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, Filename), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if root, ok := FindRoot(dir); ok {
		t.Fatalf("FindRoot = %q, true; want a directory named %s to be ignored", root, Filename)
	}
}

// TestLoadRecordsTheProjectRoot proves Load stamps the directory it read
// from onto the Manifest. That field is what every action's working
// directory comes from, so a manifest without it silently falls back to the
// process's cwd.
func TestLoadRecordsTheProjectRoot(t *testing.T) {
	dir := t.TempDir()
	body := "project_name = \"demo\"\n"
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !filepath.IsAbs(m.Root) {
		t.Errorf("Root = %q, want an absolute path", m.Root)
	}
	if !samePath(t, m.Root, dir) {
		t.Errorf("Root = %q, want %q", m.Root, dir)
	}
}

// TestParseLeavesTheRootEmpty proves an already-decoded table (an archetype
// fragment) claims no directory of its own — the caller's fallback, not a
// wrong guess, is what covers it.
func TestParseLeavesTheRootEmpty(t *testing.T) {
	m, err := Parse(map[string]any{"project_name": "demo"}, "test")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Root != "" {
		t.Errorf("Root = %q, want empty", m.Root)
	}
}

// samePath compares two paths through EvalSymlinks — macOS's /var is a
// symlink to /private/var, so a TempDir and its resolved form differ as
// strings while naming the same directory.
func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", a, err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", b, err)
	}
	return ra == rb
}
