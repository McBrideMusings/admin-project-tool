package kinds

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func haveGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
}

// TestGoInvalidModeRejectedAtBuildTime proves an unknown mode fails when the
// factory builds the Runner (a programming error caught early), not merely
// when the Runner is invoked.
func TestGoInvalidModeRejectedAtBuildTime(t *testing.T) {
	_, err := makeGoKind("weird", manifest.Action{"run": "fly"})
	if err == nil {
		t.Fatal("makeGoKind with an invalid mode should error at build time")
	}
	if !strings.Contains(err.Error(), "fly") {
		t.Errorf("error %q should name the offending mode", err.Error())
	}
}

// TestGoDefaultModeIsBuild proves an action with no `run` defaults to mode
// "build" rather than erroring — matching admin_lib/interp.py::_make_go's
// `(cmd.run or "build").strip()`.
//
// MUTATION EVIDENCE: replace makeGoKind's `if mode == "" { mode = "build" }`
// default with a hard error on empty mode, and this test fails: makeGoKind
// returns a non-nil error where it should return nil.
func TestGoDefaultModeIsBuild(t *testing.T) {
	_, err := makeGoKind("default", manifest.Action{})
	if err != nil {
		t.Fatalf("makeGoKind with no run should default to build, got error: %v", err)
	}
}

// TestGoBuildRunsRealGoBuild proves kind="go" mode=build actually shells out
// to `go build` against a real module, using -o to produce a named binary —
// this exercises the real toolchain end to end rather than mocking exec.
func TestGoBuildRunsRealGoBuild(t *testing.T) {
	haveGo(t)
	dir := t.TempDir()
	t.Chdir(dir)

	writeFile(t, dir, "go.mod", "module example.com/gobuildtest\n\ngo 1.21\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	runner, err := makeGoKind("build", manifest.Action{"run": "build", "binary": "out/bin"})
	if err != nil {
		t.Fatalf("makeGoKind: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	assertFileExists(t, dir, "out/bin")
}

// TestGoBuildFailureIsError proves a compile error in the target package
// surfaces as a non-nil Runner error.
func TestGoBuildFailureIsError(t *testing.T) {
	haveGo(t)
	dir := t.TempDir()
	t.Chdir(dir)

	writeFile(t, dir, "go.mod", "module example.com/gobuildfail\n\ngo 1.21\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() { this is not go }\n")

	runner, err := makeGoKind("build", manifest.Action{"run": "build"})
	if err != nil {
		t.Fatalf("makeGoKind: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a package that fails to compile")
	}
}
