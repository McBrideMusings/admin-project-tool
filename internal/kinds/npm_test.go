package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestNpmMissingRunIsRejected proves kind="npm" requires 'run' at build
// time.
func TestNpmMissingRunIsRejected(t *testing.T) {
	if _, err := makeNpm("bad", manifest.Action{}, nil, nil); err == nil {
		t.Fatal("makeNpm with no 'run' should error")
	}
}

// fakeNpmScript writes an executable shell script named "npm" onto a fresh
// PATH-only directory, so the test exercises `pkg run <script>` for real
// without depending on a real npm/bun install or network access.
func fakeNpmScript(t *testing.T, dir, body string) {
	t.Helper()
	script := filepath.Join(dir, "npm")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestNpmRunsPickedScript proves the Runner actually invokes `npm run
// <script>` — verified via a fake `npm` on PATH that records its own
// argv into a marker file, since asserting on a real npm/bun call would
// require network access this test suite must not depend on.
func TestNpmRunsPickedScript(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	binDir := t.TempDir()
	fakeNpmScript(t, binDir, `echo "$@" > argv.txt`)

	runner, err := makeNpm("build", manifest.Action{"run": "build"}, nil, nil)
	if err != nil {
		t.Fatalf("makeNpm: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "argv.txt"))
	if err != nil {
		t.Fatalf("expected argv.txt from the fake npm: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "run build" {
		t.Errorf("fake npm saw argv %q, want %q", got, "run build")
	}
}

// TestNpmPicksBunWhenLockfilePresent proves pkgRunner prefers bun when a bun
// lockfile sits in the given directory.
//
// MUTATION EVIDENCE: hard-code pkgRunner to always return "npm", and this
// test fails — the fake "bun" script is never invoked, so bun-marker.txt is
// never created and the os.Stat below returns an error.
func TestNpmPicksBunWhenLockfilePresent(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	binDir := t.TempDir()
	bunScript := filepath.Join(binDir, "bun")
	if err := os.WriteFile(bunScript, []byte("#!/bin/sh\ntouch bun-marker.txt\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner, err := makeNpm("dev", manifest.Action{"run": "dev"}, nil, nil)
	if err != nil {
		t.Fatalf("makeNpm: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bun-marker.txt")); err != nil {
		t.Fatalf("bun was not invoked despite bun.lock present: %v", err)
	}
}

// TestNpmNonZeroExitIsError proves a failing script surfaces as a non-nil
// error.
func TestNpmNonZeroExitIsError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	binDir := t.TempDir()
	fakeNpmScript(t, binDir, "exit 3")

	runner, err := makeNpm("broken", manifest.Action{"run": "broken"}, nil, nil)
	if err != nil {
		t.Fatalf("makeNpm: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a nonzero exit")
	}
}
