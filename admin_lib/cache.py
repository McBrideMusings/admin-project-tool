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

from .core import get_project_root, ok

# Session-level cache: avoids re-reading the same file set within a single
# admin invocation. Keyed by (path, mtime) tuples so any file change naturally
# misses the cache.
_session_cache = {}


def _cache_dir():
    """Return the build cache directory path (tmp/.build-cache/)."""
    return os.path.join(get_project_root(), "tmp", ".build-cache")


def collect_sources(*specs):
    """Collect source paths. Specs may be: a file path, a glob string (recursive=True),
    or a (directory, extensions) tuple for recursive ext-matched walk. Returns sorted
    deduplicated absolute paths.
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
    sorted_paths = sorted(paths)
    try:
        key = tuple((p, os.path.getmtime(p)) for p in sorted_paths)
    except OSError:
        key = None
    if key is not None and key in _session_cache:
        return _session_cache[key]
    h = hashlib.sha256()
    for p in sorted_paths:
        h.update(p.encode())
        with open(p, "rb") as f:
            h.update(f.read())
    result = h.hexdigest()
    if key is not None:
        _session_cache[key] = result
    return result


def is_fresh(cache_key, sources, force=False, artifact=None):
    """True iff cached hash matches current sources.

    If `artifact` is given and missing on disk, cache is stale regardless of hash
    — prevents "skipping build" when the artifact was deleted or renamed.
    """
    if force or not sources:
        return False
    if artifact is not None:
        expanded = os.path.expanduser(artifact)
        if any(c in expanded for c in "*?["):
            import glob as _glob
            if not _glob.glob(expanded):
                return False
        elif not os.path.exists(expanded):
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


def check_build(cache_key, sources, label=None, force=False, artifact=None):
    """True if build should proceed. Prints "up to date" when cached. Pair with
    mark_fresh() on success. `artifact`: missing on disk → stale (avoid ghosts).
    """
    if is_fresh(cache_key, sources, force=force, artifact=artifact):
        ok(f"{label or cache_key}: up to date (skipping build)")
        return False
    return True
