"""Docs detector.

Matches a project that ships a VitePress-style docs site — a package.json
declaring a `docs:dev` script, either at the root or under docs/.

Unlike the other detectors this one is orthogonal: it rides along with whatever
archetype the project actually is (go-cli, apple, …) rather than competing with
it, so it contributes the docs archetype and nothing else.
"""
import json
from pathlib import Path

_SCRIPT = "docs:dev"


def _declares_docs_script(pkg: Path) -> bool:
    try:
        data = json.loads(pkg.read_text())
    except (OSError, ValueError):
        return False
    scripts = data.get("scripts")
    return isinstance(scripts, dict) and _SCRIPT in scripts


def detect(project_dir: Path) -> dict | None:
    if _declares_docs_script(project_dir / "package.json"):
        return {"archetypes": ["docs"]}
    if _declares_docs_script(project_dir / "docs" / "package.json"):
        return {"archetypes": ["docs"], "docs": {"dir": "docs"}}
    return None
