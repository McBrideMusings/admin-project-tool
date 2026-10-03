package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/dockerrun"
)

var envVerbose bool

// envCmd lists every environment variable the project's manifest references
// and whether the current environment (with the project's .env loaded)
// satisfies it.
var envCmd = &cobra.Command{
	Use:   "env",
	Short: "List every environment variable admin.toml references and its state",
	Long: `List every ${VAR} admin.toml references — in action bodies, [status], [logs]
and the config tables — with its state: set (its value, masked when the name
looks like a credential), default (unset, the reference supplies a default),
EMPTY (set to "" and required — ADR-0013 treats that as unset) or UNSET
(required). Each line names up to three places that use the variable;
-v lists all of them.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		m, ok, err := resolveProjectManifest(cmd.Context(), ".")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no admin.toml here or in any directory above")
		}
		out := cmd.OutOrStdout()
		color := false
		if f, ok := out.(*os.File); ok {
			color = isatty.IsTerminal(f.Fd())
		}
		printEnvRefs(out, projectEnvRefs(m), envVerbose, color)
		return nil
	},
}

func init() {
	envCmd.Flags().BoolVarP(&envVerbose, "verbose", "v", false, "list every place a variable is used, not just the first three")
	rootCmd.AddCommand(envCmd)
}

func printEnvRefs(w io.Writer, refs []*envRef, verbose, color bool) {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return "\033[" + code + "m" + s + "\033[0m"
	}
	if len(refs) == 0 {
		fmt.Fprintln(w, "no env vars referenced by this project")
		return
	}
	fmt.Fprintln(w, "environment variables referenced by this project:")
	width := 0
	for _, r := range refs {
		width = max(width, len(r.Name))
	}
	for _, r := range refs {
		val, set := os.LookupEnv(r.Name)
		var line string
		switch {
		case val != "":
			shown := val
			if dockerrun.IsSecretEnvName(r.Name) {
				shown = dockerrun.MaskValue
			} else if len(shown) >= 60 {
				shown = shown[:57] + "..."
			}
			line = fmt.Sprintf("  %-*s  %s  = %s", width, r.Name, paint("32", "set"), shown)
		case r.HasDefault:
			line = fmt.Sprintf("  %-*s  %s  = %s", width, r.Name, paint("33", "default"), r.Default)
		case set:
			line = fmt.Sprintf("  %-*s  %s  (set to \"\" — required)", width, r.Name, paint("31", "EMPTY"))
		default:
			line = fmt.Sprintf("  %-*s  %s  (required)", width, r.Name, paint("31", "UNSET"))
		}
		if len(r.UsedBy) > 0 {
			shown := r.UsedBy
			suffix := ""
			if len(shown) > 3 {
				suffix = fmt.Sprintf(", +%d more", len(shown)-3)
				shown = shown[:3]
			}
			line += "  (used by: " + strings.Join(shown, ", ") + suffix + ")"
		}
		fmt.Fprintln(w, line)
		if verbose && len(r.UsedBy) > 3 {
			fmt.Fprintln(w, strings.Repeat(" ", 4+width)+paint("2", "full used_by: "+strings.Join(r.UsedBy, ", ")))
		}
	}
}
