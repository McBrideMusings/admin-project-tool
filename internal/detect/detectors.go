package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// searchable reports whether match (an absolute path under root) is NOT
// nested under any of the given vendored directory names, nor under a
// hidden (dot-prefixed) directory — checked against every path component
// between root and match's parent, exclusive of match's own basename. Ports
// the identical _searchable() helper duplicated (with different vendored
// sets) in detectors/apple.py and detectors/react-native.py.
func searchable(match, root string, vendored map[string]bool) bool {
	rel, err := filepath.Rel(root, match)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 {
		return true
	}
	for _, p := range parts[:len(parts)-1] {
		if vendored[p] || strings.HasPrefix(p, ".") {
			return false
		}
	}
	return true
}

// toRelPosix converts an absolute path under root to a POSIX-style relative
// path.
func toRelPosix(abs, root string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// sortedGlob is filepath.Glob with the ErrBadPattern case treated as no
// matches. filepath.Glob already returns lexically sorted matches.
func sortedGlob(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	return matches
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func readJSONObject(path string) (map[string]any, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false
	}
	return out, true
}

// ---------------------------------------------------------------------------
// apple
// ---------------------------------------------------------------------------

// Vendored / generated dirs that ship their own .xcodeproj/.xcworkspace — the
// Xcode analogue of node_modules for react-native. Ports detectors/apple.py.
var appleVendored = map[string]bool{
	"node_modules": true,
	"Pods":         true,
	"Carthage":     true,
	"DerivedData":  true,
	".build":       true,
}

func detectApple(root string) *Match {
	// Depth-ordered (root first), .xcworkspace preferred over .xcodeproj at
	// each depth within a single dir.
	for _, depth := range []string{"", "*/", "*/*/"} {
		for _, suffix := range []string{"xcworkspace", "xcodeproj"} {
			pattern := filepath.Join(root, depth+"*."+suffix)
			matches := sortedGlob(pattern)
			var filtered []string
			for _, m := range matches {
				if searchable(m, root, appleVendored) {
					filtered = append(filtered, m)
				}
			}
			if len(filtered) == 0 {
				continue
			}
			match := filtered[0]
			rel := toRelPosix(match, root)
			out := &Match{Archetypes: []string{"apple"}}
			if rel != filepath.Base(match) {
				// Only hint project_path when nested — a root-level project
				// is what _detect_project_path already finds on its own.
				out.Tables = map[string]map[string]string{
					"apple": map[string]string{"project_path": rel},
				}
			}
			return out
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// react-native
// ---------------------------------------------------------------------------

func isRNPackage(pkgJSON string) bool {
	data, ok := readJSONObject(pkgJSON)
	if !ok {
		return false
	}
	for _, key := range []string{"dependencies", "devDependencies"} {
		deps, ok := data[key].(map[string]any)
		if !ok {
			continue
		}
		if _, ok := deps["react-native"]; ok {
			return true
		}
	}
	return false
}

// react-native's node_modules trap: half the RN ecosystem
// (react-native-screens, react-native-reanimated, …) declares react-native
// in devDependencies AND ships an ios/ dir, so a dependency satisfies every
// test a real app does. Sorted order even puts node_modules/ ahead of
// apps/ and packages/, so the first match would be a library rather than
// the app being looked for — hence node_modules is excluded outright rather
// than merely deprioritized.
var reactNativeVendored = map[string]bool{"node_modules": true}

func detectReactNative(root string) *Match {
	var candidates []string
	candidates = append(candidates, filepath.Join(root, "package.json"))
	candidates = append(candidates, sortedGlob(filepath.Join(root, "*", "package.json"))...)
	candidates = append(candidates, sortedGlob(filepath.Join(root, "*", "*", "package.json"))...)

	for _, pkg := range candidates {
		if !searchable(pkg, root, reactNativeVendored) {
			continue
		}
		if !isRNPackage(pkg) {
			continue
		}
		// An RN app has a native ios/ or android/ dir beside its
		// package.json; without one this is a react-native dependency in
		// some other package (a shared component library, a web app
		// pulling in react-native-web), which needs no Metro lifecycle and
		// no archetype.
		appDir := filepath.Dir(pkg)
		if !isDir(filepath.Join(appDir, "ios")) && !isDir(filepath.Join(appDir, "android")) {
			continue
		}
		rel := toRelPosix(appDir, root)
		if rel == "" || rel == "." {
			rel = "."
		}
		return &Match{
			Archetypes: []string{"react-native"},
			Tables: map[string]map[string]string{
				"react_native": map[string]string{"app_dir": rel},
			},
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// hugo
// ---------------------------------------------------------------------------

// Vendored / generated dirs that can hold Hugo-shaped files but aren't the
// site.
var hugoVendored = map[string]bool{
	"node_modules": true,
	"public":       true,
	"resources":    true,
	"themes":       true,
}

var hugoConfigExts = []string{"yaml", "yml", "toml", "json"}

func hasConfig(dir, stem string) bool {
	for _, ext := range hugoConfigExts {
		if exists(filepath.Join(dir, stem+"."+ext)) {
			return true
		}
	}
	return false
}

func isHugo(dir string) bool {
	hasHugoNamedConfig := hasConfig(dir, "hugo")
	hasGenericConfig := hasConfig(dir, "config")
	hasThemes := isDir(filepath.Join(dir, "themes"))
	hasConfigDir := isDir(filepath.Join(dir, "config", "_default"))
	hasLock := exists(filepath.Join(dir, ".hugo_build.lock"))
	return hasHugoNamedConfig || (hasGenericConfig && hasThemes) || hasConfigDir || hasLock
}

func detectHugo(root string) *Match {
	if isHugo(root) {
		return &Match{Archetypes: []string{"hugo"}}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		sub := filepath.Join(root, name)
		if !isDir(sub) || strings.HasPrefix(name, ".") || hugoVendored[name] {
			continue
		}
		if isHugo(sub) {
			return &Match{Archetypes: []string{"hugo"}}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// go-cli / rust / docker / cloudflare-workers / unraid-plugin
// ---------------------------------------------------------------------------

func detectGoCLI(root string) *Match {
	if exists(filepath.Join(root, "go.mod")) && !exists(filepath.Join(root, "Dockerfile")) {
		return &Match{Archetypes: []string{"go-cli"}}
	}
	return nil
}

func detectRust(root string) *Match {
	if exists(filepath.Join(root, "Cargo.toml")) && !exists(filepath.Join(root, "Dockerfile")) {
		return &Match{Archetypes: []string{"rust"}}
	}
	return nil
}

func detectDocker(root string) *Match {
	if exists(filepath.Join(root, "Dockerfile")) {
		return &Match{Archetypes: []string{"docker"}}
	}
	return nil
}

func detectCloudflareWorkers(root string) *Match {
	for _, ext := range []string{"jsonc", "json", "toml"} {
		if exists(filepath.Join(root, "wrangler."+ext)) {
			return &Match{Archetypes: []string{"cloudflare-workers"}}
		}
	}
	return nil
}

func detectUnraidPlugin(root string) *Match {
	if len(sortedGlob(filepath.Join(root, "*.plg"))) > 0 {
		return &Match{Archetypes: []string{"unraid-plugin"}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// rust-tauri
// ---------------------------------------------------------------------------

// Bounded patterns anchored on src-tauri/ — avoids traversing Cargo's
// target/ dir. Covers root-level, one-deep (apps/desktop/), and two-deep
// layouts.
var tauriPatterns = []string{
	filepath.Join("src-tauri", "tauri.conf.json"),
	filepath.Join("*", "src-tauri", "tauri.conf.json"),
	filepath.Join("*", "*", "src-tauri", "tauri.conf.json"),
}

func detectRustTauri(root string) *Match {
	if !exists(filepath.Join(root, "Cargo.toml")) {
		return nil
	}
	for _, pattern := range tauriPatterns {
		if len(sortedGlob(filepath.Join(root, pattern))) > 0 {
			return &Match{Archetypes: []string{"rust-tauri"}}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// firefox-extension
// ---------------------------------------------------------------------------

func detectFirefoxExtension(root string) *Match {
	m := filepath.Join(root, "manifest.json")
	if !exists(m) {
		return nil
	}
	data, ok := readJSONObject(m)
	if !ok {
		return nil
	}
	if _, ok := data["browser_specific_settings"]; ok {
		return &Match{Archetypes: []string{"firefox-extension"}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// docs
// ---------------------------------------------------------------------------

const docsScript = "docs:dev"

func declaresDocsScript(pkg string) bool {
	data, ok := readJSONObject(pkg)
	if !ok {
		return false
	}
	scripts, ok := data["scripts"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = scripts[docsScript]
	return ok
}

func detectDocs(root string) *Match {
	if declaresDocsScript(filepath.Join(root, "package.json")) {
		return &Match{Archetypes: []string{"docs"}}
	}
	if declaresDocsScript(filepath.Join(root, "docs", "package.json")) {
		return &Match{
			Archetypes: []string{"docs"},
			Tables: map[string]map[string]string{
				"docs": map[string]string{"dir": "docs"},
			},
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// simple (fallback)
// ---------------------------------------------------------------------------

func detectSimple(root string) *Match {
	return &Match{Archetypes: []string{"simple"}}
}

// ---------------------------------------------------------------------------
// forge-engine
// ---------------------------------------------------------------------------

func detectForgeEngine(root string) *Match {
	if exists(filepath.Join(root, "engine", "build.zig")) && isDir(filepath.Join(root, "sample-games")) {
		return &Match{Archetypes: []string{"forge-engine"}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// raylib-game
// ---------------------------------------------------------------------------

var raylibProjectRe = regexp.MustCompile(`(?im)^\s*project\s*\(\s*([A-Za-z0-9_.-]+)`)

func detectRaylibGame(root string) *Match {
	cmakelists := filepath.Join(root, "CMakeLists.txt")
	if !isFile(cmakelists) {
		return nil
	}
	if exists(filepath.Join(root, "engine", "build.zig")) {
		return nil // the engine repo, not a game built on it
	}
	data, err := os.ReadFile(cmakelists)
	if err != nil {
		return nil
	}
	text := string(data)
	if !strings.Contains(strings.ToLower(text), "raylib") {
		return nil
	}

	out := &Match{Archetypes: []string{"raylib-game"}}
	if m := raylibProjectRe.FindStringSubmatch(text); m != nil {
		out.Tables = map[string]map[string]string{
			"raylib": map[string]string{"binary": m[1]},
		}
	}
	return out
}
