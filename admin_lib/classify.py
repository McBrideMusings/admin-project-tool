"""Classify the input passed to `admin branch` / `admin worktree`.

Pure syntax only — no network. Decides whether a string is a GitHub PR/issue/
project-board URL, a bare issue/PR number, or free-form branch text. The second
resolution stage (admin_lib/resolve.py) turns the classified result into an
actual branch name, hitting `gh` when needed. Modeled on wtree's
internal/classify (reference only).
"""
from __future__ import annotations

import re
from dataclasses import dataclass
from urllib.parse import urlparse, parse_qs

# Recognized input shapes.
TEXT = "text"      # free-form branch / new-branch name
NUMBER = "number"  # bare issue/PR number, with or without leading "#"
PR = "pr"          # GitHub PR URL
ISSUE = "issue"    # GitHub issue URL


@dataclass
class Classified:
    kind: str
    number: int = 0      # populated for NUMBER, PR, ISSUE
    nwo: str = ""        # "owner/repo" — populated for PR, ISSUE
    text: str = ""       # original input, for TEXT


_PR_URL = re.compile(r"^https://github\.com/([^/]+/[^/]+)/pull/(\d+)")
_ISSUE_URL = re.compile(r"^https://github\.com/([^/]+/[^/]+)/issues/(\d+)")
_PROJECT_URL = re.compile(r"^https://github\.com/orgs/[^/]+/projects/\d+")
_NUMBER = re.compile(r"^\d+$")


def classify(inp: str) -> Classified:
    """Inspect `inp` and return its kind plus parsed components."""
    m = _PR_URL.match(inp)
    if m:
        return Classified(kind=PR, nwo=m.group(1), number=int(m.group(2)))
    m = _ISSUE_URL.match(inp)
    if m:
        return Classified(kind=ISSUE, nwo=m.group(1), number=int(m.group(2)))
    if _PROJECT_URL.match(inp):
        parsed = _parse_project_board_issue(inp)
        if parsed is not None:
            return parsed
    stripped = inp.lstrip("#") if inp.startswith("#") else inp
    if _NUMBER.match(stripped):
        return Classified(kind=NUMBER, number=int(stripped))
    return Classified(kind=TEXT, text=inp)


def _parse_project_board_issue(inp: str):
    """Parse the `issue` query param — encoded as "org|repo|number"."""
    raw = parse_qs(urlparse(inp).query).get("issue", [""])[0]
    if not raw:
        return None
    parts = raw.split("|")
    if len(parts) != 3:
        return None
    org, repo, num_str = parts
    try:
        n = int(num_str)
    except ValueError:
        return None
    if n <= 0:
        return None
    return Classified(kind=ISSUE, nwo=f"{org}/{repo}", number=n)
