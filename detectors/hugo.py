"""Hugo detector.

Matches a project with any one of:
- a Hugo-named config file (hugo.yaml/yml/toml/json) — that filename is
  Hugo-specific, so it stands alone; this is the modern modules-based layout,
  which often has no themes/ directory
- a generic config file (config.yaml/yml/toml/json) *and* a themes/ directory —
  "config.*" is too common a name to count on its own
- a config/_default/ directory (Hugo's alternate, split config location)
- the .hugo_build.lock file

— at the repo root or one directory down (a monorepo keeping its site under
site/, www/, web/, etc.). The hugo archetype runs against ${HUGO_SOURCE:-.}, so
a nested site still works once HUGO_SOURCE is set; this only fixes bootstrap
detection finding it.
"""
from pathlib import Path

# Vendored / generated dirs that can hold Hugo-shaped files but aren't the site.
_VENDORED = {"node_modules", "public", "resources", "themes"}

_CONFIG_EXTS = ("yaml", "yml", "toml", "json")


def _has_config(d: Path, stem: str) -> bool:
    return any((d / f"{stem}.{ext}").exists() for ext in _CONFIG_EXTS)


def _is_hugo(d: Path) -> bool:
    has_hugo_named_config = _has_config(d, "hugo")
    has_generic_config = _has_config(d, "config")
    has_themes = (d / "themes").is_dir()
    has_config_dir = (d / "config" / "_default").is_dir()
    has_lock = (d / ".hugo_build.lock").exists()
    return (
        has_hugo_named_config
        or (has_generic_config and has_themes)
        or has_config_dir
        or has_lock
    )


def detect(project_dir: Path) -> dict | None:
    if _is_hugo(project_dir):
        return {"archetypes": ["hugo"]}
    for sub in sorted(project_dir.iterdir() if project_dir.is_dir() else []):
        if not sub.is_dir() or sub.name.startswith(".") or sub.name in _VENDORED:
            continue
        if _is_hugo(sub):
            return {"archetypes": ["hugo"]}
    return None
