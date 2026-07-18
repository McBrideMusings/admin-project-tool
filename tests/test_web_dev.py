"""Tests for admin_lib.web_dev.setup_dev_mounts.

Regression cover for #392: setup_dev_mounts added/updated the configured
symlinks but never pruned symlinks under data/ for names dropped from
DEV_MOUNTS, so stale mounts persisted as top-level roots.
"""
from __future__ import annotations

import os
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.web_dev import setup_dev_mounts  # noqa: E402


def _target(tmp_path, name):
    t = tmp_path / "targets" / name
    t.mkdir(parents=True)
    return str(t)


def test_creates_configured_symlinks(tmp_path):
    data = tmp_path / "data"
    data.mkdir()
    tgt = _target(tmp_path, "media")
    assert setup_dev_mounts(str(data), [("media", tgt)]) == 0
    link = data / "media"
    assert link.is_symlink() and os.readlink(link) == tgt


def test_prunes_stale_symlink_when_mount_dropped(tmp_path):
    data = tmp_path / "data"
    data.mkdir()
    media, docs = _target(tmp_path, "media"), _target(tmp_path, "docs")

    assert setup_dev_mounts(str(data), [("media", media), ("docs", docs)]) == 0
    assert (data / "media").is_symlink() and (data / "docs").is_symlink()

    # Narrow DEV_MOUNTS to just media — docs must be pruned.
    assert setup_dev_mounts(str(data), [("media", media)]) == 0
    assert (data / "media").is_symlink()
    assert not (data / "docs").exists()


def test_prune_never_removes_real_dirs_or_files(tmp_path):
    data = tmp_path / "data"
    data.mkdir()
    real_dir = data / "keepdir"
    real_dir.mkdir()
    real_file = data / "keepfile.txt"
    real_file.write_text("x")
    media = _target(tmp_path, "media")

    assert setup_dev_mounts(str(data), [("media", media)]) == 0
    assert real_dir.is_dir()
    assert real_file.is_file()
    assert (data / "media").is_symlink()


def test_repoints_changed_mount_target(tmp_path):
    data = tmp_path / "data"
    data.mkdir()
    old, new = _target(tmp_path, "old"), _target(tmp_path, "new")

    assert setup_dev_mounts(str(data), [("media", old)]) == 0
    assert os.readlink(data / "media") == old
    assert setup_dev_mounts(str(data), [("media", new)]) == 0
    assert os.readlink(data / "media") == new
