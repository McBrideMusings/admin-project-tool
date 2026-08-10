"""Tests for the hugo detector — root and one-level-nested Hugo sites.

Regression cover for #381: the detector matched only at the repo root, so a
monorepo keeping its Hugo site under site/ (the archetype already supports this
via ${HUGO_SOURCE}) was missed at bootstrap.

Regression cover for #64: modern Hugo projects using modules-based config have
a hugo.{toml,yaml,json} at the root with no themes/ directory, or split their
config across config/_default/ — none of those matched before.
"""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.detectors import detect  # noqa: E402

DET_DIR = REPO / "detectors"


def _hugo_site(root: Path, rel: str):
    site = root / rel if rel != "." else root
    site.mkdir(parents=True, exist_ok=True)
    (site / "config.toml").write_text('title = "x"')
    (site / "themes").mkdir()
    return site


def test_detects_hugo_at_root():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _hugo_site(root, ".")
        assert "hugo" in detect(root, DET_DIR)["archetypes"]


def test_detects_hugo_in_subdir():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _hugo_site(root, "site")
        assert "hugo" in detect(root, DET_DIR)["archetypes"]


def test_detects_hugo_via_lock_in_subdir():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "www").mkdir()
        (root / "www" / ".hugo_build.lock").write_text("")
        assert "hugo" in detect(root, DET_DIR)["archetypes"]


def test_no_hugo_returns_no_match():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "README.md").write_text("nothing")
        assert "hugo" not in detect(root, DET_DIR)["archetypes"]


def test_detects_hugo_named_config_without_themes():
    # Modules-based layout from #64: hugo.toml at root, no themes/ dir.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "hugo.toml").write_text('title = "x"')
        assert "hugo" in detect(root, DET_DIR)["archetypes"]


def test_detects_hugo_json_without_themes():
    # Same signal as above via a different extension — hugo.* covers all four.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "hugo.json").write_text('{"title": "x"}')
        assert "hugo" in detect(root, DET_DIR)["archetypes"]


def test_detects_config_default_directory():
    # Hugo's split-config location: config/_default/*.toml, no themes/ dir.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "config" / "_default").mkdir(parents=True)
        (root / "config" / "_default" / "hugo.toml").write_text('title = "x"')
        assert "hugo" in detect(root, DET_DIR)["archetypes"]


def test_generic_config_without_themes_is_not_hugo():
    # "config.toml" is too common a filename to mean Hugo on its own — it only
    # counts alongside a themes/ dir, which the hugo.* checks don't need.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "config.toml").write_text('title = "x"')
        assert "hugo" not in detect(root, DET_DIR)["archetypes"]


def test_ignores_vendored_and_dot_dirs():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        # Directly inside an immediate vendored/hidden dir — the only level the
        # detector probes, so these must be skipped.
        _hugo_site(root, "node_modules")
        _hugo_site(root, ".cache")
        assert "hugo" not in detect(root, DET_DIR)["archetypes"]
