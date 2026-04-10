"""docker-unraid detector: Dockerfile present.

Greedy by design — composes cleanly with apple/go-cli/etc. for projects that
are both a Docker deployment target and another stack.
"""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    if (project_dir / "Dockerfile").exists():
        return {"archetypes": ["docker-unraid"]}
    return None
