package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// cmdCmd is the reserved `admin cmd` verb (admin-u7qi.1): with no args it
// lists every command the current project's admin.toml declares in the same
// arrow-key picker internal/kinds/multitarget.go already uses for target
// selection (admin-u7qi.4), and runs the highlighted one through the exact
// same kinds.RunCommand path `admin <name>` already uses — so a picked
// command gets identical launcher/detach/reentry behavior to a typed name.
// It is registered on rootCmd in cmd/root.go's init(), which is what makes
// dispatchArgs' loop over rootCmd.Commands() match `admin cmd` before ever
// consulting the manifest for a command literally named "cmd" (see
// reservedCmdVerbs in cmd/manifest.go, which admin check's
// checkReservedCollisions enforces against a manifest declaring the name).
var cmdCmd = &cobra.Command{
	Use:   "cmd",
	Short: "pick this project's admin.toml commands from an arrow-key list and run it inline",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCmdPicker(cmd.Context())
	},
}

// commandDesc looks up name's Desc the way kinds.resolveCommand does
// (custom shadows archetype-provided — admin_lib/archetypes.py::resolve's
// rule) — that resolver is unexported to this package, and the picker only
// needs the description, not the whole dispatch machinery resolveCommand
// carries.
func commandDesc(m *manifest.Manifest, name string) string {
	if c, ok := m.Custom[name]; ok {
		return c.Desc
	}
	if c, ok := m.Overrides[name]; ok {
		return c.Desc
	}
	return ""
}

// commandEntries builds the picker rows for names, one luahost.TargetEntry
// per command in the order kinds.CommandNames returns, using commandDesc for
// each row's description.
func commandEntries(m *manifest.Manifest, names []string) []luahost.TargetEntry {
	entries := make([]luahost.TargetEntry, len(names))
	for i, name := range names {
		entries[i] = luahost.TargetEntry{Key: name, Description: commandDesc(m, name)}
	}
	return entries
}

// runCmdPicker resolves the project manifest, builds a luahost.Host rooted
// at it (the same construction pattern internal/kinds/helpers.go's newHost
// uses, exported as luahost.New for exactly this cross-package case), and
// hands off to pickAndRunCommand.
//
// The ok/err handling mirrors registerManifestCommand: ok is false only
// when there is no admin.toml at all (never paired with a non-nil err, per
// resolveProjectManifest's own contract), so that case is checked first and
// reported plainly; a manifest that exists but fails to parse/resolve is
// reported as invalid rather than silently producing an empty list.
func runCmdPicker(ctx context.Context) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	m, ok, err := resolveProjectManifest(ctx, wd)
	if !ok {
		return fmt.Errorf("no admin.toml in %s or any directory above it, so this project has no admin commands", wd)
	}
	if err != nil {
		return fmt.Errorf("admin.toml is invalid: %w", err)
	}

	names := kinds.CommandNames(m)
	if len(names) == 0 {
		return fmt.Errorf("admin.toml declares no commands")
	}

	h := luahost.New(m.Root)
	return pickAndRunCommand(h, m, names)
}

// pickAndRunCommand prompts over h.PickTarget for one of names and runs the
// chosen command via kinds.RunCommand — the same call registerManifestCommand's
// RunE makes for `admin <name>`. Split out from runCmdPicker so tests can
// drive it against a Host they construct themselves (its own Stdin/
// Stdout/Stderr) rather than the real terminal.
//
// The outcome switch mirrors internal/kinds/multitarget.go:277-289 exactly:
// PickCancelled means the user answered the question by dismissing the
// picker — nothing failed, so it prints "cancelled" and returns cleanly;
// PickUnavailable means PickTarget could not prompt at all (no TTY) and has
// already printed why via h.Err, so this only needs to fail the command
// rather than silently doing nothing.
func pickAndRunCommand(h *luahost.Host, m *manifest.Manifest, names []string) error {
	entries := commandEntries(m, names)
	key, outcome := h.PickTarget(entries, "select a command")
	return handlePickOutcome(h, m, key, outcome)
}

// handlePickOutcome is pickAndRunCommand's outcome switch, split out so a
// test can drive each luahost.PickOutcome directly — reaching PickChosen and
// PickCancelled through a real h.PickTarget call needs an actual terminal,
// which no test in this repo can synthesize (internal/luahost/pick_test.go
// only reaches PickUnavailable for the same reason).
func handlePickOutcome(h *luahost.Host, m *manifest.Manifest, key string, outcome luahost.PickOutcome) error {
	switch outcome {
	case luahost.PickCancelled:
		fmt.Fprintln(h.Stderr, "cancelled")
		return nil
	case luahost.PickUnavailable:
		return fmt.Errorf("admin cmd: no command could be picked (pass one as `admin <name>`, or run from a TTY)")
	default:
		return kinds.RunCommand(m, kinds.Default, key, nil, nil)
	}
}
