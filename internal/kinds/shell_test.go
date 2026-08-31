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

	runner, err := makeShell("touch-marker", manifest.Action{"run": "touch marker.txt"}, nil, nil)
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
	runner, err := makeShell("fails", manifest.Action{"run": "exit 7"}, nil, nil)
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
	runner, err := makeShell("blank", manifest.Action{"run": "   "}, nil, nil)
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
	}, nil, nil)
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
	}, nil, nil)
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
	}, nil, nil)
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

	runner, err := makeShellPassthrough("echo-args", manifest.Action{"run": "touch"}, nil, nil)
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
	runner, err := makeShellPassthrough("blank", manifest.Action{"run": ""}, nil, nil)
	if err != nil {
		t.Fatalf("makeShellPassthrough: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a blank command")
	}
}

// TestShellPassthroughInteractiveInheritsStdout is the regression test for the
// bug that made `admin prototype` render a 96x30 monochrome island of a
// full-screen TUI: the Go port silently dropped the `interactive` knob, so the
// child ran through RunCmd's pipe. A piped child gets no window size and no
// TTY, so it falls back to a fixed default and turns color off.
//
// The assertion is the one that actually matters and cannot be faked: the
// child's stdout must be the SAME open file as this process's stdout, proven
// by having the child write to fd 1 and reading it back from a real file
// swapped in as os.Stdout. A piped or captured implementation writes somewhere
// else and this fails.
func TestShellPassthroughInteractiveInheritsStdout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	f, err := os.Create(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	orig := os.Stdout
	os.Stdout = f
	t.Cleanup(func() { os.Stdout = orig })

	runner, err := makeShellPassthrough("proto",
		manifest.Action{"run": "printf inherited", "interactive": true}, nil, nil)
	if err != nil {
		t.Fatalf("makeShellPassthrough: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	os.Stdout = orig

	got, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "inherited" {
		t.Fatalf("child stdout = %q, want %q — interactive must hand over the "+
			"real stdout, not a pipe", got, "inherited")
	}
}

// TestShellPassthroughInteractiveNonZeroExitIsError proves the handover still
// reports failure: a raw terminal passthrough that swallowed exit codes would
// make every broken prototype look like it ran fine.
func TestShellPassthroughInteractiveNonZeroExitIsError(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeShellPassthrough("fails",
		manifest.Action{"run": "exit 3", "interactive": true}, nil, nil)
	if err != nil {
		t.Fatalf("makeShellPassthrough: %v", err)
	}
	err = runner(nil)
	if err == nil {
		t.Fatal("runner() = nil, want an error for a nonzero exit")
	}
	if !strings.Contains(err.Error(), "exit 3") {
		t.Fatalf("error = %q, want it to name the exit code", err)
	}
}

// TestShellPassthroughNonInteractiveStillLogs guards the other side: without
// the knob, the existing captured path is unchanged.
func TestShellPassthroughNonInteractiveStillLogs(t *testing.T) {
	t.Chdir(t.TempDir())
	runner, err := makeShellPassthrough("plain", manifest.Action{"run": "true"}, nil, nil)
	if err != nil {
		t.Fatalf("makeShellPassthrough: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
}
