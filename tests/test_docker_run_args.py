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
    assert result.returncode == 0, result.stderr
    assert "--memory 24g" in result.stdout
    assert "--cap-add PERFMON" in result.stdout
    assert "-v /mnt/user/appdata/x:/data" in result.stdout
    assert "-p 8080:80/tcp" in result.stdout


if __name__ == "__main__":
    test_render_run_args_includes_memory_and_cap_add()
    test_render_run_args_shell_quotes_values_with_spaces()
    test_render_run_args_empty_cfg_is_empty_string()
    test_run_args_command_dispatches_end_to_end()
    print("OK: docker run-args")
