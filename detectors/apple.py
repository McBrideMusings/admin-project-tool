"""apple detector: an .xcodeproj or .xcworkspace at the root or nested a level or two down.

An Xcode project is commonly nested (ios/MyApp/MyApp.xcodeproj) when a native app
coexists with web/server code in one repo, so the root-only glob missed it. Searches
the root and up to two subdirectory levels, prefers a .xcworkspace over a bare
.xcodeproj (matching admin_lib.apple._detect_project_path), and reports the found
path so bootstrap can seed [apple].project_path.
"""
from pathlib import Path

# Vendored / generated dirs that ship their own .xcodeproj/.xcworkspace — the Xcode
# analogue of node_modules for react-native. CocoaPods writes Pods/Pods.xcodeproj,
# Carthage checks out dependency projects, and DerivedData/.build hold build output.
_VENDORED = {"node_modules", "Pods", "Carthage", "DerivedData", ".build"}


def _searchable(match: Path, root: Path) -> bool:
    """Reject matches under a vendored or hidden directory."""
    parts = match.relative_to(root).parts
    return not any(p in _VENDORED or p.startswith(".") for p in parts[:-1])


def detect(project_dir: Path) -> dict | None:
    # Depth-ordered (root first), .xcworkspace preferred over .xcodeproj at each
    # depth — the same precedence _detect_project_path applies within a single dir.
    for depth in ("", "*/", "*/*/"):
        for suffix in ("xcworkspace", "xcodeproj"):
            matches = sorted(project_dir.glob(f"{depth}*.{suffix}"))
            matches = [m for m in matches if _searchable(m, project_dir)]
            if matches:
                rel = matches[0].relative_to(project_dir).as_posix()
                out: dict = {"archetypes": ["apple"]}
                if rel != matches[0].name:
                    # Only hint project_path when nested — a root-level project is
                    # what _detect_project_path already finds on its own.
                    out["apple"] = {"project_path": rel}
                return out
    return None
