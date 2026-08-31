package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// writeManifest writes a minimal admin.toml declaring one command/action
// pair into dir.
func writeManifest(t *testing.T, dir, commandName string) {
	t.Helper()
	body := `project_name = "fixture"

[commands.` + commandName + `]
desc  = "test command"
steps = ["noop"]

[actions.noop]
kind = "shell"
run  = "true"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}
}

// removeRootCommand undoes registerManifestCommand's rootCmd.AddCommand so
// one test's dynamic registration can't leak into the next — rootCmd is a
// package-level singleton shared across every test in this package.
func removeRootCommand(t *testing.T, name string) {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() == name {
			rootCmd.RemoveCommand(c)
		}
	}
}

// runRegistered finds the command dispatchArgs registered under name and
// returns the error its RunE produces. It fails the test if nothing was
// registered — "dispatchArgs handled it" is exactly the claim these tests
// make, so an absent command is a failure, not a skip.
func runRegistered(t *testing.T, name string) error {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() == name {
			return c.RunE(c, nil)
		}
	}
	t.Fatalf("dispatchArgs registered no command under %q", name)
	return nil
}

// TestDispatchArgsNoManifestIsUnknownCommand is admin-oeqo's core
// acceptance criterion: with no admin.toml at cwd, a bare unrecognized
// argument is an error, NOT a branch. This is the exact shape that made
// `admin dev` in a fresh clone create and check out a branch called "dev".
//
// MUTATION EVIDENCE: restoring the old fall-through
// (`return append([]string{"branch", "--yes"}, args...)`) makes this fail with:
//
//	dispatchArgs = [branch --yes my-feature], want [my-feature]
func TestDispatchArgsNoManifestIsUnknownCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Cleanup(func() { removeRootCommand(t, "my-feature") })

	got := dispatchArgs([]string{"my-feature"})
	want := []string{"my-feature"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v (a bare word must never become branch input)", got, want)
	}

	err := runRegistered(t, "my-feature")
	if err == nil {
		t.Fatal("RunE = nil, want an unknown-command error")
	}
	for _, want := range []string{`unknown command "my-feature"`, "no admin.toml", "admin branch my-feature", "admin worktree my-feature"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// TestDispatchArgsUnknownNameWithManifestListsCommands covers the other half
// of the unknown-command message: in a project that HAS a manifest, the
// remedy is a name from it, so the error names what the manifest declares.
func TestDispatchArgsUnknownNameWithManifestListsCommands(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeManifest(t, dir, "deploy")
	t.Cleanup(func() { removeRootCommand(t, "some-branch-name") })

	got := dispatchArgs([]string{"some-branch-name"})
	want := []string{"some-branch-name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v", got, want)
	}

	err := runRegistered(t, "some-branch-name")
	if err == nil {
		t.Fatal("RunE = nil, want an unknown-command error")
	}
	if !strings.Contains(err.Error(), "deploy") {
		t.Errorf("error %q does not name the manifest's own commands", err.Error())
	}
}

// TestDispatchArgsManifestCommandDispatches is ADR-0011 §2's resolution
// order step 2: a name the project's admin.toml declares as a command
// dispatches through it rather than being reported unknown.
func TestDispatchArgsManifestCommandDispatches(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeManifest(t, dir, "deploy")
	t.Cleanup(func() { removeRootCommand(t, "deploy") })

	got := dispatchArgs([]string{"deploy"})
	want := []string{"deploy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v (manifest command should win)", got, want)
	}

	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "deploy" {
			found = true
		}
	}
	if !found {
		t.Fatal("dispatchArgs matched the manifest command but never registered it on rootCmd")
	}
}

// TestDispatchArgsToolVerbBeatsManifestCommand is ADR-0011 §2's resolution
// order step 1 outranking step 2: even if the manifest also declares a
// command called "branch", the registered cmd/ verb of the same name is
// matched first and the manifest is never consulted for it.
func TestDispatchArgsToolVerbBeatsManifestCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeManifest(t, dir, "branch")

	got := dispatchArgs([]string{"branch", "some-input"})
	want := []string{"branch", "some-input"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v (cmd/ verb should win over the manifest)", got, want)
	}
	for _, c := range rootCmd.Commands() {
		if c.Name() == "branch" && c != branchCmd {
			t.Fatal("dispatchArgs registered a second 'branch' command from the manifest")
		}
	}
}

// TestDispatchArgsWorktreeVerbIsRegistered pins step 1 for the "worktree"
// verb specifically. `branch` and `worktree` are the only two ways to create
// a git ref now that the bare-word fall-through is gone (admin-oeqo), so
// each must actually be on rootCmd — a dropped worktreeCmd would turn
// `admin worktree my-feature` into an unknown-command error with no
// replacement.
//
// MUTATION EVIDENCE: commenting out rootCmd.AddCommand(worktreeCmd) in
// cmd/root.go makes this fail — dispatchArgs registers an unknown-command
// stub under "worktree" instead of matching the real verb.
func TestDispatchArgsWorktreeVerbIsRegistered(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got := dispatchArgs([]string{"worktree", "my-feature"})
	want := []string{"worktree", "my-feature"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v", got, want)
	}
}

// TestDispatchArgsBrokenManifestErrorsNotBranch is admin-3rgp.52's fixture
// exactly: an admin.toml that EXISTS but fails to resolve (missing
// project_name, per internal/manifest's own requirement) must produce a
// manifest error, never a fall-through — the fall-through here is what let
// `admin run-args` silently `git checkout -b run-args` against a live repo,
// and with the fall-through gone (ADR-0014) the same silence would now
// misreport a declared command as a typo. dispatchArgs returns the bare args unchanged
// (matching the "handled here" shape it already uses for a real manifest
// command), but the command it registered under that name must report the
// manifest problem instead of running anything or falling through.
//
// MUTATION EVIDENCE: reverting registerManifestCommand's err-handling to the
// old `if !ok || err != nil { return false }` makes this fail with:
//
//	RunE error = `unknown command "run-args" …`, want it to report the invalid manifest
func TestDispatchArgsBrokenManifestErrorsNotBranch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// No project_name: internal/manifest.Load rejects this at resolve time.
	body := `archetypes = []
[commands.run-args]
desc = "x"
steps = ["run-args"]
[actions.run-args]
kind = "shell"
run = "echo hi"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}
	t.Cleanup(func() { removeRootCommand(t, "run-args") })

	got := dispatchArgs([]string{"run-args"})
	want := []string{"run-args"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v (a broken manifest must not fall through to branch shorthand)", got, want)
	}

	var found *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "run-args" {
			found = c
		}
	}
	if found == nil {
		t.Fatal("dispatchArgs matched the broken manifest but registered no command to report it")
	}
	err := found.RunE(found, nil)
	if err == nil {
		t.Fatal("RunE = nil, want an error naming the manifest problem")
	}
	if !strings.Contains(err.Error(), "admin.toml is invalid") {
		t.Fatalf("RunE error = %q, want it to say the manifest is invalid", err.Error())
	}
	if !strings.Contains(err.Error(), "project_name") {
		t.Fatalf("RunE error = %q, want it to name the actual manifest problem (project_name)", err.Error())
	}
}

// TestDispatchArgsBrokenManifestBeatsUnknownCommand pins the distinction the
// two error paths have to keep: a broken manifest reports the manifest
// problem, never "unknown command", which would send the user hunting for a
// typo in a name their manifest does declare.
func TestDispatchArgsBrokenManifestBeatsUnknownCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := `archetypes = []
[commands.deploy]
desc = "x"
steps = ["deploy"]
[actions.deploy]
kind = "shell"
run = "echo hi"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}
	t.Cleanup(func() { removeRootCommand(t, "whatever-branch") })

	dispatchArgs([]string{"whatever-branch"})
	err := runRegistered(t, "whatever-branch")
	if err == nil {
		t.Fatal("RunE = nil, want the manifest error")
	}
	if !strings.Contains(err.Error(), "admin.toml is invalid") {
		t.Fatalf("RunE error = %q, want it to report the invalid manifest", err.Error())
	}
	if strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("RunE error = %q, want the manifest problem rather than a typo report", err.Error())
	}
}

// TestRootCmdIdentity is ADR-0011 §1: the root command is named "admin",
// with "repo" surviving only as a cobra alias.
//
// MUTATION EVIDENCE: with rootCmd.Use reverted to "repo" (and no alias),
// this test fails with:
//
//	root command name = "repo", want "admin"
func TestRootCmdIdentity(t *testing.T) {
	if got := rootCmd.Name(); got != "admin" {
		t.Fatalf("root command name = %q, want %q", got, "admin")
	}
	if !rootCmd.HasAlias("repo") {
		t.Fatal(`root command has no "repo" alias — .zshrc's "repo completion zsh" would break`)
	}
}

// TestRegisterManifestCommandRunsThroughKindsRunCommand proves the wired
// manifest command actually executes its steps via kinds.RunCommand, not
// merely that dispatchArgs recognizes the name.
//
// MUTATION EVIDENCE: changing the registered RunE to call
// kinds.RunCommand(m, kinds.Default, name, cmdArgs) with `name` replaced by
// a literal "" makes RunCommand report `unknown command ""` instead of
// running "noop", and this test's marker-file assertion fails with:
//
//	stat marker.txt: no such file or directory
func TestRegisterManifestCommandRunsThroughKindsRunCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	body := `project_name = "fixture"

[commands.touch]
desc  = "writes a marker"
steps = ["write-marker"]

[actions.write-marker]
kind = "shell"
run  = "touch marker.txt"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}
	t.Cleanup(func() { removeRootCommand(t, "touch") })

	if ok := registerManifestCommand("touch"); !ok {
		t.Fatal("registerManifestCommand(touch) = false, want true")
	}
	var found *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "touch" {
			found = c
		}
	}
	if found == nil {
		t.Fatal("registered command not found on rootCmd")
	}
	if err := found.RunE(found, nil); err != nil {
		t.Fatalf("RunE = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("stat marker.txt: %v (the registered command's RunE never actually ran the step)", err)
	}
}
