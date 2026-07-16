"""Tests for the go kind and the [go] config table (#414).

wtree, repo-dash, and cnav each declared archetypes = ["go-cli"] and then
shadowed most of its actions inline, because the one thing they needed — a
named binary at bin/<name> — could not be expressed. [go] expresses it.
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


def _parse(toml: str):
    return parse(tomllib.loads(toml), source="t")


def _run(toml: str, argv: list[str], extra_path: Path | None = None):
    """Dispatch through the real interpreter with a stub `go` on PATH."""
    import os

    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "admin.toml").write_text(toml)
        bin_dir = root / "stub-bin"
        bin_dir.mkdir()
        stub = bin_dir / "go"
        stub.write_text('#!/bin/sh\necho "GO $*"\n')
        stub.chmod(0o755)
        env = dict(os.environ)
        env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
        return subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", str(root), *argv],
            capture_output=True, text=True, timeout=20, env=env,
        )


# --- config table --------------------------------------------------------


def test_go_table_parses():
    m = _parse('project_name = "X"\n[go]\nbinary = "bin/wtree"\nmain = "./cmd/x"\n')
    assert m.go == {"binary": "bin/wtree", "main": "./cmd/x"}


def test_go_table_rejects_unknown_key():
    try:
        _parse('project_name = "X"\n[go]\noutput = "bin/x"\n')
    except ManifestError as e:
        assert "unknown keys" in str(e) and "output" in str(e)
    else:
        raise AssertionError("expected ManifestError for unknown [go] key")


def test_go_table_rejects_non_string_binary():
    try:
        _parse('project_name = "X"\n[go]\nbinary = 1\n')
    except ManifestError as e:
        assert "'[go].binary' must be a string" in str(e)
    else:
        raise AssertionError("expected ManifestError")


# --- archetype defaults are unchanged without [go] -----------------------


def test_go_cli_without_config_still_builds_all_packages():
    """power-hour-generator sets no [go]; its behavior must not change."""
    r = _run('project_name = "X"\narchetypes = ["go-cli"]\n', ["build"])
    assert r.returncode == 0, r.stderr
    assert "GO build ./..." in r.stdout, r.stdout


def test_go_cli_without_config_installs_all_packages():
    r = _run('project_name = "X"\narchetypes = ["go-cli"]\n', ["deploy"])
    assert r.returncode == 0, r.stderr
    assert "GO install ./..." in r.stdout, r.stdout


def test_go_cli_without_config_cleans_via_go_clean_only():
    r = _run('project_name = "X"\narchetypes = ["go-cli"]\n', ["clean"])
    assert r.returncode == 0, r.stderr
    assert "GO clean ./..." in r.stdout, r.stdout


# --- [go] drives build/install/clean ------------------------------------


def test_binary_makes_build_emit_a_named_output():
    r = _run(
        'project_name = "X"\narchetypes = ["go-cli"]\n[go]\nbinary = "bin/wtree"\n',
        ["build"],
    )
    assert r.returncode == 0, r.stderr
    assert "GO build -o bin/wtree ." in r.stdout, r.stdout


def test_main_selects_the_package_to_build():
    r = _run(
        'project_name = "X"\narchetypes = ["go-cli"]\n'
        '[go]\nbinary = "bin/cnav"\nmain = "./cmd/cnav"\n',
        ["build"],
    )
    assert r.returncode == 0, r.stderr
    assert "GO build -o bin/cnav ./cmd/cnav" in r.stdout, r.stdout


def test_main_selects_the_package_to_install():
    r = _run(
        'project_name = "X"\narchetypes = ["go-cli"]\n[go]\nmain = "."\n',
        ["deploy"],
    )
    assert r.returncode == 0, r.stderr
    assert "GO install ." in r.stdout, r.stdout


def test_clean_removes_the_configured_binary_by_name():
    """go clean never removes a -o target, so the binary is removed explicitly."""
    r = _run(
        'project_name = "X"\narchetypes = ["go-cli"]\n[go]\nbinary = "bin/wtree"\n',
        ["clean"],
    )
    assert r.returncode == 0, r.stderr
    assert "GO clean ./..." in r.stdout, r.stdout
    assert "rm -f bin/wtree" in r.stdout, r.stdout


def test_binary_path_is_shell_quoted():
    m = _parse('project_name = "X"\narchetypes = ["go-cli"]\n[go]\nbinary = "bin/my app"\n')
    resolved = resolve(m, ARCH_DIR)
    assert resolved.actions["build"].kind == "go"
    r = _run(
        'project_name = "X"\narchetypes = ["go-cli"]\n[go]\nbinary = "bin/my app"\n',
        ["build"],
    )
    assert r.returncode == 0, r.stderr
    assert "'bin/my app'" in r.stdout, r.stdout


# --- misuse --------------------------------------------------------------


def test_unknown_go_mode_is_rejected():
    r = _run(
        'project_name = "X"\n[actions.oops]\nkind = "go"\nrun = "publish"\n'
        '[commands.oops]\nsteps = ["oops"]\ndesc = "x"\n',
        ["oops"],
    )
    assert r.returncode != 0
    assert "must be one of" in (r.stdout + r.stderr)


# --- the manifests this issue is about ----------------------------------


def test_wtree_shape_resolves_without_shadowing():
    """wtree's whole reason for shadowing was `go build -o bin/wtree .`."""
    m = _parse('project_name = "Wtree"\narchetypes = ["go-cli"]\n[go]\nbinary = "bin/wtree"\n')
    resolved = resolve(m, ARCH_DIR)
    for name in ("build", "test", "vet", "fmt", "clean", "deploy"):
        assert resolved.actions[name].source == "go-cli", (
            f"{name} should come from the archetype, not a manifest override"
        )


def test_command_override_without_steps_still_inherits_archetype_steps():
    """wtree's [commands.deploy] carries desc/group only — steps come from go-cli."""
    m = _parse(
        'project_name = "Wtree"\narchetypes = ["go-cli"]\n'
        '[commands.deploy]\ndesc = "Install binary to ~/go/bin"\ngroup = 1\n'
    )
    resolved = resolve(m, ARCH_DIR)
    assert resolved.commands["deploy"].meta.get("steps") == ["deploy"]
