package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func TestStarterProjectName(t *testing.T) {
	// Expected values are `name.replace("-", " ").title()`.
	for dir, want := range map[string]string{
		"/x/MY-APP":    "My App",
		"/x/my-app":    "My App",
		"/x/app2go":    "App2Go",
		"/x/it's-mine": "It'S Mine",
	} {
		if got := starterProjectName(dir); got != want {
			t.Errorf("starterProjectName(%q) = %q, want %q", dir, got, want)
		}
	}
}

// TestRunNewWritesDetectorTables proves the partial tables a detector finds
// reach the starter admin.toml rather than being dropped after detection.
func TestRunNewWritesDetectorTables(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{"react-native":"0.76"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "android"), 0o755); err != nil {
		t.Fatal(err)
	}

	// runNew ends with runCheck, which resolves the archetypes it wrote —
	// TestMain's empty installed dir has none, so use this checkout's.
	archetypes, err := filepath.Abs("../archetypes")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(archetype.UseInstalledDir(archetypes))

	c := &cobra.Command{}
	c.Flags().Bool("force", false, "")
	c.SetContext(context.Background())
	if err := runNew(c, []string{dir}); err != nil {
		t.Fatalf("runNew = %v", err)
	}

	m, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if got := m.Settings["react_native"]["app_dir"]; got != "." {
		t.Errorf(`[react_native].app_dir = %v, want "."`, got)
	}
}
