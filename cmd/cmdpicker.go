package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// cmdCmd is the reserved `admin cmd` verb (admin-u7qi.1): with no args it
// lists every command the current project's admin.toml declares, reads one
// numbered selection from stdin, and runs the chosen command inline through
// the exact same kinds.RunCommand path `admin <name>` already uses — so a
// picked command gets identical launcher/detach/reentry behavior to a typed
// name. It is registered on rootCmd in cmd/root.go's init(), which is what
// makes dispatchArgs' loop over rootCmd.Commands() match `admin cmd` before
// ever consulting the manifest for a command literally named "cmd" (see
// reservedCmdVerbs in cmd/manifest.go, which admin check's
// checkReservedCollisions enforces against a manifest declaring the name).
var cmdCmd = &cobra.Command{
	Use:   "cmd",
	Short: "list this project's admin.toml commands and run a numbered pick inline",
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

// runCmdPicker resolves the project manifest, lists every declared command
// with its desc to stderr (cmd/'s own output already goes there so stdout
// stays clean for the cd sentinel), reads one numbered stdin selection, and
// runs the chosen command via kinds.RunCommand(m, kinds.Default, name, nil,
// nil) — the same call registerManifestCommand's RunE makes for `admin
// <name>`.
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

	for i, name := range names {
		if desc := commandDesc(m, name); desc != "" {
			fmt.Fprintf(os.Stderr, "%2d. %s — %s\n", i+1, name, desc)
		} else {
			fmt.Fprintf(os.Stderr, "%2d. %s\n", i+1, name)
		}
	}
	fmt.Fprint(os.Stderr, "select a command: ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading selection: %w", err)
	}
	line = strings.TrimSpace(line)
	idx, convErr := strconv.Atoi(line)
	if convErr != nil || idx < 1 || idx > len(names) {
		return fmt.Errorf("%q is not a valid selection (1-%d)", line, len(names))
	}

	name := names[idx-1]
	return kinds.RunCommand(m, kinds.Default, name, nil, nil)
}
