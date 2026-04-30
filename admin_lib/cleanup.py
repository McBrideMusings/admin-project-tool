"""Disk cleanup helpers — directory size accounting, safe rmtree, SQLite health.

Composable building blocks for `clean`-style commands. Project admin.toml
manifests should call these instead of inlining loops/imports — keeps the
manifest a thin description of *what* to clean, while the *how* lives here
once and gets bundled into every project's admin script.

Pairs naturally with admin_lib.ports.nuke_dev_processes — call that first to
make sure no process is holding files open, then call clean_paths /
clean_with_db.
"""

import os
import shutil
import sqlite3

from .core import info, ok, resolve_env, warn


def directory_size(path):
    """Total bytes used by `path`. Returns 0 for missing paths.

    Walks the directory once; never raises on permission errors mid-walk
    (any unreadable child contributes 0). Symlinks count as the size of the
    link itself, not the target — same behavior as `du` without `-L`.
    """
    if not os.path.lexists(path):
        return 0
    if os.path.islink(path) or os.path.isfile(path):
        try:
            return os.path.getsize(path)
        except OSError:
            return 0
    if not os.path.isdir(path):
        return 0
    total = 0
    for root, _, files in os.walk(path):
        for f in files:
            try:
                total += os.path.getsize(os.path.join(root, f))
            except OSError:
                continue
    return total


def format_bytes(n):
    """Human-readable byte count, e.g. 1.6GB. Intentionally short — fits
    inside log lines without wrapping. Uses 1024-base units (matching `du -h`).
    """
    n = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if n < 1024:
            return f"{n:.1f}{unit}"
        n /= 1024
    return f"{n:.1f}PB"


def nuke_path(path, label=None):
    """Remove a file / directory / symlink, log size, return bytes freed.

    Missing path is a silent no-op (returns 0). Permission failures on the
    final unlink/rmtree are warned but swallowed — this is for "clear caches"
    flows where one stuck file shouldn't abort the whole sweep.
    """
    if not os.path.lexists(path):
        return 0
    sz = directory_size(path)
    label_text = f"{label}: {path}" if label else path
    info(f"  {label_text} ({format_bytes(sz)})")
    try:
        if os.path.islink(path) or os.path.isfile(path):
            os.unlink(path)
        else:
            shutil.rmtree(path, ignore_errors=True)
    except OSError as e:
        warn(f"    failed: {e}")
        return 0
    return sz


def clean_paths(paths):
    """Nuke each `(path, label)` (or bare path) in `paths`, summing freed bytes.

    Skips duplicates and entries whose path is empty/None. Returns total
    bytes freed across the whole sweep, suitable for the caller's final
    `ok(f"freed {format_bytes(total)}")` line.
    """
    seen = set()
    total = 0
    for entry in paths:
        if isinstance(entry, (tuple, list)):
            path, label = entry[0], (entry[1] if len(entry) > 1 else None)
        else:
            path, label = entry, None
        if not path:
            continue
        ap = os.path.abspath(path)
        if ap in seen:
            continue
        seen.add(ap)
        total += nuke_path(path, label=label)
    return total


def db_quick_check(db_path, timeout=2.0):
    """Run `PRAGMA quick_check` on a SQLite DB. Returns `(healthy, status)`.

    `healthy` is True iff the DB exists, opens, and reports "ok". `status`
    is a short human string ("healthy", "missing", "CORRUPT: <detail>",
    "unreadable: <detail>") suitable for logging or showing the user.

    quick_check is ~10× faster than integrity_check on a healthy DB and
    still catches the malformed-image / page-corruption cases that motivate
    a forced drop. For a pre-deploy "is this DB sound enough to copy"
    audit, call integrity_check directly instead.
    """
    if not os.path.exists(db_path):
        return (False, "missing")
    try:
        conn = sqlite3.connect(db_path, timeout=timeout)
    except sqlite3.DatabaseError as e:
        return (False, f"unreadable: {e}")
    try:
        try:
            row = conn.execute("PRAGMA quick_check").fetchone()
        except sqlite3.DatabaseError as e:
            return (False, f"CORRUPT: {e}")
        if row and row[0] == "ok":
            return (True, "healthy")
        return (False, f"CORRUPT: {row[0] if row else '<no result>'}")
    finally:
        try:
            conn.close()
        except sqlite3.Error:
            pass


def clean_with_db(paths, db_path, force=False, db_companions=("-shm", "-wal")):
    """High-level clean: nuke `paths` always, drop SQLite DB only if corrupt.

    Combines `clean_paths` with `db_quick_check`. The DB is dropped iff
    `force=True` OR `db_quick_check` reports unhealthy. Companion files
    (`-shm`, `-wal` by default) are dropped alongside the main file. A
    healthy DB is kept and logged so the user knows it was inspected.

    Returns total bytes freed (matching `clean_paths`).

    Typical use:
        clean_with_db(
            paths=[
                (os.environ.get("HLS_CACHE_DIR"), "HLS cache"),
                (os.environ.get("WORK_DIR"), "transcode work dir"),
                ("web/dist", "frontend build"),
            ],
            db_path=os.path.join(config_dir, "app.db"),
            force=("--force" in args),
        )
    """
    info("clearing caches and transcode outputs:")
    freed = clean_paths(paths)

    healthy, status = db_quick_check(db_path)
    should_drop = force or (not healthy and os.path.exists(db_path))

    if should_drop:
        reason = status if not healthy else "--force"
        info(f"dropping DB ({reason}):")
        for ext in ("",) + tuple(db_companions):
            freed += nuke_path(db_path + ext, label=f"DB{ext or ' main'}")
    elif os.path.exists(db_path):
        info(f"DB: {status} — keeping (pass --force to drop)")

    ok(f"clean done — freed {format_bytes(freed)}")
    return freed


def clean_dev_from_config(cfg, force=False):
    """Adapter: read a manifest `[clean_dev]` table and call `clean_with_db`.

    Schema:
        paths = [
            {env, default?, label?},   # path resolved from env var (with optional default)
            {path, label?},             # literal path (relative to cwd)
            ...
        ]
        db = {env?, default?, file}    # db_path = (env_or_default)/file; or {path = "..."}
    """
    if not cfg:
        return 0

    paths = []
    for entry in cfg.get("paths") or ():
        if "env" in entry:
            base = os.environ.get(entry["env"], resolve_env(entry.get("default", "")))
        else:
            base = resolve_env(entry.get("path", ""))
        if not base:
            continue
        if not os.path.isabs(base):
            base = os.path.abspath(base)
        paths.append((base, entry.get("label")))

    db = cfg.get("db") or {}
    if "path" in db:
        db_path = resolve_env(db["path"])
    elif "file" in db:
        base = os.environ.get(db.get("env", ""), resolve_env(db.get("default", "")))
        if base and not os.path.isabs(base):
            base = os.path.abspath(base)
        db_path = os.path.join(base, db["file"]) if base else db["file"]
    else:
        db_path = ""

    return clean_with_db(paths=paths, db_path=db_path, force=force)
