"""firefox-extension detector: manifest.json with browser_specific_settings."""
import json
from pathlib import Path


def detect(project_dir: Path) -> dict | None:
    m = project_dir / "manifest.json"
    if not m.exists():
        return None
    try:
        data = json.loads(m.read_text())
    except Exception:
        return None
    if isinstance(data, dict) and "browser_specific_settings" in data:
        return {"archetypes": ["firefox-extension"]}
    return None
