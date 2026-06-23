"""Runtime interpreter tests (ADR-0006).

Validates that admin_lib.interp registers the same command/action/env set a
manifest describes, that ported kinds execute, and that `compile` produces a
runnable standalone ./admin.
"""
import base64
import os
import pickle
import subprocess
import sys
from pathlib import Path

import pytest

from admin_lib.manifest import parse
from admin_lib.archetypes import resolve
from admin_lib import core, interp

REPO_DIR = Path(__file__).resolve().parent.parent
ARCHETYPE_DIR = REPO_DIR / "archetypes"


@pytest.fixture(autouse=True)
def _reset_registry():
    """The core registry is process-global; clear it around each test."""
    core._commands.clear()
    core._actions.clear()
    core._referenced_env_vars.clear()
    core._pending_urls = []
    core._spacer_counter = 0
    yield
    core._commands.clear()
    core._actions.clear()
    core._referenced_env_vars.clear()


def _resolve(data):
    return resolve(parse(data, source="<test>"), ARCHETYPE_DIR)


def test_register_commands_actions_and_env():
    data = {
        "project_name": "T",
        "archetypes": [],
        "commands": {
            "build": {"desc": "Build", "steps": ["compile-step"]},
            "greet": {"desc": "Greet", "steps": ["greet"]},
        },
        "actions": {
            "compile-step": {"kind": "shell", "run": "echo hi"},
            "greet": {"kind": "shell", "run": "echo ${WHO:-world}"},
        },
    }
    resolved = _resolve(data)
    interp.register_all(resolved, "T")

    # No [logs] section and no `logs` command → no auto-injected `logs` command.
    assert set(core._all_commands()) == {"build", "greet"}
    assert set(core._actions) == {"compile-step", "greet"}
    # ${WHO:-world} is discovered and registered with its default + origin.
    assert "WHO" in core._referenced_env_vars
    assert core._referenced_env_vars["WHO"]["default"] == "world"
    assert "cmd:greet" in core._referenced_env_vars["WHO"]["used_by"]


def test_reserved_verb_collision_is_caught_by_cli(tmp_path):
    (tmp_path / "admin.toml").write_text(
        'project_name = "T"\narchetypes = []\n'
        '[commands.compile]\ndesc = "x"\nsteps = ["a"]\n'
        '[actions.a]\nkind = "shell"\nrun = "echo hi"\n'
    )
    rc = subprocess.run(
        [sys.executable, str(REPO_DIR / "admin-run"), "--project-dir", str(tmp_path), "check"],
        capture_output=True, text=True,
    )
    assert rc.returncode == 1
    assert "reserved" in rc.stderr.lower()


def test_python_kind_sees_config_globals():
    data = {
        "project_name": "T",
        "archetypes": [],
        "apple": {"scheme": "Demo"},
        "commands": {"go": {"desc": "go", "steps": ["act"]}},
        "actions": {
            "act": {"kind": "python", "run": "globals_have = globals().get('_APPLE_CONFIG')"},
        },
    }
    resolved = _resolve(data)
    ns = interp.register_all(resolved, "T")
    # The python body is compiled against the flat namespace, which carries the
    # [apple] table under _APPLE_CONFIG.
    assert ns["_APPLE_CONFIG"] == {"scheme": "Demo"}


def test_multi_target_resolves_targets():
    data = {
        "project_name": "T",
        "archetypes": [],
        "commands": {"pkg": {"desc": "pkg", "steps": ["pkg"]}},
        "actions": {
            "pkg": {
                "kind": "multi-target",
                "desc": "Package",
                "mac": {"run": "echo mac"},
                "ios": {"run": "echo ios"},
            }
        },
    }
    resolved = _resolve(data)
    interp.register_all(resolved, "T")
    assert "pkg" in core._actions
    assert resolved.actions["pkg"].targets == {"mac": "echo mac", "ios": "echo ios"}


def test_compile_produces_runnable_standalone(tmp_path):
    proj = tmp_path / "proj"
    proj.mkdir()
    (proj / "admin.toml").write_text(
        'project_name = "Compiled"\narchetypes = []\n'
        '[commands.hello]\ndesc = "say hello"\nsteps = ["hello"]\n'
        '[actions.hello]\nkind = "shell"\nrun = "echo HELLO_FROM_COMPILED"\n'
    )
    rc = subprocess.run(
        [sys.executable, str(REPO_DIR / "admin-run"), "--project-dir", str(proj), "compile"],
        capture_output=True, text=True,
    )
    assert rc.returncode == 0, rc.stderr
    admin = proj / "admin"
    assert admin.exists()

    # Run the compiled artifact with the repo intentionally OFF sys.path so we
    # prove it's standalone (uses its own embedded admin_lib/gen).
    env = dict(os.environ, PYTHONPATH="")
    out = subprocess.run(
        [str(admin), "hello"], capture_output=True, text=True, cwd=str(proj), env=env,
    )
    assert out.returncode == 0, out.stderr
    assert "HELLO_FROM_COMPILED" in out.stdout


def test_flat_action_runs_requires_before_body(monkeypatch):
    """A guarded flat action runs its `requires` guards before the body, fail-fast."""
    data = {
        "project_name": "T",
        "archetypes": [],
        "guards": {"no-go": {"run": "false", "fail": "blocked"}},
        "commands": {"deploy": {"desc": "d", "steps": ["deploy"]}},
        "actions": {"deploy": {"kind": "shell", "run": "echo deploying", "requires": ["no-go"]}},
    }
    resolved = _resolve(data)
    interp.register_all(resolved, "T")

    calls = []
    monkeypatch.setattr("admin_lib.guards.run_requires", lambda names: calls.append(names) or 1)
    with pytest.raises(SystemExit):
        core._actions["deploy"]([])
    assert calls == [["no-go"]], "guards must run before the action body, fail-fast"
