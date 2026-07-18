"""Tests for the apple detector — root and nested .xcodeproj/.xcworkspace layouts.

Mirrors the react-native detector tests: a native project is commonly nested a
level or two down (ios/MyApp/MyApp.xcodeproj) when it coexists with web/server
code, so the detector searches beyond the root and reports the found path so
bootstrap can seed [apple].project_path.
"""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.detectors import detect  # noqa: E402

DET_DIR = REPO / "detectors"


def _xcodeproj(root: Path, rel: str) -> Path:
    """Create a fake .xcodeproj bundle at <root>/<rel> and return it."""
    proj = root / rel
    proj.mkdir(parents=True, exist_ok=True)
    (proj / "project.pbxproj").write_text("// fake")
    return proj


def test_detector_finds_project_at_root():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _xcodeproj(root, "MyApp.xcodeproj")
        out = detect(root, DET_DIR)
        assert "apple" in out["archetypes"]
        # A root-level project is what _detect_project_path finds on its own —
        # no project_path hint needed.
        assert "apple" not in out


def test_detector_finds_nested_project_and_hints_path():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _xcodeproj(root, "ios/StashReels/StashReels.xcodeproj")
        out = detect(root, DET_DIR)
        assert "apple" in out["archetypes"]
        assert out["apple"] == {"project_path": "ios/StashReels/StashReels.xcodeproj"}


def test_detector_prefers_workspace_over_project_in_same_dir():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _xcodeproj(root, "ios/App.xcodeproj")
        ws = root / "ios" / "App.xcworkspace"
        ws.mkdir(parents=True)
        (ws / "contents.xcworkspacedata").write_text("<Workspace></Workspace>")
        out = detect(root, DET_DIR)
        assert out["apple"] == {"project_path": "ios/App.xcworkspace"}


def test_detector_prefers_shallower_match():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _xcodeproj(root, "App.xcodeproj")
        _xcodeproj(root, "ios/App.xcodeproj")
        out = detect(root, DET_DIR)
        # Root wins over the nested copy, and a root match carries no hint.
        assert "apple" not in out


def test_detector_ignores_cocoapods_pods_project():
    # CocoaPods writes Pods/Pods.xcodeproj beside the real app; it is the Xcode
    # analogue of node_modules and must not be reported as the project.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _xcodeproj(root, "Pods/Pods.xcodeproj")
        _xcodeproj(root, "ios/App.xcodeproj")
        out = detect(root, DET_DIR)
        assert out["apple"] == {"project_path": "ios/App.xcodeproj"}


def test_detector_ignores_only_vendored_project():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _xcodeproj(root, "Pods/Pods.xcodeproj")
        out = detect(root, DET_DIR)
        assert "apple" not in out["archetypes"]


def test_detector_ignores_dot_dirs():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _xcodeproj(root, ".build/Cached.xcodeproj")
        out = detect(root, DET_DIR)
        assert "apple" not in out["archetypes"]


def test_detector_respects_depth_cap():
    # Three directory levels deep is past the 2-level cap; not matched.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _xcodeproj(root, "a/b/c/Deep.xcodeproj")
        out = detect(root, DET_DIR)
        assert "apple" not in out["archetypes"]


def test_detector_no_match_returns_no_apple():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "README.md").write_text("nothing here")
        out = detect(root, DET_DIR)
        assert "apple" not in out["archetypes"]
