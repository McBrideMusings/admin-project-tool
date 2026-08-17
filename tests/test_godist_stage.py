"""Tests for admin_lib.godist.stage_dist — copies a [go_dist]-built matrix
(binary-* + VERSION) into a destination dir, e.g. an app bundle's resource dir."""
from __future__ import annotations

import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import godist  # noqa: E402


def _dist_dir(tmp_path):
    d = tmp_path / "dist"
    d.mkdir()
    (d / "myserver-darwin-arm64").write_text("a")
    (d / "myserver-darwin-amd64").write_text("b")
    (d / "myserver-linux-amd64").write_text("c")
    (d / "VERSION").write_text("v1.2.3\n")
    return d


def test_stage_dist_copies_binaries_and_version(tmp_path):
    dist = _dist_dir(tmp_path)
    dest = tmp_path / "staged"
    n = godist.stage_dist({"out_dir": str(dist), "binary": "myserver"}, str(dest))
    assert n == 3
    assert (dest / "VERSION").read_text() == "v1.2.3\n"
    assert (dest / "myserver-darwin-arm64").exists()


def test_stage_dist_removes_stale_binaries_first(tmp_path):
    dist = _dist_dir(tmp_path)
    dest = tmp_path / "staged"
    dest.mkdir()
    (dest / "myserver-stale-arch").write_text("old")
    godist.stage_dist({"out_dir": str(dist), "binary": "myserver"}, str(dest))
    assert not (dest / "myserver-stale-arch").exists()


def test_stage_dist_fails_loudly_without_a_built_dist(tmp_path):
    dest = tmp_path / "staged"
    with pytest.raises(SystemExit):
        godist.stage_dist({"out_dir": str(tmp_path / "no-dist"), "binary": "myserver"}, str(dest))
