"""Tests for the `docker` archetype's `run-args` command and the underlying
`admin_lib.docker.render_run_args` helper (issue #429).

`run-args` prints the `[docker_run]` table rendered to shlex-quoted `docker run`
args so an out-of-tree native-build script can capture `$(admin run-args)`
instead of re-parsing admin.toml itself — which is how the shell awk parser
silently dropped `memory` and `cap_add` and drifted from `./admin deploy`.
"""
from __future__ import annotations

import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.docker import render_run_args  # noqa: E402


def test_render_run_args_includes_memory_and_cap_add():
    """The exact keys the awk parser dropped must survive the render."""
    cfg = {
        "memory": "24g",
        "cap_add": ["PERFMON", "SYS_NICE"],
        "bind_mounts": [{"host": "/mnt/user/appdata/x", "container": "/data"}],
    }
    out = render_run_args(cfg)
    assert "--memory 24g" in out
    assert "--cap-add PERFMON" in out
    assert "--cap-add SYS_NICE" in out
    assert "-v /mnt/user/appdata/x:/data" in out


def test_render_run_args_shell_quotes_values_with_spaces():
    """Output must be eval-safe: a value with spaces stays a single token."""
    cfg = {"env": [{"name": "MOTD", "value": "hello world"}]}
    out = render_run_args(cfg)
    assert "-e 'MOTD=hello world'" in out


def test_render_run_args_empty_cfg_is_empty_string():
    assert render_run_args({}) == ""
    assert render_run_args(None) == ""


def test_run_args_command_dispatches_end_to_end():
    """`admin run-args` on a real [docker_run] table prints the rendered args.

    Exercises the full path: archetype command -> action -> render_run_args
    exposed in the interpreter namespace. Uses raw TOML because dump() doesn't
    serialize [docker_run].
    """
    manifest = (
        'project_name = "RunArgs Test"\n'
        'python_min = "3.11"\n'
        'archetypes = ["docker"]\n'
        "\n"
        "[docker_run]\n"
        'memory = "24g"\n'
        'cap_add = ["PERFMON"]\n'
        "\n"
        "[[docker_run.bind_mounts]]\n"
        'host = "/mnt/user/appdata/x"\n'
        'container = "/data"\n'
        "\n"
        "[[docker_run.ports]]\n"
        "host = 8080\n"
        "container = 80\n"
    )
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "admin.toml").write_text(manifest)
        result = subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", d, "run-args"],
            capture_output=True, text=True, timeout=10,
        )
        # run-args is a pure-query command (log = false) — it must not litter
        # the project's tmp/ with a per-call timestamped log file.
        tmp_logs = list((Path(d) / "tmp").glob("run-args.*.log"))
    assert result.returncode == 0, result.stderr
    assert "--memory 24g" in result.stdout
    assert "--cap-add PERFMON" in result.stdout
    assert "-v /mnt/user/appdata/x:/data" in result.stdout
    assert "-p 8080:80/tcp" in result.stdout
    assert tmp_logs == [], f"run-args wrote log files: {tmp_logs}"


if __name__ == "__main__":
    test_render_run_args_includes_memory_and_cap_add()
    test_render_run_args_shell_quotes_values_with_spaces()
    test_render_run_args_empty_cfg_is_empty_string()
    test_run_args_command_dispatches_end_to_end()
    print("OK: docker run-args")


def test_render_run_args_expands_env_in_bind_mount_host(monkeypatch):
    """A host path must be able to come from .env — a committed manifest should
    not hardcode a deploy target's directory layout."""
    monkeypatch.setenv("APPDATA_DIR", "/mnt/user/appdata/plex-home")
    cfg = {"bind_mounts": [{"host": "${APPDATA_DIR}", "container": "/app"}]}

    out = render_run_args(cfg)

    assert "-v /mnt/user/appdata/plex-home:/app" in out
    assert "${" not in out


def test_render_run_args_skips_bind_mount_with_unset_env(monkeypatch):
    """An unset var drops the mount instead of mounting a literal '${VAR}' dir."""
    monkeypatch.delenv("NOT_SET_ANYWHERE", raising=False)
    cfg = {"bind_mounts": [{"host": "${NOT_SET_ANYWHERE}", "container": "/app"}]}

    out = render_run_args(cfg)

    assert "-v" not in out


def test_render_run_args_expands_the_default_form_everywhere(monkeypatch):
    """`${VAR:-default}` is the form the docker archetype's own templates are
    written in — ports, user, and env all ship with it. Expansion here used to
    match only the bare `${VAR}` form, so every one of these survived as literal
    text: `docker run -p '${ETV_PORT_HOST:-8409}:8409'`, and the drift check
    compared that string against the live container's real `8419`, reporting
    three false drifts on a correctly configured project.
    """
    monkeypatch.setenv("PORT_HOST", "8419")
    monkeypatch.delenv("UID_VAR", raising=False)
    monkeypatch.delenv("TZ_VAR", raising=False)
    cfg = {
        "ports": [{"host": "${PORT_HOST:-8409}", "container": 8409}],
        "user": "${UID_VAR:-99}:100",
        "env": [{"name": "TZ", "value": "${TZ_VAR:-America/Chicago}"}],
    }

    out = render_run_args(cfg)

    assert "${" not in out, out
    assert "-p 8419:8409" in out
    assert "--user 99:100" in out
    assert "TZ=America/Chicago" in out


def test_render_run_args_default_wins_only_when_var_is_unset(monkeypatch):
    """A set var beats its default — otherwise .env could never override one."""
    monkeypatch.setenv("PORT_HOST", "9001")
    cfg = {"ports": [{"host": "${PORT_HOST:-8409}", "container": 80}]}

    assert "-p 9001:80" in render_run_args(cfg)

    monkeypatch.delenv("PORT_HOST", raising=False)

    assert "-p 8409:80" in render_run_args(cfg)


def test_render_run_args_empty_default_resolves_to_empty_not_dropped(monkeypatch):
    """`${VAR:-}` is an explicit empty default, not an unresolved reference."""
    monkeypatch.delenv("MAYBE_UNSET", raising=False)
    cfg = {"env": [{"name": "OPTIONAL", "value": "${MAYBE_UNSET:-}"}]}

    out = render_run_args(cfg)

    assert "${" not in out
    assert "OPTIONAL=" in out
