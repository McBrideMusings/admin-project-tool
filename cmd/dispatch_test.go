package cmd

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestDispatchArgsNoManifestFallsToBranchShorthand is the ADR-0011 §2
// "a project with no admin.toml keeps exactly today's behaviour" acceptance
// criterion: with no admin.toml at cwd, a bare unrecognized argument still
// becomes branch shorthand.
func TestDispatchArgsNoManifestFallsToBranchShorthand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got := dispatchArgs([]string{"my-feature"})
	want := []string{"branch", "--yes", "my-feature"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v", got, want)
	}
}

// TestDispatchArgsManifestCommandBeatsBranchShorthand is ADR-0011 §2's
// resolution order step 2: a name the project's admin.toml declares as a
// command dispatches through it rather than falling to branch shorthand.
func TestDispatchArgsManifestCommandBeatsBranchShorthand(t *testing.T) {
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

// TestDispatchArgsManifestCommandNotMatchingFallsThrough proves a manifest
// present at cwd that simply doesn't declare the given name still falls
// through to branch shorthand — the manifest lookup is a lookup, not an
// unconditional claim on every bare argument.
func TestDispatchArgsManifestCommandNotMatchingFallsThrough(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeManifest(t, dir, "deploy")

	got := dispatchArgs([]string{"some-branch-name"})
	want := []string{"branch", "--yes", "some-branch-name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v", got, want)
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

// TestDispatchArgsWorktreeVerbBeatsBranchShorthand pins ADR-0011 §2 step 1 for
// the "worktree" verb specifically: it is a registered cmd/ verb (listed by
// name in the ADR alongside branch/prs/populate), so "admin worktree <input>"
// must dispatch straight to it rather than falling through to branch
// shorthand as "branch --yes worktree <input>" — which is what a caller gets
// if worktreeCmd is ever dropped from rootCmd.
//
// MUTATION EVIDENCE: commenting out rootCmd.AddCommand(worktreeCmd) in
// cmd/root.go makes this fail with:
//
//	dispatchArgs = [branch --yes worktree my-feature], want [worktree my-feature]
func TestDispatchArgsWorktreeVerbBeatsBranchShorthand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got := dispatchArgs([]string{"worktree", "my-feature"})
	want := []string{"worktree", "my-feature"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchArgs = %v, want %v", got, want)
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
