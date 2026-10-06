package shim

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
)

// ErrHerdrUnavailable reports that repo is not running under a usable herdr (not
// a herdr pane, REPO_HERDR=off, or the CLI is missing) — distinct from a herdr
// call that ran and failed, which returns the exec error instead.
var ErrHerdrUnavailable = errors.New("herdr unavailable")

// OpenInHerdr adopts worktreePath as a herdr child workspace of the repo at
// repoRoot and focuses it, instead of cd-ing the current pane into it. It
// returns nil when herdr handled the move, so the caller must NOT also emit the
// cd sentinel (which would drag the current pane along too).
//
// It returns ErrHerdrUnavailable when herdr isn't usable (the caller then falls
// back to PrintCD) and the underlying exec error when herdr ran but failed — so
// the dashboard can report a genuine failure distinctly. herdr resolves the
// child under the right repo parent from --cwd, so it does not depend on which
// pane launched repo. Its output is discarded (Stdout/Stderr left nil →
// os.DevNull) because the repo() shell wrapper captures repo's stdout and would
// print any noise.
func OpenInHerdr(repoRoot, worktreePath string) error {
	if !Available() {
		return ErrHerdrUnavailable
	}
	cmd := exec.Command("herdr", "worktree", "open",
		"--cwd", repoRoot, "--path", worktreePath, "--focus")
	return cmd.Run()
}

// OpenWorkspaces maps each worktree checkout path herdr currently has a workspace
// open on to that workspace's id. A path in the map is the strongest "something is
// working here right now" signal available — it distinguishes a worktree an agent
// is sitting in from leftovers that merely look identical to git — and the id is
// what FocusWorkspace needs to jump there.
//
// Empty when herdr is unavailable or the call fails, so every caller degrades to
// the timestamp-based signals rather than to a wrong answer.
func OpenWorkspaces() map[string]string {
	open := map[string]string{}
	if !Available() {
		return open
	}
	out, err := exec.Command("herdr", "workspace", "list").Output()
	if err != nil {
		return open
	}
	var resp struct {
		Result struct {
			Workspaces []struct {
				ID       string `json:"workspace_id"`
				Worktree struct {
					CheckoutPath string `json:"checkout_path"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &resp) != nil {
		return open
	}
	for _, w := range resp.Result.Workspaces {
		if p := w.Worktree.CheckoutPath; p != "" && w.ID != "" {
			open[p] = w.ID
		}
	}
	return open
}

// FocusWorkspace brings an already-open herdr workspace to the front.
func FocusWorkspace(id string) error {
	if !Available() {
		return ErrHerdrUnavailable
	}
	return exec.Command("herdr", "workspace", "focus", id).Run()
}

// Available reports whether repo is running in a herdr pane with the herdr CLI
// reachable and not disabled via REPO_HERDR=off. When true the dashboard creates
// worktrees inline (opening a focused herdr workspace) instead of quitting to
// cd the parent shell.
func Available() bool {
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("REPO_HERDR") == "off" {
		return false
	}
	_, err := exec.LookPath("herdr")
	return err == nil
}
