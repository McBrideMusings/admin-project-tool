"""Git branch + worktree primitives for the `branch` / `worktree` verbs.

Shells out to `git` (same pattern as admin_lib/guards.py). Branch attachment is
always derived from the branch's *actual* git state — never from a caller's
guess — so a resolved name that already exists locally, on origin, or nowhere is
each handled correctly. Modeled on repo-dash's internal/git + internal/create and
wtree's internal/gitwt (reference only).
"""
from __future__ import annotations

import os
import subprocess
from dataclasses import dataclass


class GitError(Exception):
    """A git invocation failed; message carries git's stderr."""


def _git(*args: str) -> str:
    """Run git, return stdout, raise GitError on non-zero."""
    proc = subprocess.run(["git", *args], capture_output=True, text=True)
    if proc.returncode != 0:
        raise GitError((proc.stderr or proc.stdout).strip() or f"git {' '.join(args)} failed")
    return proc.stdout


def _git_ok(*args: str) -> bool:
    return subprocess.run(["git", *args], capture_output=True, text=True).returncode == 0


def _git_quiet(*args: str) -> str:
    """Run git, return trimmed stdout, empty string on failure."""
    proc = subprocess.run(["git", *args], capture_output=True, text=True)
    return proc.stdout.strip() if proc.returncode == 0 else ""


# --- repo introspection -----------------------------------------------------

def repo_root() -> str:
    """Absolute path of the main working tree's top level."""
    root = _git_quiet("rev-parse", "--show-toplevel")
    if not root:
        raise GitError("not inside a git repository")
    return root


def current_branch() -> str:
    return _git_quiet("symbolic-ref", "--short", "HEAD")


def default_branch() -> str:
    """The repo's default branch — origin/HEAD if known, else main/master/develop."""
    ref = _git_quiet("symbolic-ref", "refs/remotes/origin/HEAD")
    if ref:
        return ref.rsplit("/", 1)[-1]
    for name in ("main", "master", "develop"):
        if branch_exists_local(name):
            return name
    return ""


def branch_exists_local(branch: str) -> bool:
    return _git_ok("show-ref", "--verify", "--quiet", "refs/heads/" + branch)


def branch_exists_remote(branch: str) -> bool:
    return _git_ok("show-ref", "--verify", "--quiet", "refs/remotes/origin/" + branch)


def origin_base() -> str:
    """Ref to fork a new branch from: origin/<default> (refreshed), else <default>."""
    default = default_branch()
    if not default:
        return ""
    subprocess.run(["git", "fetch", "origin", default], capture_output=True, text=True)
    return f"origin/{default}" if branch_exists_remote(default) else default


def fetch_branch(branch: str) -> None:
    _git("fetch", "origin", branch)


# --- current-tree branch attach (P1) ----------------------------------------

def checkout_in_current_tree(branch: str) -> str:
    """Create/checkout `branch` in the current working tree. Returns an action word.

    Attachment derived from git state: already-on → noop; local → switch;
    remote-only → fetch + switch (auto-tracks origin); nowhere → new branch forked
    from origin/<default>.
    """
    if current_branch() == branch:
        return "already on"
    if branch_exists_local(branch):
        _git("switch", branch)
        return "switched to"
    if branch_exists_remote(branch):
        fetch_branch(branch)
        _git("switch", branch)
        return "checked out"
    base = origin_base()
    if base:
        _git("switch", "-c", branch, base)
    else:
        _git("switch", "-c", branch)
    return "created"


# --- worktrees (P2) ---------------------------------------------------------

@dataclass
class Worktree:
    path: str
    branch: str = ""   # short branch name, "" if detached/bare
    bare: bool = False


def worktrees() -> list[Worktree]:
    """Parse `git worktree list --porcelain`."""
    out = _git_quiet("worktree", "list", "--porcelain")
    result: list[Worktree] = []
    cur: dict = {}
    for line in out.splitlines():
        if line.startswith("worktree "):
            if cur:
                result.append(_wt_from(cur))
            cur = {"path": line[len("worktree "):]}
        elif line == "bare":
            cur["bare"] = True
        elif line.startswith("branch "):
            cur["branch"] = line[len("branch "):].removeprefix("refs/heads/")
    if cur:
        result.append(_wt_from(cur))
    return result


def _wt_from(d: dict) -> Worktree:
    return Worktree(path=d["path"], branch=d.get("branch", ""), bare=d.get("bare", False))


def add_new_branch(path: str, branch: str, base: str) -> None:
    args = ["worktree", "add", "-b", branch, path]
    if base:
        args.append(base)
    _git(*args)


def add_existing(path: str, branch: str) -> None:
    _git("worktree", "add", path, branch)


def worktree_for_branch(branch: str) -> str | None:
    """Path of the worktree that has `branch` checked out, or None."""
    for w in worktrees():
        if w.branch == branch:
            return w.path
    return None


def add_worktree(path: str, branch: str) -> None:
    """Create a worktree at `path` for `branch`, attaching by the branch's state.

    git allows one worktree per branch, so callers check worktree_for_branch first.
    """
    if branch_exists_local(branch):
        add_existing(path, branch)
    elif branch_exists_remote(branch):
        fetch_branch(branch)
        add_existing(path, branch)
    else:
        add_new_branch(path, branch, origin_base())


# --- cleanup (P3) -----------------------------------------------------------

@dataclass
class Branch:
    name: str
    is_merged: bool = False
    is_stale: bool = False
    is_personal: bool = False   # configured user has a commit unique to this branch
    remote_gone: bool = False   # tracked an origin branch that no longer exists
    fully_pushed: bool = False  # tracks origin and is not ahead (no local-only commits)
    ahead: bool = False         # local tip has unpushed local-only commits
    age_str: str = ""


@dataclass
class DeadBranch:
    name: str
    reason: str


def fetch_prune() -> None:
    """Refresh origin + prune stale remote-tracking refs so 'gone'/'pushed' are
    judged against current origin. Never mutates origin; errors swallowed."""
    subprocess.run(["git", "fetch", "--prune", "origin"], capture_output=True, text=True)


def force_delete_branch(name: str) -> None:
    _git("branch", "-D", name)


def remove_worktree(path: str, force: bool = False) -> None:
    args = ["worktree", "remove"]
    if force:
        args.append("--force")
    args.append(path)
    _git(*args)


def worktree_dirty(path: str) -> bool:
    """True if the worktree at `path` has uncommitted changes."""
    proc = subprocess.run(["git", "-C", path, "status", "--porcelain"],
                          capture_output=True, text=True)
    return bool(proc.stdout.strip())


def _merged_set(default: str) -> set[str]:
    """Local branches merged into the default branch (origin/<default>, else local)."""
    if not default:
        return set()
    proc = subprocess.run(
        ["git", "branch", "--merged", "origin/" + default, "--format=%(refname:short)"],
        capture_output=True, text=True)
    if proc.returncode != 0:
        proc = subprocess.run(
            ["git", "branch", "--merged", default, "--format=%(refname:short)"],
            capture_output=True, text=True)
    return {b.strip() for b in proc.stdout.splitlines() if b.strip()}


def _is_personal(branch: str, default: str, email: str) -> bool:
    if not email:
        return False
    rng = f"{default}..refs/heads/{branch}" if default else "refs/heads/" + branch
    return bool(_git_quiet("log", "-1", "--author=" + email, "--format=%H", rng))


def list_cleanup_candidates(stale_after_days: int = 30) -> list[Branch]:
    """Local branches merged into the default, or older than the stale cutoff.

    Excludes the current branch, the default branch, and any branch checked out
    in a worktree. A fresh personal branch with local-only work is skipped (still
    in use). Callers should fetch_prune() first so 'gone'/'pushed' are accurate.
    """
    default = default_branch()
    current = current_branch()
    email = _git_quiet("config", "user.email")
    checked_out = {w.branch for w in worktrees() if w.branch}

    out = _git_quiet("for-each-ref",
                     "--format=%(refname:short)\t%(committerdate:unix)\t%(committerdate:relative)\t%(upstream:short)\t%(upstream:track)",
                     "refs/heads/")
    merged = _merged_set(default)
    import time
    cutoff = time.time() - stale_after_days * 86400

    result: list[Branch] = []
    for line in out.splitlines():
        if not line.strip():
            continue
        parts = line.split("\t")
        if len(parts) < 3:
            continue
        name, ts_str, age_str = parts[0], parts[1], parts[2]
        upstream = parts[3].strip() if len(parts) >= 4 else ""
        track = parts[4] if len(parts) >= 5 else ""
        if not name or name == default or name == current or name in checked_out:
            continue
        try:
            ts = float(ts_str.strip())
        except ValueError:
            continue
        is_merged = name in merged
        stale = ts < cutoff
        personal = _is_personal(name, default, email)
        gone = "gone" in track
        ahead = "ahead" in track
        fully_pushed = bool(upstream) and not gone and not ahead
        # A fresh personal branch (not merged/stale, has local-only work) is still
        # in active use — skip it. Gone/fully-pushed are always safe candidates.
        if not gone and not fully_pushed and not is_merged and not stale and personal:
            continue
        result.append(Branch(name=name, is_merged=is_merged, is_stale=stale,
                             is_personal=personal, remote_gone=gone,
                             fully_pushed=fully_pushed, ahead=ahead,
                             age_str=age_str.strip()))
    result.sort(key=lambda b: b.name)
    return result


def branch_status(name: str) -> Branch:
    """Compute cleanup signals for a single branch, even one checked out in a
    worktree (which list_cleanup_candidates excludes). Used by worktree pruning."""
    default = default_branch()
    email = _git_quiet("config", "user.email")
    line = _git_quiet(
        "for-each-ref",
        "--format=%(committerdate:relative)\t%(upstream:short)\t%(upstream:track)",
        "refs/heads/" + name)
    parts = line.split("\t")
    age_str = parts[0].strip() if parts else ""
    upstream = parts[1].strip() if len(parts) >= 2 else ""
    track = parts[2] if len(parts) >= 3 else ""
    gone = "gone" in track
    ahead = "ahead" in track
    return Branch(
        name=name,
        is_merged=name in _merged_set(default),
        is_personal=_is_personal(name, default, email),
        remote_gone=gone,
        fully_pushed=bool(upstream) and not gone and not ahead,
        ahead=ahead,
        age_str=age_str,
    )


def classify_dead(cands: list[Branch], merged_prs: dict[str, bool]):
    """Split candidates into provably-safe-to-delete branches and survivors.

    merged_prs: head branch names whose PR merged on GitHub (catches squash/rebase
    merges git's --merged misses); gated on !ahead so a re-created name carrying
    new local work survives. Origin is never touched. Empty map when gh absent.
    """
    dead: list[DeadBranch] = []
    survivors: list[Branch] = []
    for br in cands:
        if br.remote_gone:
            dead.append(DeadBranch(br.name, "remote gone"))
        elif merged_prs.get(br.name) and not br.ahead:
            dead.append(DeadBranch(br.name, "PR merged"))
        elif br.is_merged:
            dead.append(DeadBranch(br.name, "merged"))
        elif br.fully_pushed:
            dead.append(DeadBranch(br.name, "on origin"))
        else:
            survivors.append(br)
    return dead, survivors
