package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestShellRunsAndSucceeds proves kind="shell" actually executes its `run`
// string against the real shell: a command that writes a marker file is run,
// and the Runner returns nil (success) only when that file exists.
func TestShellRunsAndSucceeds(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeShell("touch-marker", manifest.Action{"run": "touch marker.txt"})
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("marker.txt was not created by the shell action: %v", err)
	}
}

// TestShellNonZeroExitIsError is this ticket's acceptance criterion "a
// non-zero exit from an action produces a non-zero result" for kind="shell"
// specifically: `exit 7` must come back as a non-nil error.
func TestShellNonZeroExitIsError(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeShell("fails", manifest.Action{"run": "exit 7"})
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a nonzero exit")
	}
}

// TestShellEmptyRunIsRefused proves a blank `run` is rejected rather than
// silently treated as success (an empty /bin/sh -c "" exits 0).
func TestShellEmptyRunIsRefused(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeShell("blank", manifest.Action{"run": "   "})
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := runner(nil); err == nil || !strings.Contains(err.Error(), emptyCmdErr) {
		t.Fatalf("runner() = %v, want an error naming %q", err, emptyCmdErr)
	}
}

// TestShellResolvesComputedNotEnv proves `run` goes through
// manifest.ResolveComputed rather than manifest.ResolveEnv: a shell-local
// reference the shell itself can expand (here simulated by an env var this
// process sets, since ResolveComputed only special-cases ComputedVars and
// otherwise passes ${NAME} straight through to the shell) must reach the
// shell as a literal placeholder, not be resolved (or fatally error) by the
// Go layer first.
//
// MUTATION EVIDENCE: with makeShell's ResolveComputed call swapped for
// ResolveEnv, this test fails — a ${NOT_SET_ANYWHERE} placeholder with no
// default and no matching environment variable makes ResolveEnv return an
// error, so the Runner returned by the mutated factory never runs the shell
// at all and the marker file this test checks for is never created.
func TestShellResolvesComputedNotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeShell("passthrough", manifest.Action{
		"run": `NOT_SET_ANYWHERE=ok; touch "${NOT_SET_ANYWHERE}-marker.txt"`,
	})
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil (the shell resolves its own local var)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok-marker.txt")); err != nil {
		t.Fatalf("shell-local ${NOT_SET_ANYWHERE} was not left for the shell to expand: %v", err)
	}
}

// TestShellPassthroughAppendsQuotedArgs proves kind="shell-passthrough"
// appends Runner args to `run`, each shell-quoted so an argument containing
// shell metacharacters cannot be reinterpreted.
func TestShellPassthroughAppendsQuotedArgs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeShellPassthrough("echo-args", manifest.Action{"run": "touch"})
	if err != nil {
		t.Fatalf("makeShellPassthrough: %v", err)
	}
	// An argument with a space and a semicolon: if shellQuote failed to quote
	// it, the shell would split it into two words / two commands instead of
	// one filename.
	if err := runner([]string{"weird; name.txt"}); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "weird; name.txt")); err != nil {
		t.Fatalf("quoted argument did not create the expected single file: %v", err)
	}
}

// TestShellPassthroughEmptyIsRefused mirrors TestShellEmptyRunIsRefused for
// the passthrough variant.
func TestShellPassthroughEmptyIsRefused(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeShellPassthrough("blank", manifest.Action{"run": ""})
	if err != nil {
		t.Fatalf("makeShellPassthrough: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a blank command")
	}
}
