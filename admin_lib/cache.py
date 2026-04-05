"""Build caching — skip rebuilds when source files haven't changed.

Source-hash-based caching. Cache entries live in tmp/.build-cache/
and are automatically cleaned when the project's tmp/ directory is removed.

Usage:
    from admin_lib.cache import collect_sources, check_build, mark_fresh

    sources = collect_sources(("src", [".swift"]), "Project.xcodeproj/project.pbxproj")
    if check_build("macos-debug", sources, "macOS client", force="--force" in args):
        rc = build(...)
        if rc == 0:
            mark_fresh("macos-debug", sources)
    else:
        # build was skipped — use existing artifacts
        ...
"""

import hashlib
import os

from .core import ok


def _cache_dir():
    """Return the build cache directory path (tmp/.build-cache/)."""
    from . import core as _core
    return os.path.join(_core._project_root, "tmp", ".build-cache")


def collect_sources(*specs):
    """Collect source file paths from various spec types.

    Each spec can be:
    - A file path string (included if it exists)
    - A glob pattern string (expanded with recursive=True)
    - A (directory, extensions) tuple — walks directory recursively,
      collecting files matching any of the extensions (e.g. [".swift", ".h"])

    Returns a sorted list of absolute paths (deduplicated).
    """
    import glob as glob_mod

    paths = []
    for spec in specs:
        if isinstance(spec, tuple):
            directory, extensions = spec
            if not os.path.isdir(directory):
                continue
            for root, _dirs, files in os.walk(directory):
                for f in files:
                    if any(f.endswith(ext) for ext in extensions):
                        paths.append(os.path.join(root, f))
        else:
            spec_str = str(spec)
            if "*" in spec_str or "?" in spec_str:
                paths.extend(glob_mod.glob(spec_str, recursive=True))
            elif os.path.isfile(spec_str):
                paths.append(spec_str)
    return sorted(set(os.path.abspath(p) for p in paths))


def source_hash(paths):
    """Compute SHA256 over sorted file paths and their contents.

    Includes file paths in the hash so additions, removals, and renames
    are detected (not just content changes).
    """
    h = hashlib.sha256()
    for p in sorted(paths):
        h.update(p.encode())
        with open(p, "rb") as f:
            h.update(f.read())
    return h.hexdigest()


def is_fresh(cache_key, sources, force=False):
    """Return True if the cached hash matches current sources.

    cache_key: unique name for this build (e.g. "macos-debug")
    sources:   list of file paths (from collect_sources)
    force:     if True, always returns False
    """
    if force or not sources:
        return False
    cache_file = os.path.join(_cache_dir(), cache_key)
    if not os.path.exists(cache_file):
        return False
    try:
        current = source_hash(sources)
    except (FileNotFoundError, PermissionError):
        return False
    with open(cache_file) as f:
        stored = f.read().strip()
    return stored == current


def mark_fresh(cache_key, sources):
    """Record that a build completed successfully with these sources."""
    d = _cache_dir()
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, cache_key), "w") as f:
        f.write(source_hash(sources) + "\n")


def invalidate(cache_key=None):
    """Remove a cache entry, or all entries if cache_key is None."""
    d = _cache_dir()
    if cache_key:
        try:
            os.remove(os.path.join(d, cache_key))
        except FileNotFoundError:
            pass
    elif os.path.isdir(d):
        import shutil
        shutil.rmtree(d)


def check_build(cache_key, sources, label=None, force=False):
    """Check if a build is needed. Returns True if the build should proceed.

    Prints "up to date" when cached. Pair with mark_fresh() after a
    successful build.

    cache_key: unique name for this build (e.g. "macos-debug")
    sources:   list of file paths (from collect_sources)
    label:     human-readable name for status messages (defaults to cache_key)
    force:     if True, always returns True (forces rebuild)
    """
    if is_fresh(cache_key, sources, force=force):
        ok(f"{label or cache_key}: up to date (skipping build)")
        return False
    return True
