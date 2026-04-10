"""go-cli detector: go.mod present and no Dockerfile."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    if (project_dir / "go.mod").exists() and not (project_dir / "Dockerfile").exists():
        return {"archetypes": ["go-cli"]}
    return None
