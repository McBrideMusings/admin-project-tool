package guards

import (
	"path/filepath"
	"testing"
)

func brokenWorkspaceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{"workspaces": ["packages/*"]}`)
	mustWrite(t, filepath.Join(dir, "packages", "app", "package.json"),
		`{"name": "app", "dependencies": {"shared": "workspace:*"}}`)
	return dir
}

// TestRunGlobalPreflightFailsOnStaleInstall proves the always-on check
// actually blocks when the workspace is stale.
func TestRunGlobalPreflightFailsOnStaleInstall(t *testing.T) {
	t.Cleanup(ResetPreflight)
	dir := brokenWorkspaceDir(t)
	t.Chdir(dir)
	ResetPreflight()

	if err := RunGlobalPreflight("build"); err == nil {
		t.Fatal("RunGlobalPreflight(build) = nil, want an error: workspace dep is unlinked")
	}
}

// TestRunGlobalPreflightMemoizesTheVerdict is the acceptance criterion "the
// preflight runs once per invocation regardless of nested dispatch": fixing
// the workspace after the first (failing) call must NOT change the second
// call's answer within the same invocation — only ResetPreflight starts a
// new one.
//
// MUTATION EVIDENCE: remove the `if preflightRan { return preflightResult }`
// early return (always re-run every check) and re-run — the real failure
// was:
//
//	preflight_test.go:47: RunGlobalPreflight(build) [2nd call] = <nil>,
//	want the same cached failure as the 1st call (memoized)
//
// i.e. the mutant re-scans the now-fixed workspace and passes, proving the
// test catches a preflight that forgot to memoize.
func TestRunGlobalPreflightMemoizesTheVerdict(t *testing.T) {
	t.Cleanup(ResetPreflight)
	dir := brokenWorkspaceDir(t)
	t.Chdir(dir)
	ResetPreflight()

	if err := RunGlobalPreflight("build"); err == nil {
		t.Fatal("RunGlobalPreflight(build) [1st call] = nil, want the workspace failure")
	}

	// Fix the workspace without resetting the memoized verdict.
	mustWrite(t, filepath.Join(dir, "packages", "app", "node_modules", "shared", "package.json"),
		`{"name": "shared"}`)

	if err := RunGlobalPreflight("build"); err == nil {
		t.Fatal("RunGlobalPreflight(build) [2nd call] = <nil>, want the same cached failure as the 1st call (memoized)")
	}

	ResetPreflight()
	if err := RunGlobalPreflight("build"); err != nil {
		t.Fatalf("RunGlobalPreflight(build) after ResetPreflight = %v, want nil (workspace is now fixed)", err)
	}
}

// TestRunGlobalPreflightExemptVerbsSkip is the acceptance criterion "the
// preflight ... is skipped for the diagnostic verbs": logs/kill must pass
// even against a broken workspace, and even without a prior ResetPreflight,
// since exemption is checked before the memoized verdict.
func TestRunGlobalPreflightExemptVerbsSkip(t *testing.T) {
	t.Cleanup(ResetPreflight)
	dir := brokenWorkspaceDir(t)
	t.Chdir(dir)
	ResetPreflight()

	for _, verb := range []string{"logs", "kill"} {
		if err := RunGlobalPreflight(verb); err != nil {
			t.Errorf("RunGlobalPreflight(%s) = %v, want nil — %s is an exempt diagnostic verb", verb, err, verb)
		}
	}
}
