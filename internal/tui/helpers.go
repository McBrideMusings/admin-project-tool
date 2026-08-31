package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// statFn is os.Stat, indirected so tests could stub it.
var statFn = os.Stat

// removeWorktreeFn is git.RemoveWorktree, indirected so tests can observe the
// force flag a delete passes without touching a real repo.
var removeWorktreeFn = git.RemoveWorktree

// stashWorktreeFn is the delete step's recovery step — git.StashWorktree with the
// step's own name in front of git's refusal, so a failed row says which half of
// the delete gave up. Wrapped with %w rather than %s: the git.CmdError underneath
// carries the lines the modal row has no space for, and the detail overlay finds
// it by unwrapping. Indirected as a var so tests can observe the call without a
// real repo.
var stashWorktreeFn = func(ctx context.Context, path string) error {
	if err := git.StashWorktree(ctx, path); err != nil {
		return fmt.Errorf("stash failed: %w", err)
	}
	return nil
}

func issueURL(nwo string, n int) string { return gh.IssueURL(nwo, n) }

func repoURL(nwo string) string { return gh.RepoURL(nwo) }

func branchURL(nwo, branch string) string { return gh.BranchURL(nwo, branch) }

// commitURL builds the GitHub URL for a commit sha.
func commitURL(nwo, sha string) string {
	if nwo == "" || sha == "" {
		return ""
	}
	return "https://github.com/" + nwo + "/commit/" + sha
}

// openURLCmd opens url in the default browser without blocking the UI.
func openURLCmd(url string) tea.Cmd {
	return func() tea.Msg {
		if url == "" {
			return actionMsg{text: "nothing to open"}
		}
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", url)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		default:
			cmd = exec.Command("xdg-open", url)
		}
		if err := cmd.Start(); err != nil {
			return actionMsg{text: "open failed", err: err}
		}
		return actionMsg{text: "opened in browser", color: cCyan}
	}
}
