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

	runner, err := makeShell("touch-marker", manifest.Action{"run": "touch marker.txt"}, nil)
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
	runner, err := makeShell("fails", manifest.Action{"run": "exit 7"}, nil)
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
	runner, err := makeShell("blank", manifest.Action{"run": "   "}, nil)
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := runner(nil); err == nil || !strings.Contains(err.Error(), emptyCmdErr) {
		t.Fatalf("runner() = %v, want an error naming %q", err, emptyCmdErr)
	}
}

// TestShellResolvesEnvBeforeTheShellSeesIt proves `run` goes through
// manifest.ResolveEnv — the same call admin_lib/core.py::run_cmd makes on
// every command string — rather than being handed to /bin/sh with its
// ${VAR} placeholders intact. Handing them over is what let
// `ssh ${UNRAID_USER:-root}@${UNRAID_HOST}` run as `ssh root@`: the shell
// expands an unset name to the empty string and executes anyway.
//
// A ${VAR} that resolves to nothing is a hard error, not an empty string.
// A body that genuinely needs a shell-local writes it unbraced ($x), which
// manifest.IterEnvRefs does not match and therefore does not touch.
func TestShellResolvesEnvBeforeTheShellSeesIt(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("MARKER_NAME", "resolved")

	runner, err := makeShell("marker", manifest.Action{
		"run": `touch "${MARKER_NAME}-${MISSING_VAR:-default}.txt"`,
	}, nil)
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "resolved-default.txt")); err != nil {
		t.Fatalf("${MARKER_NAME}/${MISSING_VAR:-default} were not expanded before the shell ran: %v", err)
	}

	// Unbraced shell-locals are left alone.
	local, err := makeShell("local", manifest.Action{
		"run": `x=ok; touch "$x-local.txt"`,
	}, nil)
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	if err := local(nil); err != nil {
		t.Fatalf("runner() = %v, want nil (unbraced $x is the shell's own)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok-local.txt")); err != nil {
		t.Fatalf("unbraced $x was not left for the shell: %v", err)
	}
}

// TestShellFailsOnUnresolvableVar is the half that makes the error useful:
// a ${VAR} with no value and no default stops the action with a message
// naming the variable, instead of running the command with a hole in it.
func TestShellFailsOnUnresolvableVar(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.Unsetenv("NOT_SET_ANYWHERE")

	runner, err := makeShell("deploy", manifest.Action{
		"run": `touch "${NOT_SET_ANYWHERE}-marker.txt"`,
	}, nil)
	if err != nil {
		t.Fatalf("makeShell: %v", err)
	}
	runErr := runner(nil)
	if runErr == nil {
		t.Fatal("runner() = nil, want an error naming the unresolved variable")
	}
	if !strings.Contains(runErr.Error(), "NOT_SET_ANYWHERE") {
		t.Errorf("error %q does not name the variable", runErr)
	}
	if _, err := os.Stat(filepath.Join(dir, "-marker.txt")); err == nil {
		t.Error("the command ran with an empty expansion instead of failing")
	}
}

// TestShellPassthroughAppendsQuotedArgs proves kind="shell-passthrough"
// appends Runner args to `run`, each shell-quoted so an argument containing
// shell metacharacters cannot be reinterpreted.
func TestShellPassthroughAppendsQuotedArgs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runner, err := makeShellPassthrough("echo-args", manifest.Action{"run": "touch"}, nil)
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
	runner, err := makeShellPassthrough("blank", manifest.Action{"run": ""}, nil)
	if err != nil {
		t.Fatalf("makeShellPassthrough: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a blank command")
	}
}
