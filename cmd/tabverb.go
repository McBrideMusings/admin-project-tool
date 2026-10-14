package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

// tabVerbs are the reserved verbs that open the dashboard with one tab mounted
// alone (ADR-0026): `admin workers`, `admin repo`, `admin prs`, `admin issues`,
// `admin beads`, `admin config`. Each takes the bare dashboard's --dump-frame
// flags. `admin cmd` opens the Commands tab alone.
func init() {
	for _, verb := range tui.AloneVerbs() {
		rootCmd.AddCommand(newTabVerb(verb))
	}
}

func newTabVerb(verb string) *cobra.Command {
	c := &cobra.Command{
		Use:   verb,
		Short: "open the dashboard on the " + verb + " tab alone",
		Long: "Open the dashboard with the " + verb + " tab mounted alone: no tab bar, no tab-switch keys,\n" +
			"focus on the body. A project that does not draw the tab prints\n" +
			"\"this project has no " + verb + " tab\" and exits 1.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dumpFixture != "" && !dumpFrame {
				return fmt.Errorf("--fixture only applies to --dump-frame")
			}
			if dumpFrame {
				return runDumpFrame(cmd.Context(), verb)
			}
			return runDashboard(cmd.Context(), verb)
		},
	}
	addDumpFrameFlags(c.Flags())
	return c
}

// dashboardOptions are the options every dashboard construction shares, plus
// the mount-alone option when tabVerb names a tab.
func dashboardOptions(ctx context.Context, checkout, nwo, tabVerb string) []tui.Option {
	opts := []tui.Option{
		tui.WithManifestRoot(dashboardManifestRoot()),
		tui.WithCheckout(checkout),
		remoteOption(ctx, checkout, nwo),
	}
	if tabVerb != "" {
		opts = append(opts, tui.WithMountedAlone(tabVerb))
	}
	return opts
}

// requireDrawn fails when tabVerb opened a tab this project does not draw.
func requireDrawn(model tui.Model, tabVerb string) error {
	if tabVerb != "" && !model.MountedDrawn() {
		return fmt.Errorf("this project has no %s tab", tabVerb)
	}
	return nil
}
