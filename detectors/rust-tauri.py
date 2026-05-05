"""rust-tauri detector: Cargo.toml at root + src-tauri/tauri.conf.json in tree."""
from pathlib import Path

# Bounded patterns anchored on src-tauri/ — avoids traversing Cargo's target/ dir.
# Covers root-level, one-deep (apps/desktop/), and two-deep layouts.
_TAURI_PATTERNS = [
    "src-tauri/tauri.conf.json",
    "*/src-tauri/tauri.conf.json",
    "*/*/src-tauri/tauri.conf.json",
]


def detect(project_dir: Path) -> dict | None:
    if not (project_dir / "Cargo.toml").exists():
        return None
    if any(
        found
        for pattern in _TAURI_PATTERNS
        for found in project_dir.glob(pattern)
    ):
        return {"archetypes": ["rust-tauri"]}
    return None
