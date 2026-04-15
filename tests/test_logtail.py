"""Integration tests for logtail's follow loop: append, rotation, truncation.

Loads admin_lib/logtail.py via exec (same workaround as test_seek_last_n_lines
in test_archetypes.py — relative imports can't resolve outside a package).
A stub `sys` is injected into the loaded namespace so tail_local writes go to
an in-memory sink instead of the real stdout, which keeps test assertions and
daemon-thread output cleanly separated.
"""
from __future__ import annotations

import io
import os
import re as _re
import tempfile
import threading
import time
from pathlib import Path

LIB_DIR = Path(__file__).resolve().parent.parent / "admin_lib"


class _Sink:
    def __init__(self):
        self.buffer = io.BytesIO()
        self._lock = threading.Lock()

    def flush(self):
        pass

    def snapshot(self) -> bytes:
        with self._lock:
            return self.buffer.getvalue()


class _BufferedWriter:
    """Wraps a BytesIO with a lock so test assertions can read concurrently."""
    def __init__(self, sink: _Sink):
        self._sink = sink

    def write(self, data: bytes) -> int:
        with self._sink._lock:
            return self._sink.buffer.write(data)


class _StubStdout:
    """Minimal stdout stub with locked buffer."""
    def __init__(self, sink: _Sink):
        self.buffer = _BufferedWriter(sink)

    def flush(self):
        pass


class _StubSys:
    def __init__(self, sink: _Sink):
        self.stdout = _StubStdout(sink)


def _load_logtail():
    source = (LIB_DIR / "logtail.py").read_text()
    source = _re.sub(r"from \.core import \([^)]*\)", "pass", source, count=1)
    ns: dict = {
        "info": lambda *a, **k: None,
        "warn": lambda *a, **k: None,
        "err": lambda *a, **k: None,
        "command": lambda *a, **k: (lambda f: f),
        "pick_target_or_none": lambda *a, **k: None,
        "resolve_env": lambda s: s,
        "_children": [],
    }
    exec(compile(source, "logtail.py", "exec"), ns)
    return ns


def _spawn_tailer(ns, path, **kwargs):
    sink = _Sink()
    ns["sys"] = _StubSys(sink)
    kwargs.setdefault("poll_interval", 0.01)
    ready = threading.Event()

    def runner():
        ready.set()
        ns["tail_local"](path, **kwargs)

    t = threading.Thread(target=runner, daemon=True)
    t.start()
    ready.wait()
    # Give tail_local a few poll_interval cycles to reach its follow loop
    # (past seek-to-EOF) so writes done by the caller afterward are seen as
    # appends rather than initial content.
    time.sleep(kwargs["poll_interval"] * 5)
    return sink


def _wait_for(sink: _Sink, needle: bytes, timeout: float = 3.0) -> bytes:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        snap = sink.snapshot()
        if needle in snap:
            return snap
        time.sleep(0.02)
    return sink.snapshot()


def test_append_during_follow():
    ns = _load_logtail()
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "log")
        with open(path, "w") as f:
            f.write("first\n")
        sink = _spawn_tailer(ns, path, follow=True)
        with open(path, "a") as f:
            f.write("second\n")
        with open(path, "a") as f:
            f.write("third\n")
        out = _wait_for(sink, b"third")
        assert b"second" in out, out
        assert b"third" in out, out
        assert b"first" not in out, "default follow should start at EOF"
        print("OK: append during follow streams new lines")


def test_rotation_reopens():
    ns = _load_logtail()
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "log")
        with open(path, "w") as f:
            f.write("before\n")
        sink = _spawn_tailer(ns, path, follow=True)
        os.rename(path, path + ".1")
        with open(path, "w") as f:
            f.write("after-rotate\n")
        out = _wait_for(sink, b"after-rotate")
        assert b"after-rotate" in out, out
        print("OK: rotation reopens on inode change")


def test_truncation_reopens():
    ns = _load_logtail()
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "log")
        with open(path, "w") as f:
            f.write("padding-line-one\npadding-line-two\n")
        sink = _spawn_tailer(ns, path, follow=True)
        with open(path, "w") as f:
            pass
        with open(path, "a") as f:
            f.write("after-truncate\n")
        out = _wait_for(sink, b"after-truncate")
        assert b"after-truncate" in out, out
        print("OK: truncation reopens on size shrink")


def _load_logtail_for_cmd(
    *,
    pick_result=None,
    tail_local_rc=0,
    tail_remote_rc=0,
):
    """Load logtail with a richer stub namespace for testing cmd_logs.

    Captures `@command(...)`-decorated functions in registry["cmd_logs"], and
    lets the caller control what `pick_target_or_none` returns (use None to
    simulate non-TTY or user-cancel) and what tail_local/tail_remote return.
    The `err` stub records messages for assertions.
    """
    source = (LIB_DIR / "logtail.py").read_text()
    source = _re.sub(r"from \.core import \([^)]*\)", "pass", source, count=1)

    registry: dict = {}
    err_calls: list = []

    def _capture_command(name, _desc):
        def _decorator(func):
            registry[name] = func
            return func
        return _decorator

    def _err(msg, *a, **k):
        err_calls.append(msg)

    def _pick(menu, prompt, non_tty_msg):
        # Mirror pick_target_or_none's real non-TTY behavior: emit the err
        # message and return None. Callers override pick_result when they
        # want to simulate a successful interactive pick.
        if pick_result is None:
            _err(non_tty_msg)
            return None
        return pick_result

    ns: dict = {
        "info": lambda *a, **k: None,
        "warn": lambda *a, **k: None,
        "err": _err,
        "command": _capture_command,
        "pick_target_or_none": _pick,
        "resolve_env": lambda s: s,
        "_children": [],
    }
    exec(compile(source, "logtail.py", "exec"), ns)
    # Replace tail_local / tail_remote with inert stubs so cmd_logs doesn't
    # open real files or spawn ssh.
    ns["tail_local"] = lambda *a, **k: tail_local_rc
    ns["tail_remote"] = lambda *a, **k: tail_remote_rc
    return ns, registry, err_calls


def _spec_two_targets():
    return {
        "app": {
            "default_env": None,
            "envs": {
                "dev": {"path": "/tmp/app-dev.log", "host": None, "user": None},
                "prod": {"path": "/tmp/app-prod.log", "host": None, "user": None},
            },
        },
        "api": {
            "default_env": None,
            "envs": {
                "dev": {"path": "/tmp/api-dev.log", "host": None, "user": None},
            },
        },
    }


def _spec_one_target_one_env():
    return {
        "app": {
            "default_env": None,
            "envs": {
                "prod": {"path": "/tmp/app.log", "host": None, "user": None},
            },
        },
    }


def test_cmd_logs_non_tty_no_target_exits_1():
    ns, registry, errs = _load_logtail_for_cmd(pick_result=None)
    ns["register_logs_command"](_spec_two_targets())
    cmd_logs = registry["logs"]
    rc = cmd_logs([])
    assert rc == 1, f"expected exit 1 in non-TTY with no target, got {rc}"
    assert any("target required" in m for m in errs), errs
    print("OK: cmd_logs with no target in non-TTY returns 1")


def test_cmd_logs_non_tty_no_env_exits_1():
    ns, registry, errs = _load_logtail_for_cmd(pick_result=None)
    ns["register_logs_command"](_spec_two_targets())
    cmd_logs = registry["logs"]
    rc = cmd_logs(["app"])
    assert rc == 1, f"expected exit 1 in non-TTY with no --env, got {rc}"
    assert any("--env required" in m for m in errs), errs
    print("OK: cmd_logs with multi-env target and no --env in non-TTY returns 1")


def test_cmd_logs_single_target_single_env_non_tty_proceeds():
    ns, registry, errs = _load_logtail_for_cmd(pick_result=None, tail_local_rc=0)
    ns["register_logs_command"](_spec_one_target_one_env())
    cmd_logs = registry["logs"]
    rc = cmd_logs([])
    assert rc == 0, f"expected tail_local rc=0 to pass through, got {rc}"
    assert not errs, f"no err expected in single-target non-TTY flow, got {errs}"
    print("OK: cmd_logs with single target/env proceeds in non-TTY")


if __name__ == "__main__":
    test_append_during_follow()
    test_rotation_reopens()
    test_truncation_reopens()
    test_cmd_logs_non_tty_no_target_exits_1()
    test_cmd_logs_non_tty_no_env_exits_1()
    test_cmd_logs_single_target_single_env_non_tty_proceeds()
    print("All tests passed.")
