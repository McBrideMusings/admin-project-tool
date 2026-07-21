"""Derive a short branch name from an issue. Deliberately simple: issue number
plus a few sanitized title words (repo-dash's form — no configurable knobs).
"""
from __future__ import annotations

_MAX_WORDS = 5


def slug_from_issue(number: int, title: str) -> str:
    """Build a branch name like "42-fix-the-thing" from an issue number + title.

    Falls back to "issue-<n>" when the title yields no usable words.
    """
    kept: list[str] = []
    for word in title.lower().split():
        w = _sanitize(word)
        if not w:
            continue
        kept.append(w)
        if len(kept) >= _MAX_WORDS:
            break
    if not kept:
        return f"issue-{number}"
    return f"{number}-" + "-".join(kept)


def _sanitize(word: str) -> str:
    """Keep lowercase letters and digits; drop everything else."""
    return "".join(c for c in word.lower() if c.isascii() and (c.isdigit() or ("a" <= c <= "z")))
