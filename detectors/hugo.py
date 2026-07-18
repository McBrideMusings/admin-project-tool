"""Hugo detector.

Matches a project that has a Hugo config file (yaml/toml/json) and a themes
directory, or the hugo_build lock file — at the repo root or one directory down
(a monorepo keeping its site under site/, www/, web/, etc.). The hugo archetype
runs against ${HUGO_SOURCE:-.}, so a nested site still works once HUGO_SOURCE is
set; this only fixes bootstrap detection finding it.
"""
from pathlib import Path

# Vendored / generated dirs that can hold Hugo-shaped files but aren't the site.
_VENDORED = {"node_modules", "public", "resources", "themes"}


def _is_hugo(d: Path) -> bool:
    has_config = any(
        (d / f"config.{ext}").exists()
        for ext in ("yaml", "yml", "toml", "json")
    ) or (d / "hugo.yaml").exists() or (d / "hugo.toml").exists()
    has_themes = (d / "themes").is_dir()
    has_lock = (d / ".hugo_build.lock").exists()
    return (has_config and has_themes) or has_lock


def detect(project_dir: Path) -> dict | None:
    if _is_hugo(project_dir):
        return {"archetypes": ["hugo"]}
    for sub in sorted(project_dir.iterdir() if project_dir.is_dir() else []):
        if not sub.is_dir() or sub.name.startswith(".") or sub.name in _VENDORED:
            continue
        if _is_hugo(sub):
            return {"archetypes": ["hugo"]}
    return None
