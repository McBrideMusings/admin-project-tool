"""Cleanup classifier + prune (P3)."""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import ghwt, gitwt, wtverbs


def _git(cwd, *args):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True)


@pytest.fixture
def repo_with_origin(tmp_path, monkeypatch):
    """A clone (main checked out) with an origin remote, cwd inside it.

    Gives real 'merged', 'fully pushed', and 'remote gone' signals to classify.
    """
    origin = tmp_path / "origin.git"
    _git(tmp_path, "init", "-q", "--bare", "-b", "main", str(origin))

    work = tmp_path / "work"
    _git(tmp_path, "clone", "-q", str(origin), str(work))
    _git(work, "config", "user.email", "t@t.co")
    _git(work, "config", "user.name", "t")
    (work / "README.md").write_text("hi\n")
    _git(work, "add", "-A")
    _git(work, "commit", "-qm", "init")
    _git(work, "push", "-q", "origin", "main")

    monkeypatch.chdir(work)
    # No real gh in tests.
    monkeypatch.setattr(ghwt, "merged_pr_head_branches", lambda *a, **k: {})
    return work


def test_merged_branch_is_dead(repo_with_origin):
    w = repo_with_origin
    _git(w, "checkout", "-q", "-b", "feature")
    (w / "f.txt").write_text("x")
    _git(w, "add", "-A")
    _git(w, "commit", "-qm", "feat")
    _git(w, "checkout", "-q", "main")
    _git(w, "merge", "-q", "feature")
    _git(w, "push", "-q", "origin", "main")  # merged status is judged vs origin/main
    cands = gitwt.list_cleanup_candidates()
    dead, survivors = gitwt.classify_dead(cands, {})
    names = {d.name for d in dead}
    assert "feature" in names


def test_fully_pushed_branch_is_dead(repo_with_origin):
    w = repo_with_origin
    _git(w, "checkout", "-q", "-b", "pushed")
    _git(w, "push", "-q", "-u", "origin", "pushed")
    _git(w, "checkout", "-q", "main")
    cands = gitwt.list_cleanup_candidates()
    dead = {d.name: d.reason for d in gitwt.classify_dead(cands, {})[0]}
    assert dead.get("pushed") in ("on origin", "merged")


def test_unmerged_personal_branch_is_never_dead(repo_with_origin):
    # A personal, unmerged, unpushed branch with local work is in active use —
    # excluded from candidates entirely, so prune never touches it.
    w = repo_with_origin
    _git(w, "checkout", "-q", "-b", "wip")
    (w / "w.txt").write_text("x")
    _git(w, "add", "-A")
    _git(w, "commit", "-qm", "wip")
    _git(w, "checkout", "-q", "main")
    cands = gitwt.list_cleanup_candidates()
    dead, _ = gitwt.classify_dead(cands, {})
    assert "wip" not in {d.name for d in dead}
    assert "wip" not in {c.name for c in cands}


def test_pr_merged_signal_catches_squash(repo_with_origin):
    w = repo_with_origin
    # A branch pushed to origin then rewound on main (simulating squash-merge:
    # commits never land on main, so git --merged misses it). gh says its PR merged.
    _git(w, "checkout", "-q", "-b", "squashed")
    (w / "s.txt").write_text("x")
    _git(w, "add", "-A")
    _git(w, "commit", "-qm", "s")
    _git(w, "push", "-q", "-u", "origin", "squashed")
    # Advance the branch past origin? No — keep not-ahead so PR-merged applies.
    _git(w, "checkout", "-q", "main")
    cands = gitwt.list_cleanup_candidates()
    dead = {d.name: d.reason for d in gitwt.classify_dead(cands, {"squashed": True})[0]}
    assert "squashed" in dead


def test_current_and_default_never_candidates(repo_with_origin):
    cands = gitwt.list_cleanup_candidates()
    assert "main" not in {c.name for c in cands}


def test_prune_dry_run_deletes_nothing(repo_with_origin):
    w = repo_with_origin
    _git(w, "checkout", "-q", "-b", "gone-soon")
    _git(w, "push", "-q", "-u", "origin", "gone-soon")
    _git(w, "checkout", "-q", "main")
    rc = wtverbs.prune_branches(dry_run=True, yes=True)
    assert rc == 0
    # Branch still exists.
    assert gitwt.branch_exists_local("gone-soon")


def test_prune_deletes_dead_branch(repo_with_origin):
    w = repo_with_origin
    _git(w, "checkout", "-q", "-b", "delete-me")
    _git(w, "push", "-q", "-u", "origin", "delete-me")
    _git(w, "checkout", "-q", "main")
    rc = wtverbs.prune_branches(dry_run=False, yes=True)
    assert rc == 0
    assert not gitwt.branch_exists_local("delete-me")
