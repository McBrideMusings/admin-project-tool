package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// TestMain isolates every test in this package from the machine's installed
// archetypes (~/.admin/archetypes/, symlinked to a checkout's archetypes/ on
// some machines) by pointing archetype.InstalledArchetypesDir at an empty
// temp directory before any test runs. check_test.go's
// resolveProjectManifest-based tests reach that seam directly — without
// this, they would silently merge whatever base archetype is installed on
// the machine running the test.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "admin-project-tool-hermetic-cmd-")
	if err != nil {
		panic(err)
	}
	restore := archetype.UseInstalledDir(dir)
	// `admin worktree -x` shares its worker through the state dir; never the user's.
	restoreLivework := livework.UseRegistry(livework.NewSharedRegistry(filepath.Join(dir, "livework")))
	code := m.Run()
	restoreLivework()
	restore()
	os.RemoveAll(dir)
	os.Exit(code)
}
