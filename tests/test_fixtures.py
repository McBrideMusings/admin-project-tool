"""Tests for admin_lib.fixtures + the fixtures-generate/fixtures-reset kinds."""
from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.fixtures import (  # noqa: E402
    generate_tree,
    parse_size,
    restore_tree,
)
from gen.archetypes import resolve  # noqa: E402
from gen.manifest import parse  # noqa: E402
from admin_lib import core, interp  # noqa: E402

ARCH_DIR = REPO / "archetypes"
LIB_DIR = REPO / "admin_lib"


def _clear_registry():
    core._commands.clear()
    core._actions.clear()
    core._referenced_env_vars.clear()


def test_parse_size_units():
    assert parse_size("100") == 100
    assert parse_size(2048) == 2048
    assert parse_size("2KB") == 2048
    assert parse_size("1.5GB") == int(1.5 * 1024 ** 3)
    assert parse_size("3 MB") == 3 * 1024 ** 2
    assert parse_size("1b") == 1
    print("OK: parse_size handles units, bare bytes, and floats")


def test_generate_tree_structure_and_sizes():
    with tempfile.TemporaryDirectory() as tmp:
        baseline = os.path.join(tmp, "baseline")
        tree = [
            {"dir": "Movies", "files": [{"name": "a.mkv", "size": "2KB"}]},
            {"dir": "Music", "files": [{"name": "t.flac", "size": "1KB", "count": 3}]},
            {"dir": "Nested/Deep", "files": [{"name": "x.txt", "size": "10"}]},
        ]
        rc = generate_tree(baseline, tree)
        assert rc == 0
        assert os.path.getsize(os.path.join(baseline, "Movies", "a.mkv")) == 2048
        # count=3 → numbered, zero-padded to width 2
        for i in (1, 2, 3):
            p = os.path.join(baseline, "Music", f"t{i:02d}.flac")
            assert os.path.getsize(p) == 1024, p
        assert os.path.exists(os.path.join(baseline, "Nested", "Deep", "x.txt"))
    print("OK: generate_tree builds dirs, sizes, and count-expanded files")


def test_generate_tree_refuses_nonempty_without_force():
    with tempfile.TemporaryDirectory() as tmp:
        baseline = os.path.join(tmp, "baseline")
        os.makedirs(baseline)
        Path(baseline, "preexisting").write_text("keep me")
        rc = generate_tree(baseline, [{"dir": "", "files": [{"name": "n.bin", "size": "1KB"}]}])
        assert rc == 1, "should refuse a non-empty baseline"
        assert os.path.exists(os.path.join(baseline, "preexisting"))
        # --force wipes and regenerates
        rc = generate_tree(baseline, [{"dir": "", "files": [{"name": "n.bin", "size": "1KB"}]}], force=True)
        assert rc == 0
        assert not os.path.exists(os.path.join(baseline, "preexisting"))
        assert os.path.getsize(os.path.join(baseline, "n.bin")) == 1024
    print("OK: generate_tree guards non-empty baseline, --force regenerates")


def test_restore_tree_roundtrip():
    with tempfile.TemporaryDirectory() as tmp:
        baseline = os.path.join(tmp, "baseline")
        working = os.path.join(tmp, "working")
        generate_tree(baseline, [
            {"dir": "d", "files": [{"name": "keep.bin", "size": "1KB"}]},
        ])
        assert restore_tree(baseline, working) == 0
        # Mutate working: delete a file, add a stray one, rename dir contents.
        os.remove(os.path.join(working, "d", "keep.bin"))
        Path(working, "stray.txt").write_text("junk")
        # Restore wipes the mutations and matches baseline exactly.
        assert restore_tree(baseline, working) == 0
        assert os.path.exists(os.path.join(working, "d", "keep.bin"))
        assert not os.path.exists(os.path.join(working, "stray.txt"))
    print("OK: restore_tree reverts deletes and strays to baseline")


def test_restore_tree_empty_baseline_errors():
    with tempfile.TemporaryDirectory() as tmp:
        baseline = os.path.join(tmp, "baseline")  # never created
        working = os.path.join(tmp, "working")
        assert restore_tree(baseline, working) == 1
        os.makedirs(baseline)  # exists but empty
        assert restore_tree(baseline, working) == 1
    print("OK: restore_tree errors on missing/empty baseline")


def test_fixtures_reset_kind_registers_and_pulls_modules():
    data = {
        "project_name": "t",
        "python_min": "3.11",
        "commands": {"reset": {"desc": "Restore fixtures", "steps": ["reset"]}},
        "actions": {"reset": {"kind": "fixtures-reset"}},
        "fixtures": {
            "baseline": "data/fixtures/baseline",
            "working": "data/fixtures/working",
        },
        "order": ["reset"],
    }
    m = parse(data)
    assert m.fixtures["baseline"] == "data/fixtures/baseline"
    resolved = resolve(m, ARCH_DIR)
    # fixtures-reset auto-pulls the fixtures + cleanup modules (KIND_MODULES).
    assert "fixtures" in resolved.modules
    assert "cleanup" in resolved.modules
    _clear_registry()
    ns = interp.register_all(resolved, "t")
    assert "reset" in core._actions
    assert ns["_FIXTURES_CONFIG"]["baseline"] == "data/fixtures/baseline"
    print("OK: fixtures-reset registers + auto-pulls fixtures/cleanup modules")


def test_fixtures_generate_kind_registers():
    data = {
        "project_name": "t",
        "python_min": "3.11",
        "commands": {"seed": {"desc": "Seed fixtures", "steps": ["seed"]}},
        "actions": {"seed": {"kind": "fixtures-generate"}},
        "fixtures": {
            "baseline": "data/fixtures/baseline",
            "tree": [{"dir": "Movies", "files": [{"name": "a.mkv", "size": "1.5GB"}]}],
        },
        "order": ["seed"],
    }
    m = parse(data)
    resolved = resolve(m, ARCH_DIR)
    assert "fixtures" in resolved.modules
    _clear_registry()
    ns = interp.register_all(resolved, "t")
    assert "seed" in core._actions
    assert ns["_FIXTURES_CONFIG"]["tree"][0]["dir"] == "Movies"
    print("OK: fixtures-generate registers with config")
