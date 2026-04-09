"""Detector loader.

Detectors live in detectors/<name>.py and each exposes a detect(project_dir: Path)
function that returns a partial starter manifest dict (or None if the stack
doesn't match). Detectors only run during bootstrap — once admin.toml exists,
it is the sole source of truth.
"""
from __future__ import annotations

import importlib.util
from pathlib import Path


class DetectorError(Exception):
    pass


# Order matters: first match wins. More specific stacks should come before
# more generic ones. 'simple' is the fallback and should always be last.
DETECTOR_ORDER = [
    "hugo",
    "simple",
]


def detect(project_dir: Path, detector_dir: Path) -> dict:
    """Run detectors in order, return the first partial manifest that matches.

    Always returns a dict (falls back to an empty 'simple' archetype manifest).
    """
    for name in DETECTOR_ORDER:
        path = detector_dir / f"{name}.py"
        if not path.exists():
            continue
        mod = _load(path, name)
        result = mod.detect(project_dir)
        if result:
            result.setdefault("_detector", name)
            return result
    return {"archetypes": ["simple"], "_detector": "fallback"}


def _load(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(f"_det_{name}", path)
    if spec is None or spec.loader is None:
        raise DetectorError(f"failed to load detector: {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    if not hasattr(mod, "detect"):
        raise DetectorError(f"{path}: detector missing detect() function")
    return mod
