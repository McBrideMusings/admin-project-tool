package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// checkCmd is a reserved tool verb (design principle 5) — reserving it here,
// as a registered cobra command, is what makes dispatchArgs' step 1 (tool
// verbs win collisions) actually true for "check" specifically, and is
// itself the thing ADR-0011 §3 requires: "admin check rejects it with an
// error naming the rule and the colliding name."
//
// admin-3rgp.8: full schema validation — command pure-routing (ADR-0003),
// reserved-name collisions (ADR-0011 §3), urls reference resolution, and
// footer shape — parses [DIR] (or the cwd), resolves it against its
// archetypes, and reports every problem found, not only the first. The
// ADR-0009 anti-pattern report (react-native-cli lints etc.) is
// admin-3rgp.9's job and is deliberately not here.
var checkCmd = &cobra.Command{
	Use:   "check [DIR]",
	Short: "Validate a project's admin.toml (parses, merges archetypes, and reports every problem)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCheck(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(checkCmd)
}

func runCheck(cmd *cobra.Command, args []string) error {
	dir, err := checkTargetDir(args)
	if err != nil {
		return err
	}
	m, ok, err := resolveProjectManifest(cmd.Context(), dir)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "no admin.toml here — nothing to check")
		return nil
	}

	var problems []error
	if err := checkReservedCollisions(m); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := checkCommandRouting(m); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := frame.ValidateURLRefs(m.Actions, m.Urls); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := frame.ValidateFooterRefs(m.Actions); err != nil {
		problems = append(problems, validate.Wrap(err))
	}

	if len(problems) == 0 {
		fmt.Fprintln(os.Stderr, "admin.toml: valid — no problems found")
		return nil
	}
	return errors.Join(problems...)
}

// checkTargetDir resolves `admin check [DIR]`'s optional positional
// argument: the named directory when given, otherwise the cwd.
func checkTargetDir(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	return os.Getwd()
}

// checkCommandRouting is ADR-0003 / design principle 2 (checkReservedCollisions'
// sibling rule) over every resolved command, custom and archetype-provided
// alike: a [commands.X] table may carry only desc/steps/log-overrides, and a
// final command must end up with at least one step. manifest.toCommand
// already stashes any key outside that known set into Command.Raw instead
// of raising at parse time (rejecting it is this ticket's job, not the
// parser's) — so an offending key surviving all the way to a resolved
// manifest shows up here, in Raw, however it got there (the project's own
// [commands.X], or an archetype's, merged straight through by
// internal/archetype.merge's whole-value replacement when the project never
// overrides that name). Reports every offending command, not just the
// first.
func checkCommandRouting(m *manifest.Manifest) error {
	var problems []string
	for _, name := range sortedCommandNames(m.Overrides) {
		cmd := m.Overrides[name]
		if err := validate.CommandRouting(fmt.Sprintf("command %q", name), rawKeys(cmd.Raw), len(cmd.Steps) > 0); err != nil {
			problems = append(problems, err.Error())
		}
	}
	for _, name := range sortedCommandNames(m.Custom) {
		cmd := m.Custom[name]
		if err := validate.CommandRouting(fmt.Sprintf("command %q (commands.custom)", name), rawKeys(cmd.Raw), len(cmd.Steps) > 0); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return validate.Errorf("%s", strings.Join(problems, "\n"))
}

func sortedCommandNames(cmds map[string]manifest.Command) []string {
	names := make([]string, 0, len(cmds))
	for name := range cmds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func rawKeys(raw map[string]any) []string {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	return keys
}
