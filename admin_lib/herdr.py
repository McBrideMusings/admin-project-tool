"""Herdr association for newly created worktrees.

When admin runs inside a Herdr pane, a fresh worktree is adopted as a Herdr child
workspace of the repo (so it groups under the right parent and gets focused)
instead of just printing a cd. Modeled on repo-dash's internal/shim/herdr.go
(reference only). Opt out per-machine with ADMIN_HERDR=off.
"""
from __future__ import annotations

import os
import shutil
import subprocess


def available() -> bool:
    """True when admin is in a Herdr pane with the herdr CLI reachable, not opted out."""
    if os.getenv("HERDR_ENV") != "1" or os.getenv("ADMIN_HERDR") == "off":
        return False
    return shutil.which("herdr") is not None


def open_in_herdr(repo_root: str, worktree_path: str) -> bool:
    """Adopt worktree_path as a focused Herdr child workspace of repo_root.

    Returns True when Herdr handled it (caller then does NOT print a cd), False
    when Herdr is unavailable or the call failed (caller falls back to cd).
    """
    if not available():
        return False
    proc = subprocess.run(
        ["herdr", "worktree", "open", "--cwd", repo_root, "--path", worktree_path, "--focus"],
        capture_output=True, text=True,
    )
    return proc.returncode == 0
