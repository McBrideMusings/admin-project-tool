package tui

import (
	"os"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
)

// TestMain isolates every test in this package from the machine's installed
// archetypes (~/.admin/archetypes/, symlinked to a checkout's archetypes/ on
// some machines) by pointing archetype.InstalledArchetypesDir at an empty
// temp directory before any test runs. Every New()-with-admin.toml test in
// this package reaches loadWorkManifest, which resolves through that seam —
// without this, each one would silently merge whatever base archetype is
// installed on the machine running the test.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "admin-project-tool-hermetic-tui-")
	if err != nil {
		panic(err)
	}
	restore := archetype.UseInstalledDir(dir)
	code := m.Run()
	restore()
	os.RemoveAll(dir)
	os.Exit(code)
}
