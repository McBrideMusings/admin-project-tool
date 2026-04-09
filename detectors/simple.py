"""Fallback detector — always matches, produces the 'simple' archetype."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    return {"archetypes": ["simple"]}
