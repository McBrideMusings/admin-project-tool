"""Tests for the sync kind and sync()'s local/remote parity (#419).

The stash-plugin archetype was deleted in favour of this: three repos of one
product is a project shape, not a platform, and all it did was build-then-copy.

The delete semantics are the sharp edge. sync() has always used
`rsync -avz --delete` for a remote destination but a plain copytree locally, so
the two halves meant different things — a removed source file lingered forever
in a local dest. Local now prunes too, which makes `excludes` load-bearing:
stash-organizer keeps live SQLite state in the very directory it deploys into.
"""
from __future__ import annotations

import subprocess
import sys
import tempfile
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.archetypes import ArchetypeError, resolve  # noqa: E402
from admin_lib.core import sync  # noqa: E402
from admin_lib.manifest import parse  # noqa: E402

ARCH_DIR = REPO / "archetypes"


def _tree(root: Path, files: dict):
    for rel, body in files.items():
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(body)


def _listing(root: Path):
    return {str(p.relative_to(root)) for p in root.rglob("*") if p.is_file()}


def test_local_sync_copies_the_tree():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        src, dest = root / "dist", root / "plugins"
        _tree(src, {"index.js": "a", "nested/style.css": "b"})
        assert sync(str(src), str(dest)) == 0
        assert _listing(dest) == {"index.js", "nested/style.css"}


def test_local_sync_deletes_extraneous_files():
    """Parity with the remote half's --delete: a removed source file must go."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        src, dest = root / "dist", root / "plugins"
        _tree(src, {"index.js": "new"})
        _tree(dest, {"index.js": "old", "stale.js": "gone", "old-dir/x.js": "gone"})
        sync(str(src), str(dest))
        assert _listing(dest) == {"index.js"}, "stale files should be pruned"
        assert (dest / "index.js").read_text() == "new"


def test_local_sync_excludes_protect_files_from_deletion():
    """The stash-organizer case: live DBs live in the deploy dir and must survive."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        src, dest = root / "dist", root / "plugins"
        _tree(src, {"index.js": "new"})
        _tree(dest, {"index.js": "old", "library.db": "PRECIOUS", "stale.js": "gone"})
        sync(str(src), str(dest), excludes=["library.db"])
        survivors = _listing(dest)
        assert "library.db" in survivors, "an excluded file must never be deleted"
        assert (dest / "library.db").read_text() == "PRECIOUS"
        assert "stale.js" not in survivors, "non-excluded stale files should still go"


def test_local_sync_excludes_accept_globs():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        src, dest = root / "dist", root / "plugins"
        _tree(src, {"index.js": "new"})
        _tree(dest, {"state.db": "x", "state.db-wal": "y", "junk.txt": "z"})
        sync(str(src), str(dest), excludes=["*.db", "*.db-wal"])
        survivors = _listing(dest)
        assert {"state.db", "state.db-wal"} <= survivors
        assert "junk.txt" not in survivors


def test_prune_never_escapes_the_destination():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        src, dest = root / "dist", root / "plugins"
        sibling = root / "sibling"
        _tree(src, {"index.js": "a"})
        _tree(dest, {"old.js": "b"})
        _tree(sibling, {"important.txt": "DO NOT TOUCH"})
        sync(str(src), str(dest))
        assert (sibling / "important.txt").exists()


# --- the kind ------------------------------------------------------------


def test_sync_kind_passes_excludes_through():
    toml = '''
project_name = "X"

[commands.deploy]
desc  = "deploy"
steps = ["deploy"]

[actions.deploy]
kind     = "sync"
desc     = "Sync dist to the plugin dir"
src      = "dist/"
run      = "/tmp/plugins/demo"
excludes = ["*.db", "state.json"]
'''
    m = parse(tomllib.loads(toml), source="t")
    r = resolve(m, ARCH_DIR)
    assert r.actions["deploy"].meta["excludes"] == ["*.db", "state.json"]


def test_sync_kind_end_to_end_prunes_and_protects():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _tree(root / "dist", {"index.js": "new"})
        dest = root / "plugins" / "demo"
        _tree(dest, {"index.js": "old", "library.db": "PRECIOUS", "stale.js": "gone"})
        (root / "admin.toml").write_text(
            'project_name = "X"\n\n'
            '[commands.deploy]\ndesc = "deploy"\nsteps = ["deploy"]\n\n'
            '[actions.deploy]\nkind = "sync"\ndesc = "sync"\nsrc = "dist/"\n'
            f'run = "{dest}"\nexcludes = ["*.db"]\n'
        )
        r = subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", str(root), "deploy"],
            capture_output=True, text=True, timeout=20,
        )
        assert r.returncode == 0, r.stderr
        assert (dest / "library.db").read_text() == "PRECIOUS"
        assert not (dest / "stale.js").exists()
        assert (dest / "index.js").read_text() == "new"


# --- the archetype is gone ----------------------------------------------


def test_stash_plugin_archetype_no_longer_exists():
    assert not (ARCH_DIR / "stash-plugin.py").exists()
    assert not (REPO / "detectors" / "stash-plugin.py").exists()


def test_declaring_the_stash_plugin_archetype_now_errors():
    m = parse(tomllib.loads('project_name = "X"\narchetypes = ["stash-plugin"]\n'), source="t")
    try:
        resolve(m, ARCH_DIR)
    except ArchetypeError as e:
        assert "unknown archetype" in str(e)
    else:
        raise AssertionError("expected ArchetypeError for the removed archetype")
