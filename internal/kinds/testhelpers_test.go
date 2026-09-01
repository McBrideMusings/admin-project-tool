package kinds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// TestMain resets the process-wide log context (internal/luahost.Process())
// to a package-level temp directory before any test in this package runs.
//
// Since admin-rc82 made runSteps name a log unconditionally
// (dispatch.go's nameCommandLog), any test that dispatches a command through
// RunCommand/RunCommandHosted materializes a file the instant a step writes
// its first line. Most such tests already t.Chdir into their own
// t.TempDir() before dispatching, which re-Configures the shared context
// safely — but a test that reaches a kind factory's newHost() WITHOUT going
// through runSteps first (calling RunAction directly, as several
// multi-target/frame tests do) never calls Configure at all, and would
// otherwise inherit whatever LogDir the process-wide singleton happened to
// have from process start (this package's own source directory, since
// luahost.Process() lazily roots itself at os.Getwd() on first use) — this
// is what keeps that stray write out of the source tree.
func TestMain(m *testing.M) {
	luahost.ResetProcessLogContext(os.TempDir())
	os.Exit(m.Run())
}

// resetProcessLogContext points the process-wide log context at t's own
// temp dir for the duration of t — a per-test seam for any test that wants
// tighter isolation than TestMain's package-wide default (e.g. asserting
// exactly what landed under a specific LogDir).
func resetProcessLogContext(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	luahost.ResetProcessLogContext(dir)
	return dir
}

// writeFile writes content to dir/relPath, creating any needed parent
// directories, and fails the test on error.
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", full, err)
	}
}

// assertFileExists fails the test unless dir/relPath exists.
func assertFileExists(t *testing.T, dir, relPath string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("expected %s to exist: %v", full, err)
	}
}

// mustReadFile reads dir/relPath, failing the test on error.
func mustReadFile(t *testing.T, dir, relPath string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", filepath.Join(dir, relPath), err)
	}
	return data
}

// assertFileMissing fails the test if dir/relPath exists.
func assertFileMissing(t *testing.T, dir, relPath string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if _, err := os.Stat(full); err == nil {
		t.Fatalf("expected %s to NOT exist", full)
	}
}
