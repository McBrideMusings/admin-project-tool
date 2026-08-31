package kinds

import (
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestSyncCopiesFiles proves kind="sync" actually copies files from src to
// dest.
func TestSyncCopiesFiles(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, root, "dist/index.html", "<html></html>")
	writeFile(t, root, "dist/assets/app.js", "console.log(1)")

	runner, err := makeSync("deploy", manifest.Action{"run": "out", "src": "dist/"})
	if err != nil {
		t.Fatalf("makeSync: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	assertFileExists(t, root, "out/index.html")
	assertFileExists(t, root, "out/assets/app.js")
}

// TestSyncDeletesExtraneousDestFiles proves the local branch mirrors the
// source (Python's delete=True default): a file that exists only in dest,
// with no counterpart in src, is removed by the sync.
//
// MUTATION EVIDENCE: comment out makeSync/runSync's pruneExtraneous call,
// and this test fails — stale.txt survives the sync and
// assertFileMissing's os.Stat finds it.
func TestSyncDeletesExtraneousDestFiles(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, root, "dist/keep.txt", "keep")
	writeFile(t, root, "out/stale.txt", "stale")

	runner, err := makeSync("deploy", manifest.Action{"run": "out", "src": "dist/"})
	if err != nil {
		t.Fatalf("makeSync: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	assertFileExists(t, root, "out/keep.txt")
	assertFileMissing(t, root, "out/stale.txt")
}

// TestSyncExcludesArePreserved proves an entry matching `excludes` is
// neither copied (if new in src) nor deleted (if pre-existing only in dest)
// — the doc comment's "state a plugin dir keeps at runtime" case.
func TestSyncExcludesArePreserved(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, root, "dist/app.txt", "app")
	writeFile(t, root, "out/state.db", "precious")

	runner, err := makeSync("deploy", manifest.Action{
		"run": "out", "src": "dist/", "excludes": []any{"*.db"},
	})
	if err != nil {
		t.Fatalf("makeSync: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	assertFileExists(t, root, "out/app.txt")
	assertFileExists(t, root, "out/state.db")
}

// TestSyncMissingRunIsRejected proves kind="sync" requires 'run' (the
// destination), matching admin_lib/validate.py's "sync kind requires 'run'".
func TestSyncMissingRunIsRejected(t *testing.T) {
	if _, err := makeSync("bad", manifest.Action{}); err == nil {
		t.Fatal("makeSync with no 'run' should error")
	}
}

// TestIsRemoteDest proves the user@host:/path detection matches
// admin_lib/core.py::sync's is_remote check exactly.
func TestIsRemoteDest(t *testing.T) {
	cases := map[string]bool{
		"deploy@example.com:/var/www": true,
		"C:\\Users\\me":               false, // colon present, no "@" before it
		"./local/out":                 false,
		"user@host":                   false, // no colon at all
	}
	for dest, want := range cases {
		if got := isRemoteDest(dest); got != want {
			t.Errorf("isRemoteDest(%q) = %v, want %v", dest, got, want)
		}
	}
}

// TestSyncDefaultSrcIsDist proves a `src`-less action defaults to "dist/",
// matching admin_lib/interp.py::_make_sync.
func TestSyncDefaultSrcIsDist(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, root, "dist/only.txt", "x")

	runner, err := makeSync("deploy", manifest.Action{"run": "out"})
	if err != nil {
		t.Fatalf("makeSync: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	assertFileExists(t, root, "out/only.txt")
}

// TestMatchesExclude is a focused unit test of the glob-matching helper
// pruneExtraneous/copyTree both depend on.
func TestMatchesExclude(t *testing.T) {
	if !matchesExclude("state.db", []string{"*.db"}) {
		t.Error("state.db should match *.db")
	}
	if matchesExclude("state.txt", []string{"*.db"}) {
		t.Error("state.txt should not match *.db")
	}
}
