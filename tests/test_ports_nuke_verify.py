"""Tests for the verify pass in nuke_dev_processes (muxarr #476).

`admin kill` promises free ports, and used to report success purely on the
strength of having killed the pids its pidfiles named. Observed on muxarr:
it printed "dev server stopped (6 processes)" for pids 2388/5528 while 3300 and
3350 were still held by 5473/5039 — a respawn loop had re-bound them under new
pids between the port pass and the end of the sweep. The next `admin dev` then
died on "Port 3350 is already in use", pointing at the wrong thing.

The port here is held by the test process itself, which `_kill_set` skips
(it never kills its own pid) — so the kill passes are guaranteed no-ops and
the only thing that can notice the port is still bound is the verify pass.
"""
from __future__ import annotations

import socket
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.ports_nuke import nuke_dev_processes  # noqa: E402


def _bind_free_port():
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", 0))
    s.listen(1)
    return s, s.getsockname()[1]


def test_still_held_port_is_reported(tmp_path):
    sock, port = _bind_free_port()
    try:
        still_held = nuke_dev_processes(str(tmp_path), ports=[port])
        assert still_held == [port], (
            f"port {port} is bound by this process and cannot be killed — "
            f"the verify pass must report it, got {still_held!r}"
        )
    finally:
        sock.close()


def test_free_port_reports_nothing_held(tmp_path):
    sock, port = _bind_free_port()
    sock.close()  # port is now free
    assert nuke_dev_processes(str(tmp_path), ports=[port]) == []
