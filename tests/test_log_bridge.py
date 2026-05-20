"""Tests for admin_lib.log_bridge."""
from __future__ import annotations

import json
import os
import socket
from pathlib import Path

import pytest

from admin_lib import log_bridge


@pytest.fixture
def isolated_discovery_dir(tmp_path, monkeypatch):
    """Point DISCOVERY_DIR at a tmp dir so tests don't litter /tmp."""
    monkeypatch.setattr(log_bridge, "LOG_BRIDGE_DISCOVERY_DIR", tmp_path / "discovery")
    yield tmp_path / "discovery"


@pytest.fixture(autouse=True)
def _reset_state():
    log_bridge.configure_log_bridge(None)
    log_bridge.stop_log_bridge()
    yield
    log_bridge.stop_log_bridge()


def test_configure_stores_cfg_and_is_enabled():
    log_bridge.configure_log_bridge({"hosts": ["localhost"]})
    assert log_bridge.log_bridge_enabled()


def test_configure_with_no_hosts_is_disabled():
    log_bridge.configure_log_bridge({})
    assert not log_bridge.log_bridge_enabled()


def test_configure_with_none_clears():
    log_bridge.configure_log_bridge({"hosts": ["x"]})
    log_bridge.configure_log_bridge(None)
    assert not log_bridge.log_bridge_enabled()


def test_allocate_port_returns_port_in_range():
    port = log_bridge._log_bridge_allocate_port()
    assert port in log_bridge.LOG_BRIDGE_PORT_RANGE_DEFAULT


def test_allocate_port_raises_when_range_exhausted(monkeypatch):
    sockets = []
    for port in log_bridge.LOG_BRIDGE_PORT_RANGE_DEFAULT:
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            s.bind((log_bridge.LOG_BRIDGE_BIND_HOST, port))
            sockets.append(s)
        except OSError:
            s.close()
    try:
        with pytest.raises(RuntimeError, match="no free port"):
            log_bridge._log_bridge_allocate_port()
    finally:
        for s in sockets:
            s.close()


def test_sweep_stale_removes_files_for_dead_pids(isolated_discovery_dir):
    isolated_discovery_dir.mkdir(parents=True)
    # PID 1 is init — always alive, never removed.
    alive_file = isolated_discovery_dir / "1.json"
    alive_file.write_text(json.dumps({"pid": 1}))
    # An obviously-impossible PID.
    dead_pid = 999999
    dead_file = isolated_discovery_dir / f"{dead_pid}.json"
    dead_file.write_text(json.dumps({"pid": dead_pid}))
    # A malformed file is also removed.
    junk_file = isolated_discovery_dir / "junk.json"
    junk_file.write_text("not json")

    log_bridge._log_bridge_sweep_stale()

    assert alive_file.exists()
    assert not dead_file.exists()
    assert not junk_file.exists()


def test_start_and_stop_lifecycle(tmp_path, isolated_discovery_dir):
    log_file = tmp_path / "session.log"
    log_bridge.configure_log_bridge({
        "hosts": ["localhost"],
        "project": "demo",
    })

    log_bridge.start_log_bridge(str(log_file))

    # Server is up.
    assert log_bridge._LOG_BRIDGE_SERVER is not None
    # Discovery file written with the right shape.
    pid_file = isolated_discovery_dir / f"{os.getpid()}.json"
    assert pid_file.exists()
    payload = json.loads(pid_file.read_text())
    assert payload["pid"] == os.getpid()
    assert payload["hosts"] == ["localhost"]
    assert payload["project"] == "demo"
    assert payload["log_path"] == str(log_file)
    assert payload["port"] in log_bridge.LOG_BRIDGE_PORT_RANGE_DEFAULT

    log_bridge.stop_log_bridge()

    assert log_bridge._LOG_BRIDGE_SERVER is None
    assert not pid_file.exists()


def test_start_is_noop_when_not_configured(tmp_path, isolated_discovery_dir):
    log_bridge.configure_log_bridge(None)
    log_bridge.start_log_bridge(str(tmp_path / "x.log"))
    assert log_bridge._LOG_BRIDGE_SERVER is None
    assert not (isolated_discovery_dir / f"{os.getpid()}.json").exists()


def test_start_is_idempotent(tmp_path, isolated_discovery_dir):
    log_bridge.configure_log_bridge({"hosts": ["localhost"]})
    log_bridge.start_log_bridge(str(tmp_path / "x.log"))
    first_server = log_bridge._LOG_BRIDGE_SERVER
    log_bridge.start_log_bridge(str(tmp_path / "y.log"))
    assert log_bridge._LOG_BRIDGE_SERVER is first_server
