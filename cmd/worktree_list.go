package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

var worktreeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List this repo's worktrees with git, PR and live agent state",
	Long: `List every worktree of the repo containing the current directory: its
branch, its working-tree counts and any paused rebase/merge/cherry-pick/revert/
bisect, how far it is from the default branch and from its origin copy, its PR,
and the agent running in it.

The git and PR values come from the same read the dashboard's Repo tab draws,
waiting up to --settle for GitHub; with no gh, no GitHub origin or a failed
fetch the PR column is empty and the command still succeeds.

The agent column is a launcher-dispatched worker ('admin worktree <input> -x',
a kind="workflow" worker) whose live-work entry names this worktree. Before
printing, each live entry's pane is checked with [terminal].status and its agent
asked whether it is working or blocked on a prompt: running, waiting, finished
(the reaper refused to remove it) or gone (its pane closed and nothing has
reaped it yet). A non-worker entry whose pane is gone is ended, as the
dashboard would. A worker with locale = "hosted" runs on a dashboard's own pty
and is visible only in that dashboard, never here.

--json prints the same rows as one JSON array; an absent group (no upstream,
no PR, no agent) is null.

To create a worktree for a branch literally named "list", use
'admin worktree -- list'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, _ := cmd.Flags().GetBool("json")
		settle, _ := cmd.Flags().GetDuration("settle")
		return runWorktreeList(cmd.Context(), os.Stdout, asJSON, settle)
	},
}

func init() {
	worktreeListCmd.Flags().Bool("json", false, "print the rows as one JSON array")
	worktreeListCmd.Flags().Duration("settle", 2*time.Second, "how long to wait for GitHub's PR data")
	worktreeCmd.AddCommand(worktreeListCmd)
}

// worktreeListRow is one printed row: the dashboard's worktree row plus the
// agent working in it.
type worktreeListRow struct {
	tui.WorktreeRow
	Agent *worktreeAgent `json:"agent"`
}

// worktreeAgent is the live worker whose worktree this is.
type worktreeAgent struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Target   string    `json:"target"`
	State    string    `json:"state"` // running, waiting, finished, gone
	Activity string    `json:"activity"`
	Started  time.Time `json:"started"`
}

func runWorktreeList(ctx context.Context, out io.Writer, asJSON bool, settle time.Duration) error {
	repoRoot, err := cwdRepoRoot(ctx)
	if err != nil {
		return err
	}
	agents := worktreeAgents(repoRoot)

	checkout := dashboardCheckout(ctx, repoRoot)
	nwo, _ := git.OriginNWO(ctx, checkout)
	model := tui.New(ctx, repoRoot, nwo, git.CurrentBranch(ctx, checkout), git.DefaultBranch(ctx, checkout),
		tui.WithManifestRoot(dashboardManifestRoot()), tui.WithCheckout(checkout), remoteOption(ctx, checkout, nwo))
	wts, err := tui.WorktreeRows(model, tui.FrameOptions{Settle: settle})
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	rows := make([]worktreeListRow, len(wts))
	for i, w := range wts {
		rows[i] = worktreeListRow{WorktreeRow: w, Agent: agentFor(agents, w.Path)}
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	printWorktreeTable(out, rows)
	return nil
}

// placedAgent is a worker and the worktree it runs in.
type placedAgent struct {
	worktree string
	agent    worktreeAgent
}

// worktreeAgents reads repoRoot's shared live-work entries, checks every live
// one's pane and agent, applies what every reader applies, and returns the
// workers still live. Nothing reconciled these since the process that wrote
// them, so a file can say running for a pane closed long ago.
func worktreeAgents(repoRoot string) []placedAgent {
	if err := livework.Default.Sync(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	var statusCmd string
	if cfg, err := config.Load(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading %s's config: %v — pane liveness not checked\n", repoRoot, err)
	} else {
		statusCmd = cfg.Terminal.Status
	}
	results := kinds.ProbeLiveness(kinds.LiveEntries(), statusCmd, kinds.PaneAndAgent)
	kinds.ApplyLiveness(results)
	gone := map[string]bool{}
	for _, l := range results {
		gone[l.ID] = l.Gone()
	}

	var out []placedAgent
	for _, e := range livework.Default.List() {
		if e.Worker == nil || !git.SamePath(e.Root, repoRoot) {
			continue
		}
		st, ok := livework.Default.State(e.ID)
		if !ok || !st.Reapable() {
			continue
		}
		state := string(st)
		if gone[e.ID] {
			state = "gone"
		}
		activity, _ := livework.Default.Activity(e.ID)
		out = append(out, placedAgent{worktree: e.Worker.Worktree, agent: worktreeAgent{
			ID: e.ID, Name: e.Name, Target: e.Worker.Target, State: state, Activity: activity, Started: e.StartedAt,
		}})
	}
	return out
}

// agentFor is the newest worker running in path, nil when none is.
func agentFor(agents []placedAgent, path string) *worktreeAgent {
	var best *worktreeAgent
	for i := range agents {
		a := &agents[i].agent
		if git.SamePath(agents[i].worktree, path) && (best == nil || a.Started.After(best.Started)) {
			best = a
		}
	}
	return best
}

func printWorktreeTable(out io.Writer, rows []worktreeListRow) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  BRANCH\tPATH\tGIT\tDEFAULT\tUPSTREAM\tPR\tAGENT")
	home, _ := os.UserHomeDir()
	for _, r := range rows {
		mark := " "
		if r.Current {
			mark = "@"
		}
		branch := r.Branch
		if r.Detached {
			branch = "(detached)"
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t%s\n", mark, branch, tildePath(r.Path, home),
			gitCell(r.WorktreeRow), defaultCell(r.WorktreeRow), upstreamCell(r.Upstream), prCell(r.PR), agentCell(r.Agent))
	}
	tw.Flush()
}

func tildePath(p, home string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + p[len(home):]
	}
	return p
}

// gitCell is the working tree in `git status` shorthand: +staged ~unstaged
// ?untracked !conflicted, then the paused operation.
func gitCell(r tui.WorktreeRow) string {
	var parts []string
	switch s := r.Status; {
	case s == nil:
		parts = append(parts, "unreadable")
	case s.Staged+s.Unstaged+s.Untracked+s.Conflicted == 0:
		parts = append(parts, "clean")
	default:
		for _, c := range []struct {
			mark string
			n    int
		}{{"+", s.Staged}, {"~", s.Unstaged}, {"?", s.Untracked}, {"!", s.Conflicted}} {
			if c.n > 0 {
				parts = append(parts, c.mark+strconv.Itoa(c.n))
			}
		}
	}
	if r.Op != nil {
		parts = append(parts, *r.Op)
	}
	return strings.Join(parts, " ")
}

func defaultCell(r tui.WorktreeRow) string {
	if r.Default == nil {
		return "—"
	}
	return fmt.Sprintf("↑%d ↓%d", r.Default.Ahead, r.Default.Behind)
}

func upstreamCell(u *tui.Upstream) string {
	switch {
	case u == nil:
		return "—"
	case u.Gone:
		return "gone"
	}
	return fmt.Sprintf("↑%d ↓%d", u.Ahead, u.Behind)
}

func prCell(pr *tui.WorktreePR) string {
	if pr == nil {
		return "—"
	}
	parts := []string{}
	if pr.Number != nil {
		parts = append(parts, "#"+strconv.Itoa(*pr.Number))
	}
	parts = append(parts, pr.State)
	if pr.Checks != nil && *pr.Checks != "none" {
		parts = append(parts, *pr.Checks)
	}
	return strings.Join(parts, " ")
}

func agentCell(a *worktreeAgent) string {
	if a == nil {
		return "—"
	}
	if a.Activity != "" {
		return a.State + " · " + a.Activity
	}
	return a.State
}
