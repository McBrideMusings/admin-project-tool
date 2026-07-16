"""raylib-game detector: a CMake project that builds against raylib.

Matches a CMakeLists.txt that names raylib, which is what every game on the
forge-engine cradle has. Deliberately does not match the forge-engine repo
itself (engine/build.zig + sample-games/) — that has its own archetype.

The binary is almost always the CMake project() name, so it is seeded into
[raylib] rather than left for the user to fill in.
"""
import re
from pathlib import Path

_PROJECT_RE = re.compile(r"^\s*project\s*\(\s*([A-Za-z0-9_.-]+)", re.M | re.I)


def detect(project_dir: Path) -> dict | None:
    cmakelists = project_dir / "CMakeLists.txt"
    if not cmakelists.is_file():
        return None
    if (project_dir / "engine" / "build.zig").exists():
        return None  # the engine repo, not a game built on it
    try:
        text = cmakelists.read_text()
    except OSError:
        return None
    if "raylib" not in text.lower():
        return None

    result: dict = {"archetypes": ["raylib-game"]}
    m = _PROJECT_RE.search(text)
    if m:
        result["raylib"] = {"binary": m.group(1)}
    return result
