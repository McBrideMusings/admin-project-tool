"""Pairing contract: archetypes/ and detectors/ are paired by filename.

A detector that outlives its archetype (or vice versa) otherwise fails only at
resolve time — admin_lib/archetypes.py raises ArchetypeError on a missing
archetype, far from the edit that broke the pairing. These tests fail at the
edit instead.
"""
from __future__ import annotations

import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.detectors import DETECTOR_ORDER  # noqa: E402

ARCHETYPE_DIR = REPO / "archetypes"
DETECTOR_DIR = REPO / "detectors"


def _stems(d: Path) -> set[str]:
    return {p.stem for p in d.glob("*.py") if p.stem != "__init__"}


def test_archetypes_and_detectors_are_paired_by_filename():
    """Every archetype has a matching detector and vice versa (paired by filename)."""
    archetypes = _stems(ARCHETYPE_DIR)
    detectors = _stems(DETECTOR_DIR)
    assert archetypes, "no archetype files found"
    assert detectors, "no detector files found"
    missing_detectors = archetypes - detectors
    missing_archetypes = detectors - archetypes
    assert not missing_detectors, f"archetypes with no matching detector: {sorted(missing_detectors)}"
    assert not missing_archetypes, f"detectors with no matching archetype: {sorted(missing_archetypes)}"


def test_detector_order_entries_have_backing_files():
    """Every name in DETECTOR_ORDER must have both a detector and an archetype file,
    so the bootstrap path can't suggest an archetype that fails to load at resolve time.

    (A detector/archetype file pair may exist without being in DETECTOR_ORDER —
    e.g. forge-engine — so the check is one-directional: order names need files,
    not the reverse.)
    """
    archetypes = _stems(ARCHETYPE_DIR)
    detectors = _stems(DETECTOR_DIR)
    for name in DETECTOR_ORDER:
        assert name in detectors, f"DETECTOR_ORDER lists '{name}' but detectors/{name}.py is missing"
        assert name in archetypes, f"DETECTOR_ORDER lists '{name}' but archetypes/{name}.py is missing"
