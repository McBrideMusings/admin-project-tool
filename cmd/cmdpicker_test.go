package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCmdVerbIsRegisteredReservedVerb pins the reserved-verb precedence
// admin-u7qi.1 requires: even if the manifest declares a command literally
// named "cmd", the registered cmd/ verb of the same name is matched first
// and the manifest is never consulted for it — the same shape
// TestDispatchArgsToolVerbBeatsManifestCommand pins for "branch".
func TestCmdVerbIsRegisteredReservedVerb(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeManifest(t, dir, "cmd")

	got := dispatchArgs([]string{"cmd"})
	want := []string{"cmd"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("dispatchArgs = %v, want %v (cmd/ verb should win over the manifest)", got, want)
	}
	for _, c := range rootCmd.Commands() {
		if c.Name() == "cmd" && c != cmdCmd {
			t.Fatal("dispatchArgs registered a second 'cmd' command from the manifest")
		}
	}
}

// TestReservedCmdVerbsIncludesCmd is admin-u7qi.1's other half of that same
// precedence: admin check's checkReservedCollisions rejects a manifest that
// declares its own command named "cmd" only because reservedCmdVerbs names
// it.
func TestReservedCmdVerbsIncludesCmd(t *testing.T) {
	for _, v := range reservedCmdVerbs {
		if v == "cmd" {
			return
		}
	}
	t.Fatalf("reservedCmdVerbs = %v, want it to include \"cmd\"", reservedCmdVerbs)
}

// TestRunCmdPickerListsCommandsWithDesc writes a manifest declaring two
// commands and asserts the picker's listing (captured off stderr) names
// both, along with their desc text.
func TestRunCmdPickerListsCommandsWithDesc(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := `project_name = "fixture"

[commands.deploy]
desc  = "ships the build"
steps = ["noop"]

[commands.build]
desc  = "compiles the project"
steps = ["noop"]

[actions.noop]
kind = "shell"
run  = "true"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}

	stderr := withCapturedStderr(t, func() {
		withStdinLine(t, "1", func() {
			if err := runCmdPicker(context.Background()); err != nil {
				t.Fatalf("runCmdPicker = %v, want nil", err)
			}
		})
	})

	for _, want := range []string{"deploy", "ships the build", "build", "compiles the project"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("picker listing = %q, want it to contain %q", stderr, want)
		}
	}
}

// TestRunCmdPickerRunsSelectionThroughKindsRunCommand proves a fed numbered
// selection actually runs the chosen command's steps via kinds.RunCommand —
// the marker-file assertion TestRegisterManifestCommandRunsThroughKindsRunCommand
// uses for the same claim about `admin <name>`.
func TestRunCmdPickerRunsSelectionThroughKindsRunCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := `project_name = "fixture"

[commands.build]
desc  = "writes a marker"
steps = ["write-marker"]

[commands.touch]
desc  = "also writes a marker"
steps = ["write-marker"]

[actions.write-marker]
kind = "shell"
run  = "touch marker.txt"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}

	// kinds.CommandNames sorts, so "build" is entry 1.
	withCapturedStderr(t, func() {
		withStdinLine(t, "1", func() {
			if err := runCmdPicker(context.Background()); err != nil {
				t.Fatalf("runCmdPicker = %v, want nil", err)
			}
		})
	})

	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("stat marker.txt: %v (the picked selection never actually ran through kinds.RunCommand)", err)
	}
}

// TestRunCmdPickerNoManifestReportsPlainly mirrors unknownCommandError's
// no-manifest case: with no admin.toml anywhere above cwd, the picker
// reports that plainly rather than crashing on a nil manifest.
func TestRunCmdPickerNoManifestReportsPlainly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	err := runCmdPicker(context.Background())
	if err == nil {
		t.Fatal("runCmdPicker = nil, want an error naming the missing admin.toml")
	}
	if !strings.Contains(err.Error(), "no admin.toml") {
		t.Fatalf("runCmdPicker error = %q, want it to say no admin.toml exists here", err.Error())
	}
}

// TestRunCmdPickerInvalidSelectionErrors covers the out-of-range/non-numeric
// case the item's acceptance criteria leaves open: rather than guessing, an
// invalid selection is a plain error naming the valid range.
func TestRunCmdPickerInvalidSelectionErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeManifest(t, dir, "deploy")

	withCapturedStderr(t, func() {
		withStdinLine(t, "not-a-number", func() {
			err := runCmdPicker(context.Background())
			if err == nil {
				t.Fatal("runCmdPicker = nil, want an error for a non-numeric selection")
			}
			if !strings.Contains(err.Error(), "not a valid selection") {
				t.Fatalf("runCmdPicker error = %q, want it to say the selection is invalid", err.Error())
			}
		})
	})
}

// withStdinLine swaps os.Stdin for a pipe carrying line (plus its
// terminator) for the duration of fn, matching
// internal/prompt/confirm_test.go's own os.Stdin swap-and-restore pattern —
// the shape runCmdPicker's bufio.Reader read needs to be substitutable in a
// test.
func withStdinLine(t *testing.T, line string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	go func() {
		w.WriteString(line + "\n")
		w.Close()
	}()

	fn()
}

// withCapturedStderr swaps os.Stderr for a pipe for the duration of fn and
// returns everything written to it — the picker's listing and prompt both
// go to stderr so stdout stays clean for the cd sentinel (see
// runCmdPicker's doc comment).
func withCapturedStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, readErr := r.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if readErr != nil {
				break
			}
		}
		done <- string(buf)
	}()

	fn()
	w.Close()
	os.Stderr = old
	return <-done
}
