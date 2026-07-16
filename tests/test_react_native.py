"""Tests for the react-native archetype, detector, and Metro lifecycle.

Metro itself is never started here — ensure_metro/stop_metro are exercised
against a stub HTTP server and monkeypatched Popen, so the suite stays offline
and does not depend on npx or a node toolchain.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

try:
    import tomllib
except ModuleNotFoundError:
    import tomli as tomllib  # type: ignore[no-redef]

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import android, metro  # noqa: E402
from admin_lib.archetypes import load_archetype, resolve  # noqa: E402
from admin_lib.detectors import detect  # noqa: E402
from admin_lib.manifest import parse  # noqa: E402

ARCH_DIR = REPO / "archetypes"
DET_DIR = REPO / "detectors"


# ---------------------------------------------------------------------------
# metro.metro_running
# ---------------------------------------------------------------------------

class _StatusHandler(BaseHTTPRequestHandler):
    body = b"packager-status:running"

    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(self.body)

    def log_message(self, *a):
        pass


def _serve(body):
    handler = type("H", (_StatusHandler,), {"body": body})
    srv = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def test_metro_running_true_on_packager_status():
    srv = _serve(b"packager-status:running")
    try:
        assert metro.metro_running(srv.server_address[1]) is True
    finally:
        srv.shutdown()


def test_metro_running_false_for_other_service_on_port():
    # A stray server holding the port is not a bundler. Treating it as one
    # would surface much later as an unexplained red screen on the device.
    srv = _serve(b"<!doctype html><title>vite</title>")
    try:
        assert metro.metro_running(srv.server_address[1]) is False
    finally:
        srv.shutdown()


def test_metro_running_false_when_nothing_listening():
    assert metro.metro_running(1) is False


def test_ensure_metro_noop_when_already_running(monkeypatch):
    srv = _serve(b"packager-status:running")
    port = srv.server_address[1]
    started = []
    monkeypatch.setattr(metro.subprocess, "Popen", lambda *a, **k: started.append(a))
    try:
        assert metro.ensure_metro(".", port=port) is False
        assert started == []
    finally:
        srv.shutdown()


def test_ensure_metro_raises_when_bundler_exits_immediately(monkeypatch):
    class _Dead:
        pid = 4242
        returncode = 1

        def poll(self):
            return 1

    monkeypatch.setattr(metro.subprocess, "Popen", lambda *a, **k: _Dead())
    with pytest.raises(RuntimeError, match="exited immediately"):
        metro.ensure_metro(".", port=1, timeout=5.0)


def test_ensure_metro_raises_on_timeout(monkeypatch):
    class _Hung:
        pid = 4243
        returncode = None

        def poll(self):
            return None

    monkeypatch.setattr(metro.subprocess, "Popen", lambda *a, **k: _Hung())
    with pytest.raises(RuntimeError, match="did not answer"):
        metro.ensure_metro(".", port=1, timeout=0.5)


def test_stop_metro_false_and_clears_pid_when_port_is_dead(tmp_path):
    pf = tmp_path / "metro.pid"
    pf.write_text("999999")          # stale: nothing listening, pid long gone
    assert metro.stop_metro(str(pf), port=1) is False
    assert not pf.exists()


def test_stop_metro_refuses_to_signal_a_bundler_it_did_not_start(tmp_path):
    # Port answering + no pid file. Signalling anything here would mean
    # trusting a pid we never recorded — or killing someone else's bundler.
    srv = _serve(b"packager-status:running")
    try:
        assert metro.stop_metro(str(tmp_path / "absent.pid"),
                                port=srv.server_address[1]) is False
    finally:
        srv.shutdown()


def test_stop_metro_reports_false_when_port_survives_sigterm(tmp_path, monkeypatch):
    # Returning True here would tell `down` the port was freed when it wasn't.
    srv = _serve(b"packager-status:running")
    pf = tmp_path / "metro.pid"
    pf.write_text(str(os.getpid()))
    monkeypatch.setattr(metro.os, "killpg", lambda *a: None)
    monkeypatch.setattr(metro.time, "monotonic", _clock())
    try:
        assert metro.stop_metro(str(pf), port=srv.server_address[1]) is False
    finally:
        srv.shutdown()


def _clock():
    t = [0.0]

    def _tick():
        t[0] += 1.0
        return t[0]
    return _tick


def test_ensure_metro_reports_missing_npx_as_runtime_error(monkeypatch):
    def _no_npx(*a, **k):
        raise FileNotFoundError("npx")
    monkeypatch.setattr(metro.subprocess, "Popen", _no_npx)
    with pytest.raises(RuntimeError, match="could not start Metro"):
        metro.ensure_metro(".", port=1)


# ---------------------------------------------------------------------------
# detector
# ---------------------------------------------------------------------------

def _rn_project(root: Path, app_rel: str, native="ios"):
    app = root / app_rel if app_rel != "." else root
    app.mkdir(parents=True, exist_ok=True)
    (app / "package.json").write_text(json.dumps({"dependencies": {"react-native": "0.76.0"}}))
    (app / native).mkdir(exist_ok=True)
    return app


def test_detector_finds_rn_app_at_root():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _rn_project(root, ".")
        out = detect(root, DET_DIR)
        assert "react-native" in out["archetypes"]
        assert out["react_native"] == {"app_dir": "."}


def test_detector_finds_rn_app_in_workspace_subdir():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _rn_project(root, "apps/mobile-rn")
        out = detect(root, DET_DIR)
        assert "react-native" in out["archetypes"]
        assert out["react_native"] == {"app_dir": "apps/mobile-rn"}


def test_detector_ignores_react_native_dep_without_native_dir():
    # react-native-web pulls the dep into a plain web package; no Metro needed.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        pkg = root / "apps" / "web"
        pkg.mkdir(parents=True)
        (pkg / "package.json").write_text(json.dumps({"dependencies": {"react-native": "0.76.0"}}))
        out = detect(root, DET_DIR)
        assert "react-native" not in out["archetypes"]


def test_detector_ignores_non_rn_package():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "package.json").write_text(json.dumps({"dependencies": {"react": "18.0.0"}}))
        (root / "ios").mkdir()
        out = detect(root, DET_DIR)
        assert "react-native" not in out["archetypes"]


def test_detector_ignores_node_modules():
    # Half the RN ecosystem declares react-native in devDependencies and ships
    # an ios/ dir, so a dependency passes every test a real app does — and
    # sorted order puts node_modules/ ahead of packages/.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        dep = root / "node_modules" / "react-native-screens"
        dep.mkdir(parents=True)
        (dep / "package.json").write_text(json.dumps({"devDependencies": {"react-native": "0.76.0"}}))
        (dep / "ios").mkdir()
        _rn_project(root, "packages/mobile")
        out = detect(root, DET_DIR)
        assert out["react_native"] == {"app_dir": "packages/mobile"}


def test_detector_ignores_dot_dirs():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        hidden = root / ".yarn" / "cache-app"
        hidden.mkdir(parents=True)
        (hidden / "package.json").write_text(json.dumps({"dependencies": {"react-native": "0.76.0"}}))
        (hidden / "ios").mkdir()
        out = detect(root, DET_DIR)
        assert "react-native" not in out["archetypes"]


def test_detector_tables_survive_the_merger():
    # The merger only carried archetypes/urls/project_name before; a dropped
    # react_native table would leave app_dir for the user to hand-fill.
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        _rn_project(root, "apps/mobile-rn")
        assert detect(root, DET_DIR).get("react_native", {}).get("app_dir") == "apps/mobile-rn"


# ---------------------------------------------------------------------------
# android device resolution
# ---------------------------------------------------------------------------

def _fake_adb(monkeypatch, stdout):
    class _Out:
        def __init__(self):
            self.stdout = stdout
    monkeypatch.setattr(android.subprocess, "run", lambda *a, **k: _Out())


def test_resolve_android_device_picks_the_only_one(monkeypatch):
    _fake_adb(monkeypatch, "List of devices attached\nemulator-5554\tdevice\n")
    assert android.resolve_android_device() == "emulator-5554"


def test_resolve_android_device_none_attached(monkeypatch):
    _fake_adb(monkeypatch, "List of devices attached\n")
    assert android.resolve_android_device() is None


def test_resolve_android_device_ambiguous_needs_a_pin(monkeypatch):
    _fake_adb(monkeypatch, "List of devices attached\nemulator-5554\tdevice\nR58M\tdevice\n")
    assert android.resolve_android_device() is None


def test_resolve_android_device_reports_unauthorized(monkeypatch):
    # Not the same as "none attached" — it's a phone waiting on the USB-debugging
    # prompt, and saying "no devices" would send the user after the wrong thing.
    _fake_adb(monkeypatch, "List of devices attached\nR58M\tunauthorized\n")
    warnings = []
    monkeypatch.setattr(android, "warn", lambda m: warnings.append(m))
    assert android.resolve_android_device() is None
    assert any("unauthorized" in w for w in warnings)


def test_resolve_android_device_honors_explicit_serial(monkeypatch):
    _fake_adb(monkeypatch, "List of devices attached\nemulator-5554\tdevice\nR58M\tdevice\n")
    assert android.resolve_android_device("R58M") == "R58M"


def test_resolve_android_device_rejects_offline_explicit_serial(monkeypatch):
    _fake_adb(monkeypatch, "List of devices attached\nR58M\toffline\n")
    assert android.resolve_android_device("R58M") is None


def test_adb_devices_survives_missing_adb(monkeypatch):
    def _boom(*a, **k):
        raise OSError("adb not found")
    monkeypatch.setattr(android.subprocess, "run", _boom)
    assert android.adb_devices() == []


def test_dev_android_requires_a_package(monkeypatch, tmp_path):
    (tmp_path / "app").mkdir()
    from admin_lib import react_native
    with pytest.raises(SystemExit):
        react_native.dev_android({"app_dir": str(tmp_path / "app")})


# ---------------------------------------------------------------------------
# streams — the shared pump apple + android both sit on
# ---------------------------------------------------------------------------

def test_stream_subprocess_tees_to_terminal_and_file(tmp_path, capsys):
    from admin_lib import streams
    proc = subprocess.Popen(
        [sys.executable, "-c", "print('alpha'); print('beta')"],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        bufsize=1, universal_newlines=True,
    )
    log = tmp_path / "out.log"
    thread, fh = streams.stream_subprocess(proc, log_file=str(log))
    proc.wait()
    thread.join(timeout=5)
    fh.close()
    assert "alpha" in capsys.readouterr().out
    assert "beta" in log.read_text()


def test_stream_subprocess_applies_formatter(tmp_path):
    from admin_lib import streams
    proc = subprocess.Popen(
        [sys.executable, "-c", "print('raw')"],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        bufsize=1, universal_newlines=True,
    )
    log = tmp_path / "out.log"
    thread, fh = streams.stream_subprocess(
        proc, log_file=str(log), formatter=lambda line: "X " + line)
    proc.wait()
    thread.join(timeout=5)
    fh.close()
    assert log.read_text().startswith("X raw")


def test_stream_subprocess_without_log_file(capsys):
    from admin_lib import streams
    proc = subprocess.Popen(
        [sys.executable, "-c", "print('only-terminal')"],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        bufsize=1, universal_newlines=True,
    )
    thread, fh = streams.stream_subprocess(proc)
    proc.wait()
    thread.join(timeout=5)
    assert fh is None
    assert "only-terminal" in capsys.readouterr().out


def test_stop_stream_terminates_and_deregisters():
    from admin_lib import core, streams
    proc = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"])
    core._children.append(proc)
    streams.stop_stream(proc)
    assert proc.poll() is not None
    assert proc not in core._children


# ---------------------------------------------------------------------------
# archetype
# ---------------------------------------------------------------------------

def test_archetype_loads_and_declares_metro_modules():
    arch = load_archetype(ARCH_DIR, "react-native")
    assert arch.name == "react-native"
    for mod in ("apple", "metro", "react_native"):
        assert mod in arch.modules


def test_archetype_dev_is_pure_routing():
    arch = load_archetype(ARCH_DIR, "react-native")
    assert set(arch.commands["dev"]) <= {"desc", "steps", "group", "priority"}
    assert arch.commands["dev"]["steps"] == ["dev"]


def test_react_native_table_parses_and_resolves():
    toml = (
        'project_name = "RN"\n'
        'archetypes = ["react-native"]\n'
        '[react_native]\n'
        'app_dir = "apps/mobile-rn"\n'
        'metro_port = 8099\n'
        '[apple]\n'
        'project_path = "apps/mobile-rn/ios/App.xcworkspace"\n'
        'ios_scheme = "App"\n'
    )
    m = parse(tomllib.loads(toml))
    assert m.react_native == {"app_dir": "apps/mobile-rn", "metro_port": 8099}
    r = resolve(m, ARCH_DIR)
    assert "dev" in r.commands


def test_react_native_table_must_be_a_table():
    with pytest.raises(Exception, match="react_native"):
        parse(tomllib.loads('project_name = "RN"\nreact_native = "nope"\n'))
