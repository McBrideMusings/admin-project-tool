"""Tests for admin_lib.cache.

Regression cover for #398: _cache_dir() read a bare _project_root that only
resolved in the old flat single-namespace bundle. Under interpreter mode (real
package import) that raised NameError; it must now resolve the root through the
core.get_project_root() accessor.
"""
from __future__ import annotations

import os
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import cache, core  # noqa: E402


def test_cache_dir_resolves_project_root_under_package_import(tmp_path):
    core._init("test-proj", str(tmp_path))
    assert cache._cache_dir() == os.path.join(str(tmp_path), "tmp", ".build-cache")


def test_cache_dir_tracks_project_root_reassignment(tmp_path):
    # Late-binding: a later _init must be reflected, not a snapshot from import.
    core._init("proj-a", str(tmp_path / "a"))
    assert cache._cache_dir() == os.path.join(str(tmp_path / "a"), "tmp", ".build-cache")
    core._init("proj-b", str(tmp_path / "b"))
    assert cache._cache_dir() == os.path.join(str(tmp_path / "b"), "tmp", ".build-cache")
