"""stash-plugin detector: package.json mentions stash and a rollup config is present."""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    pkg = project_dir / "package.json"
    if not pkg.exists():
        return None
    has_rollup = any(
        (project_dir / name).exists()
        for name in ("rollup.config.js", "rollup.config.mjs", "rollup.config.ts")
    )
    if not has_rollup:
        return None
    try:
        if "stash" in pkg.read_text().lower():
            return {"archetypes": ["stash-plugin"]}
    except Exception:
        pass
    return None
