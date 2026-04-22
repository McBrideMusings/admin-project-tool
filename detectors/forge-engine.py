"""forge-engine detector: engine/build.zig + sample-games/ sibling."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    if (project_dir / "engine" / "build.zig").exists() and (project_dir / "sample-games").is_dir():
        return {"archetypes": ["forge-engine"]}
    return None
