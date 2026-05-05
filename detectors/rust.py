"""rust detector: Cargo.toml present at project root."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    if (project_dir / "Cargo.toml").exists():
        return {"archetypes": ["rust"]}
    return None
