"""Tests for the raylib-game archetype, [raylib] config, and the raylib kind (#415).

Seven games hand-declared the same cmake-build/dev/smoke/clean set and drifted:
-j8 vs -j vs none, RF_HOTRELOAD on/off, RF_SMOKE vs GDH_SMOKE, and clean dirs
ranging from `build` to `build build-web web/dist`.
"""
from __future__ import annotations

import subprocess
import sys
import tempfile
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.archetypes import resolve  # noqa: E402
from admin_lib.manifest import ManifestError, parse  # noqa: E402

ARCH_DIR = REPO / "archetypes"
DET_DIR = REPO / "detectors"


def _parse(toml: str):
    return parse(tomllib.loads(toml), source="t")


def _run(toml: str, argv: list[str]):
    """Dispatch through the interpreter with stub cmake/rm so nothing is built."""
    import os

    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "admin.toml").write_text(toml)
        bin_dir = root / "stub-bin"
        bin_dir.mkdir()
        for tool in ("cmake", "rm"):
            stub = bin_dir / tool
            stub.write_text(f'#!/bin/sh\necho "{tool.upper()} $*"\n')
            stub.chmod(0o755)
        env = dict(os.environ)
        env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
        return subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", str(root), *argv],
            capture_output=True, text=True, timeout=20, env=env,
        )


_BASE = 'project_name = "G"\narchetypes = ["raylib-game"]\n'


# --- config validation ---------------------------------------------------


def test_raylib_table_parses_every_key():
    m = _parse(
        _BASE + '[raylib]\nbinary = "g"\nbuild_dir = "b"\ncmake_flags = ["-DX=ON"]\n'
        'jobs = 8\nsmoke_env = "GDH_SMOKE"\nclean_dirs = ["b", "c"]\n'
    )
    assert m.raylib == {
        "binary": "g", "build_dir": "b", "cmake_flags": ["-DX=ON"],
        "jobs": 8, "smoke_env": "GDH_SMOKE", "clean_dirs": ["b", "c"],
    }


def test_raylib_table_rejects_unknown_key():
    try:
        _parse(_BASE + '[raylib]\ntarget = "g"\n')
    except ManifestError as e:
        assert "unknown keys" in str(e) and "target" in str(e)
    else:
        raise AssertionError("expected ManifestError")


def test_raylib_table_rejects_a_bad_jobs_value():
    try:
        _parse(_BASE + '[raylib]\njobs = "8"\n')
    except ManifestError as e:
        assert "non-negative integer" in str(e) and "auto" in str(e)
    else:
        raise AssertionError("expected ManifestError")


def test_raylib_table_accepts_jobs_auto():
    """game-overgrowth builds with a bare -j (one job per core)."""
    m = _parse(_BASE + '[raylib]\njobs = "auto"\n')
    assert m.raylib["jobs"] == "auto"


def test_jobs_auto_emits_a_bare_j_flag():
    r = _run(_BASE + '[raylib]\nbinary = "overgrowth"\njobs = "auto"\n', ["build"])
    assert r.returncode == 0, r.stderr
    assert "CMAKE --build build -j" in r.stdout, r.stdout
    assert "-j8" not in r.stdout, "bare -j must not gain a number"


def test_raylib_table_rejects_non_list_cmake_flags():
    try:
        _parse(_BASE + '[raylib]\ncmake_flags = "-DX=ON"\n')
    except ManifestError as e:
        assert "must be a list of strings" in str(e)
    else:
        raise AssertionError("expected ManifestError")


# --- build ---------------------------------------------------------------


def test_build_defaults_to_plain_configure_and_build():
    r = _run(_BASE + '[raylib]\nbinary = "g"\n', ["build"])
    assert r.returncode == 0, r.stderr
    assert "CMAKE -S . -B build" in r.stdout, r.stdout
    assert "CMAKE --build build" in r.stdout, r.stdout
    assert "-j" not in r.stdout, "no jobs configured, so no -j flag"


def test_build_includes_cmake_flags_and_jobs():
    r = _run(
        _BASE + '[raylib]\nbinary = "g"\ncmake_flags = ["-DRF_HOTRELOAD=ON"]\njobs = 8\n',
        ["build"],
    )
    assert r.returncode == 0, r.stderr
    assert "CMAKE -S . -B build -DRF_HOTRELOAD=ON" in r.stdout, r.stdout
    assert "CMAKE --build build -j8" in r.stdout, r.stdout


def test_build_honors_build_dir():
    r = _run(_BASE + '[raylib]\nbinary = "g"\nbuild_dir = "build-rel"\n', ["build"])
    assert r.returncode == 0, r.stderr
    assert "CMAKE -S . -B build-rel" in r.stdout, r.stdout


# --- smoke ---------------------------------------------------------------


def test_smoke_defaults_to_rf_smoke():
    m = _parse(_BASE + '[raylib]\nbinary = "game-backrooms"\n')
    resolved = resolve(m, ARCH_DIR)
    assert resolved.actions["smoke"].kind == "raylib"
    assert resolved.actions["smoke"].run == "smoke"


def test_smoke_env_is_configurable():
    """game-die-hard uses GDH_SMOKE, not RF_SMOKE.

    `smoke` is an action, not a command — the `test` command runs cmake-build
    then smoke, so this drives the real path with a stub cmake and a real exe.
    """
    import os

    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "admin.toml").write_text(
            _BASE + '[raylib]\nbinary = "game"\nsmoke_env = "GDH_SMOKE"\n'
        )
        build = root / "build"
        build.mkdir()
        exe = build / "game"
        exe.write_text('#!/bin/sh\necho "SMOKE=$GDH_SMOKE"\n')
        exe.chmod(0o755)
        bin_dir = root / "stub-bin"
        bin_dir.mkdir()
        stub = bin_dir / "cmake"
        stub.write_text('#!/bin/sh\nexit 0\n')
        stub.chmod(0o755)
        env = dict(os.environ)
        env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
        r = subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", str(root), "test"],
            capture_output=True, text=True, timeout=20, env=env,
        )
        assert r.returncode == 0, r.stderr
        assert "SMOKE=1" in r.stdout, r.stdout


def test_run_without_a_binary_errors_clearly():
    r = _run(_BASE, ["dev"])
    assert r.returncode != 0
    assert "[raylib].binary is not set" in (r.stdout + r.stderr)


# --- clean ---------------------------------------------------------------


def _clean_project(toml: str, make: list[str]):
    """Run `clean` for real against a scratch tree; return which paths survived."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "admin.toml").write_text(toml)
        for rel in make:
            p = root / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            if rel.endswith("/"):
                p.mkdir(parents=True, exist_ok=True)
            else:
                p.write_text("x")
        r = subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", str(root), "clean"],
            capture_output=True, text=True, timeout=20,
        )
        assert r.returncode == 0, r.stderr
        return {rel for rel in make if (root / rel).exists()}


def test_clean_defaults_to_the_build_dir():
    survived = _clean_project(
        _BASE + '[raylib]\nbinary = "g"\n',
        ["build/artifact.o", "src/main.c"],
    )
    assert "build/artifact.o" not in survived
    assert "src/main.c" in survived, "clean must not touch sources"


def test_clean_dirs_is_configurable():
    """game-virtual-cottage-web removes build, build-web, and web/dist."""
    survived = _clean_project(
        _BASE + '[raylib]\nbinary = "cottage"\nclean_dirs = ["build", "build-web", "web/dist"]\n',
        ["build/a.o", "build-web/b.js", "web/dist/c.html", "web/src/keep.ts"],
    )
    assert survived == {"web/src/keep.ts"}, survived


def test_clean_dirs_expands_globs():
    """game-adventure removes data/zones/*.db — a glob, not a literal path."""
    survived = _clean_project(
        _BASE + '[raylib]\nbinary = "game-adventure"\n'
        'clean_dirs = ["build", "data/zones/*.db", "data/gamedata.db"]\n',
        ["build/a.o", "data/zones/overworld.db", "data/zones/dungeon.db",
         "data/gamedata.db", "data/zones/overworld/tiles.json"],
    )
    assert survived == {"data/zones/overworld/tiles.json"}, survived


def test_clean_is_quiet_when_nothing_matches():
    survived = _clean_project(_BASE + '[raylib]\nbinary = "g"\n', ["src/main.c"])
    assert survived == {"src/main.c"}


# --- misuse --------------------------------------------------------------


def test_unknown_raylib_mode_is_rejected():
    r = _run(
        'project_name = "G"\n[actions.oops]\nkind = "raylib"\nrun = "publish"\n'
        '[commands.oops]\nsteps = ["oops"]\ndesc = "x"\n',
        ["oops"],
    )
    assert r.returncode != 0
    assert "must be one of" in (r.stdout + r.stderr)


# --- detector ------------------------------------------------------------


def _detect_raylib_only(project_dir: Path):
    import importlib.util

    spec = importlib.util.spec_from_file_location("_det_rl", DET_DIR / "raylib-game.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.detect(project_dir)


def test_detector_matches_raylib_cmake_project_and_seeds_binary():
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "CMakeLists.txt").write_text(
            "cmake_minimum_required(VERSION 3.16)\nproject(game-jerry-road C)\n"
            "add_subdirectory(third_party/raylib)\n"
        )
        assert _detect_raylib_only(Path(d)) == {
            "archetypes": ["raylib-game"],
            "raylib": {"binary": "game-jerry-road"},
        }


def test_detector_ignores_cmake_project_without_raylib():
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "CMakeLists.txt").write_text("project(other C)\nadd_subdirectory(sdl)\n")
        assert _detect_raylib_only(Path(d)) is None


def test_detector_ignores_the_forge_engine_repo_itself():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "CMakeLists.txt").write_text("project(forge C)\n# raylib cradle\n")
        (root / "engine").mkdir()
        (root / "engine" / "build.zig").write_text("")
        assert _detect_raylib_only(root) is None
