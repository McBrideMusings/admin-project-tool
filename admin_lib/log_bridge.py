"""Lifecycle for the per-process log-bridge HTTP server.

The generator emits `configure_log_bridge(...)` at admin startup with the
manifest's [log_bridge] table. Long-running action supervisors call
`start_log_bridge(log_path, project)` before spawning the wrapped process
and `stop_log_bridge()` in their finally block. Multiple admin processes
coexist by each grabbing a different port in LOG_BRIDGE_PORT_RANGE.
"""
from __future__ import annotations

import atexit
import json
import os
import socket
import datetime
from pathlib import Path

from .log_server import set_log_server_state, start_log_server
from .ports import _pid_alive

LOG_BRIDGE_PORT_RANGE_DEFAULT = range(9988, 10000)
LOG_BRIDGE_DISCOVERY_DIR = Path("/tmp/admin-project-tool")
LOG_BRIDGE_BIND_HOST = "0.0.0.0"

_LOG_BRIDGE_CFG: dict = {}
_LOG_BRIDGE_SERVER = None
_LOG_BRIDGE_DISCOVERY_PATH: Path | None = None


def configure_log_bridge(cfg: dict | None):
    global _LOG_BRIDGE_CFG
    _LOG_BRIDGE_CFG = dict(cfg) if cfg else {}


def log_bridge_enabled() -> bool:
    return bool(_LOG_BRIDGE_CFG.get("hosts"))


def _log_bridge_allocate_port() -> int:
    pr = _LOG_BRIDGE_CFG.get("port_range")
    port_range = range(pr[0], pr[1] + 1) if pr else LOG_BRIDGE_PORT_RANGE_DEFAULT
    for port in port_range:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            try:
                s.bind((LOG_BRIDGE_BIND_HOST, port))
            except OSError:
                continue
            return port
    raise RuntimeError(
        f"log_bridge: no free port in {port_range.start}-{port_range.stop - 1}"
    )


def _log_bridge_sweep_stale():
    if not LOG_BRIDGE_DISCOVERY_DIR.exists():
        return
    for path in LOG_BRIDGE_DISCOVERY_DIR.glob("*.json"):
        stale = False
        try:
            data = json.loads(path.read_text())
            pid = int(data.get("pid", 0))
        except (OSError, ValueError, json.JSONDecodeError):
            stale = True
        else:
            if not _pid_alive(pid):
                stale = True
        if stale:
            try:
                path.unlink()
            except OSError:
                pass


def start_log_bridge(log_path: str):
    global _LOG_BRIDGE_SERVER, _LOG_BRIDGE_DISCOVERY_PATH
    if not log_bridge_enabled() or _LOG_BRIDGE_SERVER is not None:
        return
    hosts = _LOG_BRIDGE_CFG["hosts"]
    # Per-listener override: a dispatch can narrow the advertised hosts for THIS
    # process (comma-separated) so two bridges in the same project isolate by host
    # (e.g. one advertising the live host, another advertising localhost). The
    # userscript routes a page only to a listener whose advertised hosts match the
    # page hostname, so distinct host sets => non-intermingling streams.
    override = os.environ.get("ADMIN_LOG_BRIDGE_HOSTS", "").strip()
    if override:
        hosts = [h.strip() for h in override.split(",") if h.strip()]
    project = _LOG_BRIDGE_CFG.get("project", "")
    LOG_BRIDGE_DISCOVERY_DIR.mkdir(parents=True, exist_ok=True)
    _log_bridge_sweep_stale()
    port = _log_bridge_allocate_port()
    started_at = datetime.datetime.now(datetime.timezone.utc).isoformat()
    pid = os.getpid()
    set_log_server_state(
        log_path=log_path,
        hosts=hosts,
        project=project,
        pid=pid,
        started_at=started_at,
        port=port,
    )
    _LOG_BRIDGE_SERVER = start_log_server(LOG_BRIDGE_BIND_HOST, port)
    print(f"[log-bridge] listening on port {port}  hosts={hosts}", flush=True)
    _LOG_BRIDGE_DISCOVERY_PATH = LOG_BRIDGE_DISCOVERY_DIR / f"{pid}.json"
    _LOG_BRIDGE_DISCOVERY_PATH.write_text(json.dumps({
        "port": port,
        "hosts": hosts,
        "project": project,
        "pid": pid,
        "started_at": started_at,
        "log_path": log_path,
    }))
    atexit.register(stop_log_bridge)


def stop_log_bridge():
    global _LOG_BRIDGE_SERVER, _LOG_BRIDGE_DISCOVERY_PATH
    if _LOG_BRIDGE_SERVER is not None:
        try:
            _LOG_BRIDGE_SERVER.shutdown()
            _LOG_BRIDGE_SERVER.server_close()
        except Exception:
            pass
        _LOG_BRIDGE_SERVER = None
    if _LOG_BRIDGE_DISCOVERY_PATH is not None:
        try:
            _LOG_BRIDGE_DISCOVERY_PATH.unlink()
        except OSError:
            pass
        _LOG_BRIDGE_DISCOVERY_PATH = None
