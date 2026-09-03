package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
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
// commands and asserts the picker model's rendered View names both, along
// with their desc text.
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

	m, ok, err := resolveProjectManifest(context.Background(), dir)
	if !ok || err != nil {
		t.Fatalf("resolveProjectManifest: ok=%v err=%v", ok, err)
	}
	names := kinds.CommandNames(m)
	view := newCmdPickerModel(m, names).View()

	for _, want := range []string{"deploy", "ships the build", "build", "compiles the project"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker View = %q, want it to contain %q", view, want)
		}
	}
}

// TestRunCmdPickerRunsSelectionThroughKindsRunCommand drives the picker
// model's Update with a synthesized enter key and proves the resulting
// chosen command actually runs its steps via kinds.RunCommand — the
// marker-file assertion TestRegisterManifestCommandRunsThroughKindsRunCommand
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

	m, ok, err := resolveProjectManifest(context.Background(), dir)
	if !ok || err != nil {
		t.Fatalf("resolveProjectManifest: ok=%v err=%v", ok, err)
	}
	names := kinds.CommandNames(m)

	var model tea.Model = newCmdPickerModel(m, names)
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	picked, ok := model.(cmdPickerModel)
	if !ok || !picked.confirmed || picked.cancelled {
		t.Fatalf("picker model after enter = %#v, want a confirmed pick", model)
	}

	if err := kinds.RunCommand(m, kinds.Default, picked.chosen, nil, nil); err != nil {
		t.Fatalf("kinds.RunCommand(%q) = %v, want nil", picked.chosen, err)
	}

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

// TestCmdPickerModelCursorMovement drives the picker model's Update through
// down/j and up/k key messages and asserts the cursor moves accordingly,
// clamped at both ends — the arrow-key navigation admin-u7qi.4's acceptance
// criteria requires in place of the old numbered-stdin selection.
func TestCmdPickerModelCursorMovement(t *testing.T) {
	names := []string{"build", "deploy", "test"}
	model := cmdPickerModel{names: names, descs: make([]string, len(names))}

	if model.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", model.cursor)
	}

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	model = next.(cmdPickerModel)
	if model.cursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", model.cursor)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(cmdPickerModel)
	if model.cursor != 2 {
		t.Fatalf("cursor after down = %d, want 2", model.cursor)
	}

	// Clamped at the bottom — one past the end does nothing.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(cmdPickerModel)
	if model.cursor != 2 {
		t.Fatalf("cursor after down past end = %d, want 2 (clamped)", model.cursor)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	model = next.(cmdPickerModel)
	if model.cursor != 1 {
		t.Fatalf("cursor after k = %d, want 1", model.cursor)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = next.(cmdPickerModel)
	if model.cursor != 0 {
		t.Fatalf("cursor after up = %d, want 0", model.cursor)
	}

	// Clamped at the top — one before the start does nothing.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = next.(cmdPickerModel)
	if model.cursor != 0 {
		t.Fatalf("cursor after up past start = %d, want 0 (clamped)", model.cursor)
	}
}

// TestCmdPickerModelCancelKeys covers q/esc/ctrl+c: each quits the program
// without confirming a pick, matching "cancels without running anything".
func TestCmdPickerModelCancelKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
	} {
		model := cmdPickerModel{names: []string{"build", "deploy"}, descs: []string{"", ""}}
		next, cmd := model.Update(key)
		picked := next.(cmdPickerModel)
		if !picked.cancelled || picked.confirmed {
			t.Fatalf("Update(%v) model = %#v, want cancelled and not confirmed", key, picked)
		}
		if cmd == nil {
			t.Fatalf("Update(%v) cmd = nil, want tea.Quit", key)
		}
	}
}
