// deps-fresh — node/bun workspace integrity (a built-in guard, also run as
// a global preflight before every command). Catches the "declared a
// `workspace:*` dependency but never linked it into node_modules" class: a
// fresh git worktree that was never installed, or a workspace dep added
// without re-running install. The symptom is an opaque bundler "failed to
// resolve import" deep in a build; this turns it into "your install is
// stale — run <pm> install" up front.
//
// The Go port of the deps-fresh half of admin_lib/guards.py.
package guards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var pmLockfiles = []struct{ file, cmd string }{
	{"bun.lock", "bun install"},
	{"bun.lockb", "bun install"},
	{"pnpm-lock.yaml", "pnpm install"},
	{"yarn.lock", "yarn install"},
	{"package-lock.json", "npm install"},
}

func pmInstallCmd(root string) string {
	for _, lf := range pmLockfiles {
		if _, err := os.Stat(filepath.Join(root, lf.file)); err == nil {
			return lf.cmd
		}
	}
	return "bun install"
}

func readJSONMap(path string) (map[string]any, bool) {
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

// workspaceGlobs normalizes a root package.json's `workspaces` field to a
// list of globs — supports both the array form and npm/bun's
// `{"packages": [...]}` form.
func workspaceGlobs(rootPkg map[string]any) []string {
	v, ok := rootPkg["workspaces"]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []any:
		return asStrings(val)
	case map[string]any:
		if packages, ok := val["packages"].([]any); ok {
			return asStrings(packages)
		}
	}
	return nil
}

func asStrings(list []any) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// pnpmGlobs parses a pnpm-workspace.yaml's top-level `packages:` block
// directly — it's a flat list of glob strings, not worth a YAML dependency
// for a tool that otherwise ships none. Only the standard block-list form
// is handled; anything else degrades to nil, matching the Python original.
func pnpmGlobs(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml"))
	if err != nil {
		return nil
	}
	var globs []string
	inPackages := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(line) == 0 || !isSpace(rune(line[0])) {
			inPackages = strings.HasPrefix(trimmed, "packages:")
			continue
		}
		if inPackages && strings.HasPrefix(trimmed, "- ") {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			item = strings.Trim(item, "'\"")
			globs = append(globs, item)
		}
	}
	return globs
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' }

// workspaceMembers is every workspace member dir holding a package.json
// (the root package included, since it can declare workspace deps too).
// Pulls globs from both package.json `workspaces` (npm/bun/yarn) and
// pnpm-workspace.yaml (pnpm). Honors npm/yarn `!`-prefixed exclusion
// patterns, and never descends into a node_modules/ (a `packages/**`-style
// glob would otherwise pull installed deps in as if they were members).
func workspaceMembers(root string, rootPkg map[string]any) []string {
	var includes, excludes []string
	for _, pat := range append(workspaceGlobs(rootPkg), pnpmGlobs(root)...) {
		if strings.HasPrefix(pat, "!") {
			excludes = append(excludes, strings.TrimPrefix(pat, "!"))
		} else {
			includes = append(includes, pat)
		}
	}

	excluded := map[string]bool{}
	for _, pat := range excludes {
		matches, _ := filepath.Glob(filepath.Join(root, pat))
		for _, m := range matches {
			excluded[m] = true
		}
	}

	members := []string{root}
	seen := map[string]bool{}
	for _, pat := range includes {
		matches, _ := filepath.Glob(filepath.Join(root, pat))
		sort.Strings(matches)
		for _, p := range matches {
			if excluded[p] || seen[p] {
				continue
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				continue
			}
			if hasNodeModulesComponent(rel) {
				continue
			}
			if info, err := os.Stat(filepath.Join(p, "package.json")); err == nil && !info.IsDir() {
				seen[p] = true
				members = append(members, p)
			}
		}
	}
	return members
}

func hasNodeModulesComponent(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
}

// linkPresent: a `workspace:*` dep is satisfied when it's linked into
// node_modules — either per-consumer or hoisted to the workspace root. We
// require an actual package.json at the link target (os.Stat follows a
// symlink), so a dangling link or a leftover same-named empty dir counts as
// missing, not satisfied.
func linkPresent(consumer, root, dep string) bool {
	for _, base := range []string{consumer, root} {
		if info, err := os.Stat(filepath.Join(base, "node_modules", dep, "package.json")); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

type missingDep struct{ consumer, dep string }

func scanMissing(root string, rootPkg map[string]any) []missingDep {
	var missing []missingDep
	for _, member := range workspaceMembers(root, rootPkg) {
		pkg, ok := readJSONMap(filepath.Join(member, "package.json"))
		if !ok {
			continue
		}
		deps := map[string]any{}
		for _, field := range []string{"dependencies", "devDependencies"} {
			if d, ok := pkg[field].(map[string]any); ok {
				for k, v := range d {
					deps[k] = v
				}
			}
		}
		for _, dep := range sortedMapKeys(deps) {
			spec, ok := deps[dep].(string)
			if !ok || !strings.HasPrefix(spec, "workspace:") {
				continue
			}
			if linkPresent(member, root, dep) {
				continue
			}
			rel := "."
			if member != root {
				if r, err := filepath.Rel(root, member); err == nil {
					rel = r
				}
			}
			missing = append(missing, missingDep{consumer: rel, dep: dep})
		}
	}
	return missing
}

func sortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// depsFreshCheck is the core of the deps-fresh guard. Returns (ok, message).
//
// root defaults to the current directory when empty. No-op pass when the
// cwd isn't a node project (no root package.json) — so it's safe to run
// globally across every archetype. For a node/bun workspace it enumerates
// each member's `workspace:*` dependencies and confirms each is linked; any
// that aren't mean the install is stale.
//
// This runs as a global preflight before every command — a bug here, or an
// unusual manifest, must never crash or block the tool, so every internal
// step degrades to a no-op pass rather than propagating an error.
func depsFreshCheck(root string) (ok bool, message string) {
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return true, ""
		}
		root = wd
	}

	defer func() {
		if r := recover(); r != nil {
			ok, message = true, ""
		}
	}()

	rootPkg, present := readJSONMap(filepath.Join(root, "package.json"))
	if !present {
		return true, ""
	}

	missing := scanMissing(root, rootPkg)
	if len(missing) == 0 {
		return true, ""
	}

	var lines []string
	for _, m := range missing {
		lines = append(lines, "  "+m.dep+"  (needed by "+m.consumer+")")
	}
	return false, "deps-fresh: workspace package(s) declared but not linked in node_modules — " +
		"your install is stale (a fresh worktree, or a workspace dep added without " +
		"reinstalling):\n" + strings.Join(lines, "\n") +
		"\nFix:  " + pmInstallCmd(root)
}
