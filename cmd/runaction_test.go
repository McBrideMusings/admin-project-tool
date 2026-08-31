package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunActionCmdRunsNamedActionDirectly is the basic case: the hidden
// re-entry command (admin-3rgp.17) actually dispatches the named action.
// TestRunActionCmdIsNotACommandName below is the one that pins down what
// makes it a re-entry point rather than a second way to run a command:
// RunAction, not RunCommand, so no [commands.X] wrapper is needed.
func TestRunActionCmdRunsNamedActionDirectly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := `project_name = "fixture"

[actions.write-marker]
kind = "shell"
run  = "touch marker.txt"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}

	if err := runActionCmd.RunE(runActionCmd, []string{"write-marker"}); err != nil {
		t.Fatalf("RunE = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("stat marker.txt: %v", err)
	}
}

// TestRunActionCmdIsNotACommandName proves __run-action takes an ACTION
// name, not a [commands.X] name. The fixture wraps write-marker in a
// [commands.wrapper] whose OWN action (a no-op) would leave no marker
// behind; only dispatching write-marker directly — bypassing command
// resolution entirely — produces the marker file.
//
// MUTATION EVIDENCE: change the registered RunE to call
// kinds.RunCommand(m, kinds.Default, args[0], args[1:]) instead of
// kinds.RunAction, and this test fails with `unknown command
// "write-marker"` — no [commands.write-marker] exists, only
// [actions.write-marker].
func TestRunActionCmdIsNotACommandName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := `project_name = "fixture"

[commands.wrapper]
desc  = "does not touch marker.txt"
steps = ["noop"]

[actions.noop]
kind = "shell"
run  = "true"

[actions.write-marker]
kind = "shell"
run  = "touch marker.txt"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}

	if err := runActionCmd.RunE(runActionCmd, []string{"write-marker"}); err != nil {
		t.Fatalf("RunE = %v, want nil (write-marker is an action, not a command)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("stat marker.txt: %v", err)
	}
}

// TestRunActionCmdNoManifestErrors proves a directory with no admin.toml
// produces a clear error rather than a panic or a silent no-op.
func TestRunActionCmdNoManifestErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := runActionCmd.RunE(runActionCmd, []string{"anything"}); err == nil {
		t.Fatal("RunE = nil, want an error with no admin.toml present")
	}
}

// TestRunActionCmdHidden proves the re-entry verb is Hidden — it is
// plumbing a herdr pane runs, never something a person is meant to type or
// see in --help output.
func TestRunActionCmdHidden(t *testing.T) {
	if !runActionCmd.Hidden {
		t.Error("runActionCmd.Hidden = false, want true")
	}
}
