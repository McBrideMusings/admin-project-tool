"""Thin wrapper over the `gh` CLI for the lookups the branch/worktree verbs need.

Shells out to `gh` (already an external-CLI dependency in spirit, like `git`).
Every call degrades gracefully: when `gh` is missing, unauthed, or errors, the
caller gets a clear signal (GhUnavailable / an error return) rather than a crash.
The text/branch-name resolver path never touches this module.
"""
from __future__ import annotations

import shutil
import subprocess


class GhUnavailable(Exception):
    """`gh` is not installed or not usable — distinct from a call that ran and failed."""


class GhError(Exception):
    """A `gh` invocation ran but returned non-zero (unknown number, no auth, etc.)."""


def available() -> bool:
    """True when the `gh` CLI is on PATH."""
    return shutil.which("gh") is not None


def _run(*args: str) -> str:
    if not available():
        raise GhUnavailable("gh CLI not found on PATH — install GitHub CLI or pass a branch name")
    proc = subprocess.run(["gh", *args], capture_output=True, text=True)
    if proc.returncode != 0:
        raise GhError((proc.stderr or proc.stdout).strip() or f"gh {' '.join(args)} failed")
    return proc.stdout


def issue_title(number: int) -> str:
    """Fetch an issue's title."""
    return _run("issue", "view", str(number), "--json", "title", "-q", ".title").strip()


def pr_head_ref(number: int) -> str:
    """Fetch a PR's head branch name."""
    return _run("pr", "view", str(number), "--json", "headRefName", "-q", ".headRefName").strip()


def merged_pr_head_branches(limit: int = 200) -> dict[str, bool]:
    """Head branch names of the most recent merged PRs, as a set (dict of name->True).

    Catches squash/rebase merges that `git branch --merged` misses because the
    branch commits never land on the default branch. Returns an empty set when
    `gh` is unavailable or the call fails — callers treat that as "no PR signal".
    """
    try:
        out = _run("pr", "list", "--state", "merged", "--limit", str(limit),
                   "--json", "headRefName", "-q", ".[].headRefName")
    except (GhUnavailable, GhError):
        return {}
    return {b.strip(): True for b in out.splitlines() if b.strip()}
