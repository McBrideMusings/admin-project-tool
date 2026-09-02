package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// TestMain resets the process-wide log context (internal/luahost.Process())
// to a package-level temp directory before any test in this package runs.
//
// Since admin-rc82 made runSteps name a log unconditionally
// (dispatch.go's nameCommandLog), any test that dispatches a command through
// RunCommand/RunCommandHosted materializes a file the instant a step writes
// its first line. Without this, such a test would inherit whatever LogDir
// the process-wide singleton happened to have from process start (this
// package's own source directory, since luahost.Process() lazily roots
// itself at os.Getwd() on first use) — this is what keeps that stray write
// out of the source tree.
//
// **A t.Chdir into a t.TempDir() is not isolation, and must not be relied on
// as any.** It used to be: Configure re-rooted AND cleared the context, and
// nameCommandLog called Configure on every dispatch, so each test's first
// command silently wiped the previous test's materialized state. That
// clearing was the admin-rc82.1 bug — it also fired on the nested
// surf.Command hop, splitting one real run across two log files — and it is
// gone. A test that asserts on which files exist under a LogDir now calls
// resetProcessLogContext(t), or luahost.ResetProcessLogContext(dir) when it
// needs the dir it already chdir'd into.
//
// It also points archetype.InstalledArchetypesDir at an empty temp
// directory, since DispatchTargets calls config.Load and would otherwise
// merge whatever base archetype is installed on the machine running the
// test (~/.admin/archetypes/, symlinked to a checkout's archetypes/ on some
// machines).
func TestMain(m *testing.M) {
	luahost.ResetProcessLogContext(os.TempDir())
	archDir, err := os.MkdirTemp("", "admin-project-tool-hermetic-kinds-")
	if err != nil {
		panic(err)
	}
	restore := archetype.UseInstalledDir(archDir)
	code := m.Run()
	restore()
	os.RemoveAll(archDir)
	os.Exit(code)
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

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it, restoring the original os.Stderr afterward.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

// withFakeTerminal builds the raw [terminal] table a manifest.Manifest.
// Terminal field carries (ADR-0020) — the launcher-dispatch sibling of
// withFakeHerdr, for the [terminal] contract rather than herdr. spawn
// prints handle and appends "spawn <ADMIN_CMD>" to logPath; attach/kill
// append "attach <ADMIN_HANDLE>"/"kill <ADMIN_HANDLE>".
func withFakeTerminal(t *testing.T, handle, logPath string) map[string]any {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake terminal scripts are POSIX shell")
	}
	return map[string]any{
		"spawn":  fmt.Sprintf(`echo "spawn $ADMIN_CMD" >> %s; echo %s`, shellQuote(logPath), shellQuote(handle)),
		"attach": fmt.Sprintf(`echo "attach $ADMIN_HANDLE" >> %s`, shellQuote(logPath)),
		"kill":   fmt.Sprintf(`echo "kill $ADMIN_HANDLE" >> %s`, shellQuote(logPath)),
	}
}
