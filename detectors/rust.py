"""rust detector: Cargo.toml present and no Dockerfile (Docker-first projects use docker)."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    if (project_dir / "Cargo.toml").exists() and not (project_dir / "Dockerfile").exists():
        return {"archetypes": ["rust"]}
    return None
