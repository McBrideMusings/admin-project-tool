"""react-native detector: a package.json depending on react-native.

Searches the root package.json and one level of subdirectories, since an RN app
is commonly a workspace member (apps/mobile/, packages/app/) rather than the
repo root. Reports the app dir so bootstrap can seed [react_native].app_dir.
"""
import json
from pathlib import Path


def _is_rn_package(pkg_json: Path) -> bool:
    try:
        data = json.loads(pkg_json.read_text())
    except (OSError, ValueError):
        return False
    if not isinstance(data, dict):
        return False
    for key in ("dependencies", "devDependencies"):
        deps = data.get(key)
        if isinstance(deps, dict) and "react-native" in deps:
            return True
    return False


def _searchable(pkg: Path, root: Path) -> bool:
    """Reject vendored and hidden paths.

    node_modules is the trap: half the RN ecosystem (react-native-screens,
    react-native-reanimated, …) declares react-native in devDependencies AND
    ships an ios/ dir, so a dependency satisfies every test a real app does.
    Sorted order even puts node_modules/ ahead of apps/ and packages/, so the
    first match would be a library rather than the app being looked for.
    """
    parts = pkg.relative_to(root).parts
    return not any(p == "node_modules" or p.startswith(".") for p in parts[:-1])


def detect(project_dir: Path) -> dict | None:
    candidates = [project_dir / "package.json"]
    candidates += sorted(project_dir.glob("*/package.json"))
    candidates += sorted(project_dir.glob("*/*/package.json"))
    candidates = [p for p in candidates if _searchable(p, project_dir)]

    for pkg in candidates:
        if not _is_rn_package(pkg):
            continue
        # An RN app has a native ios/ or android/ dir beside its package.json;
        # without one this is a react-native dependency in some other package
        # (a shared component library, a web app pulling in react-native-web),
        # which needs no Metro lifecycle and no archetype.
        app_dir = pkg.parent
        if not (app_dir / "ios").is_dir() and not (app_dir / "android").is_dir():
            continue
        rel = app_dir.relative_to(project_dir).as_posix()
        return {
            "archetypes": ["react-native"],
            "react_native": {"app_dir": rel or "."},
        }
    return None
