package shim

import (
	"encoding/json"
	"errors"
	"fmt"
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

// OpenWorktreePaths is the set of worktree checkout paths herdr currently has a
// workspace open on — the strongest "something is working here right now" signal
// available, and the one that distinguishes a worktree an agent is sitting in from
// leftovers that merely look identical to git.
//
// Empty when herdr is unavailable or the call fails, so every caller degrades to
// the timestamp-based signals rather than to a wrong answer.
func OpenWorktreePaths() map[string]bool {
	open := map[string]bool{}
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
		if p := w.Worktree.CheckoutPath; p != "" {
			open[p] = true
		}
	}
	return open
}

// SplitPane opens a new sibling pane in the caller's current herdr tab,
// rooted at cwd, without stealing focus from the calling pane — the
// `herdr pane split --current --direction right --cwd <cwd> --no-focus`
// invocation the herdr skill documents for "run an ordinary command in
// another pane". It returns the new pane's ID (.result.pane.pane_id).
//
// Callers must check Available() first (as RunInPane's caller,
// internal/kinds's herdr dispatch, does): SplitPane makes no availability
// check of its own and simply fails if herdr isn't reachable, because
// "--current" only resolves to something meaningful from inside a pane
// herdr itself launched.
func SplitPane(cwd string) (string, error) {
	out, err := exec.Command("herdr", "pane", "split",
		"--current", "--direction", "right", "--cwd", cwd, "--no-focus").Output()
	if err != nil {
		return "", err
	}
	var resp struct {
		Result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if jsonErr := json.Unmarshal(out, &resp); jsonErr != nil {
		return "", jsonErr
	}
	if resp.Result.Pane.PaneID == "" {
		return "", errors.New("herdr pane split: no pane_id in response")
	}
	return resp.Result.Pane.PaneID, nil
}

// RunInPane types command into paneID followed by Enter — `herdr pane run
// <paneID> <command>` — and returns as soon as herdr has sent it, exactly
// like a person typing the command and hitting return. It does not wait for
// the command to finish or report its exit status; the pane runs
// independently of this process from this point on.
func RunInPane(paneID, command string) error {
	return exec.Command("herdr", "pane", "run", paneID, command).Run()
}

// ClosePane closes paneID — `herdr pane close <paneID>` — the mechanism
// behind the live-work registry's "kill" operation
// (internal/livework.Ops.Kill): closing the pane is a clean, definitive
// stop for both a bare long-running process and an agent worker, since
// both are, at bottom, a herdr pane.
func ClosePane(paneID string) error {
	return exec.Command("herdr", "pane", "close", paneID).Run()
}

// FocusPane brings paneID's workspace and tab to the front — the mechanism
// behind the live-work registry's "attach" operation
// (internal/livework.Ops.Attach). herdr has no single verb to focus an
// arbitrary pane by ID: `herdr pane focus` only moves focus in a direction
// relative to a pane that already has it. A pane's workspace and tab ARE
// each focusable directly by ID, and a pane is always visible within its
// own tab once that tab is frontmost, so focusing both is the real
// mechanism "attaching" needs.
func FocusPane(paneID string) error {
	out, err := exec.Command("herdr", "pane", "get", paneID).Output()
	if err != nil {
		return err
	}
	var resp struct {
		Result struct {
			Pane struct {
				WorkspaceID string `json:"workspace_id"`
				TabID       string `json:"tab_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return err
	}
	if resp.Result.Pane.WorkspaceID == "" || resp.Result.Pane.TabID == "" {
		return fmt.Errorf("herdr pane get %s: missing workspace_id/tab_id", paneID)
	}
	if err := exec.Command("herdr", "workspace", "focus", resp.Result.Pane.WorkspaceID).Run(); err != nil {
		return err
	}
	return exec.Command("herdr", "tab", "focus", resp.Result.Pane.TabID).Run()
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
