"""Dev-fixture management — generate a junk file tree, snapshot-restore it.

Two complementary capabilities for projects that test against a tree of files
on disk (a media scanner, a file manager, …):

  * generate — synthesize a `baseline` tree from a declarative spec (real
    random bytes, varied names/sizes/depths). Run once to build the snapshot.
  * reset    — restore a mutable `working` copy from `baseline`, discarding any
    moves/renames/deletes a test session made. The pure-Python equivalent of
    `rsync -a --delete baseline/ working/`.

Point the app's dev mount at `working`; `reset` gives a consistent baseline
before each session. `baseline`/`working` both live under a gitignored `data/`
dir, so nothing large is committed — only the spec in admin.toml.

Pairs with admin_lib.cleanup: a project can fold its DB/cache wipes into reset
via the `clean` / `db` keys (see fixtures_reset_from_config).
"""

import os
import shutil

from .cleanup import clean_dev_from_config
from .core import err, info, ok, resolve_env, warn

_CHUNK = 8 * 1024 * 1024  # 8MB write chunk — bounds memory on multi-GB files.

_UNITS = {
    "B": 1,
    "KB": 1024,
    "MB": 1024 ** 2,
    "GB": 1024 ** 3,
    "TB": 1024 ** 4,
}


def parse_size(spec):
    """Parse a human size (`"1.5GB"`, `"500KB"`, `"2048"`) to bytes.

    Bare numbers are bytes. 1024-base units (matching `du -h`). Accepts an int
    directly. Raises ValueError on anything unparseable.
    """
    if isinstance(spec, (int, float)):
        return int(spec)
    s = str(spec).strip().upper().replace(" ", "")
    if not s:
        raise ValueError("empty size")
    for unit in ("TB", "GB", "MB", "KB", "B"):  # longest first so "KB" wins over "B"
        if s.endswith(unit):
            num = s[: -len(unit)]
            return int(float(num) * _UNITS[unit])
    return int(float(s))  # unitless → bytes


def format_bytes(n):
    """Human-readable byte count (1024-base), e.g. 1.5GB."""
    n = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if n < 1024:
            return f"{n:.1f}{unit}"
        n /= 1024
    return f"{n:.1f}PB"


def _write_random(path, size):
    """Write `size` bytes of os.urandom to `path` in bounded chunks."""
    remaining = int(size)
    with open(path, "wb") as fh:
        while remaining > 0:
            chunk = _CHUNK if remaining > _CHUNK else remaining
            fh.write(os.urandom(chunk))
            remaining -= chunk


def _iter_files(entry):
    """Yield (name, size_bytes) for one tree entry's `files` list.

    A file with `count = N` expands to N numbered copies: `track.flac` →
    `track01.flac` … `trackNN.flac`, zero-padded to the count's width.
    """
    for f in entry.get("files") or ():
        name = f.get("name")
        if not name:
            raise ValueError(f"fixtures: file entry missing 'name': {f!r}")
        size = parse_size(f.get("size", 0))
        count = int(f.get("count", 1))
        if count <= 1:
            yield name, size
            continue
        stem, ext = os.path.splitext(name)
        width = max(2, len(str(count)))
        for i in range(1, count + 1):
            yield f"{stem}{i:0{width}d}{ext}", size


def generate_tree(baseline, tree, force=False):
    """Synthesize `tree` into `baseline` as real files of random bytes.

    `tree` is a list of `{dir, files=[{name, size, count?}]}` entries. Refuses
    to populate a non-empty `baseline` unless `force` (so an accidental re-run
    can't silently rewrite a snapshot you've curated). Returns total bytes.
    """
    if not baseline:
        err("fixtures: no baseline path configured")
        return 1
    if os.path.isdir(baseline) and os.listdir(baseline) and not force:
        err(f"fixtures: baseline {baseline} is not empty — pass --force to regenerate")
        return 1
    if force and os.path.isdir(baseline):
        shutil.rmtree(baseline, ignore_errors=True)
    os.makedirs(baseline, exist_ok=True)

    total = 0
    count = 0
    for entry in tree or ():
        subdir = entry.get("dir", "")
        target_dir = os.path.join(baseline, subdir) if subdir else baseline
        os.makedirs(target_dir, exist_ok=True)
        for name, size in _iter_files(entry):
            path = os.path.join(target_dir, name)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            _write_random(path, size)
            total += size
            count += 1
            info(f"  {os.path.relpath(path, baseline)} ({format_bytes(size)})")
    ok(f"fixtures: generated {count} files ({format_bytes(total)}) under {baseline}")
    return 0


def restore_tree(baseline, working):
    """Restore `working` from `baseline` (pure-Python `rsync --delete`).

    Wipes `working` entirely then deep-copies `baseline` into it, so any
    renames/moves/deletes made during a test session are reverted. Refuses if
    `baseline` is missing or empty (nothing to restore from). Returns 0/1.
    """
    if not baseline or not os.path.isdir(baseline) or not os.listdir(baseline):
        err(f"fixtures: baseline is empty or missing ({baseline}) — run the generate/seed step first")
        return 1
    if not working:
        err("fixtures: no working path configured")
        return 1
    if os.path.exists(working):
        shutil.rmtree(working, ignore_errors=True)
    os.makedirs(os.path.dirname(os.path.abspath(working)), exist_ok=True)
    shutil.copytree(baseline, working)
    n = sum(len(files) for _, _, files in os.walk(working))
    ok(f"fixtures: restored {working} from baseline ({n} files)")
    return 0


def _resolve_dir(cfg, key):
    """Resolve a baseline/working path from cfg: expand ${VAR}, make absolute."""
    val = resolve_env(cfg.get(key, ""))
    if val and not os.path.isabs(val):
        val = os.path.abspath(val)
    return val


def fixtures_generate_from_config(cfg, force=False):
    """Adapter for kind=fixtures-generate. Reads [fixtures] {baseline, tree}."""
    if not cfg:
        warn("fixtures: no [fixtures] table configured")
        return 1
    return generate_tree(_resolve_dir(cfg, "baseline"), cfg.get("tree"), force=force)


def fixtures_reset_from_config(cfg):
    """Adapter for kind=fixtures-reset. Reads [fixtures] {baseline, working}.

    Optional `clean` (list of {env|path, default?, label?}) and `db`
    ({env?, default?, file} | {path}) keys fold project cache/DB wipes into the
    same reset, via admin_lib.cleanup (bundled alongside this module).
    """
    if not cfg:
        warn("fixtures: no [fixtures] table configured")
        return 1
    rc = restore_tree(_resolve_dir(cfg, "baseline"), _resolve_dir(cfg, "working"))
    if rc != 0:
        return rc

    # Optional cache/DB cleanup, reusing the cleanup module's config schema.
    clean = cfg.get("clean")
    db = cfg.get("db")
    if clean or db:
        sub = {}
        if clean:
            sub["paths"] = clean
        if db:
            sub["db"] = db
        clean_dev_from_config(sub, force=True)
    return 0
