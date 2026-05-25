"""Cloudflare Workers detector.

Matches a project with a wrangler config file at its root (wrangler.jsonc,
wrangler.json, or wrangler.toml).
"""
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    has_wrangler = any(
        (project_dir / f"wrangler.{ext}").exists()
        for ext in ("jsonc", "json", "toml")
    )
    if has_wrangler:
        return {"archetypes": ["cloudflare-workers"]}
    return None
