package detect

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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
		// first in Order, so both archetypes are proposed — same union
		// behavior as admin_lib.detectors.detect() on this exact fixture.
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
		raylib, _ := m.Tables["raylib"].(map[string]any)
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
		apple, ok := res.Tables["apple"].(map[string]any)
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
		rn, ok := res.Tables["react_native"].(map[string]any)
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
		docs, ok := res.Tables["docs"].(map[string]any)
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

// ---------------------------------------------------------------------------
// parity with the Python detectors
// ---------------------------------------------------------------------------

// repoRoot walks up from the current test file's directory looking for
// go.mod, returning "" if none is found (e.g. run from a stripped-down
// checkout).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// TestParityWithPythonDetectors shells out to the reference admin_lib
// detector loader against the same fixture trees the Go tests exercise and
// diffs the archetype sets, the strongest evidence this port matches
// today's behaviour. It skips cleanly (never fails) when the repo root or
// its .venv isn't available, so `go test ./...` stays hermetic on a fresh
// checkout or the installed ~/.admin copy.
func TestParityWithPythonDetectors(t *testing.T) {
	root := repoRoot(t)
	if root == "" {
		t.Skip("repo root (go.mod) not found — skipping Python parity check")
	}
	python := filepath.Join(root, ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		t.Skip(".venv/bin/python not found — skipping Python parity check")
	}

	cases := []struct {
		name  string
		files map[string]string
		dirs  []string
	}{
		{"empty", nil, nil},
		{"go-cli", map[string]string{"go.mod": "module x\n"}, nil},
		{"go-docker", map[string]string{"go.mod": "module x\n", "Dockerfile": "FROM scratch\n"}, nil},
		{"rust", map[string]string{"Cargo.toml": "[package]\nname=\"x\"\n"}, nil},
		{"hugo-nested", map[string]string{"site/hugo.toml": "baseURL=\"/\"\n"}, nil},
		{"cloudflare", map[string]string{"wrangler.toml": "name=\"x\"\n"}, nil},
		{"unraid", map[string]string{"myplugin.plg": "<PLUGIN/>\n"}, nil},
		{
			"firefox",
			map[string]string{"manifest.json": `{"manifest_version": 2, "browser_specific_settings": {"gecko": {"id": "x@example.com"}}}`},
			nil,
		},
		{
			"apple-nested",
			nil,
			[]string{"ios/MyApp.xcodeproj"},
		},
		{
			"react-native",
			map[string]string{"apps/mobile/package.json": `{"dependencies": {"react-native": "0.74.0"}}`},
			[]string{"apps/mobile/ios", "apps/mobile/android"},
		},
		{
			"docs-nested",
			map[string]string{"docs/package.json": `{"scripts": {"docs:dev": "vitepress dev"}}`},
			nil,
		},
		{
			"multi-match",
			map[string]string{
				"MyMacApp.xcodeproj/project.pbxproj": "// stub\n",
				"apps/mobile/package.json":           `{"dependencies": {"react-native": "0.74.0"}}`,
				"docs/package.json":                  `{"scripts": {"docs:dev": "vitepress dev"}}`,
			},
			[]string{"apps/mobile/ios", "apps/mobile/android"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fixture := writeFixture(t, c.files, c.dirs...)

			goResult := Run(fixture)

			script := `
import json, sys
sys.path.insert(0, ` + pyStr(root) + `)
from pathlib import Path
from admin_lib import detectors
result = detectors.detect(Path(` + pyStr(fixture) + `), Path(` + pyStr(filepath.Join(root, "detectors")) + `))
print(json.dumps(sorted(result.get("archetypes", []))))
`
			cmd := exec.Command(python, "-c", script)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("python detector run failed: %v\nstderr: %s", err, stderr.String())
			}

			var pyArchetypes []string
			if err := json.Unmarshal(stdout.Bytes(), &pyArchetypes); err != nil {
				t.Fatalf("parsing python output %q: %v", stdout.String(), err)
			}

			goSorted := sortedCopy(goResult.Archetypes)
			pySorted := sortedCopy(pyArchetypes)
			if len(goSorted) != len(pySorted) {
				t.Fatalf("archetype set mismatch: go=%v python=%v", goSorted, pySorted)
			}
			for i := range goSorted {
				if goSorted[i] != pySorted[i] {
					t.Fatalf("archetype set mismatch: go=%v python=%v", goSorted, pySorted)
				}
			}
		})
	}
}

// pyStr renders s as a double-quoted Python string literal for embedding in
// the -c script above.
func pyStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
