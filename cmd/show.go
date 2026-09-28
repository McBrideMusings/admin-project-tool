package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

var showCmd = &cobra.Command{
	Use:   "show <tab>",
	Short: "Print a dashboard tab's rows as JSON",
	Long: `Print the rows a dashboard tab holds as one JSON array on stdout, so they can
be read, diffed and asserted on without driving a TUI.

<tab> is one of: ` + "work, repo, prs, issues, beads, config" + `.

The rows come from the same code the tab draws itself from, loaded the way
--dump-frame loads the dashboard (local git, the on-disk cache, then a live
fetch bounded by --settle), so data that is missing or stale on the tab is
missing or stale here. A tab the project does not show — Work without an
admin.toml, Beads without the tracker, PRs and Issues without a GitHub origin —
is an error.

"admin show prs" carries each PR's bucket (mine, needs-me, waiting, other),
review, checks and conflict state by name. "Needs me" is a real classification,
not a filter over GitHub's review-request list: GitHub clears a review request
the moment you review, so anything reading that list alone loses the PRs you
have already spoken on.`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: tui.ShowTabNames(),
	RunE: func(cmd *cobra.Command, args []string) error {
		settle, _ := cmd.Flags().GetDuration("settle")
		return runShow(cmd.Context(), args[0], settle)
	},
}

func init() {
	showCmd.Flags().Duration("settle", 2*time.Second, "how long to wait for background loads (the live PR/issue fetch)")
}

func runShow(ctx context.Context, tab string, settle time.Duration) error {
	repoRoot, err := effectiveRepoRoot(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // winds down the watcher and the sync poll the load started

	checkout := dashboardCheckout(ctx, repoRoot)
	nwo, _ := git.OriginNWO(ctx, checkout)
	model := tui.New(ctx, repoRoot, nwo, git.CurrentBranch(ctx, checkout), git.DefaultBranch(ctx, checkout),
		tui.WithManifestRoot(dashboardManifestRoot()), tui.WithCheckout(checkout))
	rows, err := tui.ShowRows(model, tab, tui.FrameOptions{Settle: settle})
	if err != nil {
		return fmt.Errorf("show: %w", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}
