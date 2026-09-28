package detect

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// fixture helpers
// ---------------------------------------------------------------------------

// writeFixture materializes files (relative-path -> content) under a fresh
// temp dir and returns the dir. A "" content creates an empty file; a
// content ending in "/" is not supported — use mkdirs for bare directories.
func writeFixture(t *testing.T, files map[string]string, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func assertArchetypes(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("archetypes = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("archetypes = %v, want %v (order matters)", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// single-stack fixtures
// ---------------------------------------------------------------------------

func TestFixtureTreesProposeExpectedArchetypes(t *testing.T) {
	t.Run("bare empty tree falls back to simple", func(t *testing.T) {
		root := writeFixture(t, nil)
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"simple"})
		if res.Detector != "fallback" {
			t.Fatalf("detector = %q, want fallback", res.Detector)
		}
	})

	t.Run("go-cli", func(t *testing.T) {
		root := writeFixture(t, map[string]string{"go.mod": "module x\n"})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"go-cli"})
		if res.Detector != "go-cli" {
			t.Fatalf("detector = %q", res.Detector)
		}
	})

	t.Run("go with Dockerfile yields docker only, not go-cli", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"go.mod":     "module x\n",
			"Dockerfile": "FROM scratch\n",
		})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"docker"})
	})

	t.Run("rust", func(t *testing.T) {
		root := writeFixture(t, map[string]string{"Cargo.toml": "[package]\nname=\"x\"\n"})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"rust"})
	})

	t.Run("rust with Dockerfile yields docker only", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"Cargo.toml": "[package]\nname=\"x\"\n",
			"Dockerfile": "FROM scratch\n",
		})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"docker"})
	})

	t.Run("rust-tauri", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"Cargo.toml":                "[package]\nname=\"x\"\n",
			"src-tauri/tauri.conf.json": "{}",
		})
		res := Run(root)
		// The rust detector also matches (Cargo.toml, no Dockerfile) and runs
		// first in Order, so both archetypes are proposed as a union.
		assertArchetypes(t, res.Archetypes, []string{"rust", "rust-tauri"})
	})

	t.Run("hugo nested under site/", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"site/hugo.toml": "baseURL = \"/\"\n",
		})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"hugo"})
	})

	t.Run("cloudflare workers", func(t *testing.T) {
		root := writeFixture(t, map[string]string{"wrangler.toml": "name=\"x\"\n"})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"cloudflare-workers"})
	})

	t.Run("unraid plugin", func(t *testing.T) {
		root := writeFixture(t, map[string]string{"myplugin.plg": "<PLUGIN/>\n"})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"unraid-plugin"})
	})

	t.Run("firefox extension", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"manifest.json": `{"manifest_version": 2, "browser_specific_settings": {"gecko": {"id": "x@example.com"}}}`,
		})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"firefox-extension"})
	})

	t.Run("raylib game", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"CMakeLists.txt": "cmake_minimum_required(VERSION 3.20)\nproject(MyGame)\ntarget_link_libraries(MyGame raylib)\n",
		})
		m := detectRaylibGame(root)
		if m == nil {
			t.Fatal("expected raylib-game match")
		}
		assertArchetypes(t, m.Archetypes, []string{"raylib-game"})
		raylib := m.Tables["raylib"]
		if raylib["binary"] != "MyGame" {
			t.Fatalf("binary = %v, want MyGame", raylib["binary"])
		}
		// Not in Order — a raylib project alone falls back to simple via Run.
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"simple"})
	})

	t.Run("forge-engine detector matches but never runs via Run", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"engine/build.zig": "// build\n",
		}, "sample-games")
		m := detectForgeEngine(root)
		if m == nil {
			t.Fatal("expected forge-engine detector match")
		}
		assertArchetypes(t, m.Archetypes, []string{"forge-engine"})
		// Not in Order, so a full run falls back to simple.
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"simple"})
	})

	t.Run("apple project at root", func(t *testing.T) {
		root := writeFixture(t, nil, "MyApp.xcodeproj")
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"apple"})
		if _, ok := res.Tables["apple"]; ok {
			t.Fatal("root-level project should not seed apple.project_path")
		}
	})

	t.Run("apple project nested seeds project_path", func(t *testing.T) {
		root := writeFixture(t, nil, "ios/MyApp.xcodeproj")
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"apple"})
		apple, ok := res.Tables["apple"]
		if !ok {
			t.Fatal("expected apple table with project_path")
		}
		if apple["project_path"] != "ios/MyApp.xcodeproj" {
			t.Fatalf("project_path = %v", apple["project_path"])
		}
	})

	t.Run("apple project vendored under Pods is ignored", func(t *testing.T) {
		root := writeFixture(t, nil, "Pods/Pods.xcodeproj")
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"simple"})
	})

	t.Run("react-native workspace", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"apps/mobile/package.json": `{"dependencies": {"react-native": "0.74.0"}}`,
		}, "apps/mobile/ios", "apps/mobile/android")
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"react-native"})
		rn, ok := res.Tables["react_native"]
		if !ok {
			t.Fatal("expected react_native table")
		}
		if rn["app_dir"] != "apps/mobile" {
			t.Fatalf("app_dir = %v", rn["app_dir"])
		}
	})

	t.Run("react-native node_modules trap is not the match", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"node_modules/react-native-screens/package.json": `{"dependencies": {"react-native": "0.74.0"}}`,
		}, "node_modules/react-native-screens/ios", "node_modules/react-native-screens/android")
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"simple"})
	})

	t.Run("react-native dependency without native dirs does not match", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"package.json": `{"dependencies": {"react-native-web": "0.19.0", "react-native": "0.74.0"}}`,
		})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"simple"})
	})

	t.Run("docs at root", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"package.json": `{"scripts": {"docs:dev": "vitepress dev"}}`,
		})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"docs"})
		if _, ok := res.Tables["docs"]; ok {
			t.Fatal("root docs match should not seed docs.dir")
		}
	})

	t.Run("docs under docs/", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"docs/package.json": `{"scripts": {"docs:dev": "vitepress dev"}}`,
		})
		res := Run(root)
		assertArchetypes(t, res.Archetypes, []string{"docs"})
		docs, ok := res.Tables["docs"]
		if !ok || docs["dir"] != "docs" {
			t.Fatalf("docs table = %v", res.Tables["docs"])
		}
	})
}

// TestMultiMatchOrdering exercises apple + react-native + docs together,
// checking Order's precedence (apple before react-native before docs) is
// reflected in Archetypes and in the _detector provenance string.
func TestMultiMatchOrdering(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"MyMacApp.xcodeproj/project.pbxproj": "// stub\n",
		"apps/mobile/package.json":           `{"dependencies": {"react-native": "0.74.0"}}`,
		"docs/package.json":                  `{"scripts": {"docs:dev": "vitepress dev"}}`,
	}, "apps/mobile/ios", "apps/mobile/android")

	res := Run(root)
	assertArchetypes(t, res.Archetypes, []string{"apple", "react-native", "docs"})
	if res.Detector != "apple+react-native+docs" {
		t.Fatalf("detector = %q", res.Detector)
	}
	if _, ok := res.Tables["react_native"]; !ok {
		t.Fatal("expected react_native table to survive the merge")
	}
	if _, ok := res.Tables["docs"]; !ok {
		t.Fatal("expected docs table to survive the merge")
	}
}

// TestNoMatchYieldsFallbackNotError is acceptance criterion 2: a tree no
// detector recognizes proposes an empty raw set from Propose and a
// ["simple"]/"fallback" Result from Run — never a panic or error return.
func TestNoMatchYieldsFallbackNotError(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"README.md": "just a readme\n",
	})

	archetypes, _, _, _, matched := Propose(root)
	if len(archetypes) != 0 {
		t.Fatalf("Propose archetypes = %v, want empty", archetypes)
	}
	if len(matched) != 0 {
		t.Fatalf("Propose matched = %v, want empty", matched)
	}

	res := Run(root)
	assertArchetypes(t, res.Archetypes, []string{"simple"})
	if res.Detector != "fallback" {
		t.Fatalf("detector = %q, want fallback", res.Detector)
	}
}

// TestLaterDetectorClearingProjectNameWins verifies that a later detector can
// explicitly clear project_name by returning HasProjectName=true with an empty
// ProjectName value — the presence of the key, not its truthiness, drives
// the merge.
func TestLaterDetectorClearingProjectNameWins(t *testing.T) {
	// Save original Order and Registry so we can restore them.
	origOrder := Order
	origRegistry := Registry
	defer func() {
		Order = origOrder
		Registry = origRegistry
	}()

	// Install two synthetic detectors in a test-only Order.
	firstDetector := func(root string) *Match {
		return &Match{
			Archetypes:     []string{"simple"},
			ProjectName:    "early",
			HasProjectName: true,
		}
	}
	secondDetector := func(root string) *Match {
		return &Match{
			Archetypes:     []string{"simple"},
			ProjectName:    "", // explicitly empty
			HasProjectName: true,
		}
	}

	Order = []string{"first", "second"}
	Registry = map[string]Detector{
		"first":  firstDetector,
		"second": secondDetector,
	}

	// Both Propose and Run should reflect the second detector clearing the name.
	root := t.TempDir()
	_, _, projectName, _, _ := Propose(root)
	if projectName != "" {
		t.Fatalf("Propose projectName = %q, want empty after clearing", projectName)
	}

	res := Run(root)
	if res.ProjectName != "" {
		t.Fatalf("Run ProjectName = %q, want empty after clearing", res.ProjectName)
	}
}
