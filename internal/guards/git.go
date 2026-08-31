package guards

import (
	"fmt"
	"os/exec"
	"strings"
)

func gitCapture(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func checkGitRepo(guardName string) (bool, string) {
	out, err := gitCapture("rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(out) != "true" {
		return false, fmt.Sprintf("%s: not a git repository (or git is unavailable).", guardName)
	}
	return true, ""
}

// gitCleanGuard is the Go port of admin_lib/guards.py::_git_clean_guard.
// Unchanged from Python — no known bugs here.
func gitCleanGuard() (bool, string) {
	if ok, msg := checkGitRepo("git-clean"); !ok {
		return false, msg
	}
	out, err := exec.Command("git", "status", "--porcelain").CombinedOutput()
	if err != nil {
		return false, "git-clean: could not read git status — aborting to be safe."
	}
	if strings.TrimSpace(string(out)) != "" {
		return false, "git-clean: working tree has uncommitted or untracked changes — " +
			"commit or stash them before deploying (run: git status)."
	}
	return true, ""
}

// gitPushedGuard is the Go port of admin_lib/guards.py::_git_pushed_guard,
// deliberately NOT a faithful port: the Python original hard-codes
// "origin/<branch>" as the upstream and compares against the local
// remote-tracking ref with no fetch, which are two independently filed bugs
// (admin-c8lc, admin-d03k) this port fixes rather than carries forward:
//
//   - admin-c8lc: a branch tracking anything other than origin/<same-name>
//     (a fork remote, a differently-named upstream branch) got a false
//     "not on origin" failure even when fully pushed. Fixed by reading the
//     branch's actual configured upstream (`@{upstream}`) instead of
//     assuming one.
//   - admin-d03k: the "pushed" check compared HEAD against the local
//     remote-tracking ref, which is only as fresh as the last fetch — a
//     branch rewound or advanced on the remote by someone else could read
//     as "pushed" against stale local knowledge. Fixed by fetching the
//     upstream ref immediately before comparing (bd's own first listed
//     option for d03k: "git fetch --quiet origin <branch> before the
//     rev-list — accurate, but adds network latency + a failure mode",
//     which this guard accepts since git-pushed is already a network-shaped
//     precondition by definition).
func gitPushedGuard() (bool, string) {
	if ok, msg := checkGitRepo("git-pushed"); !ok {
		return false, msg
	}

	branchOut, err := gitCapture("rev-parse", "--abbrev-ref", "HEAD")
	branch := strings.TrimSpace(branchOut)
	if err != nil || branch == "" || branch == "HEAD" {
		return false, "git-pushed: detached HEAD — check out a branch and push it before deploying."
	}

	upstreamOut, err := gitCapture("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	upstream := strings.TrimSpace(upstreamOut)
	if err != nil || upstream == "" {
		return false, fmt.Sprintf(
			"git-pushed: branch '%s' has no upstream configured — "+
				"push it first:  git push -u origin %s", branch, branch)
	}
	remote, remoteBranch, ok := strings.Cut(upstream, "/")
	if !ok || remote == "" || remoteBranch == "" {
		return false, fmt.Sprintf(
			"git-pushed: could not parse upstream %q for branch '%s'.", upstream, branch)
	}

	if _, err := gitCapture("fetch", "--quiet", remote, remoteBranch); err != nil {
		return false, fmt.Sprintf(
			"git-pushed: could not fetch '%s' from '%s' to check whether '%s' is current — "+
				"check your network/remote access.", remoteBranch, remote, branch)
	}

	aheadOut, err := gitCapture("rev-list", "--count", "FETCH_HEAD..HEAD")
	ahead := strings.TrimSpace(aheadOut)
	if err != nil || ahead != "0" {
		return false, fmt.Sprintf(
			"git-pushed: '%s' has commits not pushed to '%s' (or the comparison failed) — "+
				"push first:  git push %s %s", branch, upstream, remote, branch)
	}
	return true, ""
}
