"""End-to-end coverage for issue #440: `admin deploy files` chowning synced
directories to the local user's uid, breaking a container that runs as a
different one.

Exercises the real pipeline — the `docker` archetype's "deploy" action,
`admin_lib.docker.deploy_dispatch` (unmodified), `admin_lib.remote.deploy_files`,
and `admin_lib.core.sync` — mocking out only the final subprocess call, so the
exact rsync command string is asserted the same way it would actually be run.
"""
from __future__ import annotations

from pathlib import Path

import pytest

from admin_lib.archetypes import resolve
from admin_lib.manifest import parse
from admin_lib import core, interp, remote

REPO_DIR = Path(__file__).resolve().parent.parent
ARCHETYPE_DIR = REPO_DIR / "archetypes"


@pytest.fixture(autouse=True)
def _reset_registry():
    """The core registry is process-global; clear it around each test."""
    core._commands.clear()
    core._actions.clear()
    core._referenced_env_vars.clear()
    yield
    core._commands.clear()
    core._actions.clear()
    core._referenced_env_vars.clear()


def _register_docker_project(tmp_path, docker_run=None, deploy_extra=None):
    src = tmp_path / "appdata"
    src.mkdir()

    deploy = {"src": str(src), "dest": "/mnt/user/appdata/app", "host": "root@tower"}
    deploy.update(deploy_extra or {})

    data = {
        "project_name": "T",
        "archetypes": ["docker"],
        "docker_run": docker_run or {},
        "deploy": deploy,
    }
    resolved = resolve(parse(data, source="<test>"), ARCHETYPE_DIR)
    interp.register_all(resolved, "T")


def _run_deploy_files(monkeypatch):
    """Invoke `admin deploy files --dry-run` through the registered action,
    capturing the exact rsync command core.sync would have run."""
    seen = []
    monkeypatch.setattr(core, "run_cmd", lambda cmd, **kw: seen.append(cmd) or 0)
    monkeypatch.setattr(remote, "_is_local_host", lambda h: False)

    with pytest.raises(SystemExit) as exc:
        core._actions["deploy"](["files", "--dry-run"])
    assert exc.value.code == 0, "deploy files must succeed for the command to be observable"
    assert seen, "no rsync command was constructed"
    return seen[0]


def test_docker_archetype_defaults_chown_from_docker_run_user(tmp_path, monkeypatch):
    """[deploy] has no chown, [docker_run].user is set -> chown defaults to it."""
    _register_docker_project(tmp_path, docker_run={"user": "99:100"})

    cmd = _run_deploy_files(monkeypatch)

    assert "--chown=99:100" in cmd


def test_docker_archetype_explicit_chown_wins_over_docker_run_user(tmp_path, monkeypatch):
    """An explicit [deploy].chown is never overridden by [docker_run].user."""
    _register_docker_project(
        tmp_path, docker_run={"user": "99:100"}, deploy_extra={"chown": "42:42"},
    )

    cmd = _run_deploy_files(monkeypatch)

    assert "--chown=42:42" in cmd
    assert "--chown=99:100" not in cmd


def test_docker_archetype_no_chown_when_neither_declared(tmp_path, monkeypatch):
    """No [docker_run].user and no [deploy].chown -> rsync gets no --chown, same
    as before this feature existed."""
    _register_docker_project(tmp_path)

    cmd = _run_deploy_files(monkeypatch)

    assert "--chown" not in cmd
