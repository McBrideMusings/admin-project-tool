package guards

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDepsFreshNoPackageJSONPasses proves the no-op pass on a non-node
// project — safe to run globally across every archetype.
func TestDepsFreshNoPackageJSONPasses(t *testing.T) {
	dir := t.TempDir()
	ok, msg := depsFreshCheck(dir)
	if !ok {
		t.Fatalf("depsFreshCheck(no package.json) = (false, %q), want (true, _)", msg)
	}
}

// TestDepsFreshMissingLinkFails proves a workspace:* dependency declared
// but not linked into node_modules is caught, naming both the dependency
// and its consumer.
func TestDepsFreshMissingLinkFails(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{"workspaces": ["packages/*"]}`)
	mustWrite(t, filepath.Join(dir, "packages", "app", "package.json"),
		`{"name": "app", "dependencies": {"shared": "workspace:*"}}`)

	ok, msg := depsFreshCheck(dir)
	if ok {
		t.Fatal("depsFreshCheck() = true, want false: 'shared' is declared but not linked")
	}
	if !strings.Contains(msg, "shared") || !strings.Contains(msg, "app") {
		t.Errorf("message %q should name the dependency and its consumer", msg)
	}
	if !strings.Contains(msg, "install") {
		t.Errorf("message %q should suggest the fix", msg)
	}
}

// TestDepsFreshLinkedPasses proves the same shape passes once the
// dependency is linked (a package.json under node_modules/<dep>).
//
// MUTATION EVIDENCE: change linkPresent's `!info.IsDir()` to `info.IsDir()`
// (require a directory rather than a file at node_modules/<dep>/package.json)
// and re-run — the real failure was:
//
//	deps_fresh_test.go:64: depsFreshCheck() = (false, "deps-fresh: workspace
//	package(s) declared but not linked..."), want (true, _)
//
// i.e. the mutant can never see a real (file) package.json as "linked", so
// every workspace dependency reads as missing even when correctly
// installed — proving the test exercises the actual link-detection branch.
func TestDepsFreshLinkedPasses(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{"workspaces": ["packages/*"]}`)
	mustWrite(t, filepath.Join(dir, "packages", "app", "package.json"),
		`{"name": "app", "dependencies": {"shared": "workspace:*"}}`)
	mustWrite(t, filepath.Join(dir, "packages", "app", "node_modules", "shared", "package.json"),
		`{"name": "shared"}`)

	ok, msg := depsFreshCheck(dir)
	if !ok {
		t.Fatalf("depsFreshCheck() = (false, %q), want (true, _)", msg)
	}
}

// TestDepsFreshHoistedLinkPasses proves a dependency hoisted to the
// workspace root's node_modules (rather than the consumer's own) also
// counts as linked.
func TestDepsFreshHoistedLinkPasses(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{"workspaces": ["packages/*"]}`)
	mustWrite(t, filepath.Join(dir, "packages", "app", "package.json"),
		`{"name": "app", "dependencies": {"shared": "workspace:*"}}`)
	mustWrite(t, filepath.Join(dir, "node_modules", "shared", "package.json"), `{"name": "shared"}`)

	ok, msg := depsFreshCheck(dir)
	if !ok {
		t.Fatalf("depsFreshCheck() = (false, %q), want (true, _) — hoisted to the root", msg)
	}
}

// TestDepsFreshExcludesNodeModulesFromMembers proves a workspace glob that
// would otherwise sweep in node_modules (e.g. "packages/**") never treats an
// installed dependency as if it were a workspace member.
func TestDepsFreshExcludesNodeModulesFromMembers(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{"workspaces": ["packages/*"]}`)
	mustWrite(t, filepath.Join(dir, "packages", "app", "package.json"), `{"name": "app"}`)
	// An installed dependency nested where a naive glob might also match.
	mustWrite(t, filepath.Join(dir, "packages", "app", "node_modules", "leftover", "package.json"),
		`{"name": "leftover", "dependencies": {"missing": "workspace:*"}}`)

	ok, msg := depsFreshCheck(dir)
	if !ok {
		t.Fatalf("depsFreshCheck() = (false, %q), want (true, _) — node_modules is not a member", msg)
	}
}

// TestDepsFreshPnpmWorkspaceYaml proves the hand-rolled pnpm-workspace.yaml
// parser recognizes the standard block-list `packages:` form.
func TestDepsFreshPnpmWorkspaceYaml(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{}`)
	mustWrite(t, filepath.Join(dir, "pnpm-workspace.yaml"), "packages:\n  - 'packages/*'\n")
	mustWrite(t, filepath.Join(dir, "packages", "app", "package.json"),
		`{"name": "app", "dependencies": {"shared": "workspace:*"}}`)

	ok, msg := depsFreshCheck(dir)
	if ok {
		t.Fatal("depsFreshCheck() = true, want false: pnpm-workspace.yaml member should be scanned")
	}
	if !strings.Contains(msg, "shared") {
		t.Errorf("message %q should name the missing dependency", msg)
	}
}

// TestDepsFreshExclusionPattern proves a `!`-prefixed exclusion glob removes
// a member that would otherwise match.
func TestDepsFreshExclusionPattern(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"),
		`{"workspaces": ["packages/*", "!packages/excluded"]}`)
	mustWrite(t, filepath.Join(dir, "packages", "excluded", "package.json"),
		`{"name": "excluded", "dependencies": {"missing": "workspace:*"}}`)

	ok, msg := depsFreshCheck(dir)
	if !ok {
		t.Fatalf("depsFreshCheck() = (false, %q), want (true, _) — excluded member is not scanned", msg)
	}
}

// TestDepsFreshNeverPanicsOnMalformedPackageJSON proves the fail-open
// contract: a malformed package.json degrades to a pass rather than
// crashing the caller, since this runs as a global preflight before every
// command.
func TestDepsFreshNeverPanicsOnMalformedPackageJSON(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{ not valid json`)

	ok, msg := depsFreshCheck(dir)
	if !ok {
		t.Fatalf("depsFreshCheck(malformed package.json) = (false, %q), want (true, _) (fail-open)", msg)
	}
}
