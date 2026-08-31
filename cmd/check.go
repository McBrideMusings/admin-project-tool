package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// checkCmd is a reserved tool verb (design principle 5) — reserving it here,
// as a registered cobra command, is what makes dispatchArgs' step 1 (tool
// verbs win collisions) actually true for "check" specifically, and is
// itself the thing ADR-0011 §3 requires: "admin check rejects it with an
// error naming the rule and the colliding name."
//
// Full manifest schema validation (parse + resolve + the ADR-0009
// anti-pattern report) is admin-3rgp.8/.9's job and is deliberately not
// here. This carries only the one check this ticket's acceptance criteria
// names: a manifest command whose name collides with a reserved tool verb
// or an existing cmd/ verb.
var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Validate the project's admin.toml (currently: reserved-name collisions only)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCheck(cmd)
	},
}

func init() {
	rootCmd.AddCommand(checkCmd)
}

func runCheck(cmd *cobra.Command) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	m, ok, err := resolveProjectManifest(cmd.Context(), wd)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "no admin.toml here — nothing to check")
		return nil
	}
	if err := checkReservedCollisions(m); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "admin.toml: no reserved-name collisions")
	return nil
}
