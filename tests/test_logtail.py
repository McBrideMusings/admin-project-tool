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
        "pick_target": lambda *a, **k: None,
        "resolve_env": lambda s: s,
        "_children": [],
    }
    exec(compile(source, "logtail.py", "exec"), ns)
    return ns


def _spawn_tailer(ns, path, **kwargs):
    sink = _Sink()
    ns["sys"] = _StubSys(sink)
    ready = threading.Event()

    def runner():
        ready.set()
        ns["tail_local"](path, **kwargs)

    t = threading.Thread(target=runner, daemon=True)
    t.start()
    ready.wait()
    time.sleep(0.5)  # let the tailer reach its read loop
    return sink


def _wait_for(sink: _Sink, needle: bytes, timeout: float = 3.0) -> bytes:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        snap = sink.snapshot()
        if needle in snap:
            return snap
        time.sleep(0.1)
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


if __name__ == "__main__":
    test_append_during_follow()
    test_rotation_reopens()
    test_truncation_reopens()
    print("All tests passed.")
