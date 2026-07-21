"""Resolve a `admin branch` / `admin worktree` input to a branch name.

Phase 2 of the resolver (phase 1 is admin_lib/classify). Turns a classified
input into a single branch-name string, hitting `gh` for issue titles and PR head
refs. How that branch then attaches to git (new / local / remote / already
checked out) is derived later from git state by admin_lib/gitwt — never guessed
here. Same-repo PRs only; a fork PR's head ref won't exist on origin, so the git
attach step fails with a clear message (fork support is a deferred follow-up).
"""
from __future__ import annotations

from dataclasses import dataclass

from admin_lib import classify, ghwt, slug


class ResolveError(Exception):
    """Input could not be resolved to a branch name."""


@dataclass
class Resolved:
    branch: str          # the branch name to create/checkout
    source: str          # classify kind that produced it: text / number / pr / issue
    is_pr: bool = False  # True when it resolved to a PR's existing head branch


def resolve(inp: str) -> Resolved:
    """Resolve `inp` (branch name, #num, num, issue/PR URL) to a branch name."""
    c = classify.classify(inp)

    if c.kind == classify.PR:
        try:
            return Resolved(branch=_pr_branch(c.number), source="pr", is_pr=True)
        except ghwt.GhError as e:
            raise ResolveError(f"could not resolve PR #{c.number}: {e}")

    if c.kind == classify.ISSUE:
        try:
            return Resolved(branch=_issue_branch(c.number), source="issue")
        except ghwt.GhError as e:
            raise ResolveError(f"could not resolve issue #{c.number}: {e}")

    if c.kind == classify.NUMBER:
        # A bare number is either a PR or an issue — ask gh, PR first.
        if not ghwt.available():
            raise ResolveError(
                f"'{inp}' looks like an issue/PR number but the gh CLI is not "
                f"available — install GitHub CLI, or pass a branch name instead"
            )
        try:
            return Resolved(branch=_pr_branch(c.number), source="number", is_pr=True)
        except ghwt.GhError:
            pass  # not a PR — try issue
        try:
            return Resolved(branch=_issue_branch(c.number), source="number")
        except ghwt.GhError as e:
            raise ResolveError(f"#{c.number} is neither a reachable PR nor issue: {e}")

    # TEXT: the input is the branch name. Trim; keep slashes (feat/foo is valid).
    name = inp.strip()
    if not name:
        raise ResolveError("empty branch name")
    return Resolved(branch=name, source="text")


def _pr_branch(number: int) -> str:
    """Head ref for a PR. Raises GhUnavailable→ResolveError; lets GhError bubble
    (callers convert it, and the NUMBER path uses it to fall back to issue)."""
    try:
        head = ghwt.pr_head_ref(number)
    except ghwt.GhUnavailable as e:
        raise ResolveError(str(e))
    if not head:
        raise ResolveError(f"PR #{number} has no head branch")
    return head


def _issue_branch(number: int) -> str:
    try:
        title = ghwt.issue_title(number)
    except ghwt.GhUnavailable as e:
        raise ResolveError(str(e))
    return slug.slug_from_issue(number, title)
