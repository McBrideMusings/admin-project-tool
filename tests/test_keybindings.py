"""Tests for the interactive-shell action kind + keybinding runtime helpers."""
from __future__ import annotations

import os
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from gen.archetypes import ArchetypeError, _to_command, resolve  # noqa: E402
from gen.fragments import _ACTION_RENDERERS, KIND_MODULES, render_interactive_shell  # noqa: E402
from gen.manifest import Manifest  # noqa: E402
from gen.render import render  # noqa: E402

ARCH_DIR = REPO / "archetypes"
LIB_DIR = REPO / "admin_lib"


def _to_action(name, data):
    return _to_command(name, data, source="test", is_action=True)


def _expect_error(fn, *, contains: str):
    """Call fn() and assert ArchetypeError is raised with `contains` in the message."""
    try:
        fn()
    except ArchetypeError as e:
        msg = str(e)
        assert contains in msg, f"expected {contains!r} in error, got: {msg!r}"
        return
    raise AssertionError(f"expected ArchetypeError containing {contains!r}, no error raised")


# ---------------------------------------------------------------------------
# Parser / validator
# ---------------------------------------------------------------------------

def test_kind_registered():
    assert "interactive-shell" in _ACTION_RENDERERS
    assert KIND_MODULES["interactive-shell"] == ["keybindings"]


def test_keys_only_on_interactive_shell():
    """`keys` on any other kind is an error — silent acceptance hides typos."""
    _expect_error(
        lambda: _to_action("foo", {
            "kind": "shell", "run": "echo hi", "keys": {"r": {"run": "echo r"}},
        }),
        contains="only valid on kind",
    )


def test_keys_run_target_accepted():
    cmd = _to_action("dev", {
        "kind": "interactive-shell",
        "run": "bash -c 'sleep 30'",
        "keys": {"r": {"label": "reload", "run": "kill -TERM 123"}},
    })
    assert cmd.keys == {"r": {"label": "reload", "run": "kill -TERM 123"}}


def test_keys_signal_target_accepted():
    cmd = _to_action("dev", {
        "kind": "interactive-shell",
        "run": "sleep 30",
        "keys": {"r": {"signal": "TERM", "pid_file": "/tmp/x.pid"}},
    })
    assert cmd.keys["r"]["signal"] == "TERM"
    assert cmd.keys["r"]["pid_file"] == "/tmp/x.pid"


def test_keys_action_target_strips_at_prefix():
    cmd = _to_action("dev", {
        "kind": "interactive-shell",
        "run": "sleep 30",
        "keys": {
            "r": {"action": "@other"},
            "v": {"action": "another"},
        },
    })
    assert cmd.keys["r"]["action"] == "other"
    assert cmd.keys["v"]["action"] == "another"


def test_keys_malformed_rejected():
    cases = [
        ({}, "must have one of"),
        ({"run": "x", "action": "y"}, "conflicting targets"),
        ({"signal": "TERM"}, "no 'pid_file'"),
        ({"run": "x", "pid_file": "/tmp/x"}, "no 'signal'"),
        ({"run": "x", "junk": "y"}, "unknown fields"),
        ({"label": "x"}, "must have one of"),
    ]
    for bad_binding, err_match in cases:
        _expect_error(
            lambda b=bad_binding: _to_action("dev", {
                "kind": "interactive-shell",
                "run": "sleep 30",
                "keys": {"r": b},
            }),
            contains=err_match,
        )


def test_keys_multichar_rejected():
    _expect_error(
        lambda: _to_action("dev", {
            "kind": "interactive-shell",
            "run": "sleep 30",
            "keys": {"rr": {"run": "x"}},
        }),
        contains="single character",
    )


def test_keys_table_must_be_dict():
    _expect_error(
        lambda: _to_action("dev", {
            "kind": "interactive-shell",
            "run": "sleep 30",
            "keys": "r",
        }),
        contains="'keys' must be a table",
    )


def test_keys_binding_must_be_dict():
    _expect_error(
        lambda: _to_action("dev", {
            "kind": "interactive-shell",
            "run": "sleep 30",
            "keys": {"r": "echo x"},
        }),
        contains="must be a table",
    )


def test_interactive_shell_requires_run():
    _expect_error(
        lambda: _to_action("dev", {"kind": "interactive-shell"}),
        contains="requires 'run'",
    )


# ---------------------------------------------------------------------------
# Cross-reference validation (resolve())
# ---------------------------------------------------------------------------

def _make_manifest(actions, commands=None):
    return Manifest(
        project_name="kb-test",
        python_min="3.11",
        archetypes=[],
        modules=["core"],
        urls={},
        overrides={},
        custom=commands or {},
        order=[],
        actions=actions,
    )


def test_action_reference_resolves():
    m = _make_manifest(
        actions={
            "dev-local": {
                "kind": "interactive-shell",
                "run": "sleep 30",
                "keys": {"r": {"action": "restart"}},
            },
            "restart": {"kind": "shell", "run": "echo restart"},
        },
        commands={"dev": {"desc": "dev", "steps": ["dev-local"]}},
    )
    resolved = resolve(m, ARCH_DIR)
    assert resolved.actions["dev-local"].keys["r"]["action"] == "restart"


def test_action_reference_unknown_rejected():
    m = _make_manifest(
        actions={
            "dev-local": {
                "kind": "interactive-shell",
                "run": "sleep 30",
                "keys": {"r": {"action": "ghost"}},
            },
        },
        commands={"dev": {"desc": "dev", "steps": ["dev-local"]}},
    )
    _expect_error(
        lambda: resolve(m, ARCH_DIR),
        contains="references unknown action 'ghost'",
    )


# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------

def test_render_interactive_shell_emits_runtime_call():
    cmd = _to_action("dev-local", {
        "kind": "interactive-shell",
        "run": "bash -c sleep",
        "keys": {
            "r": {"label": "restart web", "signal": "TERM", "pid_file": "/tmp/main.pid"},
            "v": {"label": "restart vite", "run": "kill -TERM 1"},
        },
    })
    body = render_interactive_shell(cmd)
    assert "def cmd_dev_local(args):" in body
    assert "run_with_keybindings(" in body
    assert '"r": {' in body
    assert '"signal": "TERM"' in body
    assert '"pid_file": "/tmp/main.pid"' in body
    assert '"v": {' in body


def test_full_render_bundles_keybindings_module():
    """An interactive-shell action pulls keybindings.py into the bundle."""
    m = _make_manifest(
        actions={
            "dev-local": {
                "kind": "interactive-shell",
                "run": "sleep 30",
                "keys": {"r": {"run": "echo reload"}},
            },
        },
        commands={"dev": {"desc": "dev", "steps": ["dev-local"]}},
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    assert "def run_with_keybindings(" in script
    assert "run_with_keybindings(" in script


# ---------------------------------------------------------------------------
# Runtime helpers — _send_signal, _run_inline
# ---------------------------------------------------------------------------

def _child_script(ready_file: Path, sig_file: Path) -> str:
    return "\n".join([
        "import sys, signal, pathlib, time",
        f"ready = pathlib.Path({str(ready_file)!r})",
        f"sigf  = pathlib.Path({str(sig_file)!r})",
        "def h(n, f):",
        "    sigf.write_text('SIGINT' if n == signal.SIGINT else 'SIGTERM' if n == signal.SIGTERM else f'SIG{n}')",
        "    raise SystemExit(0)",
        "signal.signal(signal.SIGINT, h)",
        "signal.signal(signal.SIGTERM, h)",
        "signal.signal(signal.SIGUSR1, h)",
        "ready.write_text('ok')",
        "time.sleep(30)",
    ])


def _wait_for_file(path: Path, timeout: float = 5.0) -> None:
    deadline = time.monotonic() + timeout
    while not path.exists():
        assert time.monotonic() < deadline, f"timeout waiting for {path}"
        time.sleep(0.05)


def test_send_signal_delivers_to_pid_file():
    """_send_signal reads the PID file and delivers the requested signal."""
    import admin_lib.keybindings as kb

    ready = Path(tempfile.mktemp(suffix=".ready"))
    sig = Path(tempfile.mktemp(suffix=".sig"))
    child_file = Path(tempfile.mktemp(suffix=".py"))
    pid_file = Path(tempfile.mktemp(suffix=".pid"))
    child_file.write_text(_child_script(ready, sig))

    proc = subprocess.Popen(
        [sys.executable, str(child_file)],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    try:
        _wait_for_file(ready)
        pid_file.write_text(str(proc.pid))
        kb._send_signal("TERM", str(pid_file))
        _wait_for_file(sig)
        assert sig.read_text() == "SIGTERM"
    finally:
        if proc.poll() is None:
            proc.kill()
        proc.wait()
        for f in (ready, sig, child_file, pid_file):
            if f.exists():
                f.unlink()


def test_send_signal_missing_pid_file_warns_no_raise():
    import admin_lib.keybindings as kb
    kb._send_signal("TERM", "/tmp/definitely-does-not-exist.pid")


def test_send_signal_unknown_signal_name_errors():
    import admin_lib.keybindings as kb
    kb._send_signal("WAT", "/tmp/whatever.pid")


def test_run_inline_spawns_subshell():
    """_run_inline calls subprocess.Popen and returns immediately."""
    import admin_lib.keybindings as kb

    marker = Path(tempfile.mktemp(suffix=".touch"))
    try:
        kb._run_inline(f"touch {marker}")
        deadline = time.monotonic() + 3.0
        while not marker.exists() and time.monotonic() < deadline:
            time.sleep(0.05)
        assert marker.exists()
    finally:
        if marker.exists():
            marker.unlink()


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    failed = 0
    for fn in tests:
        try:
            fn()
            print(f"OK  {fn.__name__}")
        except Exception as e:
            print(f"FAIL {fn.__name__}: {e}")
            failed += 1
    if failed:
        sys.exit(failed)
    else:
        print(f"\nAll {len(tests)} tests passed.")
