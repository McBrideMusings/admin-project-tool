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
import types as _types
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


def _stub_log_deps():
    """Stubs for the runtime deps logtail pulls from sibling modules, so the
    exec'd source resolves without the package context."""
    return {
        "make_formatter": lambda **k: (lambda line: line),
        "C": _types.SimpleNamespace(
            RED="", GREEN="", YELLOW="", BLUE="", MAGENTA="", CYAN="",
            BOLD="", DIM="", RESET="",
        ),
    }


def _strip_relative_imports(source: str) -> str:
    source = _re.sub(r"from \.core import \([^)]*\)", "pass", source, count=1)
    source = _re.sub(r"from \.log_format import .*", "pass", source, count=1)
    return source


def _load_logtail():
    source = _strip_relative_imports((LIB_DIR / "logtail.py").read_text())
    ns: dict = {
        "info": lambda *a, **k: None,
        "warn": lambda *a, **k: None,
        "err": lambda *a, **k: None,
        "command": lambda *a, **k: (lambda f: f),
        "pick_target_or_none": lambda *a, **k: None,
        "resolve_env": lambda s: s,
        "_children": [],
        **_stub_log_deps(),
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
    source = _strip_relative_imports((LIB_DIR / "logtail.py").read_text())

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
        **_stub_log_deps(),
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


# --- non-file kinds: dispatch ------------------------------------------------

def _capture(ns, name, rc=0):
    """Replace ns[name] with a recorder; returns the list it appends (a, k) to."""
    calls = []

    def _rec(*a, **k):
        calls.append((a, k))
        return rc

    ns[name] = _rec
    return calls


def test_cmd_logs_command_kind_dispatches():
    ns, registry, errs = _load_logtail_for_cmd()
    calls = _capture(ns, "tail_command", rc=0)
    ns["register_logs_command"]({
        "plugin": {
            "kind": "command",
            "run": "node scripts/logs.mjs --follow",
            "host": None, "user": None, "highlight": [],
        }
    })
    rc = registry["logs"](["plugin"])
    assert rc == 0
    (args, _kw), = calls
    assert args[0] == "node scripts/logs.mjs --follow"
    print("OK: command kind routes to tail_command")


def test_cmd_logs_docker_kind_dispatches_with_highlight():
    ns, registry, errs = _load_logtail_for_cmd()
    calls = _capture(ns, "tail_docker", rc=0)
    ns["register_logs_command"]({
        "docker": {
            "kind": "docker",
            "container": "stash",
            "host": "tower", "user": None,
            "highlight": ["stash-organize"],
        }
    })
    rc = registry["logs"](["docker"])
    assert rc == 0
    (args, kw), = calls
    assert args[0] == "stash"
    assert kw["highlight"] == ["stash-organize"]
    print("OK: docker kind routes to tail_docker with highlight")


def test_cmd_logs_pick_kind_dispatches():
    ns, registry, errs = _load_logtail_for_cmd()
    calls = _capture(ns, "tail_pick", rc=0)
    ns["register_logs_command"]({
        "pick": {"kind": "pick", "glob": "tmp/*.log", "highlight": []}
    })
    rc = registry["logs"](["pick"])
    assert rc == 0
    (args, _kw), = calls
    assert args[0] == "tmp/*.log"
    print("OK: pick kind routes to tail_pick")


def test_line_renderer_highlights_matching_lines_only():
    ns = _load_logtail()

    class _Stdout:
        def __init__(self):
            self.chunks = []

        def isatty(self):
            return True

        def write(self, s):
            self.chunks.append(s)

        def flush(self):
            pass

    out = _Stdout()
    ns["sys"] = _types.SimpleNamespace(stdout=out)
    renderer = ns["LineRenderer"](["stash-organize"])
    renderer.write("hit stash-organize here\n")
    renderer.write("plain line\n")
    assert out.chunks[0].startswith("\033[1;7m"), out.chunks[0]
    assert not out.chunks[1].startswith("\033[1;7m"), out.chunks[1]
    print("OK: LineRenderer wraps only matching lines")


# --- kind dispatch catalog guard --------------------------------------------

def test_log_kind_handlers_cover_manifest_kinds():
    # Mirrors test_interp.test_factories_cover_known_kinds: the logtail dispatch
    # catalog must match the parser/scanner catalog so a new kind can't be
    # half-wired (parseable but undispatchable, or vice versa).
    from admin_lib.kinds import LOG_KINDS
    ns = _load_logtail()
    assert set(ns["LOG_KIND_HANDLERS"]) == set(LOG_KINDS)
    print("OK: LOG_KIND_HANDLERS covers exactly admin_lib.kinds.LOG_KINDS")


# --- file/pick sinks (highlight vs raw) -------------------------------------

def test_make_file_sink_renders_only_with_highlight():
    ns = _load_logtail()
    assert type(ns["_make_file_sink"]([])).__name__ == "_RawSink"
    assert type(ns["_make_file_sink"](["x"])).__name__ == "_RenderSink"
    print("OK: _make_file_sink picks RawSink without highlight, RenderSink with")


def test_raw_sink_writes_bytes_verbatim():
    ns = _load_logtail()

    class _Buf:
        def __init__(self):
            self.data = b""

        def write(self, b):
            self.data += b

        def flush(self):
            pass

    out = _types.SimpleNamespace(buffer=_Buf(), flush=lambda: None)
    ns["sys"] = _types.SimpleNamespace(stdout=out)
    sink = ns["_RawSink"]()
    sink.feed(b"\x00raw\xff")
    sink.flush()
    assert out.buffer.data == b"\x00raw\xff", "raw sink must pass bytes untouched"
    print("OK: _RawSink streams bytes verbatim (binary-safe)")


def test_render_sink_buffers_partial_lines_then_flushes():
    ns = _load_logtail()

    class _Out:
        def __init__(self):
            self.chunks = []

        def isatty(self):
            return False  # no color, so lines pass through the identity formatter

        def write(self, s):
            self.chunks.append(s)

        def flush(self):
            pass

    out = _Out()
    ns["sys"] = _types.SimpleNamespace(stdout=out)
    sink = ns["_RenderSink"]([])
    sink.feed(b"line1\npart")
    assert out.chunks == ["line1\n"], out.chunks  # partial line held back
    sink.feed(b"ial\nline3\n")
    assert out.chunks == ["line1\n", "partial\n", "line3\n"], out.chunks
    sink.feed(b"trailing-no-newline")
    assert out.chunks == ["line1\n", "partial\n", "line3\n"], "no newline yet"
    sink.flush()
    assert out.chunks[-1] == "trailing-no-newline", "flush emits the trailing line"
    print("OK: _RenderSink line-buffers across reads and flushes the tail")


# --- highlight forwarding to file/pick (was a silent no-op) ------------------

def test_cmd_logs_file_kind_forwards_highlight_to_tail_local():
    ns, registry, errs = _load_logtail_for_cmd()
    calls = _capture(ns, "tail_local", rc=0)
    ns["register_logs_command"]({
        "app": {
            "kind": "file", "default_env": "prod",
            "envs": {"prod": {"path": "/tmp/a.log", "host": None, "user": None}},
            "highlight": ["ERR"],
        }
    })
    assert registry["logs"](["app"]) == 0
    (_args, kw), = calls
    assert kw.get("highlight") == ["ERR"], kw
    print("OK: file kind forwards highlight to tail_local")


def test_cmd_logs_pick_kind_forwards_highlight():
    ns, registry, errs = _load_logtail_for_cmd()
    calls = _capture(ns, "tail_pick", rc=0)
    ns["register_logs_command"]({
        "pick": {"kind": "pick", "glob": "tmp/*.log", "highlight": ["WARN"]}
    })
    assert registry["logs"](["pick"]) == 0
    (_args, kw), = calls
    assert kw.get("highlight") == ["WARN"], kw
    print("OK: pick kind forwards highlight to tail_pick")


# --- output lock (bridge sink vs tail thread) -------------------------------

def test_line_renderer_write_holds_output_lock():
    ns = _load_logtail()
    entered = []

    class _TrackLock:
        def __enter__(self):
            entered.append(True)
            return self

        def __exit__(self, *a):
            return False

    ns["_OUTPUT_LOCK"] = _TrackLock()
    ns["sys"] = _types.SimpleNamespace(
        stdout=_types.SimpleNamespace(
            isatty=lambda: False, write=lambda s: None, flush=lambda: None,
        )
    )
    ns["LineRenderer"]().write("x\n")
    assert entered == [True], "write must run inside the shared output lock"
    print("OK: LineRenderer.write serializes through _OUTPUT_LOCK")


