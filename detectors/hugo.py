"""Hugo detector.

Matches a project that has a Hugo config file (yaml/toml/json) and a themes
directory, or the hugo_build lock file.
"""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    has_config = any(
        (project_dir / f"config.{ext}").exists()
        for ext in ("yaml", "yml", "toml", "json")
    ) or (project_dir / "hugo.yaml").exists() or (project_dir / "hugo.toml").exists()
    has_themes = (project_dir / "themes").is_dir()
    has_lock = (project_dir / ".hugo_build.lock").exists()

    if (has_config and has_themes) or has_lock:
        return {"archetypes": ["hugo"]}
    return None
