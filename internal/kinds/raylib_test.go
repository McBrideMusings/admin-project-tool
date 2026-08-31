package kinds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestMakeRaylibRejectsAnUnknownMode proves the build-time mode validation
// admin_lib/interp.py::_make_raylib does on `run`.
func TestMakeRaylibRejectsAnUnknownMode(t *testing.T) {
	_, err := makeRaylib("game", manifest.Action{"run": "launch"}, &manifest.Manifest{}, nil)
	if err == nil {
		t.Fatal("makeRaylib accepted an unknown mode, want an error")
	}
}

// TestMakeRaylibRunModeRequiresABinary proves run/smoke modes refuse to
// proceed with no [raylib].binary configured — the Go port of
// admin_lib/interp.py::_make_raylib's "nothing to run" guard.
func TestMakeRaylibRunModeRequiresABinary(t *testing.T) {
	runner, err := makeRaylib("game-run", manifest.Action{"run": "run"}, &manifest.Manifest{}, nil)
	if err != nil {
		t.Fatalf("makeRaylib: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Error("runner(nil) = nil, want an error when [raylib].binary is unset")
	}
}

// TestMakeRaylibCleanRemovesGlobMatchedPaths proves clean mode's glob
// expansion actually removes matched files/dirs — the Go port of
// admin_lib/interp.py::_make_raylib's clean branch, which the doc comment
// there specifically calls out as needing real glob expansion (not `rm
// -rf`, since clean_dirs entries may be globs).
//
// MUTATION EVIDENCE: change the `mode == "clean"` branch's os.RemoveAll
// call to a no-op (skip the actual removal) and this test fails — the
// build/ directory and the glob-matched db file would both still exist.
func TestMakeRaylibCleanRemovesGlobMatchedPaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "build", "game"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "data", "zones"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "zones", "level1.db"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m := &manifest.Manifest{Raylib: map[string]any{
		"clean_dirs": []any{"build", "data/zones/*.db"},
	}}
	runner, err := makeRaylib("clean", manifest.Action{"run": "clean"}, m, nil)
	if err != nil {
		t.Fatalf("makeRaylib: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner(nil) = %v, want nil", err)
	}

	if _, err := os.Stat(filepath.Join(root, "build")); !os.IsNotExist(err) {
		t.Error("build/ still exists after clean")
	}
	if _, err := os.Stat(filepath.Join(root, "data", "zones", "level1.db")); !os.IsNotExist(err) {
		t.Error("data/zones/level1.db still exists after glob-matched clean")
	}
}
