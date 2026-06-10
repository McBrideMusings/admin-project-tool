"""Tests for the kill-port interpreter factory and kill_port_interactive."""
from __future__ import annotations

import sys
from pathlib import Path
from unittest.mock import patch

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.interp import InterpError, _make_kill_port  # noqa: E402
from gen.manifest import Command  # noqa: E402
from admin_lib.ports_nuke import kill_port_interactive  # noqa: E402


def _cmd(name="kill", run="", desc="Kill dev server"):
    return Command(name=name, run=run, desc=desc, kind="kill-port")


# ---------------------------------------------------------------------------
# interp._make_kill_port — port parsing + validation happen at build time
# ---------------------------------------------------------------------------

def test_make_kill_port_valid_defaults_build_callable():
    assert callable(_make_kill_port(_cmd(run="3000"), {}))
    assert callable(_make_kill_port(_cmd(run="3000 8080 5000"), {}))
    print("OK: valid default ports build a callable")


def test_make_kill_port_no_default_builds_callable():
    # No defaults is legal — the port is required as a runtime arg instead.
    assert callable(_make_kill_port(_cmd(run=""), {}))
    print("OK: no-default kill-port builds a callable")


def test_make_kill_port_non_numeric_raises():
    with pytest.raises(InterpError) as e:
        _make_kill_port(_cmd(run="not-a-port"), {})
    assert "not-a-port" in str(e.value)
    print("OK: non-numeric run raises InterpError")


def test_make_kill_port_mixed_invalid_raises():
    with pytest.raises(InterpError) as e:
        _make_kill_port(_cmd(run="3000 abc"), {})
    assert "abc" in str(e.value)
    print("OK: mixed valid/invalid run raises InterpError")


def test_kill_port_default_dispatch_calls_interactive_per_port(monkeypatch):
    """The built closure kills each default port via kill_port_interactive."""
    killed = []
    monkeypatch.setattr(
        "admin_lib.ports_nuke.kill_port_interactive",
        lambda p: killed.append(p) or True,
    )
    _make_kill_port(_cmd(run="3000 4000 5000"), {})([])
    assert killed == [3000, 4000, 5000]
    print("OK: closure iterates all default ports")


# ---------------------------------------------------------------------------
# kill_port_interactive
# ---------------------------------------------------------------------------

def test_kill_port_interactive_no_holders_returns_true():
    with patch("admin_lib.ports_nuke.port_holders", return_value=[]):
        result = kill_port_interactive(9999)
    assert result is True
    print("OK: no holders → returns True without prompting")


_HOLDERS = [{"pid": 1234, "command": "node", "parent_pid": None, "parent_command": None}]


def test_kill_port_interactive_user_abort_returns_false():
    with (
        patch("admin_lib.ports_nuke.port_holders", return_value=_HOLDERS),
        patch("sys.stdin") as mock_stdin,
        patch("builtins.input", side_effect=KeyboardInterrupt),
    ):
        mock_stdin.isatty.return_value = True
        result = kill_port_interactive(3000)
    assert result is False
    print("OK: KeyboardInterrupt at prompt → returns False")


def test_kill_port_interactive_kill_success_returns_true():
    with (
        patch("admin_lib.ports_nuke.port_holders", return_value=_HOLDERS),
        patch("sys.stdin") as mock_stdin,
        patch("builtins.input", return_value=""),
        patch("admin_lib.ports_nuke._kill_pids", return_value=True),
    ):
        mock_stdin.isatty.return_value = True
        result = kill_port_interactive(3000)
    assert result is True
    print("OK: successful kill → returns True")


def test_kill_port_interactive_kill_failure_returns_false():
    with (
        patch("admin_lib.ports_nuke.port_holders", return_value=_HOLDERS),
        patch("sys.stdin") as mock_stdin,
        patch("builtins.input", return_value=""),
        patch("admin_lib.ports_nuke._kill_pids", return_value=False),
    ):
        mock_stdin.isatty.return_value = True
        result = kill_port_interactive(3000)
    assert result is False
    print("OK: failed kill → returns False")


