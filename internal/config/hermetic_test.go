package config

import (
	"os"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
)

// TestMain isolates every test in this package from the machine's installed
// archetypes (~/.admin/archetypes/, symlinked to a checkout's archetypes/ on
// some machines) by pointing archetype.InstalledArchetypesDir at an empty
// temp directory before any test runs. A test that means to exercise
// archetype merging (implicit base included) installs its own fixture
// through archetype.UseInstalledDir instead of relying on this default.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "admin-project-tool-hermetic-config-")
	if err != nil {
		panic(err)
	}
	restore := archetype.UseInstalledDir(dir)
	code := m.Run()
	restore()
	os.RemoveAll(dir)
	os.Exit(code)
}
