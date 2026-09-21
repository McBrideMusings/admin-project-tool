package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
)

// TestMain isolates every test in this package from the machine's installed
// archetypes (~/.admin/archetypes/, symlinked to a checkout's archetypes/ on
// some machines) by pointing archetype.InstalledArchetypesDir at an empty
// temp directory before any test runs. Every New()-with-admin.toml test in
// this package reaches loadWorkManifest, which resolves through that seam —
// without this, each one would silently merge whatever base archetype is
// installed on the machine running the test.
//
// The same reasoning covers viewstate.GlobalPath (admin-y5lc.1): every New()
// call in this package's tests reads it as level 3 of the view.toml cascade,
// so without this override a test would read whatever ~/.admin/view.toml
// happens to exist on the machine running it. The path here never has a
// file written to it, so that level always falls through to defaults unless
// a test writes one itself.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "admin-project-tool-hermetic-tui-")
	if err != nil {
		panic(err)
	}
	restoreArchetypes := archetype.UseInstalledDir(dir)
	restoreViewState := viewstate.UseGlobalPath(filepath.Join(dir, "unused-global-view.toml"))
	code := m.Run()
	restoreViewState()
	restoreArchetypes()
	os.RemoveAll(dir)
	os.Exit(code)
}

// freshLivework gives one test its own livework.Registry rather than
// writing into the process-wide livework.Default singleton: Default is
// shared across every test in this package (and across every -count>1
// iteration of the same test), so an entry one test adds and leaves behind
// is still there for the next test to trip over (admin-hw2i) — e.g. a
// hosted-pty-shaped entry left in Default makes
// TestRequestQuitWithNothingHostedQuitsImmediately see a phantom running
// process, or a stale entry named "build" makes a later rowkeys test attach
// to it instead of dispatching fresh. Call this at the top of any test that
// calls livework.Default.Add, directly or through a helper, instead of
// manually resetting state on cleanup — a per-test instance can't leak
// because there is nothing shared left to leak into.
func freshLivework(t *testing.T) {
	t.Helper()
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
}
