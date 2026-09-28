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

// TestRunNewDetectsGameEngines proves the game-engine detectors run through
// the real verb instead of falling back to simple (admin-x6tn).
func TestRunNewDetectsGameEngines(t *testing.T) {
	archetypes, err := filepath.Abs("../archetypes")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(archetype.UseInstalledDir(archetypes))

	for _, tc := range []struct {
		name  string
		files map[string]string
		dirs  []string
		want  string
	}{
		{
			name:  "forge-engine",
			files: map[string]string{"engine/build.zig": "// build\n"},
			dirs:  []string{"sample-games"},
			want:  "forge-engine",
		},
		{
			name:  "raylib-game",
			files: map[string]string{"CMakeLists.txt": "project(MyGame)\ntarget_link_libraries(MyGame raylib)\n"},
			want:  "raylib-game",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, d := range tc.dirs {
				if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}

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
			if len(m.Archetypes) != 1 || m.Archetypes[0] != tc.want {
				t.Errorf("archetypes = %v, want [%s]", m.Archetypes, tc.want)
			}
		})
	}
}
