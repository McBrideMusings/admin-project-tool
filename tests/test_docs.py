"""Tests for the docs archetype, the [docs] config table, and the docs kind.

The docs site was hand-declared in ~25 manifests before this archetype existed
(#413), in three drifted shapes: kind="npm" run="docs:dev", a shell
`cd docs && npm run docs:dev`, and an interactive-shell variant. The two that
matter structurally are root-package.json projects and projects whose site has
its own docs/package.json — both must work off one archetype.
"""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.archetypes import load_archetype, resolve  # noqa: E402
from admin_lib.interp import _pkg_runner  # noqa: E402
from admin_lib.manifest import ManifestError, parse  # noqa: E402

ARCH_DIR = REPO / "archetypes"
DET_DIR = REPO / "detectors"


def _pkg(scripts: dict) -> str:
    return json.dumps({"name": "x", "scripts": scripts})


def _parse(toml: str):
    """parse() takes a raw dict, so every test writes TOML and loads it first."""
    return parse(tomllib.loads(toml), source="t")


# --- [docs] config table -------------------------------------------------


def test_docs_table_parses():
    m = _parse('project_name = "X"\n[docs]\ndir = "docs"\nscript = "site:dev"\n')
    assert m.docs == {"dir": "docs", "script": "site:dev"}


def test_docs_table_defaults_to_empty():
    m = _parse('project_name = "X"\n')
    assert m.docs == {}


def test_docs_table_rejects_unknown_key():
    try:
        _parse('project_name = "X"\n[docs]\nport = 5193\n')
    except ManifestError as e:
        assert "unknown keys" in str(e) and "port" in str(e)
    else:
        raise AssertionError("expected ManifestError for unknown [docs] key")


def test_docs_table_rejects_non_string_dir():
    try:
        _parse('project_name = "X"\n[docs]\ndir = 3\n')
    except ManifestError as e:
        assert "'[docs].dir' must be a string" in str(e)
    else:
        raise AssertionError("expected ManifestError for non-string [docs].dir")


# --- archetype -----------------------------------------------------------


def test_docs_archetype_provides_docs_command_and_action():
    arch = load_archetype(ARCH_DIR, "docs")
    assert "docs" in arch.commands
    assert arch.commands["docs"]["steps"] == ["docs"]
    assert arch.actions["docs"]["kind"] == "docs"
    # Config lives in [docs], never in the action — the action carries no run.
    assert "run" not in arch.actions["docs"]


def test_docs_archetype_composes_with_another_archetype():
    """docs rides along with the project's real archetype; no command collision."""
    m = _parse('project_name = "X"\narchetypes = ["go-cli", "docs"]\n')
    resolved = resolve(m, ARCH_DIR)
    assert "docs" in resolved.commands
    assert "build" in resolved.commands  # go-cli's, untouched
    assert resolved.actions["docs"].kind == "docs"


def test_manifest_action_still_overrides_the_archetype_docs_action():
    """An escape hatch stays: a project can replace the action wholesale."""
    m = _parse(
        'project_name = "X"\narchetypes = ["docs"]\n'
        '[actions.docs]\nkind = "shell"\nrun = "mkdocs serve"\n'
    )
    resolved = resolve(m, ARCH_DIR)
    assert resolved.actions["docs"].kind == "shell"
    assert resolved.actions["docs"].run == "mkdocs serve"


# --- runner selection ----------------------------------------------------


def test_pkg_runner_defaults_to_npm():
    with tempfile.TemporaryDirectory() as d:
        assert _pkg_runner(d) == "npm"


def test_pkg_runner_picks_bun_from_lockfile():
    for lock in ("bun.lockb", "bun.lock"):
        with tempfile.TemporaryDirectory() as d:
            (Path(d) / lock).write_text("")
            assert _pkg_runner(d) == "bun", f"{lock} should select bun"


def test_pkg_runner_is_scoped_to_the_given_dir():
    """A bun docs site inside an npm project resolves per-directory, not per-repo."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "docs").mkdir()
        (root / "docs" / "bun.lock").write_text("")
        assert _pkg_runner(str(root)) == "npm"
        assert _pkg_runner(str(root / "docs")) == "bun"


# --- detector ------------------------------------------------------------


def test_detector_matches_root_package_json():
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "package.json").write_text(_pkg({"docs:dev": "vitepress dev docs"}))
        assert _detect_docs_only(Path(d)) == {"archetypes": ["docs"]}


def test_detector_matches_docs_subdir_and_sets_dir():
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "docs").mkdir()
        (Path(d) / "docs" / "package.json").write_text(_pkg({"docs:dev": "vitepress dev"}))
        assert _detect_docs_only(Path(d)) == {"archetypes": ["docs"], "docs": {"dir": "docs"}}


def test_detector_ignores_package_json_without_the_script():
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "package.json").write_text(_pkg({"build": "tsc"}))
        assert _detect_docs_only(Path(d)) is None


def test_detector_survives_malformed_package_json():
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "package.json").write_text("{not json")
        assert _detect_docs_only(Path(d)) is None


def _detect_docs_only(project_dir: Path):
    """Call the docs detector directly, bypassing the combining loader."""
    import importlib.util

    spec = importlib.util.spec_from_file_location("_det_docs", DET_DIR / "docs.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.detect(project_dir)


# --- end to end ----------------------------------------------------------


def test_docs_action_runs_the_script_from_the_configured_dir():
    """Full dispatch: the site under docs/ is run from docs/, not the root."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "admin.toml").write_text(
            'project_name = "X"\narchetypes = ["docs"]\n[docs]\ndir = "docs"\nscript = "docs:dev"\n'
        )
        docs = root / "docs"
        docs.mkdir()
        # A real npm run would need node; assert on the invocation instead by
        # making the script echo its cwd through a stub `npm` on PATH.
        bin_dir = root / "bin"
        bin_dir.mkdir()
        stub = bin_dir / "npm"
        stub.write_text('#!/bin/sh\necho "npm-cwd=$PWD args=$*"\n')
        stub.chmod(0o755)

        import os

        env = dict(os.environ)
        env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
        result = subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", str(root), "docs"],
            capture_output=True, text=True, timeout=20, env=env,
        )
        assert result.returncode == 0, f"docs dispatch failed:\n{result.stderr}"
        assert "args=run docs:dev" in result.stdout, f"stdout={result.stdout!r}"
        assert str(docs.resolve()) in result.stdout, (
            f"expected the script to run from {docs}; stdout={result.stdout!r}"
        )


def test_docs_action_errors_when_the_dir_is_missing():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "admin.toml").write_text(
            'project_name = "X"\narchetypes = ["docs"]\n[docs]\ndir = "nope"\n'
        )
        result = subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", str(root), "docs"],
            capture_output=True, text=True, timeout=20,
        )
        assert result.returncode != 0
        assert "no such directory: nope" in (result.stdout + result.stderr)
