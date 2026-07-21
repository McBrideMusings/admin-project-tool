"""branch / worktree verb bodies (P1, P2) against a real temp git repo."""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import gitwt, herdr, resolve, wtverbs


def _git(cwd, *args):
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True)


@pytest.fixture
def repo(tmp_path, monkeypatch):
    """A git repo with one commit on main, cwd set into it. Herdr disabled."""
    r = tmp_path / "proj"
    r.mkdir()
    _git(r, "init", "-q", "-b", "main")
    _git(r, "config", "user.email", "t@t.co")
    _git(r, "config", "user.name", "t")
    (r / "README.md").write_text("hi\n")
    _git(r, "add", "-A")
    _git(r, "commit", "-qm", "init")
    monkeypatch.chdir(r)
    # Never touch real Herdr in tests.
    monkeypatch.setattr(herdr, "open_in_herdr", lambda *a, **k: False)
    return r


def _branch(name):
    return subprocess.run(["git", "branch", "--show-current"], capture_output=True, text=True).stdout.strip()


# --- branch (P1) ------------------------------------------------------------

def test_branch_creates_new_in_current_tree(repo):
    assert wtverbs.cmd_branch("my-feature") == 0
    assert _branch("") == "my-feature"


def test_branch_switches_to_existing(repo):
    _git(repo, "branch", "existing")
    assert wtverbs.cmd_branch("existing") == 0
    assert _branch("") == "existing"


def test_branch_already_on_is_noop(repo):
    assert wtverbs.cmd_branch("main") == 0
    assert _branch("") == "main"


def test_branch_keeps_slash_name(repo):
    assert wtverbs.cmd_branch("feat/thing") == 0
    assert _branch("") == "feat/thing"


def test_branch_outside_repo_errors(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    assert wtverbs.cmd_branch("x") == 1


# --- worktree (P2) ----------------------------------------------------------

def test_worktree_creates_at_expected_path(repo, tmp_path, monkeypatch):
    root = tmp_path / "wts"
    rc = wtverbs.cmd_worktree("feat/oauth", cfg={"root": str(root)})
    assert rc == 0
    expect = root / "proj" / "feat-oauth"
    assert expect.is_dir()
    assert gitwt.worktree_for_branch("feat/oauth") == str(expect)


def test_worktree_copies_configured_files(repo, tmp_path):
    (repo / ".env").write_text("SECRET=1\n")
    root = tmp_path / "wts"
    wtverbs.cmd_worktree("feat/x", cfg={"root": str(root), "copy": [".env"]})
    assert (root / "proj" / "feat-x" / ".env").read_text() == "SECRET=1\n"


def test_worktree_runs_setup_action(repo, tmp_path):
    calls = []

    def run_action(path, name):
        calls.append((path, name))
        return 0

    root = tmp_path / "wts"
    rc = wtverbs.cmd_worktree("feat/y", cfg={"root": str(root), "setup": "deps"}, run_action=run_action)
    assert rc == 0
    assert calls == [(str(root / "proj" / "feat-y"), "deps")]


def test_worktree_setup_failure_propagates(repo, tmp_path):
    root = tmp_path / "wts"
    rc = wtverbs.cmd_worktree("feat/z", cfg={"root": str(root), "setup": "deps"},
                              run_action=lambda p, n: 3)
    assert rc == 3


def test_worktree_symlinks_manifest(repo, tmp_path):
    (repo / "admin.toml").write_text('project_name = "P"\narchetypes = ["simple"]\n')
    root = tmp_path / "wts"
    wtverbs.cmd_worktree("feat/link", cfg={"root": str(root)})
    link = root / "proj" / "feat-link" / "admin.toml"
    assert link.is_symlink() and link.resolve() == (repo / "admin.toml").resolve()


def test_worktree_idempotent_when_branch_has_one(repo, tmp_path):
    root = tmp_path / "wts"
    assert wtverbs.cmd_worktree("feat/dup", cfg={"root": str(root)}) == 0
    # Second call: branch already has a worktree — reuse, no error.
    assert wtverbs.cmd_worktree("feat/dup", cfg={"root": str(root)}) == 0


def test_worktree_fork_pr_error_hint(repo, tmp_path, monkeypatch):
    # A PR resolving to a head branch that exists nowhere: add_worktree forks a new
    # branch of that name from origin (no origin here) — force the failure path.
    monkeypatch.setattr(resolve, "resolve",
                        lambda inp: resolve.Resolved(branch="ghost", source="pr", is_pr=True))
    monkeypatch.setattr(gitwt, "add_worktree",
                        lambda *a: (_ for _ in ()).throw(gitwt.GitError("no such ref")))
    root = tmp_path / "wts"
    assert wtverbs.cmd_worktree("https://github.com/o/r/pull/1", cfg={"root": str(root)}) == 1
