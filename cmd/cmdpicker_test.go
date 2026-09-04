package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
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

// writeMarkerManifest writes a one-command admin.toml (a "build" command
// whose only step touches marker.txt) into dir, returning its resolved
// manifest.
func writeMarkerManifest(t *testing.T, dir string) *manifest.Manifest {
	t.Helper()
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
	return m
}

// TestRunCmdPickerListsCommandsWithDesc writes a manifest declaring two
// commands and asserts commandEntries — the picker rows runCmdPicker hands
// to h.PickTarget — names both, each paired with its own desc text, in
// kinds.CommandNames' order.
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
	entries := commandEntries(m, names)

	want := []luahost.TargetEntry{
		{Key: "build", Description: "compiles the project"},
		{Key: "deploy", Description: "ships the build"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("commandEntries = %#v, want %#v", entries, want)
	}
}

// TestCommandEntriesOrdersByGroupPriority pins the fix for the picker
// falling back to kinds.CommandNames' plain alphabetical order: three
// group-2 commands declared out of priority order, plus one group-1
// command that alphabetically sorts last, must come out group-1-then-
// group-2 and priority-ascending within each group — with a blank spacer
// row (a Key-less luahost.TargetEntry, per PickTarget's own contract)
// between the two groups.
func TestCommandEntriesOrdersByGroupPriority(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := `project_name = "fixture"

[commands.zdev]
desc     = "run dev"
steps    = ["noop"]
group    = 1
priority = 20

[commands.fmt]
desc     = "format"
steps    = ["noop"]
group    = 2
priority = 30

[commands.test]
desc     = "run tests"
steps    = ["noop"]
group    = 2
priority = 10

[commands.lint]
desc     = "lint"
steps    = ["noop"]
group    = 2
priority = 20

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
	entries := commandEntries(m, names)

	want := []luahost.TargetEntry{
		{Key: "zdev", Description: "run dev"},   // group 1 alone — "z" name, alphabetically last, must still lead
		{},                                      // spacer: group 1 -> group 2
		{Key: "test", Description: "run tests"}, // group 2, priority 10
		{Key: "lint", Description: "lint"},      // group 2, priority 20
		{Key: "fmt", Description: "format"},     // group 2, priority 30
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("commandEntries = %#v, want %#v", entries, want)
	}
}

// TestHandlePickOutcomeChosenRunsTheCommand covers handlePickOutcome's
// default (PickChosen) branch: the returned key runs through
// kinds.RunCommandInline — proven with the same marker-file assertion
// TestRegisterManifestCommandRunsThroughKindsRunCommand uses for `admin
// <name>`'s own kinds.RunCommand claim.
func TestHandlePickOutcomeChosenRunsTheCommand(t *testing.T) {
	dir := t.TempDir()
	m := writeMarkerManifest(t, dir)
	h := luahost.New(dir)
	var out, errOut bytes.Buffer
	h.Stdout, h.Stderr = &out, &errOut

	if err := handlePickOutcome(h, m, "touch", luahost.PickChosen); err != nil {
		t.Fatalf("handlePickOutcome(PickChosen) = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("stat marker.txt: %v (PickChosen never ran the picked command through kinds.RunCommand)", err)
	}
}

// TestHandlePickOutcomeChosenClearsScreenBeforeRunning covers admin-qshs.1:
// the PickChosen branch must write clearScreenEscape to h.Stdout before the
// picked command's own output — otherwise the picker's leftover rendered
// frame (prompt, rows, footer) stays sitting in the terminal and the
// command's output just scrolls it up instead of replacing it.
func TestHandlePickOutcomeChosenClearsScreenBeforeRunning(t *testing.T) {
	dir := t.TempDir()
	m := writeMarkerManifest(t, dir)
	h := luahost.New(dir)
	var out, errOut bytes.Buffer
	h.Stdout, h.Stderr = &out, &errOut

	if err := handlePickOutcome(h, m, "touch", luahost.PickChosen); err != nil {
		t.Fatalf("handlePickOutcome(PickChosen) = %v, want nil", err)
	}
	if !strings.HasPrefix(out.String(), clearScreenEscape) {
		t.Fatalf("stdout = %q, want it to start with the clear-screen escape sequence %q", out.String(), clearScreenEscape)
	}
}

// TestHandlePickOutcomeCancelledDoesNotClearScreen and
// TestHandlePickOutcomeUnavailableDoesNotClearScreen pin the other half of
// admin-qshs.1: the clear must stay scoped to PickChosen only, since
// PickCancelled and PickUnavailable already print their own short messages
// and should not wipe the screen out from under them.
func TestHandlePickOutcomeCancelledDoesNotClearScreen(t *testing.T) {
	dir := t.TempDir()
	m := writeMarkerManifest(t, dir)
	h := luahost.New(dir)
	var out, errOut bytes.Buffer
	h.Stdout, h.Stderr = &out, &errOut

	if err := handlePickOutcome(h, m, "", luahost.PickCancelled); err != nil {
		t.Fatalf("handlePickOutcome(PickCancelled) = %v, want nil", err)
	}
	if strings.Contains(out.String(), clearScreenEscape) {
		t.Fatalf("stdout = %q, want no clear-screen escape sequence on cancel", out.String())
	}
}

func TestHandlePickOutcomeUnavailableDoesNotClearScreen(t *testing.T) {
	dir := t.TempDir()
	m := writeMarkerManifest(t, dir)
	h := luahost.New(dir)
	var out, errOut bytes.Buffer
	h.Stdout, h.Stderr = &out, &errOut

	if err := handlePickOutcome(h, m, "", luahost.PickUnavailable); err == nil {
		t.Fatal("handlePickOutcome(PickUnavailable) = nil, want an error")
	}
	if strings.Contains(out.String(), clearScreenEscape) {
		t.Fatalf("stdout = %q, want no clear-screen escape sequence when unavailable", out.String())
	}
}

// TestHandlePickOutcomeChosenDetachCommandRunsInlineNeverDispatches is
// admin-cmd-never-dispatch's picker-level proof (acceptance criterion 3): a
// picked command whose action declares detach=true, with a spawnable
// [terminal] configured, still runs on this process's own terminal — the
// marker file is written — and never invokes the fake launcher, unlike
// `admin <name>` typed directly (kinds.RunCommand), which would dispatch
// through it.
func TestHandlePickOutcomeChosenDetachCommandRunsInlineNeverDispatches(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "terminal-calls.log")
	body := `project_name = "fixture"

[terminal]
spawn = "echo \"spawn $ADMIN_CMD\" >> ` + logPath + `; echo fake-handle-1"

[commands.touch]
desc  = "writes a marker"
steps = ["write-marker"]

[actions.write-marker]
kind   = "shell"
run    = "touch marker.txt"
detach = true
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}
	m, ok, err := resolveProjectManifest(context.Background(), dir)
	if !ok || err != nil {
		t.Fatalf("resolveProjectManifest: ok=%v err=%v", ok, err)
	}

	h := luahost.New(dir)
	var out, errOut bytes.Buffer
	h.Stdout, h.Stderr = &out, &errOut

	if err := handlePickOutcome(h, m, "touch", luahost.PickChosen); err != nil {
		t.Fatalf("handlePickOutcome(PickChosen) = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("stat marker.txt: %v (a detach=true picked command must still run inline)", err)
	}
	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — admin cmd must never dispatch a picked command through the launcher", string(logged))
	}
}

// TestHandlePickOutcomeCancelledReturnsNilAndRunsNothing covers
// handlePickOutcome's PickCancelled branch: the user answered the question
// by dismissing the picker, so this must return cleanly and print
// "cancelled" — matching internal/kinds/multitarget.go:278-283's identical
// handling — without running any command.
func TestHandlePickOutcomeCancelledReturnsNilAndRunsNothing(t *testing.T) {
	dir := t.TempDir()
	m := writeMarkerManifest(t, dir)
	h := luahost.New(dir)
	var out, errOut bytes.Buffer
	h.Stdout, h.Stderr = &out, &errOut

	if err := handlePickOutcome(h, m, "", luahost.PickCancelled); err != nil {
		t.Fatalf("handlePickOutcome(PickCancelled) = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "cancelled") {
		t.Fatalf("stderr = %q, want it to say cancelled", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err == nil {
		t.Fatal("marker.txt exists — a command ran despite the picker being cancelled")
	}
}

// TestHandlePickOutcomeUnavailableReturnsError covers handlePickOutcome's
// PickUnavailable branch: PickTarget has already printed why it could not
// prompt, so this only needs to fail the command rather than silently doing
// nothing.
func TestHandlePickOutcomeUnavailableReturnsError(t *testing.T) {
	dir := t.TempDir()
	m := writeMarkerManifest(t, dir)
	h := luahost.New(dir)
	var out, errOut bytes.Buffer
	h.Stdout, h.Stderr = &out, &errOut

	if err := handlePickOutcome(h, m, "", luahost.PickUnavailable); err == nil {
		t.Fatal("handlePickOutcome(PickUnavailable) = nil, want an error")
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err == nil {
		t.Fatal("marker.txt exists — a command ran despite the picker being unavailable")
	}
}

// TestPickAndRunCommandNonTTYIsUnavailable exercises pickAndRunCommand end
// to end against a Host whose Stdin is not a *os.File TTY — the shape every
// test process runs under — proving the real h.PickTarget call reaches
// PickUnavailable and that handlePickOutcome's error branch fires, without
// running any command.
func TestPickAndRunCommandNonTTYIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	m := writeMarkerManifest(t, dir)
	h := luahost.New(dir)
	var out, errOut bytes.Buffer
	h.Stdout, h.Stderr = &out, &errOut
	h.Stdin = strings.NewReader("")

	names := kinds.CommandNames(m)
	if err := pickAndRunCommand(h, m, names); err == nil {
		t.Fatal("pickAndRunCommand = nil, want an error when the picker can't prompt")
	}
	if !strings.Contains(errOut.String(), "not a TTY") {
		t.Fatalf("stderr = %q, want PickTarget's non-TTY diagnostic", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err == nil {
		t.Fatal("marker.txt exists — a command ran despite the picker being unavailable")
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
