"""Tests for kill-port renderer and kill_port_interactive."""
from __future__ import annotations

import sys
from pathlib import Path
from unittest.mock import patch

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from gen.fragments import FragmentError, render_kill_port  # noqa: E402
from gen.manifest import Command  # noqa: E402
from admin_lib.ports_nuke import kill_port_interactive  # noqa: E402


def _cmd(name="kill", run="", desc="Kill dev server"):
    return Command(name=name, run=run, desc=desc, kind="kill-port")


# ---------------------------------------------------------------------------
# render_kill_port
# ---------------------------------------------------------------------------

def test_render_kill_port_with_single_default():
    src = render_kill_port(_cmd(run="3000"))
    assert "_to_kill = [3000]" in src
    assert "if args:" in src
    assert "[int(a) for a in args]" in src
    assert "for _port in _to_kill:" in src
    assert "kill_port_interactive" in src
    assert "[PORT]" not in src
    print("OK: single default port renders correctly")


def test_render_kill_port_with_multiple_defaults():
    src = render_kill_port(_cmd(run="3000 8080"))
    assert "_to_kill = [3000, 8080]" in src
    assert "if args:" in src
    assert "kill_port_interactive" in src
    print("OK: multiple default ports render correctly")


def test_render_kill_port_no_default_appends_port_hint():
    src = render_kill_port(_cmd(run=""))
    assert "[PORT]" in src
    assert "port argument required" in src
    assert "kill_port_interactive" in src
    print("OK: no-default command gets [PORT] hint in description")


def test_render_kill_port_no_default_empty_desc():
    src = render_kill_port(_cmd(run="", desc=""))
    assert '"[PORT]"' in src
    print("OK: empty desc with no default gets [PORT] as full description")


def test_render_kill_port_non_numeric_raises():
    try:
        render_kill_port(_cmd(run="not-a-port"))
        assert False, "expected FragmentError"
    except FragmentError as e:
        assert "not-a-port" in str(e)
    print("OK: non-numeric run raises FragmentError")


def test_render_kill_port_mixed_invalid_raises():
    try:
        render_kill_port(_cmd(run="3000 abc"))
        assert False, "expected FragmentError"
    except FragmentError as e:
        assert "abc" in str(e)
    print("OK: mixed valid/invalid run raises FragmentError")


def test_render_kill_port_iterates_all_ports():
    src = render_kill_port(_cmd(run="3000 4000 5000"))
    assert "_to_kill = [3000, 4000, 5000]" in src
    assert "for _port in _to_kill:" in src
    print("OK: three-port default generates iteration loop")


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


