"""unraid-plugin detector: any .plg file at project root."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    if any(project_dir.glob("*.plg")):
        return {"archetypes": ["unraid-plugin"]}
    return None
