"""Tests for admin_lib.log_server."""
from __future__ import annotations

import json
import urllib.request
from pathlib import Path

import pytest

from admin_lib import log_server


@pytest.fixture(autouse=True)
def _reset_state():
    """Reset module-level state between tests so they don't leak."""
    log_server.set_log_server_state(
        log_path=None, hosts=[], project="", pid=0, started_at="", port=0,
    )
    log_server._SEEN_CLIENTS.clear()
    log_server.set_log_server_sink(None)
    yield
    log_server.set_log_server_sink(None)


def test_sink_receives_lines_and_suppresses_stdout(tmp_path, capsys):
    log_file = tmp_path / "session.log"
    log_server.set_log_server_state(
        log_path=str(log_file), hosts=["x"], project="p", pid=1, started_at="t", port=9988,
    )
    received = []
    log_server.set_log_server_sink(received.append)
    written = log_server.append_log_bridge_entries(
        [{"message": "hi", "level": "info", "host": "app.example.net"}], user_agent="UA/1"
    )
    assert written == 1
    # Sink got the banner line + the entry line; nothing went to raw stdout.
    assert any("client connected" in ln for ln in received)
    assert any("[client] [info] hi" in ln for ln in received)
    assert capsys.readouterr().out == ""
    # File append still happens regardless of the sink.
    assert "[client] [info] hi" in log_file.read_text()


def test_format_entry_includes_timestamp_level_message():
    out = log_server.format_log_bridge_entry({
        "timestamp": "2026-05-19T14:23:01.234Z",
        "level": "WARN",
        "message": "hello",
    })
    assert out == "2026-05-19T14:23:01.234Z [client] [warn] hello\n"


def test_format_entry_synthesizes_timestamp_when_missing():
    out = log_server.format_log_bridge_entry({"message": "x"})
    assert " [client] [log] x\n" in out


def test_append_entries_noop_when_log_path_unset():
    written = log_server.append_log_bridge_entries([{"message": "x"}])
    assert written == 0


def test_append_entries_writes_lines_to_log_path(tmp_path):
    log_file = tmp_path / "session.log"
    log_server.set_log_server_state(
        log_path=str(log_file), hosts=["localhost"], project="p",
        pid=1, started_at="t", port=9988,
    )
    written = log_server.append_log_bridge_entries([
        {"message": "first", "level": "info"},
        {"message": "second", "level": "error"},
    ])
    assert written == 2
    contents = log_file.read_text().splitlines()
    assert len(contents) == 2
    assert "[client] [info] first" in contents[0]
    assert "[client] [error] second" in contents[1]


def test_append_entries_appends_to_existing_log(tmp_path):
    log_file = tmp_path / "session.log"
    log_file.write_text("preexisting line\n")
    log_server.set_log_server_state(
        log_path=str(log_file), hosts=[], project="", pid=0, started_at="", port=0,
    )
    log_server.append_log_bridge_entries([{"message": "added"}])
    lines = log_file.read_text().splitlines()
    assert lines[0] == "preexisting line"
    assert "[client] [log] added" in lines[1]


def test_connect_banner_prints_host_and_browser_once(tmp_path):
    log_file = tmp_path / "session.log"
    log_server.set_log_server_state(
        log_path=str(log_file), hosts=["x"], project="p", pid=1, started_at="t", port=9988,
    )
    entry = {"message": "m", "level": "info", "host": "app.example.net"}
    log_server.append_log_bridge_entries([entry], user_agent="UA/1")
    log_server.append_log_bridge_entries([entry], user_agent="UA/1")
    banners = [ln for ln in log_file.read_text().splitlines() if "client connected" in ln]
    assert len(banners) == 1
    assert "app.example.net" in banners[0]
    assert "UA/1" in banners[0]


def test_connect_banner_falls_back_to_url_host_for_legacy_clients(tmp_path):
    log_file = tmp_path / "session.log"
    log_server.set_log_server_state(
        log_path=str(log_file), hosts=["x"], project="p", pid=1, started_at="t", port=9988,
    )
    log_server.append_log_bridge_entries(
        [{"message": "m", "url": "https://legacy.example.net/game?token=abc"}], user_agent="UA/2"
    )
    banners = [ln for ln in log_file.read_text().splitlines() if "client connected" in ln]
    assert "legacy.example.net" in banners[0]


# -- HTTP server roundtrip ----------------------------------------------------

@pytest.fixture
def running_server(tmp_path):
    log_file = tmp_path / "session.log"
    log_server.set_log_server_state(
        log_path=str(log_file),
        hosts=["localhost", "*.dev.example.internal"],
        project="test-project",
        pid=12345,
        started_at="2026-05-19T00:00:00Z",
        port=0,  # will be replaced
    )
    server = log_server.start_log_server("127.0.0.1", 0)
    actual_port = server.server_address[1]
    log_server.set_log_server_state(
        log_path=str(log_file),
        hosts=["localhost", "*.dev.example.internal"],
        project="test-project",
        pid=12345,
        started_at="2026-05-19T00:00:00Z",
        port=actual_port,
    )
    yield server, actual_port, log_file
    server.shutdown()
    server.server_close()


def test_health_endpoint_returns_state(running_server):
    _server, port, _log_file = running_server
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/health", timeout=2) as resp:
        assert resp.status == 200
        body = json.loads(resp.read())
    assert body["port"] == port
    assert body["hosts"] == ["localhost", "*.dev.example.internal"]
    assert body["project"] == "test-project"
    assert body["pid"] == 12345


def test_log_endpoint_appends_to_log_file(running_server):
    _server, port, log_file = running_server
    payload = json.dumps([
        {"timestamp": "T1", "level": "info", "message": "m1", "url": "u1"},
        {"timestamp": "T2", "level": "error", "message": "m2", "url": "u2"},
    ]).encode("utf-8")
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}/log",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=2) as resp:
        body = json.loads(resp.read())
    assert body == {"written": 2}
    # The first POST from a new client also emits a one-time `client connected`
    # banner line ahead of the entries — filter it out before indexing entries.
    entry_lines = [ln for ln in log_file.read_text().splitlines() if "[client] [" in ln]
    assert "[client] [info] m1" in entry_lines[0]
    assert "[client] [error] m2" in entry_lines[1]


def test_log_endpoint_rejects_non_array(running_server):
    _server, port, _log_file = running_server
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}/log",
        data=b'{"not": "an array"}',
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with pytest.raises(urllib.error.HTTPError) as exc:
        urllib.request.urlopen(req, timeout=2)
    assert exc.value.code == 400


def test_unknown_path_returns_404(running_server):
    _server, port, _log_file = running_server
    with pytest.raises(urllib.error.HTTPError) as exc:
        urllib.request.urlopen(f"http://127.0.0.1:{port}/something-else", timeout=2)
    assert exc.value.code == 404
