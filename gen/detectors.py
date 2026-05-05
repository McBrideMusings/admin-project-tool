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


# All detectors run; matches are merged in listed order (later wins on URL/name
# conflicts, archetype lists are unioned). 'simple' runs last and is treated as
# a fallback — its contribution is only used if no other detector matched.
DETECTOR_ORDER = [
    "hugo",
    "go-cli",
    "rust",
    "rust-tauri",
    "firefox-extension",
    "stash-plugin",
    "unraid-plugin",
    "docker-unraid",
    "apple",
    "simple",
]


def detect(project_dir: Path, detector_dir: Path) -> dict:
    """Run all detectors, union their archetype contributions.

    Each detector returns a partial manifest dict (or None). Archetype lists
    across detectors are concatenated (dedup, detector order preserved). URL
    and project_name keys are merged with later detectors winning. The
    'simple' fallback is only included if nothing else matched.
    """
    combined_archetypes: list[str] = []
    combined_urls: dict[str, str] = {}
    project_name: str | None = None
    matched_detectors: list[str] = []

    for name in DETECTOR_ORDER:
        if name == "simple":
            continue  # handled as fallback below
        path = detector_dir / f"{name}.py"
        if not path.exists():
            continue
        result = _load(path, name).detect(project_dir)
        if not result:
            continue
        matched_detectors.append(name)
        for a in result.get("archetypes", []):
            if a not in combined_archetypes:
                combined_archetypes.append(a)
        combined_urls.update(result.get("urls", {}))
        if "project_name" in result:
            project_name = result["project_name"]

    if not combined_archetypes:
        combined_archetypes = ["simple"]
        matched_detectors = ["fallback"]

    out: dict = {
        "archetypes": combined_archetypes,
        "_detector": "+".join(matched_detectors),
    }
    if combined_urls:
        out["urls"] = combined_urls
    if project_name:
        out["project_name"] = project_name
    return out


def _load(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(f"_det_{name}", path)
    if spec is None or spec.loader is None:
        raise DetectorError(f"failed to load detector: {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    if not hasattr(mod, "detect"):
        raise DetectorError(f"{path}: detector missing detect() function")
    return mod
