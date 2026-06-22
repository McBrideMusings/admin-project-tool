"""Tests for the bind-probe free/busy check and the macOS holder-identification
fallback added to fix preflight_port silently passing a busy port when lsof
hangs on a stale network mount.
"""
from __future__ import annotations

import errno
import socket
import sys
from pathlib import Path
from unittest.mock import patch

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.ports import port_free  # noqa: E402
from admin_lib import ports_nuke  # noqa: E402
from admin_lib.ports_nuke import (  # noqa: E402
    _cwd_in_root,
    _is_project_dev_proc,
    _list_processes_ps,
    _pid_cwd,
    project_dev_holders,
)


def _listener(family, host):
    """A bound, listening socket and its port. Caller closes it."""
    s = socket.socket(family, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    if family == socket.AF_INET6:
        try:
            s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
        except OSError:
            pass
    s.bind((host, 0))
    s.listen(1)
    return s, s.getsockname()[1]


# ---------------------------------------------------------------------------
# port_free — the authoritative free/busy probe
# ---------------------------------------------------------------------------

def test_port_free_true_when_nothing_listening():
    s, port = _listener(socket.AF_INET, "0.0.0.0")
    s.close()  # port is now free
    assert port_free(port) is True
    print("OK: free port → True")


def test_port_free_false_for_wildcard_listener():
    s, port = _listener(socket.AF_INET, "0.0.0.0")
    try:
        assert port_free(port) is False
    finally:
        s.close()
    print("OK: live 0.0.0.0 listener → busy")


def test_port_free_false_for_loopback_listener():
    # The case a wildcard-only probe would miss: SO_REUSEADDR lets a 0.0.0.0
    # bind succeed over a live 127.0.0.1 listener, so port_free must also probe
    # the loopback address or it would falsely report this port free.
    s, port = _listener(socket.AF_INET, "127.0.0.1")
    try:
        assert port_free(port) is False
    finally:
        s.close()
    print("OK: live 127.0.0.1 listener → busy")


def test_port_free_false_for_ipv6_loopback_listener():
    try:
        s, port = _listener(socket.AF_INET6, "::1")
    except OSError:
        pytest.skip("IPv6 not available in this environment")
    try:
        assert port_free(port) is False
    finally:
        s.close()
    print("OK: live ::1 listener → busy")


def test_port_free_true_for_timewait_only():
    # Regression guard for the back-to-back `admin dev` restart: a connection
    # socket left in TIME_WAIT by a just-exited server must read as FREE (the
    # server itself sets SO_REUSEADDR and would rebind fine). Only meaningful on
    # a platform that actually parks the socket in TIME_WAIT — verify that by
    # confirming a no-SO_REUSEADDR bind is refused, else skip.
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", 0))
    listener.listen(1)
    port = listener.getsockname()[1]
    client = socket.create_connection(("127.0.0.1", port))
    conn, _ = listener.accept()
    conn.close()        # server-side active close → local port enters TIME_WAIT
    client.close()
    listener.close()

    probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)  # SO_REUSEADDR off
    try:
        probe.bind(("0.0.0.0", port))
        reproduced = False
    except OSError:
        reproduced = True
    finally:
        probe.close()
    if not reproduced:
        pytest.skip("platform did not park the socket in TIME_WAIT")

    assert port_free(port) is True
    print("OK: TIME_WAIT-only port → free (server could rebind)")


def test_port_free_ipv6_unavailable_does_not_false_busy():
    # If the IPv6 probe fails because the stack is disabled/absent
    # (EADDRNOTAVAIL / EAFNOSUPPORT), that must be read as "skip this family",
    # not "port busy" — otherwise an IPv6-off host reports every port occupied.
    real_socket = socket.socket

    def fake_socket(family, *a, **k):
        if family == socket.AF_INET6:
            raise OSError(errno.EAFNOSUPPORT, "Address family not supported")
        return real_socket(family, *a, **k)

    s, port = _listener(socket.AF_INET, "0.0.0.0")
    s.close()  # IPv4 side is free
    with patch("socket.socket", side_effect=fake_socket):
        assert port_free(port) is True
    print("OK: IPv6 stack error → skipped, not reported busy")


# ---------------------------------------------------------------------------
# _cwd_in_root
# ---------------------------------------------------------------------------

def test_cwd_in_root():
    assert _cwd_in_root("/proj", "/proj") is True
    assert _cwd_in_root("/proj/sub", "/proj") is True
    assert _cwd_in_root("/projsuffix", "/proj") is False  # not a path boundary
    assert _cwd_in_root("/other", "/proj") is False
    assert _cwd_in_root(None, "/proj") is False
    assert _cwd_in_root("", "/proj") is False
    print("OK: _cwd_in_root respects path boundaries and falsy cwd")


# ---------------------------------------------------------------------------
# _is_project_dev_proc — what counts as this project's dev process
# ---------------------------------------------------------------------------

ROOT = "/Users/me/proj"


def _proc(cmdline, argv0=None, argv=None):
    argv = argv if argv is not None else cmdline.split()
    return {
        "cmdline": cmdline,
        "argv0": argv0 if argv0 is not None else (argv[0] if argv else ""),
        "argv": argv,
    }


def test_is_project_dev_proc_trusts_markers():
    assert _is_project_dev_proc(_proc("cargo-watch watch -x run"), ROOT)
    assert _is_project_dev_proc(_proc("node /x/.bin/vite"), ROOT)
    assert _is_project_dev_proc(_proc("target/debug/web-file-manager"), ROOT)
    print("OK: dev markers (cargo-watch, vite, target/debug) match on their own")


def test_is_project_dev_proc_runtime_needs_project_rooted_script():
    # A bare runtime counts only when the script it runs lives in the project.
    assert _is_project_dev_proc(
        _proc(f"node {ROOT}/frontend/node_modules/.bin/foo"), ROOT
    )
    print("OK: node running a project-rooted script → matched")


def test_is_project_dev_proc_excludes_unrelated_runtime():
    # The MCP-server footgun: node/python with cwd inside the project but a
    # script under ~/.npm must NOT be swept up and killed.
    assert not _is_project_dev_proc(
        _proc("node /Users/me/.npm/_npx/abc/node_modules/.bin/playwright-mcp"),
        ROOT,
    )
    assert not _is_project_dev_proc(_proc("ssh me@host"), ROOT)
    print("OK: unrelated runtime (MCP/npx) and non-dev procs → excluded")


# ---------------------------------------------------------------------------
# _list_processes_ps — macOS fallback parsing
# ---------------------------------------------------------------------------

class _Run:
    def __init__(self, stdout):
        self.stdout = stdout


def test_list_processes_ps_parses_and_skips_self():
    out = (
        f"{__import__('os').getpid()} self-process here\n"
        "  4805 target/debug/web-file-manager\n"
        "4668 /Users/me/.cargo/bin/cargo-watch watch -x run\n"
        "\n"  # blank line tolerated
        "notanint garbage\n"
    )
    with patch("admin_lib.ports_nuke.subprocess.run", return_value=_Run(out)):
        procs = list(_list_processes_ps())
    pids = [p["pid"] for p in procs]
    assert __import__("os").getpid() not in pids  # self skipped
    assert pids == [4805, 4668]
    by_pid = {p["pid"]: p for p in procs}
    assert by_pid[4805]["argv0"] == "target/debug/web-file-manager"
    assert by_pid[4668]["argv"][0] == "/Users/me/.cargo/bin/cargo-watch"
    assert by_pid[4805]["cwd"] is None  # ps can't report cwd
    print("OK: ps output parsed; self + non-integer lines skipped; cwd None")


# ---------------------------------------------------------------------------
# _pid_cwd — per-pid cwd via lsof (when /proc is absent)
# ---------------------------------------------------------------------------

def test_pid_cwd_parses_lsof_field_output():
    with (
        patch("os.readlink", side_effect=OSError),  # force the lsof branch
        patch(
            "admin_lib.ports_nuke.subprocess.run",
            return_value=_Run("p4805\nfcwd\nn/Users/me/proj/backend\n"),
        ),
    ):
        assert _pid_cwd(4805) == "/Users/me/proj/backend"
    print("OK: _pid_cwd extracts the n<path> field from lsof -F output")


def test_pid_cwd_timeout_returns_none():
    import subprocess as sp
    with (
        patch("os.readlink", side_effect=OSError),
        patch("admin_lib.ports_nuke.subprocess.run",
              side_effect=sp.TimeoutExpired("lsof", 2.0)),
    ):
        assert _pid_cwd(4805) is None
    print("OK: _pid_cwd returns None on lsof timeout")


# ---------------------------------------------------------------------------
# project_dev_holders — integration of the above on the lsof-hang fallback path
# ---------------------------------------------------------------------------

def test_project_dev_holders_scopes_to_project_and_excludes_mcp():
    procs = [
        _proc("target/debug/web-file-manager") | {"pid": 4805, "cwd": None},
        _proc("cargo-watch watch -x run") | {"pid": 4668, "cwd": None},
        # MCP server: node, cwd happens to be in project, but script is in ~/.npm
        _proc("node /Users/me/.npm/_npx/abc/.bin/playwright-mcp")
        | {"pid": 42738, "cwd": None},
        # A dev proc rooted in a DIFFERENT project — must be scoped out by cwd.
        _proc("cargo-watch watch -x run") | {"pid": 9001, "cwd": None},
    ]
    cwds = {4805: f"{ROOT}/backend", 4668: ROOT, 42738: ROOT, 9001: "/other/proj"}

    with (
        patch("admin_lib.ports_nuke.list_processes", return_value=iter(procs)),
        patch("admin_lib.ports_nuke._pid_cwd", side_effect=lambda pid, **k: cwds[pid]),
        patch("admin_lib.ports_nuke._ps_parent", return_value=(None, None)),
    ):
        holders = project_dev_holders(ROOT)

    pids = sorted(h["pid"] for h in holders)
    assert pids == [4668, 4805]  # MCP excluded by argv, other-project by cwd
    print("OK: project_dev_holders keeps in-project dev procs, drops MCP + other-project")
