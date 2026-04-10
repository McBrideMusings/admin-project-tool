"""apple detector: any .xcodeproj or .xcworkspace at project root."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    if any(project_dir.glob("*.xcodeproj")) or any(project_dir.glob("*.xcworkspace")):
        return {"archetypes": ["apple"]}
    return None
