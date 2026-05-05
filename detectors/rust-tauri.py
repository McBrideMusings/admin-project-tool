"""rust-tauri detector: Cargo.toml at root + any tauri.conf.json in tree."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    if not (project_dir / "Cargo.toml").exists():
        return None
    if any(project_dir.rglob("tauri.conf.json")):
        return {"archetypes": ["rust-tauri"]}
    return None
